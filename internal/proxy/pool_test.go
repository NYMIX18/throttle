package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func upstream(name string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		io.WriteString(w, name)
	}))
}

func get(t *testing.T, h http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.test/x", nil))
	return rec
}

func TestRoundRobin(t *testing.T) {
	a, b := upstream("a"), upstream("b")
	defer a.Close()
	defer b.Close()
	p, err := NewPool([]string{a.URL, b.URL}, http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]int{}
	for i := 0; i < 10; i++ {
		seen[get(t, p).Body.String()]++
	}
	if seen["a"] != 5 || seen["b"] != 5 {
		t.Fatalf("distribution = %v, want 5/5", seen)
	}
}

func TestNextSkipsUnhealthy(t *testing.T) {
	p, err := NewPool([]string{"http://a:1", "http://b:1", "http://c:1"}, http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	p.backends[1].healthy.Store(false)

	counts := map[string]int{}
	for i := 0; i < 6; i++ {
		counts[p.Next().URL.Host]++
	}
	if counts["b:1"] != 0 || counts["a:1"] == 0 || counts["c:1"] == 0 {
		t.Fatalf("counts = %v", counts)
	}
}

func TestNoHealthyUpstreamIs503(t *testing.T) {
	p, _ := NewPool([]string{"http://a:1"}, http.DefaultTransport)
	p.backends[0].healthy.Store(false)
	if code := get(t, p).Code; code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", code)
	}
}

func TestDeadBackendGives502AndIsMarkedUnhealthy(t *testing.T) {
	dead := upstream("dead")
	url := dead.URL
	dead.Close() // nothing is listening any more

	p, _ := NewPool([]string{url}, http.DefaultTransport)
	if code := get(t, p).Code; code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", code)
	}
	if p.Stats()[0].Healthy {
		t.Fatal("backend should be marked unhealthy after a transport error")
	}
	if p.Stats()[0].Failures != 1 {
		t.Fatalf("failures = %d, want 1", p.Stats()[0].Failures)
	}
}

func TestHealthCheckRevivesBackend(t *testing.T) {
	var healthy atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if healthy.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p, _ := NewPool([]string{srv.URL}, http.DefaultTransport)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.StartHealthChecks(ctx, "/healthz", 20*time.Millisecond, time.Second)

	waitFor(t, func() bool { return !p.Stats()[0].Healthy }, "backend to be marked down")
	healthy.Store(true)
	waitFor(t, func() bool { return p.Stats()[0].Healthy }, "backend to recover")
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestForwardedHeadersCannotBeSpoofed(t *testing.T) {
	seen := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("X-Forwarded-For")
	}))
	defer srv.Close()

	p, _ := NewPool([]string{srv.URL}, http.DefaultTransport)
	req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	req.RemoteAddr = "198.51.100.7:1234"
	req.Header.Set("X-Forwarded-For", "6.6.6.6")
	p.ServeHTTP(httptest.NewRecorder(), req)

	if got := <-seen; got != "198.51.100.7" {
		t.Fatalf("upstream saw X-Forwarded-For = %q, want only the real client IP", got)
	}
}
