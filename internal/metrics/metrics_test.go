package metrics

import (
	"strings"
	"testing"
	"time"
)

func TestObserveAndSnapshot(t *testing.T) {
	r := New()
	r.Observe("a", true, 200, 3*time.Millisecond)
	r.Observe("a", true, 200, 3*time.Millisecond)
	r.Observe("a", false, 429, 0)
	r.Observe("b", true, 502, 80*time.Millisecond)

	s := r.Snapshot(10)
	if s.Total != 4 || s.Allowed != 3 || s.Blocked != 1 || s.UpstreamErrors != 1 {
		t.Fatalf("unexpected totals: %+v", s)
	}
	if len(s.Series) != seriesLen {
		t.Fatalf("series length = %d, want %d", len(s.Series), seriesLen)
	}
	last := s.Series[len(s.Series)-1]
	// The current second is the last point (tests run within one or two seconds).
	var allowed, blocked uint64
	for _, p := range s.Series {
		allowed += p.Allowed
		blocked += p.Blocked
	}
	if allowed != 3 || blocked != 1 {
		t.Fatalf("series sums = %d/%d, want 3/1 (last point %+v)", allowed, blocked, last)
	}
	if len(s.TopClients) != 2 || s.TopClients[0].Key != "a" || s.TopClients[0].Blocked != 1 {
		t.Fatalf("unexpected top clients: %+v", s.TopClients)
	}
}

func TestQuantile(t *testing.T) {
	r := New()
	for i := 0; i < 90; i++ {
		r.Observe("k", true, 200, 2*time.Millisecond) // bucket le=5
	}
	for i := 0; i < 10; i++ {
		r.Observe("k", true, 200, 200*time.Millisecond) // bucket le=250
	}
	if got := r.quantile(0.50); got != 5 {
		t.Fatalf("p50 = %v, want 5", got)
	}
	if got := r.quantile(0.95); got != 250 {
		t.Fatalf("p95 = %v, want 250", got)
	}
}

func TestClientTableIsBounded(t *testing.T) {
	r := New()
	for i := 0; i < maxClients+500; i++ {
		r.Observe(string(rune('a'+i%26))+strings.Repeat("x", i), true, 200, time.Millisecond)
	}
	r.mu.Lock()
	n := len(r.clients)
	r.mu.Unlock()
	if n > maxClients+1 { // +1 for the "(other)" bucket
		t.Fatalf("client table grew to %d, want <= %d", n, maxClients+1)
	}
}

func TestPrometheusOutput(t *testing.T) {
	r := New()
	r.Observe("a", true, 200, time.Millisecond)
	var sb strings.Builder
	r.WritePrometheus(&sb)
	out := sb.String()
	for _, want := range []string{
		"throttle_requests_total 1",
		`throttle_responses_total{class="2xx"} 1`,
		`throttle_upstream_duration_ms_bucket{le="+Inf"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}
}
