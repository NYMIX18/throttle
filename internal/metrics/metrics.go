// Package metrics collects request statistics with low overhead: global
// counters are lock-free atomics; the per-second time series and per-client
// table share one mutex held only for a few instructions.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	seriesLen  = 120 // seconds of history kept for the dashboard chart
	maxClients = 2048
	otherKey   = "(other)"
)

// latencyBounds are the upper bounds (ms) of the latency histogram buckets.
var latencyBounds = [...]float64{1, 5, 10, 25, 50, 100, 250, 500, 1000}

type slot struct {
	sec     int64
	allowed uint64
	blocked uint64
}

type clientStat struct {
	total   uint64
	blocked uint64
}

// Registry holds all counters. Create it with New; do not copy it.
type Registry struct {
	start time.Time

	total          atomic.Uint64
	allowed        atomic.Uint64
	blocked        atomic.Uint64
	upstreamErrors atomic.Uint64
	classes        [6]atomic.Uint64 // index = status/100 (1xx..5xx)

	latency       [len(latencyBounds) + 1]atomic.Uint64
	latencyMicros atomic.Uint64

	mu      sync.Mutex
	series  [seriesLen]slot
	clients map[string]*clientStat
}

// New creates an empty Registry.
func New() *Registry {
	return &Registry{start: time.Now(), clients: make(map[string]*clientStat)}
}

// Observe records one finished request. Latency is only tracked for requests
// that were allowed through to an upstream.
func (r *Registry) Observe(key string, allowed bool, status int, took time.Duration) {
	r.total.Add(1)
	if allowed {
		r.allowed.Add(1)
		if status >= 502 && status <= 504 {
			r.upstreamErrors.Add(1)
		}
		ms := float64(took) / float64(time.Millisecond)
		idx := len(latencyBounds)
		for i, b := range latencyBounds {
			if ms <= b {
				idx = i
				break
			}
		}
		r.latency[idx].Add(1)
		r.latencyMicros.Add(uint64(took / time.Microsecond))
	} else {
		r.blocked.Add(1)
	}

	class := status / 100
	if class < 1 || class > 5 {
		class = 5
	}
	r.classes[class].Add(1)

	sec := time.Now().Unix()
	r.mu.Lock()
	sl := &r.series[sec%seriesLen]
	if sl.sec != sec {
		*sl = slot{sec: sec}
	}
	if allowed {
		sl.allowed++
	} else {
		sl.blocked++
	}
	c := r.clients[key]
	if c == nil {
		if len(r.clients) >= maxClients {
			// Bound memory: attackers can mint unlimited distinct keys.
			key = otherKey
			c = r.clients[key]
		}
		if c == nil {
			c = &clientStat{}
			r.clients[key] = c
		}
	}
	c.total++
	if !allowed {
		c.blocked++
	}
	r.mu.Unlock()
}

// Point is one second of the time series.
type Point struct {
	T       int64  `json:"t"`
	Allowed uint64 `json:"allowed"`
	Blocked uint64 `json:"blocked"`
}

// ClientStat is the cumulative traffic of one rate-limit key.
type ClientStat struct {
	Key     string `json:"key"`
	Total   uint64 `json:"total"`
	Blocked uint64 `json:"blocked"`
}

// Snapshot is a point-in-time copy of the metrics, safe to marshal.
type Snapshot struct {
	UptimeSeconds  float64      `json:"uptime_seconds"`
	Total          uint64       `json:"total"`
	Allowed        uint64       `json:"allowed"`
	Blocked        uint64       `json:"blocked"`
	UpstreamErrors uint64       `json:"upstream_errors"`
	P50Ms          float64      `json:"p50_ms"`
	P95Ms          float64      `json:"p95_ms"`
	Series         []Point      `json:"series"`
	TopClients     []ClientStat `json:"top_clients"`
}

