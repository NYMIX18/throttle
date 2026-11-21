package gateway

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/NYMIX18/throttle/internal/limiter"
	"github.com/NYMIX18/throttle/internal/metrics"
)

func newGateway(t *testing.T, burst int, keyBy string, trust bool) (*Gateway, *metrics.Registry) {
	t.Helper()
	lim := limiter.NewTokenBucket(0.0001, burst) // effectively no refill during a test
	t.Cleanup(lim.Close)
	reg := metrics.New()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })
	return &Gateway{
		Limiter: lim,
		Next:    next,
		KeyFunc: NewKeyFunc(keyBy, trust),
		Metrics: reg,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, reg
}

func do(g *Gateway, remote string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	req.RemoteAddr = remote
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	return rec
}

func TestBlocksAfterBurstWithHeaders(t *testing.T) {
	g, reg := newGateway(t, 2, "ip", false)

	for i, wantRemaining := range []string{"1", "0"} {
		rec := do(g, "10.0.0.1:5000", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, rec.Code)
		}
		if rec.Header().Get("X-RateLimit-Limit") != "2" || rec.Header().Get("X-RateLimit-Remaining") != wantRemaining {
			t.Fatalf("request %d: headers = %v", i, rec.Header())
		}
	}

	rec := do(g, "10.0.0.1:5001", nil) // same IP, different source port
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	secs, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || secs < 1 {
		t.Fatalf("Retry-After = %q, want integer >= 1", rec.Header().Get("Retry-After"))
	}
	if !strings.Contains(rec.Body.String(), "rate_limit_exceeded") {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if rec.Header().Get("X-Request-Id") == "" {
		t.Fatal("missing X-Request-Id")
	}

	s := reg.Snapshot(5)
	if s.Total != 3 || s.Allowed != 2 || s.Blocked != 1 {
		t.Fatalf("metrics = %+v", s)
	}
}

func TestDifferentIPsAreIndependent(t *testing.T) {
	g, _ := newGateway(t, 1, "ip", false)
	if do(g, "10.0.0.1:1", nil).Code != 200 {
		t.Fatal("first client should pass")
	}
	if do(g, "10.0.0.2:1", nil).Code != 200 {
		t.Fatal("second client should have its own budget")
	}
	if do(g, "10.0.0.1:2", nil).Code != 429 {
		t.Fatal("first client should now be limited")
	}
}

func TestHeaderKey(t *testing.T) {
	g, _ := newGateway(t, 1, "header:X-API-Key", false)
	// Same IP, different API keys: independent budgets.
	if do(g, "10.0.0.1:1", map[string]string{"X-API-Key": "alice"}).Code != 200 {
		t.Fatal("alice should pass")
	}
	if do(g, "10.0.0.1:1", map[string]string{"X-API-Key": "bob"}).Code != 200 {
		t.Fatal("bob should pass")
	}
	if do(g, "10.0.0.1:1", map[string]string{"X-API-Key": "alice"}).Code != 429 {
		t.Fatal("alice should be limited")
	}
}

func TestForwardedForOnlyTrustedWhenEnabled(t *testing.T) {
	spoof := map[string]string{"X-Forwarded-For": "1.1.1.1"}

	untrusting, _ := newGateway(t, 1, "ip", false)
	do(untrusting, "10.0.0.1:1", spoof)
	if do(untrusting, "10.0.0.1:1", map[string]string{"X-Forwarded-For": "2.2.2.2"}).Code != 429 {
		t.Fatal("with trust disabled, X-Forwarded-For must be ignored")
	}

	trusting, _ := newGateway(t, 1, "ip", true)
	do(trusting, "10.0.0.1:1", spoof)
	if do(trusting, "10.0.0.1:1", map[string]string{"X-Forwarded-For": "2.2.2.2"}).Code != 200 {
		t.Fatal("with trust enabled, a different forwarded client gets its own budget")
	}
}
