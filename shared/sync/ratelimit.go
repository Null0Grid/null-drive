package sync

import (
	"context"
	"io"
	"sync"
	"time"
)

// Limiter is a token bucket shared by all transfers in one direction, so
// "limit upload to 2 MB/s" holds no matter how many files are in flight.
// A nil *Limiter or a rate <= 0 means unlimited.
type Limiter struct {
	mu     sync.Mutex
	rate   float64 // bytes per second
	tokens float64
	last   time.Time
	now    func() time.Time
	sleep  func(context.Context, time.Duration) error
}

// NewLimiter returns a limiter for bytesPerSec (0 = unlimited).
func NewLimiter(bytesPerSec int64) *Limiter {
	return &Limiter{rate: float64(bytesPerSec), now: time.Now, sleep: sleepCtx}
}

// SetRate changes the limit at runtime (from the settings screen).
func (l *Limiter) SetRate(bytesPerSec int64) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.rate = float64(bytesPerSec)
	l.mu.Unlock()
}

// wait blocks until n bytes may be transferred.
func (l *Limiter) wait(ctx context.Context, n int) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	if l.rate <= 0 {
		l.mu.Unlock()
		return nil
	}
	now := l.now()
	if l.last.IsZero() {
		l.last = now
		l.tokens = l.rate // allow an initial burst of one second
	}
	l.tokens += now.Sub(l.last).Seconds() * l.rate
	if l.tokens > l.rate {
		l.tokens = l.rate
	}
	l.last = now
	l.tokens -= float64(n)
	deficit := -l.tokens
	rate := l.rate
	l.mu.Unlock()
	if deficit <= 0 {
		return nil
	}
	return l.sleep(ctx, time.Duration(deficit/rate*float64(time.Second)))
}

// Reader wraps r so reads are paced by the limiter.
func (l *Limiter) Reader(ctx context.Context, r io.Reader) io.Reader {
	if l == nil {
		return r
	}
	return &limitedReader{ctx: ctx, r: r, l: l}
}

type limitedReader struct {
	ctx context.Context
	r   io.Reader
	l   *Limiter
}

func (lr *limitedReader) Read(p []byte) (int, error) {
	if len(p) > 32<<10 {
		p = p[:32<<10] // small chunks keep pacing smooth
	}
	n, err := lr.r.Read(p)
	if n > 0 {
		if werr := lr.l.wait(lr.ctx, n); werr != nil {
			return n, werr
		}
	}
	return n, err
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
