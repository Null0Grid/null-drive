package sync

import (
	"context"
	"errors"
	"fmt"
	"os"
	gosync "sync"
	"time"

	"github.com/nulldrive/nulldrive/shared/client"
	"github.com/nulldrive/nulldrive/shared/pathutil"
)

// State is the agent's high-level state, shown as the tray icon.
type State string

const (
	StateStarting  State = "starting"
	StateSyncing   State = "syncing"
	StateIdle      State = "idle" // up to date
	StatePaused    State = "paused"
	StateOffline   State = "offline"   // server unreachable; retrying with backoff
	StateError     State = "error"     // something needs fixing (folder missing...)
	StateAttention State = "attention" // waiting for the user (mass-delete guard)
)

// Progress of the current cycle.
type Progress struct {
	Done  int    `json:"done"`
	Total int    `json:"total"`
	Path  string `json:"path"`
	// Bytes / BytesTotal: network progress of the file at Path (chunked
	// transfers; only the chunks that actually travel are counted).
	Bytes      int64 `json:"bytes"`
	BytesTotal int64 `json:"bytes_total"`
}

// Status is a snapshot for the UI.
type Status struct {
	State     State          `json:"state"`
	Message   string         `json:"message"`
	LastSync  time.Time      `json:"last_sync"`
	Progress  Progress       `json:"progress"`
	Conflicts int            `json:"conflicts"`
	Failed    int            `json:"failed"`
	Skipped   []Skipped      `json:"skipped"`
	MassDel   *ErrMassDelete `json:"mass_delete,omitempty"`
	// Live is true while the realtime push channel is connected.
	Live bool `json:"live"`
}

// Agent runs the engine continuously: on local changes (Trigger), on
// server pushes (RemoteChanged), on a poll interval as a safety net, and
// with exponential backoff while the server is unreachable.
type Agent struct {
	Engine       *Engine
	PollInterval time.Duration
	// LivePollInterval replaces PollInterval while the push channel is up:
	// pushes make frequent polling redundant, but a slow poll still
	// catches anything a notification could miss.
	LivePollInterval time.Duration
	MaxBackoff       time.Duration
	OnStatus         func(Status)
	OnActivity       func(Event)

	mu       gosync.Mutex
	status   Status
	paused   bool
	activity []Event
	wake     chan struct{}
}

const activityCap = 200

// NewAgent wires an agent around an engine.
func NewAgent(e *Engine) *Agent {
	a := &Agent{
		Engine: e, PollInterval: 30 * time.Second, LivePollInterval: 5 * time.Minute,
		MaxBackoff: 5 * time.Minute, wake: make(chan struct{}, 1),
	}
	a.status.State = StateStarting
	prevEvent := e.OnEvent
	e.OnEvent = func(ev Event) {
		a.addActivity(ev)
		if prevEvent != nil {
			prevEvent(ev)
		}
	}
	e.OnProgress = func(done, total int, p string) {
		a.update(func(s *Status) { s.Progress = Progress{Done: done, Total: total, Path: p} })
	}
	var lastBytes time.Time
	e.OnBytes = func(p string, done, total int64) {
		// Throttle UI updates to ~5/s; always report completion.
		if done < total && time.Since(lastBytes) < 200*time.Millisecond {
			return
		}
		lastBytes = time.Now()
		a.update(func(s *Status) { s.Progress.Path, s.Progress.Bytes, s.Progress.BytesTotal = p, done, total })
	}
	return a
}

// Trigger asks for a sync as soon as possible (file watcher, "Sync now").
func (a *Agent) Trigger() {
	select {
	case a.wake <- struct{}{}:
	default: // one pending wake-up is enough
	}
}

// SetLive records whether the push channel is connected; it changes the
// poll interval and is shown in the UI.
func (a *Agent) SetLive(live bool) {
	a.update(func(s *Status) { s.Live = live })
	if !live {
		a.Trigger() // pushes may have been missed while the socket was down
	}
}

// RemoteChanged is called when the server announces its change feed is at
// version. A sync runs only if that is newer than what this device has
// already applied, so the "hello" sent on every reconnect is free when
// nothing changed.
func (a *Agent) RemoteChanged(ctx context.Context, version int64) {
	cur, err := a.Engine.Index.Cursor(ctx)
	if err != nil || version > cur {
		a.Trigger()
	}
}

func (a *Agent) pollInterval() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.status.Live && a.LivePollInterval > 0 {
		return a.LivePollInterval
	}
	return a.PollInterval
}

func (a *Agent) Pause() {
	a.mu.Lock()
	a.paused = true
	a.mu.Unlock()
	a.update(func(s *Status) { s.State, s.Message = StatePaused, "Paused" })
	a.Trigger()
}

func (a *Agent) Resume() {
	a.mu.Lock()
	a.paused = false
	a.mu.Unlock()
	a.Trigger()
}

// ConfirmMassDelete lets the next cycle perform a large deletion the guard
// stopped.
func (a *Agent) ConfirmMassDelete() {
	a.Engine.AllowMassDelete = true
	a.Trigger()
}

func (a *Agent) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

// Activity returns recent events, newest first.
func (a *Agent) Activity() []Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Event, len(a.activity))
	for i, ev := range a.activity {
		out[len(a.activity)-1-i] = ev
	}
	return out
}

