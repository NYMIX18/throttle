// Package proxy implements a round-robin reverse proxy pool with active and
// passive health checking.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// Backend is one upstream server.
type Backend struct {
	URL *url.URL

	healthy  atomic.Bool
	requests atomic.Uint64
	failures atomic.Uint64
	inflight atomic.Int64
	rp       *httputil.ReverseProxy
}

// BackendStat is a JSON-friendly view of a Backend.
type BackendStat struct {
	URL      string `json:"url"`
	Healthy  bool   `json:"healthy"`
	Requests uint64 `json:"requests"`
	Failures uint64 `json:"failures"`
	Inflight int64  `json:"inflight"`
}

// Pool load-balances requests across backends.
type Pool struct {
	backends []*Backend
	next     atomic.Uint64
}

// NewTransport returns an HTTP transport tuned for proxying: bounded dial
// time, keep-alive connection reuse, and a response-header timeout so that a
// hung upstream cannot hold client connections forever.
func NewTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	t.ResponseHeaderTimeout = 30 * time.Second
	t.MaxIdleConns = 256
	t.MaxIdleConnsPerHost = 64
	t.IdleConnTimeout = 90 * time.Second
	return t
}

// NewPool builds a pool from upstream URLs. All backends start healthy
// (optimistic) until the first probe says otherwise.
func NewPool(upstreams []string, rt http.RoundTripper) (*Pool, error) {
	if len(upstreams) == 0 {
		return nil, errors.New("proxy: no upstreams")
	}
	p := &Pool{}
	for _, raw := range upstreams {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("proxy: bad upstream %q: %w", raw, err)
		}
		b := &Backend{URL: u}
		b.healthy.Store(true)
		b.rp = &httputil.ReverseProxy{
			// Rewrite (not Director) strips client-supplied X-Forwarded-*
			// headers before SetXForwarded adds trustworthy ones.
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(u)
				pr.SetXForwarded()
			},
			Transport: rt,
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				if errors.Is(err, context.Canceled) {
					return // the client went away; not the backend's fault
				}
				b.failures.Add(1)
				b.healthy.Store(false) // passive check: stop routing here until a probe succeeds
				http.Error(w, "bad gateway", http.StatusBadGateway)
			},
		}
		p.backends = append(p.backends, b)
	}
	return p, nil
}

// Next picks the next healthy backend in round-robin order, or nil if none.
func (p *Pool) Next() *Backend {
	n := uint64(len(p.backends))
	start := p.next.Add(1) - 1
	for i := uint64(0); i < n; i++ {
		b := p.backends[(start+i)%n]
		if b.healthy.Load() {
			return b
		}
	}
	return nil
}

// ServeHTTP proxies the request to the next healthy backend.
func (p *Pool) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b := p.Next()
	if b == nil {
		http.Error(w, "no healthy upstream", http.StatusServiceUnavailable)
		return
	}
	b.requests.Add(1)
	b.inflight.Add(1)
	defer b.inflight.Add(-1)
	b.rp.ServeHTTP(w, r)
}

// Stats returns a snapshot of every backend.
func (p *Pool) Stats() []BackendStat {
	out := make([]BackendStat, len(p.backends))
	for i, b := range p.backends {
		out[i] = BackendStat{
			URL:      b.URL.String(),
			Healthy:  b.healthy.Load(),
			Requests: b.requests.Load(),
			Failures: b.failures.Load(),
			Inflight: b.inflight.Load(),
		}
	}
	return out
}

// StartHealthChecks probes every backend immediately and then every interval
// until ctx is cancelled. A backend is healthy if GET path returns 2xx/3xx.
func (p *Pool) StartHealthChecks(ctx context.Context, path string, interval, timeout time.Duration) {
	client := &http.Client{Timeout: timeout}
	checkAll := func() {
		var wg sync.WaitGroup
		for _, b := range p.backends {
			wg.Add(1)
			go func(b *Backend) {
				defer wg.Done()
				b.healthy.Store(probe(ctx, client, b.URL, path))
			}(b)
		}
		wg.Wait()
	}
	go func() {
		checkAll()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				checkAll()
			}
		}
	}()
}

func probe(ctx context.Context, client *http.Client, base *url.URL, path string) bool {
	target := base.ResolveReference(&url.URL{Path: path})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}
