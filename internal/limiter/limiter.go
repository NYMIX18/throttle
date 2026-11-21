// Package limiter implements per-key rate limiting algorithms.
//
// Every algorithm satisfies the Limiter interface, so the gateway does not
// care which one is configured. State is kept per key in a sharded map to keep
// lock contention low under concurrent load.
package limiter

import (
	"errors"
	"fmt"
	"time"
)

// Algorithm names accepted by New.
const (
	AlgoTokenBucket   = "token_bucket"
	AlgoSlidingWindow = "sliding_window"
)

// Decision is the outcome of a single Allow call.
type Decision struct {
	Allowed   bool
	Limit     int
	Remaining int
	// RetryAfter is a hint for how long a denied caller should wait.
	RetryAfter time.Duration
}

// Limiter decides whether a request identified by key may proceed.
// Implementations are safe for concurrent use.
type Limiter interface {
	Allow(key string) Decision
	// Keys reports how many keys currently have state (for observability).
	Keys() int
	// Close stops background cleanup.
	Close()
}

// Config describes a limiter. The long-run rate is Requests per Per.
type Config struct {
	Algorithm string
	Requests  int
	Per       time.Duration
	// Burst only applies to the token bucket. Defaults to Requests.
	Burst int
}

// New builds a Limiter from a Config.
func New(c Config) (Limiter, error) {
	if c.Requests <= 0 {
		return nil, errors.New("limiter: requests must be > 0")
	}
	if c.Per <= 0 {
		return nil, errors.New("limiter: per must be > 0")
	}
	switch c.Algorithm {
	case AlgoTokenBucket, "":
		burst := c.Burst
		if burst <= 0 {
			burst = c.Requests
		}
		rate := float64(c.Requests) / c.Per.Seconds()
		return NewTokenBucket(rate, burst), nil
	case AlgoSlidingWindow:
		return NewSlidingWindow(c.Requests, c.Per), nil
	default:
		return nil, fmt.Errorf("limiter: unknown algorithm %q", c.Algorithm)
	}
}
