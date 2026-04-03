package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/debugarchive"
)

func TestWithRequestID_WritesArchiveRequestWhenEnabled(t *testing.T) {
	archiveDir := t.TempDir()
	store := config.NewStaticRuntimeStore(config.Config{DebugArchiveRootDir: archiveDir})
	h := withRequestID(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"gpt-5"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	requestID := rec.Header().Get("X-Request-Id")
	if requestID == "" {
		t.Fatal("expected request id header")
	}
	path := filepath.Join(archiveDir, requestID, "request.ndjson")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected archive request file at %s: %v", path, err)
	}
}

func TestWithRequestIDPrefersRuntimeConfigArchiveDirOverEnvironment(t *testing.T) {
	envDir := t.TempDir()
	configDir := t.TempDir()
	t.Setenv(debugarchive.EnvRootDir, envDir)

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		DebugArchiveRootDir:  configDir,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			SupportsResponses: true,
			UpstreamBaseURL:   "https://example.test",
			UpstreamAPIKey:    "test-key",
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"gpt-5","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	requestID := rec.Header().Get("X-Request-Id")
	if requestID == "" {
		t.Fatal("expected request id header")
	}
	configPath := filepath.Join(configDir, requestID, "request.ndjson")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("expected archive request file at config dir %s: %v", configPath, err)
	}
	envPath := filepath.Join(envDir, requestID, "request.ndjson")
	if _, err := os.Stat(envPath); err == nil {
		t.Fatalf("expected env archive dir to stay unused, but found %s", envPath)
	}
}
