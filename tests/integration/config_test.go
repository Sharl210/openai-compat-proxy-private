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
	t.Setenv("LOG_ENABLE", "false")
	t.Setenv("LOG_INCLUDE_BODIES", "true")
	t.Setenv("LOG_MAX_SIZE_MB", "12")
	t.Setenv("LOG_MAX_BACKUPS", "7")

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
	if cfg.LogEnable {
		t.Fatal("expected log enable flag to be false")
	}
	if !cfg.LogIncludeBodies {
		t.Fatal("expected log include bodies flag to be true")
	}
	if cfg.LogMaxSizeMB != 12 {
		t.Fatalf("unexpected log max size: %d", cfg.LogMaxSizeMB)
	}
	if cfg.LogMaxBackups != 7 {
		t.Fatalf("unexpected log max backups: %d", cfg.LogMaxBackups)
	}
	_ = os.Getenv("LISTEN_ADDR")
}

func TestDefaultConfigDisablesLoggingByDefault(t *testing.T) {
	cfg := config.Default()
	if cfg.LogEnable {
		t.Fatal("expected logging disabled by default")
	}
}
