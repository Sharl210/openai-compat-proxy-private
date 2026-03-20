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

func TestLoadFromEnvLoadsProviderGlobals(t *testing.T) {
	t.Setenv("PROVIDERS_DIR", "/tmp/providers")
	t.Setenv("DEFAULT_PROVIDER", "openai")
	t.Setenv("ENABLE_LEGACY_V1_ROUTES", "true")

	cfg := config.LoadFromEnv()

	if cfg.ProvidersDir != "/tmp/providers" {
		t.Fatalf("unexpected providers dir: %q", cfg.ProvidersDir)
	}
	if cfg.DefaultProvider != "openai" {
		t.Fatalf("unexpected default provider: %q", cfg.DefaultProvider)
	}
	if !cfg.EnableLegacyV1Routes {
		t.Fatal("expected legacy v1 routes to be enabled")
	}
}

func TestValidateRequiresDefaultProviderWhenLegacyRoutesEnabled(t *testing.T) {
	cfg := config.Default()
	cfg.EnableLegacyV1Routes = true

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error when legacy routes are enabled without default provider")
	}
}

func TestLoadProvidersFromDir(t *testing.T) {
	dir := t.TempDir()
	writeProviderFile(t, dir, "openai.env", "PROVIDER_ID=openai\nPROVIDER_ENABLED=true\nPROVIDER_IS_DEFAULT=true\nUPSTREAM_BASE_URL=http://127.0.0.1:18081/v1\nUPSTREAM_API_KEY=key-openai\nSUPPORTS_CHAT=true\nSUPPORTS_RESPONSES=true\nSUPPORTS_MODELS=true\n")
	writeProviderFile(t, dir, "anthropic.env", "PROVIDER_ID=anthropic\nPROVIDER_ENABLED=false\nUPSTREAM_BASE_URL=http://127.0.0.1:18082/v1\nUPSTREAM_API_KEY=key-anthropic\nSUPPORTS_CHAT=true\nSUPPORTS_RESPONSES=true\nSUPPORTS_MODELS=true\n")
	writeProviderFile(t, dir, "ignored.env.example", "PROVIDER_ID=ignored\n")

	providers, err := config.LoadProvidersFromDir(dir)
	if err != nil {
		t.Fatalf("expected providers to load, got error: %v", err)
	}

	if len(providers) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(providers))
	}
	if providers[0].ID != "anthropic" && providers[1].ID != "anthropic" {
		t.Fatalf("expected anthropic provider to be loaded, got %#v", providers)
	}
	for _, provider := range providers {
		if provider.ID == "openai" && !provider.IsDefault {
			t.Fatal("expected openai to be default provider")
		}
	}
}

func TestLoadProvidersRejectsDuplicateDefaultProviders(t *testing.T) {
	dir := t.TempDir()
	writeProviderFile(t, dir, "a.env", "PROVIDER_ID=a\nPROVIDER_ENABLED=true\nPROVIDER_IS_DEFAULT=true\nUPSTREAM_BASE_URL=http://127.0.0.1:18081/v1\n")
	writeProviderFile(t, dir, "b.env", "PROVIDER_ID=b\nPROVIDER_ENABLED=true\nPROVIDER_IS_DEFAULT=true\nUPSTREAM_BASE_URL=http://127.0.0.1:18082/v1\n")

	_, err := config.LoadProvidersFromDir(dir)
	if err == nil {
		t.Fatal("expected duplicate default providers to fail")
	}
}

func writeProviderFile(t *testing.T, dir, name, body string) {
	t.Helper()
	path := dir + string(os.PathSeparator) + name
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write provider file: %v", err)
	}
}
