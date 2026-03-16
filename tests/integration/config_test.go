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
	t.Setenv("LOG_FILE_PATH", "/tmp/proxy.jsonl")
	t.Setenv("LOG_INCLUDE_BODIES", "true")

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
	if cfg.LogFilePath != "/tmp/proxy.jsonl" {
		t.Fatalf("unexpected log file path: %q", cfg.LogFilePath)
	}
	if !cfg.LogIncludeBodies {
		t.Fatal("expected log include bodies flag to be true")
	}
	_ = os.Getenv("LISTEN_ADDR")
}
