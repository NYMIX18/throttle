// Package admin serves the dashboard UI and the stats/metrics endpoints.
//
// It listens on a separate port from the proxy so that admin routes can never
// be reached through proxied traffic, and it binds to localhost by default.
// There is no authentication: do not expose it publicly.
package admin

import (
	_ "embed"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/NYMIX18/throttle/internal/limiter"
	"github.com/NYMIX18/throttle/internal/metrics"
	"github.com/NYMIX18/throttle/internal/proxy"
)

//go:embed web/index.html
var indexHTML []byte

// Info describes the running configuration, for display in the dashboard.
type Info struct {
	Algorithm string `json:"algorithm"`
	Requests  int    `json:"requests"`
	Per       string `json:"per"`
	Burst     int    `json:"burst"`
	KeyBy     string `json:"key_by"`
	Listen    string `json:"listen"`
}

// Server bundles everything the admin endpoints need.
type Server struct {
	Metrics *metrics.Registry
	Pool    *proxy.Pool
	Limiter limiter.Limiter
	Info    Info
	// Gateway is the proxy-facing handler. The demo endpoint pushes
	// synthetic requests through it in-process.
	Gateway     http.Handler
	DemoEnabled bool
}

type statsResponse struct {
	Info        Info                `json:"info"`
	Metrics     metrics.Snapshot    `json:"metrics"`
	Backends    []proxy.BackendStat `json:"backends"`
	TrackedKeys int                 `json:"tracked_keys"`
	DemoEnabled bool                `json:"demo_enabled"`
}

// Handler returns the admin HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; img-src data:")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(indexHTML)
	})
	mux.HandleFunc("GET /api/stats", s.stats)
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		s.Metrics.WritePrometheus(w)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	if s.DemoEnabled {
		mux.HandleFunc("POST /api/demo/burst", s.burst)
	}
	return mux
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, statsResponse{
		Info:        s.Info,
		Metrics:     s.Metrics.Snapshot(10),
		Backends:    s.Pool.Stats(),
		TrackedKeys: s.Limiter.Keys(),
		DemoEnabled: s.DemoEnabled,
	})
}

// burst sends `count` synthetic requests from one simulated client through
// the real gateway (limiter + proxy), so the dashboard can be exercised with
// no external load tool.
func (s *Server) burst(w http.ResponseWriter, r *http.Request) {
	count := clamp(atoi(r.URL.Query().Get("count"), 25), 1, 500)
	client := r.URL.Query().Get("client")
	if client == "" {
		client = "203.0.113.10"
	}
	if !validClient(client) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "client may only contain letters, digits and . : _ -"})
		return
	}

	var allowed, blocked, failed atomic.Int64
	var wg sync.WaitGroup
	sem := make(chan struct{}, 32) // bound concurrency
	for i := 0; i < count; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "http://throttle.local/demo", nil)
			if err != nil {
				failed.Add(1)
				return
			}
			req.RemoteAddr = net.JoinHostPort(client, "40000")
			req.Header.Set("X-API-Key", client) // honoured when key_by is a header
			rec := &discard{h: http.Header{}}
			s.Gateway.ServeHTTP(rec, req)
			switch {
			case rec.code == http.StatusTooManyRequests:
				blocked.Add(1)
			case rec.code >= 500:
				failed.Add(1)
			default:
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()

	writeJSON(w, http.StatusOK, map[string]int64{
		"sent":    int64(count),
		"allowed": allowed.Load(),
		"blocked": blocked.Load(),
		"failed":  failed.Load(),
	})
}

// discard is a ResponseWriter that throws the body away and remembers the status.
type discard struct {
	h    http.Header
	code int
}

func (d *discard) Header() http.Header { return d.h }
func (d *discard) WriteHeader(c int) {
	if d.code == 0 {
		d.code = c
	}
}
func (d *discard) Write(b []byte) (int, error) {
	if d.code == 0 {
		d.code = http.StatusOK
	}
	return len(b), nil
}

func validClient(s string) bool {
	if len(s) > 64 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == ':', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

func atoi(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func clamp(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
