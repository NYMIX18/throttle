// Package config loads and validates Throttle's JSON configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Duration is a time.Duration that is written as a string in JSON ("1s", "250ms").
type Duration time.Duration

// Std converts to a standard time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// UnmarshalJSON implements json.Unmarshaler.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string such as \"1s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// RateLimit configures the limiter.
type RateLimit struct {
	Algorithm      string   `json:"algorithm"`
	Requests       int      `json:"requests"`
	Per            Duration `json:"per"`
	Burst          int      `json:"burst"`
	KeyBy          string   `json:"key_by"`
	TrustForwarded bool     `json:"trust_forwarded"`
}

// HealthCheck configures active upstream probing.
type HealthCheck struct {
	Path     string   `json:"path"`
	Interval Duration `json:"interval"`
	Timeout  Duration `json:"timeout"`
}

// Config is the full application configuration.
type Config struct {
	Listen          string      `json:"listen"`
	AdminListen     string      `json:"admin_listen"`
	Upstreams       []string    `json:"upstreams"`
	RateLimit       RateLimit   `json:"rate_limit"`
	HealthCheck     HealthCheck `json:"health_check"`
	ShutdownTimeout Duration    `json:"shutdown_timeout"`
	DemoEndpoints   bool        `json:"demo_endpoints"`
}

// Default returns a Config with sensible defaults. Upstreams has no default.
func Default() Config {
	return Config{
		Listen:      ":8080",
		AdminListen: "127.0.0.1:9090",
		RateLimit: RateLimit{
			Algorithm: "token_bucket",
			Requests:  10,
			Per:       Duration(time.Second),
			Burst:     20,
			KeyBy:     "ip",
		},
		HealthCheck: HealthCheck{
			Path:     "/healthz",
			Interval: Duration(5 * time.Second),
			Timeout:  Duration(2 * time.Second),
		},
		ShutdownTimeout: Duration(10 * time.Second),
		DemoEndpoints:   true,
	}
}

// Load reads, defaults, and validates a config file. Unknown fields are an
// error so that typos in the file do not silently become defaults.
func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()

	cfg := Default()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

// Validate checks that the configuration is usable.
func (c Config) Validate() error {
	var errs []error
	if len(c.Upstreams) == 0 {
		errs = append(errs, errors.New("upstreams: at least one is required"))
	}
	for _, raw := range c.Upstreams {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("upstreams: %q is not a valid http(s) URL", raw))
		}
	}
	rl := c.RateLimit
	if rl.Requests <= 0 {
		errs = append(errs, errors.New("rate_limit.requests must be > 0"))
	}
	if rl.Per.Std() <= 0 {
		errs = append(errs, errors.New("rate_limit.per must be > 0"))
	}
	if rl.Burst < 0 {
		errs = append(errs, errors.New("rate_limit.burst must be >= 0"))
	}
	switch rl.Algorithm {
	case "token_bucket", "sliding_window":
	default:
		errs = append(errs, fmt.Errorf("rate_limit.algorithm: unknown %q", rl.Algorithm))
	}
	if rl.KeyBy != "ip" && !(strings.HasPrefix(rl.KeyBy, "header:") && len(rl.KeyBy) > len("header:")) {
		errs = append(errs, fmt.Errorf("rate_limit.key_by must be \"ip\" or \"header:<Name>\", got %q", rl.KeyBy))
	}
	if c.HealthCheck.Interval.Std() <= 0 || c.HealthCheck.Timeout.Std() <= 0 {
		errs = append(errs, errors.New("health_check interval and timeout must be > 0"))
	}
	return errors.Join(errs...)
}
