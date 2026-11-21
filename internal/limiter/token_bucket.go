package limiter

import "time"

// TokenBucket allows bursts up to `burst` requests and refills at `rate`
// tokens per second. Each request consumes one token.
//
// State per key is just (tokens, lastRefill): tokens are refilled lazily on
// each call, so there is no timer per key.
type TokenBucket struct {
	rate    float64 // tokens per second
	burst   int
	idleTTL time.Duration
	s       *store[bucket]
	j       *janitor
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewTokenBucket creates a token bucket limiter and starts its janitor.
func NewTokenBucket(rate float64, burst int) *TokenBucket {
	return newTokenBucket(rate, burst, time.Now, true)
}

func newTokenBucket(rate float64, burst int, now func() time.Time, withJanitor bool) *TokenBucket {
	// A key may only be forgotten once its bucket would have refilled to
	// full anyway; otherwise eviction would hand out a free burst.
	ttl := time.Duration(float64(burst) / rate * float64(time.Second))
	if ttl < time.Minute {
		ttl = time.Minute
	}
	tb := &TokenBucket{rate: rate, burst: burst, idleTTL: ttl, s: newStore[bucket](now)}
	if withJanitor {
		tb.j = startJanitor(time.Minute, func() { tb.s.evict(tb.idleTTL) })
	}
	return tb
}

// Allow implements Limiter.
func (tb *TokenBucket) Allow(key string) Decision {
	var d Decision
	tb.s.update(key,
		func(now time.Time) bucket {
			return bucket{tokens: float64(tb.burst), last: now}
		},
		func(b *bucket, now time.Time) {
			if el := now.Sub(b.last).Seconds(); el > 0 {
				b.tokens += el * tb.rate
				if b.tokens > float64(tb.burst) {
					b.tokens = float64(tb.burst)
				}
			}
			b.last = now
			d.Limit = tb.burst
			if b.tokens >= 1 {
				b.tokens--
				d.Allowed = true
				d.Remaining = int(b.tokens)
				return
			}
			d.RetryAfter = time.Duration((1 - b.tokens) / tb.rate * float64(time.Second))
		})
	return d
}

// Keys implements Limiter.
func (tb *TokenBucket) Keys() int { return tb.s.len() }

// Close implements Limiter.
func (tb *TokenBucket) Close() {
	if tb.j != nil {
		tb.j.close()
	}
}
