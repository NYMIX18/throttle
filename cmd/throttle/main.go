// Command throttle is a rate-limiting reverse proxy with a live dashboard.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/your-username/throttle/internal/admin"
	"github.com/your-username/throttle/internal/config"
	"github.com/your-username/throttle/internal/gateway"
	"github.com/your-username/throttle/internal/limiter"
	"github.com/your-username/throttle/internal/metrics"
	"github.com/your-username/throttle/internal/proxy"
)

func main() {
	cfgPath := flag.String("config", "configs/throttle.json", "path to the JSON config file")
	verbose := flag.Bool("v", false, "log every request (debug level)")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if err := run(*cfgPath, logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(cfgPath string, logger *slog.Logger) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	lim, err := limiter.New(limiter.Config{
		Algorithm: cfg.RateLimit.Algorithm,
		Requests:  cfg.RateLimit.Requests,
		Per:       cfg.RateLimit.Per.Std(),
		Burst:     cfg.RateLimit.Burst,
	})
	if err != nil {
		return err
	}
	defer lim.Close()

	pool, err := proxy.NewPool(cfg.Upstreams, proxy.NewTransport())
	if err != nil {
		return err
	}

	// ctx is cancelled on Ctrl+C / SIGTERM, which stops health checks and
	// triggers graceful shutdown below.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool.StartHealthChecks(ctx, cfg.HealthCheck.Path, cfg.HealthCheck.Interval.Std(), cfg.HealthCheck.Timeout.Std())

	reg := metrics.New()
	gw := &gateway.Gateway{
		Limiter: lim,
		Next:    pool,
		KeyFunc: gateway.NewKeyFunc(cfg.RateLimit.KeyBy, cfg.RateLimit.TrustForwarded),
		Metrics: reg,
		Logger:  logger,
	}

	adm := &admin.Server{
		Metrics: reg,
		Pool:    pool,
		Limiter: lim,
		Gateway: gw,
		Info: admin.Info{
			Algorithm: cfg.RateLimit.Algorithm,
			Requests:  cfg.RateLimit.Requests,
			Per:       cfg.RateLimit.Per.Std().String(),
			Burst:     cfg.RateLimit.Burst,
			KeyBy:     cfg.RateLimit.KeyBy,
			Listen:    cfg.Listen,
		},
		DemoEnabled: cfg.DemoEndpoints,
	}

	// No WriteTimeout on the proxy server: it would cut off slow or streaming
	// upstream responses. Header and idle timeouts guard against slow clients.
	proxySrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           gw,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	adminSrv := &http.Server{
		Addr:              cfg.AdminListen,
		Handler:           adm.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errc := make(chan error, 2)
	go func() { errc <- proxySrv.ListenAndServe() }()
	go func() { errc <- adminSrv.ListenAndServe() }()

	fmt.Printf("\n  Throttle is running\n")
	fmt.Printf("  proxy      http://localhost%s   (%d upstreams)\n", portOf(cfg.Listen), len(cfg.Upstreams))
	fmt.Printf("  dashboard  http://%s\n", cfg.AdminListen)
	fmt.Printf("  limit      %s, %d per %s (burst %d), keyed by %s\n\n",
		cfg.RateLimit.Algorithm, cfg.RateLimit.Requests, cfg.RateLimit.Per.Std(), cfg.RateLimit.Burst, cfg.RateLimit.KeyBy)

	select {
	case err := <-errc:
		return err // a listener failed (e.g. port already in use)
	case <-ctx.Done():
	}

	logger.Info("shutting down, draining in-flight requests")
	sctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout.Std())
	defer cancel()
	return errors.Join(proxySrv.Shutdown(sctx), adminSrv.Shutdown(sctx))
}

// portOf turns ":8080" or "0.0.0.0:8080" into ":8080" for display.
func portOf(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[i:]
		}
	}
	return addr
}
