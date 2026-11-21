package admin

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/your-username/throttle/internal/gateway"
	"github.com/your-username/throttle/internal/limiter"
	"github.com/your-username/throttle/internal/metrics"
	"github.com/your-username/throttle/internal/proxy"
)

func newServer(t *testing.T, burst int) http.Handler {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hello")
	}))
	t.Cleanup(up.Close)

	pool, err := proxy.NewPool([]string{up.URL}, http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	lim := limiter.NewTokenBucket(0.0001, burst)
	t.Cleanup(lim.Close)
	reg := metrics.New()
	gw := &gateway.Gateway{
		Limiter: lim,
		Next:    pool,
		KeyFunc: gateway.NewKeyFunc("header:X-API-Key", false),
		Metrics: reg,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return (&Server{Metrics: reg, Pool: pool, Limiter: lim, Gateway: gw, DemoEnabled: true,
		Info: Info{Algorithm: "token_bucket", Requests: 1, Per: "1s", Burst: burst}}).Handler()
}

func call(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestDashboardIsServed(t *testing.T) {
	rec := call(newServer(t, 5), http.MethodGet, "/")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<title>") {
		t.Fatalf("status=%d, body does not look like the dashboard", rec.Code)
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("dashboard should set a Content-Security-Policy")
	}
}

func TestBurstFlowsThroughLimiterAndShowsInStats(t *testing.T) {
	h := newServer(t, 5)

	rec := call(h, http.MethodPost, "/api/demo/burst?count=20&client=alice")
	if rec.Code != 200 {
		t.Fatalf("burst status = %d: %s", rec.Code, rec.Body.String())
	}
	var res map[string]int64
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res["sent"] != 20 || res["allowed"] != 5 || res["blocked"] != 15 {
		t.Fatalf("burst result = %v, want 5 allowed and 15 blocked", res)
	}

	var st statsResponse
	if err := json.Unmarshal(call(h, http.MethodGet, "/api/stats").Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Metrics.Total != 20 || st.Metrics.Blocked != 15 || len(st.Backends) != 1 || st.TrackedKeys != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if st.Backends[0].Requests != 5 {
		t.Fatalf("backend served %d requests, want 5 (blocked ones must not reach it)", st.Backends[0].Requests)
	}
}

func TestBurstRejectsBadClient(t *testing.T) {
	rec := call(newServer(t, 5), http.MethodPost, "/api/demo/burst?client=bad%0d%0aHeader:x")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	rec := call(newServer(t, 5), http.MethodGet, "/metrics")
	if !strings.Contains(rec.Body.String(), "throttle_requests_total") {
		t.Fatal("metrics output missing counters")
	}
}

func TestBurstDisabled(t *testing.T) {
	s := &Server{Metrics: metrics.New(), DemoEnabled: false}
	if code := call(s.Handler(), http.MethodPost, "/api/demo/burst").Code; code == 200 {
		t.Fatal("demo endpoint must not be registered when disabled")
	}
}
