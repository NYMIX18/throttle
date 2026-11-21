// Command demo-backend is a tiny upstream server for trying Throttle out.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"time"
)

func main() {
	port := flag.Int("port", 9001, "port to listen on")
	name := flag.String("name", "backend", "name returned in responses")
	latency := flag.Duration("latency", 40*time.Millisecond, "max random latency added to each request")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if *latency > 0 {
			time.Sleep(time.Duration(rand.Int63n(int64(*latency))))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"backend":   *name,
			"path":      r.URL.Path,
			"client":    r.Header.Get("X-Forwarded-For"),
			"requestId": r.Header.Get("X-Request-Id"),
		})
	})

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("demo backend %q listening on %s", *name, addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
