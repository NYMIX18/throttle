package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load(write(t, `{"upstreams": ["http://localhost:9001"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":8080" || cfg.RateLimit.Requests != 10 || cfg.RateLimit.Per.Std() != time.Second {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(write(t, `{
		"upstreams": ["http://a:1", "http://b:2"],
		"rate_limit": {"algorithm": "sliding_window", "requests": 5, "per": "10s", "key_by": "header:X-API-Key"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Upstreams) != 2 || cfg.RateLimit.Algorithm != "sliding_window" || cfg.RateLimit.Per.Std() != 10*time.Second {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"unknown field": `{"upstreams": ["http://a:1"], "listn": ":1"}`,
		"no upstreams":  `{}`,
		"bad upstream":  `{"upstreams": ["not a url"]}`,
		"bad duration":  `{"upstreams": ["http://a:1"], "rate_limit": {"per": 5}}`,
		"bad algorithm": `{"upstreams": ["http://a:1"], "rate_limit": {"algorithm": "x"}}`,
		"bad key_by":    `{"upstreams": ["http://a:1"], "rate_limit": {"key_by": "cookie"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(write(t, body)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestValidateReportsAllProblems(t *testing.T) {
	c := Default()
	c.RateLimit.Requests = 0
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "upstreams") || !strings.Contains(err.Error(), "requests") {
		t.Fatalf("want both problems reported, got %v", err)
	}
}
