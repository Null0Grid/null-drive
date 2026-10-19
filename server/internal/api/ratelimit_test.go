package api

import (
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func TestLimiter(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	l := newLimiter(6, 3, clk.now) // 1 token / 10s, burst 3
	for i := 0; i < 3; i++ {
		if ok, _ := l.allow("a"); !ok {
			t.Fatalf("burst request %d refused", i)
		}
	}
	ok, wait := l.allow("a")
	if ok || wait != 10*time.Second {
		t.Fatalf("4th: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := l.allow("b"); !ok {
		t.Fatal("keys must be independent")
	}
	clk.t = clk.t.Add(10 * time.Second)
	if ok, _ := l.allow("a"); !ok {
		t.Fatal("token should have refilled")
	}
	if ok, _ := l.allow("a"); ok {
		t.Fatal("only one token refilled")
	}
	// Idle keys are swept.
	clk.t = clk.t.Add(time.Hour)
	_, _ = l.allow("c")
	if _, ok := l.buckets["a"]; ok {
		t.Fatal("idle bucket not swept")
	}
}
