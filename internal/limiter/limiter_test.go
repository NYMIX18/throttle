package limiter

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock lets tests control time precisely, so they are fast and
// deterministic (no time.Sleep).
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestTokenBucketBurstThenDeny(t *testing.T) {
	clk := newClock()
	tb := newTokenBucket(1, 3, clk.Now, false) // 1 token/s, burst 3

	for i, wantRemaining := range []int{2, 1, 0} {
		d := tb.Allow("a")
		if !d.Allowed || d.Remaining != wantRemaining {
			t.Fatalf("request %d: got %+v, want allowed with remaining=%d", i, d, wantRemaining)
		}
	}
	d := tb.Allow("a")
	if d.Allowed {
		t.Fatalf("4th request should be denied, got %+v", d)
	}
	if d.RetryAfter != time.Second {
		t.Fatalf("RetryAfter = %v, want 1s", d.RetryAfter)
	}
}

func TestTokenBucketRefill(t *testing.T) {
	clk := newClock()
	tb := newTokenBucket(1, 3, clk.Now, false)
	for i := 0; i < 3; i++ {
		tb.Allow("a")
	}
	clk.Advance(2 * time.Second) // earns 2 tokens

	if !tb.Allow("a").Allowed || !tb.Allow("a").Allowed {
		t.Fatal("expected 2 requests allowed after refill")
	}
	if tb.Allow("a").Allowed {
		t.Fatal("3rd request after a 2s refill should be denied")
	}
}

func TestTokenBucketCapsAtBurst(t *testing.T) {
	clk := newClock()
	tb := newTokenBucket(1, 3, clk.Now, false)
	tb.Allow("a")
	clk.Advance(time.Hour) // must not accumulate beyond burst

	allowed := 0
	for i := 0; i < 10; i++ {
		if tb.Allow("a").Allowed {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("allowed = %d, want 3 (burst cap)", allowed)
	}
}

func TestKeysAreIsolated(t *testing.T) {
	clk := newClock()
	tb := newTokenBucket(1, 1, clk.Now, false)
	if !tb.Allow("alice").Allowed {
		t.Fatal("alice first request should pass")
	}
	if tb.Allow("alice").Allowed {
		t.Fatal("alice second request should be denied")
	}
	if !tb.Allow("bob").Allowed {
		t.Fatal("bob must not be affected by alice")
	}
}

// With a near-zero refill rate, exactly `burst` requests may succeed no matter
// how many goroutines race. Run with -race.
func TestTokenBucketConcurrent(t *testing.T) {
	const burst = 100
	tb := newTokenBucket(0.0001, burst, time.Now, false)

	var allowed atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				if tb.Allow("shared").Allowed {
					allowed.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	if got := allowed.Load(); got != burst {
		t.Fatalf("allowed = %d, want exactly %d", got, burst)
	}
}

func TestTokenBucketEviction(t *testing.T) {
	clk := newClock()
	tb := newTokenBucket(1, 3, clk.Now, false)
	tb.Allow("a")
	tb.Allow("b")
	if tb.Keys() != 2 {
		t.Fatalf("Keys = %d, want 2", tb.Keys())
	}
	clk.Advance(2 * time.Hour)
	tb.Allow("b") // b is active again
	if n := tb.s.evict(tb.idleTTL); n != 1 {
		t.Fatalf("evicted %d keys, want 1", n)
	}
	if tb.Keys() != 1 {
		t.Fatalf("Keys = %d, want 1", tb.Keys())
	}
}

func TestSlidingWindowLimit(t *testing.T) {
	clk := newClock()
	sw := newSlidingWindow(5, 10*time.Second, clk.Now, false)

	for i := 0; i < 5; i++ {
		if !sw.Allow("a").Allowed {
			t.Fatalf("request %d should be allowed", i)
		}
	}
	d := sw.Allow("a")
	if d.Allowed {
		t.Fatal("6th request in the window should be denied")
	}
	if d.RetryAfter <= 0 {
		t.Fatalf("RetryAfter = %v, want > 0", d.RetryAfter)
	}
}

// 5s into the next window, the previous window (5 requests) still counts for
// 50%: estimate = 5*0.5 = 2.5, so exactly 2 more requests fit under a limit of 5.
func TestSlidingWindowWeightsPreviousWindow(t *testing.T) {
	clk := newClock()
	sw := newSlidingWindow(5, 10*time.Second, clk.Now, false)
	for i := 0; i < 5; i++ {
		sw.Allow("a")
	}

	clk.Advance(10 * time.Second)
	if sw.Allow("a").Allowed {
		t.Fatal("at the window boundary the previous window still counts fully")
	}

	clk.Advance(5 * time.Second)
	allowed := 0
	for i := 0; i < 5; i++ {
		if sw.Allow("a").Allowed {
			allowed++
		}
	}
	if allowed != 2 {
		t.Fatalf("allowed = %d, want 2", allowed)
	}
}

func TestSlidingWindowResetsAfterIdle(t *testing.T) {
	clk := newClock()
	sw := newSlidingWindow(5, 10*time.Second, clk.Now, false)
	for i := 0; i < 5; i++ {
		sw.Allow("a")
	}
	clk.Advance(30 * time.Second)

	allowed := 0
	for i := 0; i < 5; i++ {
		if sw.Allow("a").Allowed {
			allowed++
		}
	}
	if allowed != 5 {
		t.Fatalf("allowed = %d, want 5 after long idle", allowed)
	}
}

func TestNewValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		ok   bool
	}{
		{"token bucket", Config{Algorithm: AlgoTokenBucket, Requests: 10, Per: time.Second, Burst: 20}, true},
		{"default algorithm", Config{Requests: 10, Per: time.Second}, true},
		{"sliding window", Config{Algorithm: AlgoSlidingWindow, Requests: 10, Per: time.Second}, true},
		{"zero requests", Config{Requests: 0, Per: time.Second}, false},
		{"zero per", Config{Requests: 1}, false},
		{"unknown algorithm", Config{Algorithm: "nope", Requests: 1, Per: time.Second}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, err := New(tc.cfg)
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
			if l != nil {
				l.Close()
			}
		})
	}
}

func ExampleTokenBucket() {
	tb := NewTokenBucket(1, 2) // 1 req/s, burst of 2
	defer tb.Close()
	for i := 0; i < 3; i++ {
		fmt.Println(tb.Allow("client").Allowed)
	}
	// Output:
	// true
	// true
	// false
}
