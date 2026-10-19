package api

import (
	"math"
	"sync"
	"time"
)

// limiter is a keyed token bucket: each key (a client IP, or "*" for the
// global bucket) may spend up to burst tokens at once, refilled at rate
// tokens per second. Idle keys are forgotten so memory stays bounded.
type limiter struct {
	rate  float64
	burst float64
	now   func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
	sweep   time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(perMinute, burst int, now func() time.Time) *limiter {
	if now == nil {
		now = time.Now
	}
	return &limiter{rate: float64(perMinute) / 60, burst: float64(burst), now: now, buckets: map[string]*bucket{}}
}

// allow spends one token for key. When refused it returns how long until
// a token is available (for Retry-After).
func (l *limiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.sweep) > 10*time.Minute {
		full := time.Duration(l.burst / l.rate * float64(time.Second))
		for k, b := range l.buckets {
			if now.Sub(b.last) > full {
				delete(l.buckets, k) // refilled completely: same as new
			}
		}
		l.sweep = now
	}
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
	return false, wait
}
