package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/testutil"
)

func TestVersionHeadersStayPresentAndUpdateOnlyAfterSuccessfulRefresh(t *testing.T) {
	rootDir := t.TempDir()
	providersDir := filepath.Join(rootDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers dir: %v", err)
	}

	rootEnvPath := filepath.Join(rootDir, ".env")
	providerEnvPath := filepath.Join(providersDir, "openai.env")
	initialRootMTime := time.Date(2026, 3, 25, 11, 0, 0, 123000000, time.UTC)
	initialProviderMTime := time.Date(2026, 3, 25, 11, 1, 0, 456000000, time.UTC)

	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	writeConfigFileWithMTime(t, rootEnvPath, "PROVIDERS_DIR="+providersDir+"\nDEFAULT_PROVIDER=openai\nENABLE_LEGACY_V1_ROUTES=true\nTOTAL_TIMEOUT=1h\n", initialRootMTime)
	writeConfigFileWithMTime(t, providerEnvPath, "PROVIDER_ID=openai\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL="+upstream.URL+"\nUPSTREAM_API_KEY=test-key\nSUPPORTS_RESPONSES=true\n", initialProviderMTime)

	store, err := config.NewRuntimeStore(rootEnvPath)
	if err != nil {
		t.Fatalf("NewRuntimeStore returned error: %v", err)
	}
	server := NewServerWithStore(store, nil)

	first := performResponsesRequest(t, server)
	if got := first.Header().Get("X-Env-Version"); got != config.FormatVersionTime(initialRootMTime) {
		t.Fatalf("expected initial X-Env-Version %q, got %q", config.FormatVersionTime(initialRootMTime), got)
	}
	if got := first.Header().Get("X-Provider-Version"); got != config.FormatVersionTime(initialProviderMTime) {
		t.Fatalf("expected initial X-Provider-Version %q, got %q", config.FormatVersionTime(initialProviderMTime), got)
	}
	if got := first.Header().Get("X-Provider-Name"); got != "openai" {
		t.Fatalf("expected X-Provider-Name openai, got %q", got)
	}

	brokenProviderMTime := time.Date(2026, 3, 25, 11, 2, 0, 789000000, time.UTC)
	writeConfigFileWithMTime(t, providerEnvPath, "PROVIDER_ID=openai\nMODEL_MAP_JSON={broken\n", brokenProviderMTime)
	if err := store.Refresh(); err == nil {
		t.Fatalf("expected Refresh to fail for broken provider config")
	}

	second := performResponsesRequest(t, server)
	if got := second.Header().Get("X-Env-Version"); got != config.FormatVersionTime(initialRootMTime) {
		t.Fatalf("expected X-Env-Version to stay %q after failed refresh, got %q", config.FormatVersionTime(initialRootMTime), got)
	}
	if got := second.Header().Get("X-Provider-Version"); got != config.FormatVersionTime(initialProviderMTime) {
		t.Fatalf("expected X-Provider-Version to stay %q after failed refresh, got %q", config.FormatVersionTime(initialProviderMTime), got)
	}

	successRootMTime := time.Date(2026, 3, 25, 11, 3, 0, 111000000, time.UTC)
	successProviderMTime := time.Date(2026, 3, 25, 11, 4, 0, 222000000, time.UTC)
	writeConfigFileWithMTime(t, rootEnvPath, "PROVIDERS_DIR="+providersDir+"\nDEFAULT_PROVIDER=openai\nENABLE_LEGACY_V1_ROUTES=true\nTOTAL_TIMEOUT=2h\n", successRootMTime)
	writeConfigFileWithMTime(t, providerEnvPath, "PROVIDER_ID=openai\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL="+upstream.URL+"\nUPSTREAM_API_KEY=test-key\nSUPPORTS_RESPONSES=true\n", successProviderMTime)
	if err := store.Refresh(); err != nil {
		t.Fatalf("expected Refresh to succeed, got %v", err)
	}

	third := performResponsesRequest(t, server)
	if got := third.Header().Get("X-Env-Version"); got != config.FormatVersionTime(successRootMTime) {
		t.Fatalf("expected X-Env-Version to update to %q, got %q", config.FormatVersionTime(successRootMTime), got)
	}
	if got := third.Header().Get("X-Provider-Version"); got != config.FormatVersionTime(successProviderMTime) {
		t.Fatalf("expected X-Provider-Version to update to %q, got %q", config.FormatVersionTime(successProviderMTime), got)
	}
	if got := third.Header().Get("X-Provider-Name"); got != "openai" {
		t.Fatalf("expected X-Provider-Name openai after successful refresh, got %q", got)
	}
}

