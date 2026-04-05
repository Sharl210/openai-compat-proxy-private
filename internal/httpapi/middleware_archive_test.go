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

func TestWithRequestIDPrunesOldArchiveDirectoriesUsingLogMaxRequests(t *testing.T) {
	archiveDir := t.TempDir()
	store := config.NewStaticRuntimeStore(config.Config{
		DebugArchiveRootDir: archiveDir,
		LogMaxRequests:      2,
	})
	h := withRequestID(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	var requestIDs []string
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"gpt-5"}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		requestID := rec.Header().Get("X-Request-Id")
		if requestID == "" {
			t.Fatalf("request %d missing request id", i)
		}
		requestIDs = append(requestIDs, requestID)
	}

	if _, err := os.Stat(filepath.Join(archiveDir, requestIDs[0])); !os.IsNotExist(err) {
		t.Fatalf("expected oldest archive dir %s to be pruned, got err=%v", requestIDs[0], err)
	}
	if _, err := os.Stat(filepath.Join(archiveDir, requestIDs[1])); err != nil {
		t.Fatalf("expected second archive dir %s to remain: %v", requestIDs[1], err)
	}
	if _, err := os.Stat(filepath.Join(archiveDir, requestIDs[2])); err != nil {
		t.Fatalf("expected newest archive dir %s to remain: %v", requestIDs[2], err)
	}
}

func TestWithRequestIDResolvesRelativeArchiveDirAgainstRootEnv(t *testing.T) {
	root := t.TempDir()
	providersDir := filepath.Join(root, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers: %v", err)
	}
	providerEnv := []byte("PROVIDER_ID=openai\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL=https://example.com/v1\nUPSTREAM_API_KEY=test-key\n")
	if err := os.WriteFile(filepath.Join(providersDir, "openai.env"), providerEnv, 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}
	rootEnvPath := filepath.Join(root, ".env")
	rootEnv := []byte("PROXY_API_KEY=test\nPROVIDERS_DIR=" + providersDir + "\nDEFAULT_PROVIDER=openai\nOPENAI_COMPAT_DEBUG_ARCHIVE_DIR=OPENAI_COMPAT_DEBUG_ARCHIVE_DIR\n")
	if err := os.WriteFile(rootEnvPath, rootEnv, 0o644); err != nil {
		t.Fatalf("write root env: %v", err)
	}
	store, err := config.NewRuntimeStore(rootEnvPath)
	if err != nil {
		t.Fatalf("NewRuntimeStore: %v", err)
	}
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
	archivePath := filepath.Join(root, debugarchive.EnvRootDir, requestID, "request.ndjson")
	if _, err := os.Stat(archivePath); err != nil {
		t.Fatalf("expected relative archive dir resolved under root env dir at %s: %v", archivePath, err)
	}
}

func TestWithRequestIDSkipsDefaultArchiveDirWithoutRootEnvPath(t *testing.T) {
	store := config.NewStaticRuntimeStore(config.Config{DebugArchiveRootDir: debugarchive.EnvRootDir})
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
	if _, err := os.Stat(filepath.Join(debugarchive.EnvRootDir, requestID, "request.ndjson")); err == nil {
		t.Fatalf("expected default archive placeholder dir to stay disabled when root env path is unavailable")
	}
}
