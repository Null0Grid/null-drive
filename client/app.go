package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	gosync "sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/options"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/Null0Grid/null-drive/shared/client"
	"github.com/Null0Grid/null-drive/shared/e2ee"
	"github.com/Null0Grid/null-drive/shared/logfile"
	"github.com/Null0Grid/null-drive/shared/pathutil"
	nsync "github.com/Null0Grid/null-drive/shared/sync"
)

// App is bound to the frontend: every exported method is callable from
// JavaScript as window.go.main.App.<Method>. It is deliberately thin: the
// sync logic lives in shared/sync, where it is unit- and integration-tested.
type App struct {
	version string

	ctx  context.Context
	log  *slog.Logger
	logW *logfile.Writer

	mu     gosync.Mutex
	cfg    Config
	agent  *nsync.Agent
	engine *nsync.Engine
	index  *nsync.Index
	cancel context.CancelFunc
	done   chan struct{}
	tray   *tray
	paused bool
	// e2eeState: off, unlocked, locked or migrating (see e2ee.go).
	e2eeState string

	// For nulldrive:// links (openlink.go): the API client and keys of
	// the running sync, whether the window is ready for dialogs, a link
	// that arrived before it was, and whether one is being opened.
	remote      *client.Client
	keys        *e2ee.Keys
	ready       bool
	pendingLink string
	linkBusy    atomic.Bool
}

func NewApp(version string) *App {
	return &App{version: version, log: slog.New(slog.NewTextHandler(os.Stderr, nil))}
}

// ---------- lifecycle ----------

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if p, err := logPath(); err == nil {
		if w, err := logfile.Open(p, 5<<20, 3); err == nil {
			a.logW = w
			a.log = slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
		}
	}
	a.log.Info("client starting", "version", a.version)
	cfg, err := loadConfig()
	if err != nil {
		a.log.Error("load config", "err", err)
	}
	a.mu.Lock()
	a.cfg = cfg
	a.mu.Unlock()
	if err := registerURLScheme(); err != nil {
		a.log.Warn("register nulldrive:// links", "err", err)
	}
	a.startTray()
	if cfg.Configured() {
		if err := a.startSync(); err != nil {
			a.log.Error("start sync", "err", err)
			a.emitStatus(nsync.Status{State: nsync.StateError, Message: err.Error()})
		}
	}
}

func (a *App) shutdown(context.Context) {
	a.stopSync()
	stopTray()
	a.log.Info("client stopped")
	if a.logW != nil {
		_ = a.logW.Close()
	}
}

// onSecondInstance: launching the app again shows the window, unless the
// launch was "NullDrive --quit" (the installer and uninstaller use that to
// stop a running copy cleanly before replacing or removing the files).
func (a *App) onSecondInstance(d options.SecondInstanceData) {
	// A link first: it came from a web page, so nothing else on that
	// command line counts (see linkFromArgs).
	if raw, ok := linkFromArgs(d.Args); ok {
		a.queueLink(raw)
		return
	}
	if slices.Contains(d.Args, "--forget") {
		return // only meaningful when no copy is running (the uninstaller quits it first)
	}
	if slices.Contains(d.Args, "--quit") {
		a.log.Info("quit requested by a second launch (installer)")
		a.Quit()
		return
	}
	a.Show()
}

// Show brings the window to the front (tray "Open", second launch).
func (a *App) Show() {
	if a.ctx == nil {
		return
	}
	wruntime.WindowShow(a.ctx)
	wruntime.WindowUnminimise(a.ctx)
}

// Quit stops syncing cleanly and exits.
func (a *App) Quit() {
	if a.ctx != nil {
		wruntime.Quit(a.ctx)
	}
}

// ---------- sync lifecycle ----------

