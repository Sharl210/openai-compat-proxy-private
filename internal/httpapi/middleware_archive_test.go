package httpapi

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/debugarchive"
	"openai-compat-proxy/internal/logging"
)

func TestWithRequestID_WritesArchiveRequestWhenEnabled(t *testing.T) {
	archiveDir := t.TempDir()
	store := config.NewStaticRuntimeStore(config.Config{LogEnable: true, DebugArchiveRootDir: archiveDir})
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
		LogEnable:            true,
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

func TestWithRequestIDSkipsArchiveRequestWhenLogEnableIsFalse(t *testing.T) {
	archiveDir := t.TempDir()
	store := config.NewStaticRuntimeStore(config.Config{
		LogEnable:               false,
		DebugArchiveRootDir:     archiveDir,
		DebugArchiveMaxRequests: 2,
	})
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
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected no archive request file when LOG_ENABLE=false, got err=%v", err)
	}
}

func TestWithRequestIDPrunesOldArchiveDirectoriesUsingArchiveMaxRequests(t *testing.T) {
	archiveDir := t.TempDir()
	store := config.NewStaticRuntimeStore(config.Config{
		LogEnable:               true,
		DebugArchiveRootDir:     archiveDir,
		LogMaxRequests:          99,
		DebugArchiveMaxRequests: 2,
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
	store := config.NewStaticRuntimeStore(config.Config{LogEnable: true, DebugArchiveRootDir: debugarchive.EnvRootDir})
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

func TestWithRequestIDSkipsWebAccessLogging(t *testing.T) {
	logDir := initMiddlewareTestLogger(t)
	h := withRequestID(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/_admin/assets/app.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Header().Get("X-Request-Id") == "" {
		t.Fatal("expected request id header")
	}
	if files := middlewareLogFiles(t, logDir); len(files) != 0 {
		t.Fatalf("expected no log files for web access requests, got %v", files)
	}
}

func TestWithRequestIDLogsAPIAccessRequests(t *testing.T) {
	logDir := initMiddlewareTestLogger(t)
	h := withRequestID(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"gpt-5"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	requestID := rec.Header().Get("X-Request-Id")
	if requestID == "" {
		t.Fatal("expected request id header")
	}
	files := middlewareLogFiles(t, logDir)
	if len(files) != 1 {
		t.Fatalf("expected exactly one api log file, got %v", files)
	}
	content, err := os.ReadFile(filepath.Join(logDir, files[0]))
	if err != nil {
		t.Fatalf("read api log file: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, `"event":"clientToProxyRequest"`) {
		t.Fatalf("expected clientToProxyRequest event in log, got %s", text)
	}
	if !strings.Contains(text, `"event":"proxyToClientResponse"`) {
		t.Fatalf("expected proxyToClientResponse event in log, got %s", text)
	}
	if !strings.Contains(text, `"path":"/v1/responses"`) {
		t.Fatalf("expected /v1/responses path in log, got %s", text)
	}
	if !strings.Contains(text, `"request_id":"`+requestID+`"`) {
		t.Fatalf("expected request id %s in log, got %s", requestID, text)
	}
}

func TestWithRequestIDLogsAliasResponsesPath(t *testing.T) {
	logDir := initMiddlewareTestLogger(t)
	h := withRequestID(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	req := httptest.NewRequest(http.MethodPost, "/responses", bytes.NewBufferString(`{"model":"gpt-5"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	requestID := rec.Header().Get("X-Request-Id")
	if requestID == "" {
		t.Fatal("expected request id header")
	}
	files := middlewareLogFiles(t, logDir)
	if len(files) != 1 {
		t.Fatalf("expected exactly one api log file for alias path, got %v", files)
	}
	content, err := os.ReadFile(filepath.Join(logDir, files[0]))
	if err != nil {
		t.Fatalf("read api alias log file: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, `"path":"/responses"`) {
		t.Fatalf("expected /responses path in log, got %s", text)
	}
	if !strings.Contains(text, `"request_id":"`+requestID+`"`) {
		t.Fatalf("expected request id %s in alias log, got %s", requestID, text)
	}
}

func initMiddlewareTestLogger(t *testing.T) string {
	t.Helper()
	logDir := t.TempDir()
	closeFn, err := logging.Init(config.Config{LogEnable: true, LogFilePath: logDir, LogMaxRequests: 50, LogMaxBodySizeMB: 5}, io.Discard)
	if err != nil {
		t.Fatalf("init logger: %v", err)
	}
	t.Cleanup(func() {
		_ = closeFn()
		_, _ = logging.Init(config.Config{LogEnable: false}, io.Discard)
	})
	return logDir
}

func middlewareLogFiles(t *testing.T, logDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("read log dir: %v", err)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".txt") {
			continue
		}
		files = append(files, entry.Name())
	}
	return files
}
