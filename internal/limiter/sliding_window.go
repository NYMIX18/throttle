package limiter

import (
	"math"
	"time"
)

// SlidingWindow is a sliding-window *counter*: it keeps two fixed-window
// counts per key and weights the previous window by how much of it still
// overlaps the sliding window:
//
//	estimate = prev * (1 - elapsedFraction) + curr
//
// It uses O(1) memory per key (unlike a sliding log) and avoids the
// "double burst at the window boundary" flaw of a plain fixed window.
// The estimate assumes requests in the previous window were evenly spread,
// so it is an approximation, not an exact count.
type SlidingWindow struct {
	limit  int
	window time.Duration
	s      *store[counter]
	j      *janitor
}

type counter struct {
	start time.Time
	curr  int
	prev  int
}

// NewSlidingWindow creates a sliding-window limiter allowing `limit`
// requests per `window` and starts its janitor.
func NewSlidingWindow(limit int, window time.Duration) *SlidingWindow {
	return newSlidingWindow(limit, window, time.Now, true)
}

func newSlidingWindow(limit int, window time.Duration, now func() time.Time, withJanitor bool) *SlidingWindow {
	sw := &SlidingWindow{limit: limit, window: window, s: newStore[counter](now)}
	if withJanitor {
		// After 2 windows with no traffic both counters are zero anyway.
		sw.j = startJanitor(time.Minute, func() { sw.s.evict(2 * sw.window) })
	}
	return sw
}

// Allow implements Limiter.
func (sw *SlidingWindow) Allow(key string) Decision {
	var d Decision
	sw.s.update(key,
		func(now time.Time) counter { return counter{start: now} },
		func(c *counter, now time.Time) {
			elapsed := now.Sub(c.start)
			if elapsed >= sw.window {
				n := elapsed / sw.window // whole windows that have passed
				if n == 1 {
					c.prev = c.curr
				} else {
					c.prev = 0 // idle for 2+ windows: nothing overlaps
				}
				c.curr = 0
				c.start = c.start.Add(n * sw.window)
				elapsed = now.Sub(c.start)
			}

			frac := float64(elapsed) / float64(sw.window)
			est := float64(c.prev)*(1-frac) + float64(c.curr)
			d.Limit = sw.limit

			if est+1 <= float64(sw.limit) {
				c.curr++
				d.Allowed = true
				d.Remaining = int(math.Max(0, math.Floor(float64(sw.limit)-est-1)))
				return
			}

			// Denied. Estimate when the weighted previous window will have
			// decayed enough; never later than the end of this window.
			left := sw.window - elapsed
			wait := left
			if c.prev > 0 {
				excess := est + 1 - float64(sw.limit)
				w := time.Duration(excess / float64(c.prev) * float64(sw.window))
				if w < wait {
					wait = w
				}
			}
			if wait < time.Millisecond {
				wait = time.Millisecond
			}
			d.RetryAfter = wait
		})
	return d
}

// Keys implements Limiter.
func (sw *SlidingWindow) Keys() int { return sw.s.len() }

// Close implements Limiter.
func (sw *SlidingWindow) Close() {
	if sw.j != nil {
		sw.j.close()
	}
}
