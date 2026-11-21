// Package gateway is the request pipeline in front of the proxy pool:
// request ID -> key extraction -> rate limit -> (proxy | 429) -> metrics+log.
package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/NYMIX18/throttle/internal/limiter"
	"github.com/NYMIX18/throttle/internal/metrics"
)

// KeyFunc extracts the rate-limit key (who is being limited) from a request.
type KeyFunc func(*http.Request) string

// Gateway applies rate limiting in front of Next.
type Gateway struct {
	Limiter limiter.Limiter
	Next    http.Handler
	KeyFunc KeyFunc
	Metrics *metrics.Registry // optional
	Logger  *slog.Logger      // optional
}

// ServeHTTP implements http.Handler.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	id := r.Header.Get("X-Request-Id")
	if id == "" || len(id) > 64 {
		id = newRequestID()
		r.Header.Set("X-Request-Id", id)
	}
	w.Header().Set("X-Request-Id", id)

	key := g.KeyFunc(r)
	d := g.Limiter.Allow(key)

	h := w.Header()
	h.Set("X-RateLimit-Limit", strconv.Itoa(d.Limit))
	h.Set("X-RateLimit-Remaining", strconv.Itoa(d.Remaining))

	sw := &statusWriter{ResponseWriter: w}
	if d.Allowed {
		g.Next.ServeHTTP(sw, r)
	} else {
		secs := int(math.Ceil(d.RetryAfter.Seconds()))
		if secs < 1 {
			secs = 1
		}
		h.Set("Retry-After", strconv.Itoa(secs))
		h.Set("Content-Type", "application/json")
		sw.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprintf(sw, "{\"error\":\"rate_limit_exceeded\",\"retry_after_seconds\":%d}\n", secs)
	}

	took := time.Since(start)
	status := sw.status()
	if g.Metrics != nil {
		g.Metrics.Observe(key, d.Allowed, status, took)
	}
	if g.Logger != nil {
		lvl := slog.LevelDebug
		if !d.Allowed || status >= 500 {
			lvl = slog.LevelInfo
		}
		g.Logger.LogAttrs(r.Context(), lvl, "request",
			slog.String("id", id),
			slog.String("key", key),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", status),
			slog.Bool("allowed", d.Allowed),
			slog.Duration("took", took),
		)
	}
}

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// NewKeyFunc builds a KeyFunc from the key_by setting: "ip" or "header:<Name>".
// With a header key, requests lacking the header fall back to the client IP.
//
// trustForwarded makes the IP key use the first X-Forwarded-For entry. Only
// enable it when a trusted load balancer sets that header; otherwise clients
// can spoof it and dodge their limit.
func NewKeyFunc(keyBy string, trustForwarded bool) KeyFunc {
	ip := ipKey(trustForwarded)
	if name, ok := strings.CutPrefix(keyBy, "header:"); ok {
		return func(r *http.Request) string {
			if v := r.Header.Get(name); v != "" {
				if len(v) > 128 {
					v = v[:128] // bound key size
				}
				return "key:" + v
			}
			return ip(r)
		}
	}
	return ip
}

func ipKey(trustForwarded bool) KeyFunc {
	return func(r *http.Request) string {
		if trustForwarded {
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				first, _, _ := strings.Cut(xff, ",")
				if s := strings.TrimSpace(first); s != "" {
					return s
				}
			}
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			return r.RemoteAddr
		}
		return host
	}
}

// statusWriter records the status code written by the downstream handler.
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.code == 0 {
		s.code = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.code == 0 {
		s.code = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) status() int {
	if s.code == 0 {
		return http.StatusOK
	}
	return s.code
}

// Flush keeps streaming responses working through the wrapper.
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }
