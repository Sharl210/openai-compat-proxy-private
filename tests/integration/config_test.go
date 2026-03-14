package integration_test

import (
	"os"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestLoadFromEnvOverridesDefaults(t *testing.T) {
	t.Setenv("UPSTREAM_BASE_URL", "http://127.0.0.1:18081")
	t.Setenv("UPSTREAM_API_KEY", "server-key")
	t.Setenv("LISTEN_ADDR", ":9090")

	cfg := config.LoadFromEnv()

	if cfg.UpstreamBaseURL != "http://127.0.0.1:18081" {
		t.Fatalf("unexpected upstream base url: %q", cfg.UpstreamBaseURL)
	}
	if cfg.UpstreamAPIKey != "server-key" {
		t.Fatalf("unexpected upstream key: %q", cfg.UpstreamAPIKey)
	}
	if cfg.ListenAddr != ":9090" {
		t.Fatalf("unexpected listen addr: %q", cfg.ListenAddr)
	}
	_ = os.Getenv("LISTEN_ADDR")
}