func (a *Agent) addActivity(ev Event) {
	a.mu.Lock()
	a.activity = append(a.activity, ev)
	if len(a.activity) > activityCap {
		a.activity = a.activity[len(a.activity)-activityCap:]
	}
	cb := a.OnActivity
	a.mu.Unlock()
	if cb != nil {
		cb(ev)
	}
}

func (a *Agent) update(fn func(*Status)) {
	a.mu.Lock()
	fn(&a.status)
	s := a.status
	cb := a.OnStatus
	a.mu.Unlock()
	if cb != nil {
		cb(s)
	}
}

func (a *Agent) isPaused() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.paused
}

// Run blocks until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) {
	var backoff time.Duration
	for {
		if ctx.Err() != nil {
			return
		}
		if a.isPaused() {
			a.update(func(s *Status) { s.State, s.Message = StatePaused, "Paused" })
			if !a.wait(ctx, 0) {
				return
			}
			continue
		}

		wait := a.pollInterval()
		err := a.cycle(ctx)
		var mass *ErrMassDelete
		switch {
		case err == nil:
			backoff = 0
		case errors.As(err, &mass):
			a.update(func(s *Status) {
				s.State, s.Message, s.MassDel = StateAttention, mass.Error(), mass
			})
			wait = 0 // until the user confirms (or something else triggers)
		case errors.Is(err, ErrRootMissing):
			a.update(func(s *Status) {
				s.State, s.Message = StateError, "The sync folder is missing. Reconnect the drive or choose the folder again in Settings."
			})
		case client.IsNetwork(err):
			backoff = nextBackoff(backoff, a.MaxBackoff)
			wait = backoff
			a.update(func(s *Status) {
				s.State, s.Message = StateOffline, fmt.Sprintf("Can't reach the server. Retrying in %s.", backoff.Round(time.Second))
			})
		default:
			backoff = nextBackoff(backoff, a.MaxBackoff)
			wait = backoff
			a.update(func(s *Status) { s.State, s.Message = StateError, err.Error() })
		}
		if !a.wait(ctx, wait) {
			return
		}
	}
}

func nextBackoff(cur, maxB time.Duration) time.Duration {
	if cur == 0 {
		return 2 * time.Second
	}
	cur *= 2
	if cur > maxB {
		return maxB
	}
	return cur
}

// wait sleeps for d (0 = until woken) or until Trigger/Resume/ctx.
func (a *Agent) wait(ctx context.Context, d time.Duration) bool {
	var timer <-chan time.Time
	if d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		timer = t.C
	}
	select {
	case <-ctx.Done():
		return false
	case <-a.wake:
		return true
	case <-timer:
		return true
	}
}

// cycle runs Sync until a pass has nothing left to do (an upload can, for
// example, create parent folders on the server that the next pass records),
// capped so a persistent failure can't spin.
func (a *Agent) cycle(ctx context.Context) error {
	a.update(func(s *Status) { s.State, s.Message, s.MassDel = StateSyncing, "Syncing…", nil })
	var last *Report
	for pass := 0; pass < 5; pass++ {
		rep, err := a.Engine.Sync(ctx)
		if err != nil {
			return err
		}
		last = rep
		work := 0
		for _, n := range rep.Ops {
			work += n
		}
		if work == 0 || rep.Failed > 0 {
			break
		}
	}
	conflicts, _ := a.Engine.Index.Conflicts(ctx)
	a.update(func(s *Status) {
		s.LastSync = time.Now()
		s.Progress = Progress{}
		s.Conflicts = len(conflicts)
		s.Skipped = last.Skipped
		s.Failed = last.Failed
		if last.Failed > 0 {
			s.State, s.Message = StateError, fmt.Sprintf("%d item(s) couldn't be synced; retrying.", last.Failed)
		} else {
			s.State, s.Message = StateIdle, "Up to date"
		}
	})
	return nil
}

// Conflict resolutions offered in the UI.
const (
	ResolveKeepBoth   = "keep-both"   // dismiss: keep the original and the copy
	ResolveKeepServer = "keep-server" // delete this device's copy
	ResolveKeepMine   = "keep-mine"   // replace the original with this device's copy
)

// ResolveConflict applies a resolution. The file operations are ordinary
// local edits: the next cycle syncs them like any other change.
func (a *Agent) ResolveConflict(ctx context.Context, id int64, how string) error {
	e := a.Engine
	c, err := e.Index.ConflictByID(ctx, id)
	if err != nil {
		return err
	}
	for _, p := range []string{c.Path, c.ConflictPath} {
		if clean, err := pathutil.Clean(p); err != nil || clean != p {
			return fmt.Errorf("invalid path in conflict record: %q", p)
		}
	}
	switch how {
	case ResolveKeepBoth:
	case ResolveKeepServer:
		if err := os.Remove(e.full(c.ConflictPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	case ResolveKeepMine:
		if err := os.Rename(e.full(c.ConflictPath), e.full(c.Path)); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown resolution %q", how)
	}
	if err := e.Index.DeleteConflict(ctx, id); err != nil {
		return err
	}
	a.update(func(s *Status) {
		if s.Conflicts > 0 {
			s.Conflicts--
		}
	})
	a.Trigger()
	return nil
}