func TestVersionHeadersIgnoreStartupOnlyRootConfigChanges(t *testing.T) {
	rootDir := t.TempDir()
	providersDir := filepath.Join(rootDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers dir: %v", err)
	}

	rootEnvPath := filepath.Join(rootDir, ".env")
	providerEnvPath := filepath.Join(providersDir, "openai.env")
	initialRootMTime := time.Date(2026, 3, 25, 11, 10, 0, 123000000, time.UTC)
	startupOnlyMTime := time.Date(2026, 3, 25, 11, 11, 0, 456000000, time.UTC)
	hotChangeMTime := time.Date(2026, 3, 25, 11, 12, 0, 789000000, time.UTC)

	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	writeConfigFileWithMTime(t, rootEnvPath, "LISTEN_ADDR=:21021\nLOG_ENABLE=false\nPROVIDERS_DIR="+providersDir+"\nDEFAULT_PROVIDER=openai\nENABLE_LEGACY_V1_ROUTES=true\nTOTAL_TIMEOUT=1h\n", initialRootMTime)
	writeConfigFileWithMTime(t, providerEnvPath, "PROVIDER_ID=openai\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL="+upstream.URL+"\nUPSTREAM_API_KEY=test-key\nSUPPORTS_RESPONSES=true\n", time.Date(2026, 3, 25, 11, 10, 30, 0, time.UTC))

	store, err := config.NewRuntimeStore(rootEnvPath)
	if err != nil {
		t.Fatalf("NewRuntimeStore returned error: %v", err)
	}
	server := NewServerWithStore(store, nil)

	first := performResponsesRequest(t, server)
	if got := first.Header().Get("X-Env-Version"); got != config.FormatVersionTime(initialRootMTime) {
		t.Fatalf("expected initial X-Env-Version %q, got %q", config.FormatVersionTime(initialRootMTime), got)
	}

	writeConfigFileWithMTime(t, rootEnvPath, "LISTEN_ADDR=:29999\nLOG_ENABLE=true\nPROVIDERS_DIR="+providersDir+"\nDEFAULT_PROVIDER=openai\nENABLE_LEGACY_V1_ROUTES=true\nTOTAL_TIMEOUT=1h\n", startupOnlyMTime)
	if err := store.Refresh(); err != nil {
		t.Fatalf("expected startup-only change refresh to succeed, got %v", err)
	}

	second := performResponsesRequest(t, server)
	if got := second.Header().Get("X-Env-Version"); got != config.FormatVersionTime(initialRootMTime) {
		t.Fatalf("expected startup-only change to keep X-Env-Version %q, got %q", config.FormatVersionTime(initialRootMTime), got)
	}

	writeConfigFileWithMTime(t, rootEnvPath, "LISTEN_ADDR=:29999\nLOG_ENABLE=true\nPROVIDERS_DIR="+providersDir+"\nDEFAULT_PROVIDER=openai\nENABLE_LEGACY_V1_ROUTES=true\nTOTAL_TIMEOUT=2h\n", hotChangeMTime)
	if err := store.Refresh(); err != nil {
		t.Fatalf("expected hot change refresh to succeed, got %v", err)
	}

	third := performResponsesRequest(t, server)
	if got := third.Header().Get("X-Env-Version"); got != config.FormatVersionTime(hotChangeMTime) {
		t.Fatalf("expected hot change to update X-Env-Version to %q, got %q", config.FormatVersionTime(hotChangeMTime), got)
	}
}

func TestSystemPromptAttachHeaderPresentOnlyWhenProviderPromptActuallyInjected(t *testing.T) {
	rootDir := t.TempDir()
	providersDir := filepath.Join(rootDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers dir: %v", err)
	}

	rootEnvPath := filepath.Join(rootDir, ".env")
	providerEnvPath := filepath.Join(providersDir, "openai.env")
	promptPath := filepath.Join(providersDir, "prompt.md")
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	writeConfigFileWithMTime(t, rootEnvPath, "PROVIDERS_DIR="+providersDir+"\nDEFAULT_PROVIDER=openai\nENABLE_LEGACY_V1_ROUTES=true\nTOTAL_TIMEOUT=1h\n", time.Date(2026, 3, 26, 9, 0, 0, 0, time.UTC))
	writeConfigFileWithMTime(t, providerEnvPath, "PROVIDER_ID=openai\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL="+upstream.URL+"\nUPSTREAM_API_KEY=test-key\nSUPPORTS_RESPONSES=true\nSYSTEM_PROMPT_FILES=prompt.md, prompts/extra.md\n", time.Date(2026, 3, 26, 9, 1, 0, 0, time.UTC))
	writeConfigFileWithMTime(t, promptPath, "provider prompt\n", time.Date(2026, 3, 26, 9, 1, 30, 0, time.UTC))

	store, err := config.NewRuntimeStore(rootEnvPath)
	if err != nil {
		t.Fatalf("NewRuntimeStore returned error: %v", err)
	}
	server := NewServerWithStore(store, nil)

	first := performResponsesRequest(t, server)
	if got := first.Header().Get("X-SYSTEM-PROMPT-ATTACH"); got != "prepend:prompt.md, prompts/extra.md" {
		t.Fatalf("expected attach header to expose configured path string, got %q", got)
	}

	writeConfigFileWithMTime(t, promptPath, "\n\n", time.Date(2026, 3, 26, 9, 2, 0, 0, time.UTC))
	if err := store.Refresh(); err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}

	second := performResponsesRequest(t, server)
	if got := second.Header().Get("X-SYSTEM-PROMPT-ATTACH"); got != "" {
		t.Fatalf("expected blank injected prompt to omit attach header, got %q", got)
	}
}