func (a *App) startSync() error {
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()
	token, err := loadToken(cfg)
	if err != nil {
		return fmt.Errorf("device credentials not found in the system keychain; please sign in again: %w", err)
	}
	if err := os.MkdirAll(cfg.Folder, 0o755); err != nil {
		return err
	}
	ip, err := indexPath(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(ip), 0o700); err != nil {
		return err
	}
	ix, err := nsync.OpenIndex(ip)
	if err != nil {
		return err
	}
	remote := client.NewPinned(cfg.ServerURL, token, cfg.CertSHA256)
	remote.UserAgent = userAgent(a.version)
	keys, e2State, kerr := a.resolveKeys(context.Background(), cfg, remote)
	a.setE2EEState(e2State)
	if kerr != nil {
		_ = ix.Close()
		st := nsync.StatePaused
		a.emitStatus(nsync.Status{State: st, Message: kerr.Error()})
		return nil // not a failure: the UI shows the unlock / waiting card
	}
	eng := &nsync.Engine{
		Root:   cfg.Folder,
		Index:  ix,
		Remote: remote,
		Device: cfg.DeviceName,
		Up:     nsync.NewLimiter(cfg.UpKBps * 1024),
		Down:   nsync.NewLimiter(cfg.DownKBps * 1024),
		OnEvent: func(ev nsync.Event) {
			a.log.Info("sync", "kind", ev.Kind, "path", ev.Path, "from", ev.From, "err", ev.Err)
		},
		OnTransfer: func(p, dir string, st client.TransferStats) {
			a.log.Info("transfer", "path", p, "direction", dir, "size", st.Size, "chunks", st.Chunks,
				"chunks_sent", st.Transferred, "bytes_sent", st.BytesSent, "bytes_reused", st.BytesReused, "resumed", st.ResumedBytes)
		},
	}
	if reset, err := applyKeys(context.Background(), eng, remote, keys); err != nil {
		_ = ix.Close()
		return err
	} else if reset {
		a.log.Info("sync index reset for a new encryption key; files are re-matched by content")
	}
	agent := nsync.NewAgent(eng)
	agent.PollInterval = time.Duration(cfg.PollSeconds) * time.Second
	agent.OnStatus = a.emitStatus
	agent.OnActivity = func(ev nsync.Event) {
		if a.ctx != nil {
			wruntime.EventsEmit(a.ctx, "activity", ev)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	a.mu.Lock()
	a.engine, a.agent, a.index, a.cancel, a.done = eng, agent, ix, cancel, done
	a.remote, a.keys = remote, keys
	paused := a.paused
	a.mu.Unlock()
	if paused {
		agent.Pause()
	}
	if err := watch(ctx, cfg.Folder, 1500*time.Millisecond, agent.Trigger, a.log); err != nil {
		a.log.Warn("file watcher unavailable; relying on periodic scans", "err", err)
	}
	notifier := &client.Notifier{
		Client:    remote,
		OnVersion: func(v int64) { agent.RemoteChanged(ctx, v) },
		OnState: func(st client.LiveState, err error) {
			a.log.Info("realtime", "state", st, "err", err)
			agent.SetLive(st == client.LiveConnected)
		},
	}
	agentDone := make(chan struct{})
	go func() {
		defer close(agentDone)
		agent.Run(ctx)
	}()
	go func() {
		defer close(done)
		// Push is an optimization: if the notifier gives up (token
		// revoked), the agent's own requests surface the real error.
		if err := notifier.Run(ctx); errors.Is(err, client.ErrUnauthorized) {
			agent.Trigger()
		}
		<-agentDone
	}()
	a.log.Info("sync started", "server", cfg.ServerURL, "folder", cfg.Folder)
	return nil
}

func (a *App) stopSync() {
	a.mu.Lock()
	cancel, done, ix := a.cancel, a.done, a.index
	a.agent, a.engine, a.index, a.cancel, a.done = nil, nil, nil, nil, nil
	a.remote, a.keys = nil, nil
	a.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	if ix != nil {
		_ = ix.Close()
	}
}

func (a *App) emitStatus(s nsync.Status) {
	a.watchForE2EE(s)
	if a.ctx != nil {
		wruntime.EventsEmit(a.ctx, "status", s)
	}
	a.updateTray(string(s.State), s.Message, s.State == nsync.StatePaused)
}

func (a *App) currentAgent() *nsync.Agent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.agent
}

func (a *App) isPaused() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.paused
}

// ---------- bound methods: state ----------

// State is everything the UI needs on load.
type State struct {
	Version    string       `json:"version"`
	Configured bool         `json:"configured"`
	Config     Config       `json:"config"`
	Status     nsync.Status `json:"status"`
	LogPath    string       `json:"log_path"`
	// E2EE: "off", "unlocked", "locked" (needs the passphrase) or
	// "migrating" (encryption being turned on elsewhere).
	E2EE string `json:"e2ee"`
	// Packaged: running as the Microsoft Store (MSIX) app, where Windows
	// manages starting at login (Settings › Apps › Startup).
	Packaged bool `json:"packaged"`
}