// Snapshot returns the current state. topN limits the client table.
func (r *Registry) Snapshot(topN int) Snapshot {
	s := Snapshot{
		UptimeSeconds:  time.Since(r.start).Seconds(),
		Total:          r.total.Load(),
		Allowed:        r.allowed.Load(),
		Blocked:        r.blocked.Load(),
		UpstreamErrors: r.upstreamErrors.Load(),
		P50Ms:          r.quantile(0.50),
		P95Ms:          r.quantile(0.95),
		Series:         make([]Point, seriesLen),
	}

	now := time.Now().Unix()
	r.mu.Lock()
	for i := 0; i < seriesLen; i++ {
		t := now - int64(seriesLen-1-i)
		p := Point{T: t}
		if sl := r.series[t%seriesLen]; sl.sec == t {
			p.Allowed, p.Blocked = sl.allowed, sl.blocked
		}
		s.Series[i] = p
	}
	clients := make([]ClientStat, 0, len(r.clients))
	for k, c := range r.clients {
		clients = append(clients, ClientStat{Key: k, Total: c.total, Blocked: c.blocked})
	}
	r.mu.Unlock()

	sort.Slice(clients, func(i, j int) bool {
		if clients[i].Total != clients[j].Total {
			return clients[i].Total > clients[j].Total
		}
		return clients[i].Key < clients[j].Key
	})
	if len(clients) > topN {
		clients = clients[:topN]
	}
	s.TopClients = clients
	return s
}

// quantile estimates a latency quantile (ms) from the histogram. The result
// is the upper bound of the bucket containing the quantile, so it is
// conservative; the last bucket reports the largest finite bound.
func (r *Registry) quantile(q float64) float64 {
	var counts [len(latencyBounds) + 1]uint64
	var total uint64
	for i := range counts {
		counts[i] = r.latency[i].Load()
		total += counts[i]
	}
	if total == 0 {
		return 0
	}
	target := q * float64(total)
	var cum uint64
	for i, c := range counts {
		cum += c
		if float64(cum) >= target {
			if i < len(latencyBounds) {
				return latencyBounds[i]
			}
			break
		}
	}
	return latencyBounds[len(latencyBounds)-1]
}

// WritePrometheus writes the counters in the Prometheus text format.
func (r *Registry) WritePrometheus(w io.Writer) {
	counter := func(name, help string, v uint64) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, v)
	}
	counter("throttle_requests_total", "Requests received.", r.total.Load())
	counter("throttle_requests_allowed_total", "Requests allowed by the rate limiter.", r.allowed.Load())
	counter("throttle_requests_blocked_total", "Requests rejected with 429.", r.blocked.Load())
	counter("throttle_upstream_errors_total", "Allowed requests that failed with 502-504.", r.upstreamErrors.Load())

	fmt.Fprintln(w, "# HELP throttle_responses_total Responses by status class.")
	fmt.Fprintln(w, "# TYPE throttle_responses_total counter")
	for c := 1; c <= 5; c++ {
		fmt.Fprintf(w, "throttle_responses_total{class=\"%dxx\"} %d\n", c, r.classes[c].Load())
	}

	fmt.Fprintln(w, "# HELP throttle_upstream_duration_ms Time to serve allowed requests.")
	fmt.Fprintln(w, "# TYPE throttle_upstream_duration_ms histogram")
	var cum uint64
	for i, b := range latencyBounds {
		cum += r.latency[i].Load()
		fmt.Fprintf(w, "throttle_upstream_duration_ms_bucket{le=\"%g\"} %d\n", b, cum)
	}
	cum += r.latency[len(latencyBounds)].Load()
	fmt.Fprintf(w, "throttle_upstream_duration_ms_bucket{le=\"+Inf\"} %d\n", cum)
	fmt.Fprintf(w, "throttle_upstream_duration_ms_sum %.3f\n", float64(r.latencyMicros.Load())/1000)
	fmt.Fprintf(w, "throttle_upstream_duration_ms_count %d\n", cum)
}