func TestProviderVersionHeaderUpdatesAfterPromptOnlyRefresh(t *testing.T) {
	rootDir := t.TempDir()
	providersDir := filepath.Join(rootDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers dir: %v", err)
	}

	rootEnvPath := filepath.Join(rootDir, ".env")
	providerEnvPath := filepath.Join(providersDir, "openai.env")
	promptPath := filepath.Join(providersDir, "prompt.md")
	providerMTime := time.Date(2026, 3, 26, 9, 30, 0, 0, time.UTC)
	initialPromptMTime := time.Date(2026, 3, 26, 9, 31, 0, 0, time.UTC)
	updatedPromptMTime := time.Date(2026, 3, 26, 9, 32, 0, 0, time.UTC)

	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	writeConfigFileWithMTime(t, rootEnvPath, "PROVIDERS_DIR="+providersDir+"\nDEFAULT_PROVIDER=openai\nENABLE_LEGACY_V1_ROUTES=true\nTOTAL_TIMEOUT=1h\n", time.Date(2026, 3, 26, 9, 29, 0, 0, time.UTC))
	writeConfigFileWithMTime(t, providerEnvPath, "PROVIDER_ID=openai\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL="+upstream.URL+"\nUPSTREAM_API_KEY=test-key\nSUPPORTS_RESPONSES=true\nSYSTEM_PROMPT_FILES=prompt.md\n", providerMTime)
	writeConfigFileWithMTime(t, promptPath, "before prompt\n", initialPromptMTime)

	store, err := config.NewRuntimeStore(rootEnvPath)
	if err != nil {
		t.Fatalf("NewRuntimeStore returned error: %v", err)
	}
	server := NewServerWithStore(store, nil)

	first := performResponsesRequest(t, server)
	if got := first.Header().Get("X-Provider-Version"); got != config.FormatVersionTime(initialPromptMTime) {
		t.Fatalf("expected initial provider version %q, got %q", config.FormatVersionTime(initialPromptMTime), got)
	}

	writeConfigFileWithMTime(t, promptPath, "after prompt\n", updatedPromptMTime)
	if err := store.Refresh(); err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}

	second := performResponsesRequest(t, server)
	if got := second.Header().Get("X-Provider-Version"); got != config.FormatVersionTime(updatedPromptMTime) {
		t.Fatalf("expected prompt-only refresh to update provider version to %q, got %q", config.FormatVersionTime(updatedPromptMTime), got)
	}
}

func TestProviderScopedResponsesRequestExposesVersionAndStatusHeadersTogether(t *testing.T) {
	rootDir := t.TempDir()
	providersDir := filepath.Join(rootDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers dir: %v", err)
	}

	rootEnvPath := filepath.Join(rootDir, ".env")
	providerEnvPath := filepath.Join(providersDir, "openai.env")
	rootMTime := time.Date(2026, 3, 26, 10, 0, 0, 123000000, time.UTC)
	providerMTime := time.Date(2026, 3, 26, 10, 1, 0, 456000000, time.UTC)

	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	writeConfigFileWithMTime(t, rootEnvPath, "PROXY_API_KEY=proxy-secret\nPROVIDERS_DIR="+providersDir+"\nDEFAULT_PROVIDER=openai\nENABLE_LEGACY_V1_ROUTES=true\nTOTAL_TIMEOUT=1h\n", rootMTime)
	writeConfigFileWithMTime(t, providerEnvPath, "PROVIDER_ID=openai\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL="+upstream.URL+"\nUPSTREAM_API_KEY=test-key\nSUPPORTS_RESPONSES=true\n", providerMTime)

	store, err := config.NewRuntimeStore(rootEnvPath)
	if err != nil {
		t.Fatalf("NewRuntimeStore returned error: %v", err)
	}
	server := NewServerWithStore(store, nil)

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer proxy-secret")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Provider-Name"); got != "openai" {
		t.Fatalf("expected X-Provider-Name openai, got %q", got)
	}
	if got := rec.Header().Get("X-Env-Version"); got != config.FormatVersionTime(rootMTime) {
		t.Fatalf("expected X-Env-Version %q, got %q", config.FormatVersionTime(rootMTime), got)
	}
	if got := rec.Header().Get("X-Provider-Version"); got != config.FormatVersionTime(providerMTime) {
		t.Fatalf("expected X-Provider-Version %q, got %q", config.FormatVersionTime(providerMTime), got)
	}
	if got := rec.Header().Get("X-STATUS-CHECK-URL"); got != "" {
		t.Fatalf("expected no X-STATUS-CHECK-URL header, got %q", got)
	}
	if got := rec.Header().Get("X-RESPONSE-PROCESS-HEALTH-FLAG"); got != "" {
		t.Fatalf("expected no X-RESPONSE-PROCESS-HEALTH-FLAG header, got %q", got)
	}
}

func performResponsesRequest(t *testing.T, server http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	return rec
}

func writeConfigFileWithMTime(t *testing.T, path string, content string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}