func (a *App) GetState() State {
	a.mu.Lock()
	cfg := a.cfg
	agent := a.agent
	a.mu.Unlock()
	a.mu.Lock()
	e2 := a.e2eeState
	a.mu.Unlock()
	if e2 == "" {
		e2 = e2eeOff
	}
	st := State{Version: a.version, Configured: cfg.Configured(), Config: cfg, E2EE: e2, Packaged: isPackaged()}
	if agent != nil {
		st.Status = agent.Status()
	}
	if a.logW != nil {
		st.LogPath = a.logW.Path()
	}
	return st
}

func (a *App) Activity() []nsync.Event {
	if ag := a.currentAgent(); ag != nil {
		return ag.Activity()
	}
	return []nsync.Event{}
}

func (a *App) Conflicts() ([]nsync.ConflictRecord, error) {
	a.mu.Lock()
	ix := a.index
	a.mu.Unlock()
	if ix == nil {
		return []nsync.ConflictRecord{}, nil
	}
	return ix.Conflicts(context.Background())
}

// ---------- bound methods: first-run setup ----------

type SetupRequest struct {
	ServerURL  string `json:"server_url"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	DeviceName string `json:"device_name"`
	Folder     string `json:"folder"`
	// CertSHA256 is the fingerprint the user chose to trust (self-signed
	// HTTPS); empty for plain HTTP or a publicly trusted certificate.
	CertSHA256 string `json:"cert_sha256"`
}

// Setup signs in, registers this computer as a device, stores the device
// token in the keychain and starts syncing. The password is used once and
// never stored; the short-lived session is logged out again.
func (a *App) Setup(req SetupRequest) error {
	u, err := client.NormalizeURL(req.ServerURL)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(req.DeviceName)
	if name == "" {
		return errors.New("give this computer a name")
	}
	folder := strings.TrimSpace(req.Folder)
	if folder == "" || !filepath.IsAbs(folder) {
		return errors.New("choose a folder to sync")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	anon := client.NewPinned(u, "", req.CertSHA256)
	sess, err := anon.Login(ctx, req.Username, req.Password)
	if err != nil {
		if client.IsStatus(err, 401) {
			return errors.New("wrong username or password")
		}
		return friendlyErr(err)
	}
	withSession := client.NewPinned(u, sess.Token, req.CertSHA256)
	dev, err := withSession.RegisterDevice(ctx, name)
	_ = withSession.Logout(ctx) // the session was only needed to register
	if err != nil {
		return err
	}
	return a.finishSetup(Config{
		ServerURL: u, Username: sess.User.Username, DeviceName: name, DeviceID: dev.Device.ID,
		Folder: folder, PollSeconds: 30, CertSHA256: req.CertSHA256,
	}, dev.Token)
}

// finishSetup stores the device token in the keychain, saves the config
// and starts syncing. Shared by password sign-in and pairing.
func (a *App) finishSetup(cfg Config, token string) error {
	if err := saveToken(cfg, token); err != nil {
		return fmt.Errorf("could not save credentials to the system keychain: %w", err)
	}
	if err := os.MkdirAll(cfg.Folder, 0o755); err != nil {
		return err
	}
	if err := saveConfig(cfg); err != nil {
		return err
	}
	a.stopSync()
	a.mu.Lock()
	a.cfg = cfg
	a.mu.Unlock()
	a.log.Info("device registered", "server", cfg.ServerURL, "device", cfg.DeviceName, "folder", cfg.Folder)
	return a.startSync()
}

func (a *App) DefaultFolder() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "NullDrive")
}

func (a *App) DefaultDeviceName() string {
	h, err := os.Hostname()
	if err != nil {
		return "My computer"
	}
	return h
}

// ChooseFolder opens the native folder picker.
func (a *App) ChooseFolder(current string) (string, error) {
	return wruntime.OpenDirectoryDialog(a.ctx, wruntime.OpenDialogOptions{
		Title:                "Choose the folder to sync",
		DefaultDirectory:     current,
		CanCreateDirectories: true,
	})
}

// ---------- bound methods: controls ----------

func (a *App) Pause() {
	a.mu.Lock()
	a.paused = true
	a.mu.Unlock()
	if ag := a.currentAgent(); ag != nil {
		ag.Pause()
	}
}

func (a *App) Resume() {
	a.mu.Lock()
	a.paused = false
	a.mu.Unlock()
	if ag := a.currentAgent(); ag != nil {
		ag.Resume()
	}
}

func (a *App) SyncNow() {
	if ag := a.currentAgent(); ag != nil {
		ag.Trigger()
	}
}

func (a *App) ConfirmMassDelete() {
	if ag := a.currentAgent(); ag != nil {
		ag.ConfirmMassDelete()
	}
}

func (a *App) OpenFolder() error {
	a.mu.Lock()
	f := a.cfg.Folder
	a.mu.Unlock()
	if f == "" {
		return errors.New("no folder configured")
	}
	return openPath(f)
}

// RevealFile shows a synced file (by server path) in the file manager.
func (a *App) RevealFile(p string) error {
	clean, err := pathutil.Clean(p)
	if err != nil {
		return err
	}
	a.mu.Lock()
	f := a.cfg.Folder
	a.mu.Unlock()
	return shellReveal(filepath.Join(f, filepath.FromSlash(clean)))
}

func (a *App) OpenLogs() error {
	p, err := logPath()
	if err != nil {
		return err
	}
	return openPath(realPath(filepath.Dir(p)))
}

func (a *App) ResolveConflict(id int64, how string) error {
	ag := a.currentAgent()
	if ag == nil {
		return errors.New("sync is not running")
	}
	return ag.ResolveConflict(context.Background(), id, how)
}

// ---------- bound methods: settings ----------

type Settings struct {
	StartOnLogin bool  `json:"start_on_login"`
	UpKBps       int64 `json:"up_kbps"`
	DownKBps     int64 `json:"down_kbps"`
	PollSeconds  int   `json:"poll_seconds"`
}

func (a *App) SaveSettings(s Settings) error {
	if s.UpKBps < 0 || s.DownKBps < 0 {
		return errors.New("limits can't be negative")
	}
	if s.PollSeconds < 5 {
		s.PollSeconds = 5
	}
	a.mu.Lock()
	prev := a.cfg.StartOnLogin
	a.cfg.StartOnLogin, a.cfg.UpKBps, a.cfg.DownKBps, a.cfg.PollSeconds = s.StartOnLogin, s.UpKBps, s.DownKBps, s.PollSeconds
	cfg := a.cfg
	eng, agent := a.engine, a.agent
	a.mu.Unlock()
	if prev != s.StartOnLogin && !isPackaged() { // packaged: Windows owns it (manifest startup task)
		if err := setStartOnLogin(s.StartOnLogin); err != nil {
			return fmt.Errorf("start on login: %w", err)
		}
	}
	if eng != nil {
		eng.Up.SetRate(s.UpKBps * 1024)
		eng.Down.SetRate(s.DownKBps * 1024)
	}
	if agent != nil {
		agent.PollInterval = time.Duration(s.PollSeconds) * time.Second
	}
	return saveConfig(cfg)
}

// ChangeFolder points sync at another folder. Nothing is deleted or moved:
// the new folder starts from a fresh index, so identical files converge and
// everything else is downloaded/uploaded.
func (a *App) ChangeFolder(folder string) error {
	if !filepath.IsAbs(folder) {
		return errors.New("choose a folder")
	}
	a.stopSync()
	a.mu.Lock()
	a.cfg.Folder = folder
	cfg := a.cfg
	a.mu.Unlock()
	if err := saveConfig(cfg); err != nil {
		return err
	}
	return a.startSync()
}

// Unlink signs this computer out: sync stops and the device token is
// removed from the keychain. Local files are left untouched. (Revoke the
// device in the web UI's Settings to invalidate the token server-side too.)
func (a *App) Unlink() error {
	a.stopSync()
	a.mu.Lock()
	cfg := a.cfg
	a.cfg = Config{PollSeconds: 30}
	a.mu.Unlock()
	if err := deleteToken(cfg); err != nil {
		a.log.Warn("delete token", "err", err)
	}
	deleteMasterKey(cfg)
	a.setE2EEState(e2eeOff)
	if ip, err := indexPath(cfg); err == nil {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			_ = os.Remove(ip + suffix)
		}
	}
	a.emitStatus(nsync.Status{State: nsync.StateIdle, Message: "Not signed in"})
	return saveConfig(Config{PollSeconds: 30})
}
