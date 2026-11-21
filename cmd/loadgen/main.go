// Command loadgen sends traffic from many simulated clients and reports how
// many requests were allowed or rate limited.
//
// Client popularity follows a Zipf distribution: a few "heavy" clients send
// most of the traffic and get limited, while the long tail stays under its
// budget. That makes the limiter's per-key behaviour easy to see.
package main

import (
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

type result struct {
	ok, limited, errs int
	lat               []time.Duration
}

func main() {
	target := flag.String("url", "http://localhost:8080/", "URL to request")
	conc := flag.Int("c", 20, "concurrent workers")
	dur := flag.Duration("d", 15*time.Second, "test duration")
	rps := flag.Int("rps", 200, "approximate total requests per second")
	clients := flag.Int("clients", 25, "number of simulated clients (sent as X-API-Key)")
	flag.Parse()

	if *conc < 1 || *rps < 1 || *clients < 1 {
		fmt.Fprintln(os.Stderr, "c, rps and clients must all be >= 1")
		os.Exit(2)
	}

	httpc := &http.Client{Timeout: 10 * time.Second}
	perWorker := time.Duration(float64(time.Second) * float64(*conc) / float64(*rps))
	deadline := time.Now().Add(*dur)

	fmt.Printf("sending ~%d req/s from %d simulated clients for %s to %s\n", *rps, *clients, *dur, *target)

	results := make([]result, *conc)
	var wg sync.WaitGroup
	for w := 0; w < *conc; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(w)))
			var zipf *rand.Zipf
			if *clients > 1 {
				zipf = rand.NewZipf(rng, 1.2, 1, uint64(*clients-1))
			}
			res := &results[w]
			for time.Now().Before(deadline) {
				id := 0
				if zipf != nil {
					id = int(zipf.Uint64())
				}
				req, err := http.NewRequest(http.MethodGet, *target, nil)
				if err != nil {
					res.errs++
					return
				}
				req.Header.Set("X-API-Key", fmt.Sprintf("client-%02d", id+1))

				start := time.Now()
				resp, err := httpc.Do(req)
				if err != nil {
					res.errs++
				} else {
					resp.Body.Close()
					res.lat = append(res.lat, time.Since(start))
					if resp.StatusCode == http.StatusTooManyRequests {
						res.limited++
					} else if resp.StatusCode < 400 {
						res.ok++
					} else {
						res.errs++
					}
				}
				time.Sleep(perWorker)
			}
		}(w)
	}
	wg.Wait()

	var total result
	for _, r := range results {
		total.ok += r.ok
		total.limited += r.limited
		total.errs += r.errs
		total.lat = append(total.lat, r.lat...)
	}
	sort.Slice(total.lat, func(i, j int) bool { return total.lat[i] < total.lat[j] })
	pct := func(p float64) time.Duration {
		if len(total.lat) == 0 {
			return 0
		}
		return total.lat[int(float64(len(total.lat)-1)*p)]
	}
	sent := total.ok + total.limited + total.errs
	fmt.Printf("\nsent %d   allowed %d   rate limited (429) %d   errors %d\n", sent, total.ok, total.limited, total.errs)
	fmt.Printf("latency  p50 %s   p95 %s   p99 %s\n", pct(0.50).Round(time.Microsecond), pct(0.95).Round(time.Microsecond), pct(0.99).Round(time.Microsecond))
}
