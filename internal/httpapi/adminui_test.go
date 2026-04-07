package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"openai-compat-proxy/internal/cacheinfo"
	"openai-compat-proxy/internal/config"
)

func TestAdminUIRootServesHTMLShell(t *testing.T) {
	server := newAdminUITestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for GET /, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Fatalf("expected HTML content type, got %q", got)
	}
	if !strings.Contains(rec.Body.String(), "openai-compat-proxy") {
		t.Fatalf("expected admin ui shell body, got %s", rec.Body.String())
	}
}

func TestAdminUIAssetsDisableCaching(t *testing.T) {
	server := newAdminUITestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/_admin/assets/app.css", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for asset, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("expected no-store for admin asset, got %q", got)
	}
}

func TestAdminUIBootstrapRequiresSession(t *testing.T) {
	server := newAdminUITestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/_admin/api/bootstrap", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated bootstrap, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminUILoginSetsSessionAndBootstrapAuthenticates(t *testing.T) {
	server := newAdminUITestServer(t)
	loginRec := adminJSONRequest(t, server, http.MethodPost, "/_admin/api/login", map[string]any{
		"password": "root-secret",
		"remember": true,
	}, nil, "")

	if loginRec.Code != http.StatusOK {
		t.Fatalf("expected login 200, got %d body=%s", loginRec.Code, loginRec.Body.String())
	}
	cookie := loginRec.Result().Cookies()[0]
	csrf := decodeAdminJSON(t, loginRec.Body.Bytes())["csrf_token"].(string)

	bootstrapReq := httptest.NewRequest(http.MethodGet, "/_admin/api/bootstrap", nil)
	bootstrapReq.AddCookie(cookie)
	bootstrapRec := httptest.NewRecorder()
	server.ServeHTTP(bootstrapRec, bootstrapReq)

	if bootstrapRec.Code != http.StatusOK {
		t.Fatalf("expected bootstrap 200, got %d body=%s", bootstrapRec.Code, bootstrapRec.Body.String())
	}
	data := decodeAdminJSON(t, bootstrapRec.Body.Bytes())
	if data["authenticated"] != true {
		t.Fatalf("expected authenticated bootstrap payload, got %#v", data)
	}
	if data["csrf_token"] != csrf {
		t.Fatalf("expected csrf token to stay stable across login/bootstrap")
	}
	actions, ok := data["actions"].([]any)
	if !ok || len(actions) != 4 {
		t.Fatalf("expected 4 admin actions, got %#v", data["actions"])
	}
}

func TestAdminUIBootstrapIncludesCurrentJob(t *testing.T) {
	server := newAdminUITestServer(t)
	stub := &stubAdminRunner{}
	stub.job = &adminJob{
		ID:       "job-bootstrap",
		Action:   "restart",
		Label:    "重启",
		Status:   adminJobStatusRunning,
		ExitCode: 0,
	}
	server.admin.runner = stub
	cookie, _ := adminLogin(t, server)

	bootstrapReq := httptest.NewRequest(http.MethodGet, "/_admin/api/bootstrap", nil)
	bootstrapReq.AddCookie(cookie)
	bootstrapRec := httptest.NewRecorder()
	server.ServeHTTP(bootstrapRec, bootstrapReq)

	if bootstrapRec.Code != http.StatusOK {
		t.Fatalf("expected bootstrap 200, got %d body=%s", bootstrapRec.Code, bootstrapRec.Body.String())
	}
	data := decodeAdminJSON(t, bootstrapRec.Body.Bytes())
	job, ok := data["job"].(map[string]any)
	if !ok {
		t.Fatalf("expected bootstrap job payload, got %#v", data["job"])
	}
	if job["id"] != "job-bootstrap" {
		t.Fatalf("expected bootstrap job id, got %#v", job)
	}
	if job["status"] != string(adminJobStatusRunning) {
		t.Fatalf("expected running bootstrap job, got %#v", job)
	}
}

func TestAdminUIBootstrapExposesCustomProvidersDirRelativePath(t *testing.T) {
	server := newAdminUITestServer(t)
	customProvidersDir := filepath.Join(server.admin.rootDir(), "custom-providers")
	if err := os.MkdirAll(customProvidersDir, 0o755); err != nil {
		t.Fatalf("mkdir custom providers dir: %v", err)
	}
	providerEnv, err := os.ReadFile(filepath.Join(server.admin.rootDir(), "providers", "openai.env"))
	if err != nil {
		t.Fatalf("read existing provider env: %v", err)
	}
	if err := os.WriteFile(filepath.Join(customProvidersDir, "openai.env"), providerEnv, 0o644); err != nil {
		t.Fatalf("write custom provider env: %v", err)
	}
	rootEnv := strings.Join([]string{
		"# 管理界面测试配置",
		"LISTEN_ADDR=:21021",
		"PROXY_API_KEY=root-secret",
		fmt.Sprintf("PROVIDERS_DIR=%s", customProvidersDir),
		fmt.Sprintf("OPENAI_COMPAT_DEBUG_ARCHIVE_DIR=%s", filepath.Join(server.admin.rootDir(), "OPENAI_COMPAT_DEBUG_ARCHIVE_DIR")),
		"DEFAULT_PROVIDER=openai",
		"ENABLE_LEGACY_V1_ROUTES=true",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(server.admin.rootDir(), ".env"), []byte(rootEnv), 0o644); err != nil {
		t.Fatalf("rewrite root env: %v", err)
	}
	if err := server.store.Refresh(); err != nil {
		t.Fatalf("refresh runtime store: %v", err)
	}
	cookie, _ := adminLogin(t, server)

	bootstrapReq := httptest.NewRequest(http.MethodGet, "/_admin/api/bootstrap", nil)
	bootstrapReq.AddCookie(cookie)
	bootstrapRec := httptest.NewRecorder()
	server.ServeHTTP(bootstrapRec, bootstrapReq)

	if bootstrapRec.Code != http.StatusOK {
		t.Fatalf("expected bootstrap 200, got %d body=%s", bootstrapRec.Code, bootstrapRec.Body.String())
	}
	data := decodeAdminJSON(t, bootstrapRec.Body.Bytes())
	if got := data["providers_dir"]; got != "custom-providers" {
		t.Fatalf("expected providers_dir custom-providers, got %#v", got)
	}
	if got := data["providers_dir_name"]; got != "custom-providers" {
		t.Fatalf("expected providers_dir_name custom-providers, got %#v", got)
	}
	if got := data["providers_dir_absolute"]; got != customProvidersDir {
		t.Fatalf("expected providers_dir_absolute %q, got %#v", customProvidersDir, got)
	}
}

func TestAdminUIAppScriptUsesDynamicProvidersDirState(t *testing.T) {
	server := newAdminUITestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/_admin/assets/app.js", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected js asset 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "state.providersDir") {
		t.Fatalf("expected app script to reference state.providersDir, got %s", body)
	}
	if strings.Contains(body, "state.currentDir === 'providers'") {
		t.Fatalf("expected app script to avoid hardcoded providers dir check, got %s", body)
	}
}

func TestAdminUIAppScriptUsesHistoryBackForEditorReturn(t *testing.T) {
	server := newAdminUITestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/_admin/assets/app.js", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected app script 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "history.back();") {
		t.Fatalf("expected editor return flow to use history.back, got %s", body)
	}
	if !strings.Contains(body, "const pending = state._pendingEditorClose;") {
		t.Fatalf("expected dirty editor close flow to preserve pending history target, got %s", body)
	}
}

func TestAdminUIAppScriptRemovesDrawerCurrentPathSummary(t *testing.T) {
	server := newAdminUITestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/_admin/assets/app.js", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected app script 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "support-card compact") || strings.Contains(body, "当前目录</span><strong>") || strings.Contains(body, "当前文件</span><strong>") {
		t.Fatalf("expected drawer bottom current path summary to be removed, got %s", body)
	}
}

func TestAdminUIAppScriptPushesUpdatedViewStateForDrawerNavigation(t *testing.T) {
	server := newAdminUITestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/_admin/assets/app.js", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected app script 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	viewIdx := strings.Index(body, "state.view = newView;")
	pushIdx := strings.Index(body[viewIdx:], "pushHistoryState();")
	if pushIdx >= 0 {
		pushIdx += viewIdx
	}
	if viewIdx < 0 || pushIdx < 0 || pushIdx < viewIdx {
		t.Fatalf("expected drawer navigation to push history after applying the new view state, got %s", body)
	}
}

func TestAdminUIFileEndpointBlocksPathTraversal(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)
	req := httptest.NewRequest(http.MethodGet, "/_admin/api/file?path=../etc/passwd", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-Admin-CSRF", csrf)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for path traversal, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminUIEnvFileReturnsStructuredBlocks(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)
	req := httptest.NewRequest(http.MethodGet, "/_admin/api/file?path=.env", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-Admin-CSRF", csrf)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected env file 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	data := decodeAdminJSON(t, rec.Body.Bytes())
	if data["mode"] != "env" {
		t.Fatalf("expected env mode, got %#v", data["mode"])
	}
	entries, ok := data["env_entries"].([]any)
	if !ok || len(entries) == 0 {
		t.Fatalf("expected structured env entries, got %#v", data["env_entries"])
	}
	first := entries[0].(map[string]any)
	if first["key"] != "LISTEN_ADDR" {
		t.Fatalf("expected first env key LISTEN_ADDR, got %#v", first["key"])
	}
}

func TestAdminUIEnvFileSkipsBlankKeyEntries(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)
	brokenEnv := strings.Join([]string{
		"# 管理界面测试配置",
		"LISTEN_ADDR=:21021",
		"=",
		"PROXY_API_KEY=root-secret",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(server.admin.rootDir(), ".env"), []byte(brokenEnv), 0o644); err != nil {
		t.Fatalf("write broken env: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/_admin/api/file?path=.env", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-Admin-CSRF", csrf)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected env file 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	data := decodeAdminJSON(t, rec.Body.Bytes())
	entries, ok := data["env_entries"].([]any)
	if !ok {
		t.Fatalf("expected env entries array, got %#v", data["env_entries"])
	}
	for _, raw := range entries {
		entry := raw.(map[string]any)
		if strings.TrimSpace(entry["key"].(string)) == "" {
			t.Fatalf("expected blank env entry skipped, got %#v", entry)
		}
	}
}

func TestAdminUITreeOnlyReturnsAllowedFileTypes(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)
	if err := os.WriteFile(filepath.Join(server.admin.rootDir(), "notes.md"), []byte("hidden"), 0o644); err != nil {
		t.Fatalf("write md file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(server.admin.rootDir(), "hidden.env.example"), []byte("template"), 0o644); err != nil {
		t.Fatalf("write example file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(server.admin.rootDir(), "visible.txt"), []byte("shown"), 0o644); err != nil {
		t.Fatalf("write txt file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(server.admin.rootDir(), "visible.ndjson"), []byte("{\"a\":1}\n{\"a\":2}\n"), 0o644); err != nil {
		t.Fatalf("write ndjson file: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/_admin/api/tree?path=", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-Admin-CSRF", csrf)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected tree 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	data := decodeAdminJSON(t, rec.Body.Bytes())
	items, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("expected tree items, got %#v", data["items"])
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.(map[string]any)["name"].(string))
	}
	if slices.Contains(names, "notes.md") {
		t.Fatalf("expected notes.md hidden from tree, got %v", names)
	}
	if slices.Contains(names, "hidden.env.example") {
		t.Fatalf("expected hidden.env.example hidden from tree, got %v", names)
	}
	if !slices.Contains(names, "visible.txt") {
		t.Fatalf("expected visible.txt shown in tree, got %v", names)
	}
	if !slices.Contains(names, "visible.ndjson") {
		t.Fatalf("expected visible.ndjson shown in tree, got %v", names)
	}
}

func TestAdminUIReadNDJSONFileUsesJSONLanguage(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)
	if err := os.WriteFile(filepath.Join(server.admin.rootDir(), "sample.ndjson"), []byte("{\"a\":1}\n{\"a\":2}\n"), 0o644); err != nil {
		t.Fatalf("write ndjson file: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/_admin/api/file?path=sample.ndjson", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-Admin-CSRF", csrf)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected ndjson file 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	data := decodeAdminJSON(t, rec.Body.Bytes())
	if data["mode"] != "text" {
		t.Fatalf("expected ndjson mode text, got %#v", data["mode"])
	}
	if data["language"] != "json" {
		t.Fatalf("expected ndjson language json, got %#v", data["language"])
	}
}

func TestAdminUITreeShowsNDJSONFilesInsideArchiveDirectories(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)
	archiveDir := filepath.Join(server.admin.rootDir(), "OPENAI_COMPAT_DEBUG_ARCHIVE_DIR", "req-demo")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		t.Fatalf("mkdir archive dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(archiveDir, "request.ndjson"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write archive ndjson: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/_admin/api/tree?path=OPENAI_COMPAT_DEBUG_ARCHIVE_DIR/req-demo", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-Admin-CSRF", csrf)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected archive tree 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	data := decodeAdminJSON(t, rec.Body.Bytes())
	items, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("expected tree items, got %#v", data["items"])
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.(map[string]any)["name"].(string))
	}
	if !slices.Contains(names, "request.ndjson") {
		t.Fatalf("expected request.ndjson shown in archive directory, got %v", names)
	}
}

func TestAdminUIStylesKeepTextEditorFullWidth(t *testing.T) {
	server := newAdminUITestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/_admin/assets/app.css", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected css asset 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, ".code-editor-textarea {") || !strings.Contains(body, "width: 100%;") {
		t.Fatalf("expected text editor css to enforce full-width textarea, got %s", body)
	}
	if !strings.Contains(body, ".title-file-pill {") || !strings.Contains(body, "max-width: 10ch;") {
		t.Fatalf("expected editor title pill css to clamp filename width to 10ch, got %s", body)
	}
}

func TestAdminUIStylesDoNotForceTransparentTextForEditors(t *testing.T) {
	server := newAdminUITestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/_admin/assets/app.css", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected css asset 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, ".code-editor-textarea,") || !strings.Contains(body, ".code-editor-highlight,") || !strings.Contains(body, ".code-editor-gutter") {
		t.Fatalf("expected editor layers to share explicit text metrics rules, got %s", body)
	}
	if !strings.Contains(body, "-webkit-text-size-adjust: 100%") || !strings.Contains(body, "text-size-adjust: 100%") {
		t.Fatalf("expected editor css to disable mobile text autosizing drift, got %s", body)
	}
	if !strings.Contains(body, "font-variant-ligatures: none") {
		t.Fatalf("expected editor css to disable ligatures for consistent caret alignment, got %s", body)
	}
	if strings.Contains(body, ".code-editor-textarea-highlighted {") || strings.Contains(body, "-webkit-text-fill-color: transparent") {
		t.Fatalf("expected editor css not to hide textarea text when syntax highlighting is disabled, got %s", body)
	}
}

func TestAdminUIAppScriptDisablesSyntaxHighlightEditors(t *testing.T) {
	server := newAdminUITestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/_admin/assets/app.js", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected app script 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "resolveEditorHighlightLanguage") {
		t.Fatalf("expected app script to remove dynamic syntax highlight selection, got %s", body)
	}
	if strings.Contains(body, "code-editor-textarea-highlighted") || strings.Contains(body, "code-editor-highlight") {
		t.Fatalf("expected app script not to render syntax highlight overlay, got %s", body)
	}
	if strings.Contains(body, "data-highlight-language") {
		t.Fatalf("expected app script not to attach highlight language metadata, got %s", body)
	}
}

func TestAdminUIAppScriptIncludesCopyAction(t *testing.T) {
	server := newAdminUITestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/_admin/assets/app.js", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected app script 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "file-action-copy") || !strings.Contains(body, "copyTreeItemFromMenu") {
		t.Fatalf("expected app script to expose copy action, got %s", body)
	}
}

func TestAdminUIAppScriptIncludesClearCacheModal(t *testing.T) {
	server := newAdminUITestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/_admin/assets/app.js", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected app script 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "clear-cache-button") {
		t.Fatalf("expected app script to include clear-cache-button, got %s", body)
	}
	if !strings.Contains(body, "pathBaseName(state.currentDir) === 'Cache_Info'") {
		t.Fatalf("expected clear-cache button to detect nested Cache_Info path, got %s", body)
	}
	if !strings.Contains(body, "renderClearCacheModal") {
		t.Fatalf("expected app script to include renderClearCacheModal, got %s", body)
	}
	if !strings.Contains(body, "openClearCacheModal") {
		t.Fatalf("expected app script to include openClearCacheModal, got %s", body)
	}
	if !strings.Contains(body, "confirmClearCache") {
		t.Fatalf("expected app script to include confirmClearCache, got %s", body)
	}
	if !strings.Contains(body, "closeClearCacheModal") {
		t.Fatalf("expected app script to include closeClearCacheModal, got %s", body)
	}
	if !strings.Contains(body, "/_admin/api/cacheinfo/providers/clear") || !strings.Contains(body, "provider_id") {
		t.Fatalf("expected app script to call cacheinfo clear API with provider_id, got %s", body)
	}
}

func TestAdminUIAppCSSIncludesClearCacheModalStyles(t *testing.T) {
	server := newAdminUITestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/_admin/assets/app.css", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected css asset 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, ".clear-cache-modal") {
		t.Fatalf("expected app css to include .clear-cache-modal styles, got %s", body)
	}
	if !strings.Contains(body, ".clear-cache-list") {
		t.Fatalf("expected app css to include .clear-cache-list styles, got %s", body)
	}
	if !strings.Contains(body, ".clear-cache-filter-row") {
		t.Fatalf("expected app css to include .clear-cache-filter-row styles, got %s", body)
	}
}

func TestAdminUITreeShowsMarkdownOnlyInProvidersRoot(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)
	providersDir := filepath.Join(server.admin.rootDir(), "providers")
	if err := os.WriteFile(filepath.Join(providersDir, "prompt.md"), []byte("provider prompt"), 0o644); err != nil {
		t.Fatalf("write provider markdown: %v", err)
	}
	nestedDir := filepath.Join(providersDir, "prompts")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested providers dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedDir, "extra.md"), []byte("nested prompt"), 0o644); err != nil {
		t.Fatalf("write nested markdown: %v", err)
	}

	providersRec := httptest.NewRecorder()
	providersReq := httptest.NewRequest(http.MethodGet, "/_admin/api/tree?path=providers", nil)
	providersReq.AddCookie(cookie)
	providersReq.Header.Set("X-Admin-CSRF", csrf)
	server.ServeHTTP(providersRec, providersReq)
	if providersRec.Code != http.StatusOK {
		t.Fatalf("expected providers tree 200, got %d body=%s", providersRec.Code, providersRec.Body.String())
	}
	providersItems, ok := decodeAdminJSON(t, providersRec.Body.Bytes())["items"].([]any)
	if !ok {
		t.Fatalf("expected providers items array")
	}
	providersNames := make([]string, 0, len(providersItems))
	for _, item := range providersItems {
		providersNames = append(providersNames, item.(map[string]any)["name"].(string))
	}
	if !slices.Contains(providersNames, "prompt.md") {
		t.Fatalf("expected prompt.md shown in providers root, got %v", providersNames)
	}

	nestedRec := httptest.NewRecorder()
	nestedReq := httptest.NewRequest(http.MethodGet, "/_admin/api/tree?path=providers/prompts", nil)
	nestedReq.AddCookie(cookie)
	nestedReq.Header.Set("X-Admin-CSRF", csrf)
	server.ServeHTTP(nestedRec, nestedReq)
	if nestedRec.Code != http.StatusOK {
		t.Fatalf("expected nested providers tree 200, got %d body=%s", nestedRec.Code, nestedRec.Body.String())
	}
	nestedItems, ok := decodeAdminJSON(t, nestedRec.Body.Bytes())["items"].([]any)
	if !ok {
		t.Fatalf("expected nested items array")
	}
	nestedNames := make([]string, 0, len(nestedItems))
	for _, item := range nestedItems {
		nestedNames = append(nestedNames, item.(map[string]any)["name"].(string))
	}
	if slices.Contains(nestedNames, "extra.md") {
		t.Fatalf("expected extra.md hidden in nested providers dir, got %v", nestedNames)
	}
}

func TestAdminUISaveEnvReportsRestartValidationErrorsButUIStaysAlive(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)
	rec := adminJSONRequest(t, server, http.MethodPut, "/_admin/api/file", map[string]any{
		"path": ".env",
		"mode": "text",
		"content": strings.Join([]string{
			"LISTEN_ADDR=:21021",
			"PROXY_API_KEY=root-secret",
			fmt.Sprintf("PROVIDERS_DIR=%s", filepath.Join(server.admin.rootDir(), "providers")),
			"DEFAULT_PROVIDER=openai",
			"ENABLE_LEGACY_V1_ROUTES=true",
			"TOTAL_TIMEOUT=bad-duration",
		}, "\n") + "\n",
	}, []*http.Cookie{cookie}, csrf)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected save 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	data := decodeAdminJSON(t, rec.Body.Bytes())
	validation := data["validation"].(map[string]any)
	if validation["restart_ok"] != false {
		t.Fatalf("expected restart validation failure, got %#v", validation)
	}

	bootstrapReq := httptest.NewRequest(http.MethodGet, "/_admin/api/bootstrap", nil)
	bootstrapReq.AddCookie(cookie)
	bootstrapRec := httptest.NewRecorder()
	server.ServeHTTP(bootstrapRec, bootstrapReq)
	if bootstrapRec.Code != http.StatusOK {
		t.Fatalf("expected UI to remain available after invalid env save, got %d body=%s", bootstrapRec.Code, bootstrapRec.Body.String())
	}
}

func TestAdminUIScriptActionUsesWhitelistRunner(t *testing.T) {
	server := newAdminUITestServer(t)
	stub := &stubAdminRunner{}
	server.admin.runner = stub
	cookie, csrf := adminLogin(t, server)

	rec := adminJSONRequest(t, server, http.MethodPost, "/_admin/api/action", map[string]any{
		"action": "restart",
	}, []*http.Cookie{cookie}, csrf)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected action 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(stub.calls) != 1 || stub.calls[0] != "restart" {
		t.Fatalf("expected whitelist runner to execute restart once, got %#v", stub.calls)
	}
	data := decodeAdminJSON(t, rec.Body.Bytes())
	if data["action"] != "restart" {
		t.Fatalf("expected restart action payload, got %#v", data)
	}
}

func TestAdminUIRunnerScriptWritesLifecycleLogs(t *testing.T) {
	root := t.TempDir()
	runner := &adminCommandRunner{rootDir: root}
	if err := os.MkdirAll(runner.jobsDir(), 0o755); err != nil {
		t.Fatalf("mkdir jobs dir: %v", err)
	}
	scriptPath := filepath.Join(root, "sample.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/usr/bin/env bash\nprintf 'script-body\\n'\n"), 0o755); err != nil {
		t.Fatalf("write sample script: %v", err)
	}
	job := &adminJob{
		ID:        "job-lifecycle",
		Action:    "restart",
		Label:     "重启",
		Status:    adminJobStatusRunning,
		StartedAt: time.Unix(1700000000, 0).UTC(),
	}
	if err := os.WriteFile(runner.currentJobPath(), []byte(job.ID), 0o644); err != nil {
		t.Fatalf("write current job: %v", err)
	}
	if err := runner.writeWrapperScript(job, scriptPath); err != nil {
		t.Fatalf("write wrapper script: %v", err)
	}

	cmd := exec.Command("bash", runner.jobScriptPath(job.ID))
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run wrapper script: %v output=%s", err, string(output))
	}

	stored, err := runner.readJob(job.ID)
	if err != nil {
		t.Fatalf("read stored job: %v", err)
	}
	if stored.Status != adminJobStatusSucceeded {
		t.Fatalf("expected succeeded job status, got %#v", stored.Status)
	}
	if !strings.Contains(stored.Output, "[admin-ui] start") {
		t.Fatalf("expected start marker in output, got %q", stored.Output)
	}
	if !strings.Contains(stored.Output, "script-body") {
		t.Fatalf("expected script output in output, got %q", stored.Output)
	}
	if !strings.Contains(stored.Output, "[admin-ui] finish exit=0") {
		t.Fatalf("expected finish marker in output, got %q", stored.Output)
	}
}

func TestAdminUICreateRootEnvFromTemplate(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)

	rec := adminJSONRequest(t, server, http.MethodPost, "/_admin/api/file", map[string]any{
		"dir":  "",
		"name": "staging",
	}, []*http.Cookie{cookie}, csrf)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected create 400 when root .env exists, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminUICreateRootDotEnvWithoutName(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)
	rootEnv := filepath.Join(server.admin.rootDir(), ".env")
	if err := os.Remove(rootEnv); err != nil {
		t.Fatalf("remove existing root env: %v", err)
	}

	rec := adminJSONRequest(t, server, http.MethodPost, "/_admin/api/file", map[string]any{
		"dir":  "",
		"name": "",
	}, []*http.Cookie{cookie}, csrf)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected create 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	content, err := os.ReadFile(rootEnv)
	if err != nil {
		t.Fatalf("read created root env: %v", err)
	}
	if !strings.Contains(string(content), "PROXY_API_KEY=") {
		t.Fatalf("expected root template content, got %s", string(content))
	}
	var payload struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if payload.Path != ".env" {
		t.Fatalf("expected response path .env, got %q", payload.Path)
	}
}

func TestAdminUICreateProviderEnvRewritesProviderID(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)

	rec := adminJSONRequest(t, server, http.MethodPost, "/_admin/api/file", map[string]any{
		"dir":  "providers",
		"name": "demo",
	}, []*http.Cookie{cookie}, csrf)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected create 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	created := filepath.Join(server.admin.rootDir(), "providers", "demo.env")
	content, err := os.ReadFile(created)
	if err != nil {
		t.Fatalf("read created provider env: %v", err)
	}
	if !strings.Contains(string(content), "PROVIDER_ID=demo") {
		t.Fatalf("expected provider id rewrite, got %s", string(content))
	}
}

func TestAdminUICreateProviderEnvUsesOpenAIExampleTemplate(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)
	providersDir := filepath.Join(server.admin.rootDir(), "providers")
	if err := os.Remove(filepath.Join(providersDir, ".env.example")); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove legacy provider template: %v", err)
	}
	customTemplate := strings.Join([]string{
		"# provider template",
		"PROVIDER_ID=openai",
		"PROVIDER_ENABLED=true",
		"UPSTREAM_BASE_URL=https://new-template.example/v1",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(providersDir, "openai.env.example"), []byte(customTemplate), 0o644); err != nil {
		t.Fatalf("write openai provider template: %v", err)
	}

	rec := adminJSONRequest(t, server, http.MethodPost, "/_admin/api/file", map[string]any{
		"dir":  "providers",
		"name": "demo2",
	}, []*http.Cookie{cookie}, csrf)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected create 200 from openai template, got %d body=%s", rec.Code, rec.Body.String())
	}
	created := filepath.Join(providersDir, "demo2.env")
	content, err := os.ReadFile(created)
	if err != nil {
		t.Fatalf("read created provider env: %v", err)
	}
	if !strings.Contains(string(content), "UPSTREAM_BASE_URL=https://new-template.example/v1") {
		t.Fatalf("expected openai provider template content, got %s", string(content))
	}
	if !strings.Contains(string(content), "PROVIDER_ID=demo2") {
		t.Fatalf("expected provider id rewrite from openai template, got %s", string(content))
	}
}

func TestAdminUICreateProviderMarkdownFileWithoutSuffix(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)

	rec := adminJSONRequest(t, server, http.MethodPost, "/_admin/api/file", map[string]any{
		"dir":  "providers",
		"name": "prompt",
		"kind": "md",
	}, []*http.Cookie{cookie}, csrf)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected markdown create 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	created := filepath.Join(server.admin.rootDir(), "providers", "prompt.md")
	content, err := os.ReadFile(created)
	if err != nil {
		t.Fatalf("read created markdown file: %v", err)
	}
	if string(content) != "" {
		t.Fatalf("expected empty markdown file, got %q", string(content))
	}
	var payload struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode markdown create response: %v", err)
	}
	if payload.Path != "providers/prompt.md" {
		t.Fatalf("expected markdown response path providers/prompt.md, got %q", payload.Path)
	}
}

func TestAdminUICreateProviderFilesUsesCustomProvidersDir(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)
	customProvidersDir := filepath.Join(server.admin.rootDir(), "custom-providers")
	if err := os.MkdirAll(customProvidersDir, 0o755); err != nil {
		t.Fatalf("mkdir custom providers dir: %v", err)
	}
	providerEnv, err := os.ReadFile(filepath.Join(server.admin.rootDir(), "providers", "openai.env"))
	if err != nil {
		t.Fatalf("read existing provider env: %v", err)
	}
	if err := os.WriteFile(filepath.Join(customProvidersDir, "openai.env"), providerEnv, 0o644); err != nil {
		t.Fatalf("write custom provider env: %v", err)
	}
	providerTemplate, err := os.ReadFile(filepath.Join(server.admin.rootDir(), "providers", "openai.env.example"))
	if err != nil {
		t.Fatalf("read existing provider template: %v", err)
	}
	if err := os.WriteFile(filepath.Join(customProvidersDir, "openai.env.example"), providerTemplate, 0o644); err != nil {
		t.Fatalf("write custom provider template: %v", err)
	}
	rootEnv := strings.Join([]string{
		"# 管理界面测试配置",
		"LISTEN_ADDR=:21021",
		"PROXY_API_KEY=root-secret",
		fmt.Sprintf("PROVIDERS_DIR=%s", customProvidersDir),
		fmt.Sprintf("OPENAI_COMPAT_DEBUG_ARCHIVE_DIR=%s", filepath.Join(server.admin.rootDir(), "OPENAI_COMPAT_DEBUG_ARCHIVE_DIR")),
		"DEFAULT_PROVIDER=openai",
		"ENABLE_LEGACY_V1_ROUTES=true",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(server.admin.rootDir(), ".env"), []byte(rootEnv), 0o644); err != nil {
		t.Fatalf("rewrite root env: %v", err)
	}
	if err := server.store.Refresh(); err != nil {
		t.Fatalf("refresh runtime store: %v", err)
	}

	cookie, csrf = adminLogin(t, server)
	envRec := adminJSONRequest(t, server, http.MethodPost, "/_admin/api/file", map[string]any{
		"dir":  "custom-providers",
		"name": "demo-custom",
	}, []*http.Cookie{cookie}, csrf)
	if envRec.Code != http.StatusOK {
		t.Fatalf("expected custom provider env create 200, got %d body=%s", envRec.Code, envRec.Body.String())
	}
	envContent, err := os.ReadFile(filepath.Join(customProvidersDir, "demo-custom.env"))
	if err != nil {
		t.Fatalf("read custom provider env: %v", err)
	}
	if !strings.Contains(string(envContent), "PROVIDER_ID=demo-custom") {
		t.Fatalf("expected custom provider env to rewrite provider id, got %s", string(envContent))
	}

	mdRec := adminJSONRequest(t, server, http.MethodPost, "/_admin/api/file", map[string]any{
		"dir":  "custom-providers",
		"name": "prompt-custom",
		"kind": "md",
	}, []*http.Cookie{cookie}, csrf)
	if mdRec.Code != http.StatusOK {
		t.Fatalf("expected custom provider markdown create 200, got %d body=%s", mdRec.Code, mdRec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(customProvidersDir, "prompt-custom.md")); err != nil {
		t.Fatalf("expected custom provider markdown file to exist, err=%v", err)
	}
}

func TestAdminUICopyRegularFileCreatesSiblingCopy(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)
	source := filepath.Join(server.admin.rootDir(), "notes.txt")
	if err := os.WriteFile(source, []byte("hello copy\n"), 0o640); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	rec := adminJSONRequest(t, server, http.MethodPost, "/_admin/api/file", map[string]any{
		"path": "notes.txt",
		"name": "notes-副本.txt",
		"kind": "copy",
	}, []*http.Cookie{cookie}, csrf)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected copy 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	data := decodeAdminJSON(t, rec.Body.Bytes())
	if data["path"] != "notes-副本.txt" {
		t.Fatalf("expected copied path notes-副本.txt, got %#v", data["path"])
	}
	content, err := os.ReadFile(filepath.Join(server.admin.rootDir(), "notes-副本.txt"))
	if err != nil {
		t.Fatalf("read copied file: %v", err)
	}
	if string(content) != "hello copy\n" {
		t.Fatalf("expected copied content preserved, got %q", string(content))
	}
	info, err := os.Stat(filepath.Join(server.admin.rootDir(), "notes-副本.txt"))
	if err != nil {
		t.Fatalf("stat copied file: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("expected copied file to preserve mode 0640, got %o", info.Mode().Perm())
	}
}

func TestAdminUICopyProviderEnvRewritesProviderID(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)
	source := filepath.Join(server.admin.rootDir(), "providers", "demo.env")
	if err := os.WriteFile(source, []byte("PROVIDER_ID=demo\nUPSTREAM_BASE_URL=https://example.com/v1\n"), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	rec := adminJSONRequest(t, server, http.MethodPost, "/_admin/api/file", map[string]any{
		"path": "providers/demo.env",
		"name": "demo-副本.env",
		"kind": "copy",
	}, []*http.Cookie{cookie}, csrf)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected provider env copy 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	content, err := os.ReadFile(filepath.Join(server.admin.rootDir(), "providers", "demo-副本.env"))
	if err != nil {
		t.Fatalf("read copied provider env: %v", err)
	}
	if !strings.Contains(string(content), "PROVIDER_ID=demo-副本") {
		t.Fatalf("expected copied provider env to rewrite provider id, got %s", string(content))
	}
}

func TestAdminUIRenameProviderEnvRewritesProviderIDAndDeleteRemovesFile(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)
	providerPath := filepath.Join(server.admin.rootDir(), "providers", "rename-me.env")
	if err := os.WriteFile(providerPath, []byte("PROVIDER_ID=rename-me\nUPSTREAM_BASE_URL=https://example.com/v1\n"), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	renameRec := adminJSONRequest(t, server, http.MethodPatch, "/_admin/api/file", map[string]any{
		"path":     "providers/rename-me.env",
		"new_name": "renamed.env",
	}, []*http.Cookie{cookie}, csrf)
	if renameRec.Code != http.StatusOK {
		t.Fatalf("expected rename 200, got %d body=%s", renameRec.Code, renameRec.Body.String())
	}
	renamedPath := filepath.Join(server.admin.rootDir(), "providers", "renamed.env")
	content, err := os.ReadFile(renamedPath)
	if err != nil {
		t.Fatalf("read renamed provider env: %v", err)
	}
	if !strings.Contains(string(content), "PROVIDER_ID=renamed") {
		t.Fatalf("expected renamed provider id, got %s", string(content))
	}

	deleteRec := adminJSONRequest(t, server, http.MethodDelete, "/_admin/api/file", map[string]any{
		"path": "providers/renamed.env",
	}, []*http.Cookie{cookie}, csrf)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("expected delete 200, got %d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	if _, err := os.Stat(renamedPath); !os.IsNotExist(err) {
		t.Fatalf("expected renamed provider env deleted, err=%v", err)
	}
}

func TestAdminUICopyRenameAndDeleteDirectory(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)
	sourceDir := filepath.Join(server.admin.rootDir(), "providers", "menus")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "child.txt"), []byte("hello dir\n"), 0o640); err != nil {
		t.Fatalf("write child file: %v", err)
	}

	copyRec := adminJSONRequest(t, server, http.MethodPost, "/_admin/api/file", map[string]any{
		"path": "providers/menus",
		"name": "menus-副本",
		"kind": "copy",
	}, []*http.Cookie{cookie}, csrf)
	if copyRec.Code != http.StatusOK {
		t.Fatalf("expected directory copy 200, got %d body=%s", copyRec.Code, copyRec.Body.String())
	}
	copyPath := filepath.Join(server.admin.rootDir(), "providers", "menus-副本", "child.txt")
	copyContent, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatalf("read copied directory child: %v", err)
	}
	if string(copyContent) != "hello dir\n" {
		t.Fatalf("expected copied directory content preserved, got %q", string(copyContent))
	}

	renameRec := adminJSONRequest(t, server, http.MethodPatch, "/_admin/api/file", map[string]any{
		"path":     "providers/menus",
		"new_name": "menus-renamed",
	}, []*http.Cookie{cookie}, csrf)
	if renameRec.Code != http.StatusOK {
		t.Fatalf("expected directory rename 200, got %d body=%s", renameRec.Code, renameRec.Body.String())
	}
	renamedChild := filepath.Join(server.admin.rootDir(), "providers", "menus-renamed", "child.txt")
	if _, err := os.Stat(renamedChild); err != nil {
		t.Fatalf("expected renamed directory child to exist, err=%v", err)
	}
	if _, err := os.Stat(sourceDir); !os.IsNotExist(err) {
		t.Fatalf("expected original directory moved away, err=%v", err)
	}

	deleteRec := adminJSONRequest(t, server, http.MethodDelete, "/_admin/api/file", map[string]any{
		"path": "providers/menus-renamed",
	}, []*http.Cookie{cookie}, csrf)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("expected directory delete 200, got %d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(server.admin.rootDir(), "providers", "menus-renamed")); !os.IsNotExist(err) {
		t.Fatalf("expected renamed directory deleted, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(server.admin.rootDir(), "providers", "menus-副本")); err != nil {
		t.Fatalf("expected copied directory to remain after deleting renamed source, err=%v", err)
	}
}

func TestAdminUISaveProviderEnvRewritesFilenameFromProviderID(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)
	rec := adminJSONRequest(t, server, http.MethodPut, "/_admin/api/file", map[string]any{
		"path": "providers/openai.env",
		"mode": "env",
		"env_entries": []map[string]any{
			{"key": "PROVIDER_ID", "value": "中文提供商", "leading_lines": []string{}},
			{"key": "PROVIDER_ENABLED", "value": "true", "leading_lines": []string{}},
			{"key": "UPSTREAM_BASE_URL", "value": "https://example.com/v1", "leading_lines": []string{}},
			{"key": "UPSTREAM_API_KEY", "value": "upstream-secret", "leading_lines": []string{}},
		},
		"tail_lines": []string{},
	}, []*http.Cookie{cookie}, csrf)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected save 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	data := decodeAdminJSON(t, rec.Body.Bytes())
	if data["path"] != "providers/中文提供商.env" {
		t.Fatalf("expected renamed provider env path, got %#v", data["path"])
	}
	content, err := os.ReadFile(filepath.Join(server.admin.rootDir(), "providers", "中文提供商.env"))
	if err != nil {
		t.Fatalf("read renamed provider env: %v", err)
	}
	if !strings.Contains(string(content), "PROVIDER_ID=中文提供商") {
		t.Fatalf("expected provider file content synced, got %s", string(content))
	}
	if _, err := os.Stat(filepath.Join(server.admin.rootDir(), "providers", "openai.env")); !os.IsNotExist(err) {
		t.Fatalf("expected old provider file removed, err=%v", err)
	}
}

func TestAdminUIMutatingRequestRequiresCSRFFromSession(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, _ := adminLogin(t, server)

	rec := adminJSONRequest(t, server, http.MethodPost, "/_admin/api/action", map[string]any{
		"action": "restart",
	}, []*http.Cookie{cookie}, "")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when csrf header missing, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminUIClearProviderCacheInfoHistory(t *testing.T) {
	server := newAdminUITestServer(t)
	loc := time.FixedZone("CST", 8*3600)
	cacheMgr := cacheinfo.NewManager(filepath.Join(server.admin.rootDir(), "providers"), loc, []string{"openai", "anthropic"}, nil)
	cacheMgr.SetEnabledProvidersSource(func() []string {
		return []string{"openai", "anthropic"}
	})
	server.CacheInfo = cacheMgr
	server.admin.cacheInfo = cacheMgr

	if err := cacheMgr.RecordFinalUsage("req-openai", "openai", &cacheinfo.Usage{InputTokens: 100, TotalTokens: 100}); err != nil {
		t.Fatal(err)
	}
	if err := cacheMgr.RecordFinalUsage("req-anthropic", "anthropic", &cacheinfo.Usage{InputTokens: 50, TotalTokens: 50}); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := adminLogin(t, server)
	rec := adminJSONRequest(t, server, http.MethodPost, "/_admin/api/cacheinfo/providers/clear", map[string]any{
		"provider_id": "openai",
	}, []*http.Cookie{cookie}, csrf)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected clear provider cacheinfo 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	data := decodeAdminJSON(t, rec.Body.Bytes())
	if data["provider_id"] != "openai" {
		t.Fatalf("expected provider_id openai, got %#v", data["provider_id"])
	}

	stats, err := cacheinfo.LoadProviderStats(filepath.Join(server.admin.rootDir(), "providers"), "openai")
	if err != nil {
		t.Fatal(err)
	}
	if stats == nil || stats.HistoryTotal.TotalTokens != 0 {
		t.Fatalf("expected persisted openai stats cleared, got %#v", stats)
	}

	allData, err := os.ReadFile(filepath.Join(server.admin.rootDir(), "providers", "Cache_Info", "全提供商总计.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(allData), "输入Tokens：150") {
		t.Fatalf("all aggregate still includes cleared provider:\n%s", string(allData))
	}
	if !strings.Contains(string(allData), "输入Tokens：50") {
		t.Fatalf("all aggregate missing remaining provider totals:\n%s", string(allData))
	}
}

func TestAdminCommandRunnerStartDoesNotDeadlock(t *testing.T) {
	runner := &adminCommandRunner{rootDir: t.TempDir()}
	done := make(chan *adminJob, 1)
	errCh := make(chan error, 1)

	go func() {
		job, err := runner.Start("unknown", "unknown")
		if err != nil {
			errCh <- err
			return
		}
		done <- job
	}()

	select {
	case err := <-errCh:
		t.Fatalf("expected Start to return job without error, got %v", err)
	case job := <-done:
		if job == nil {
			t.Fatalf("expected job payload, got nil")
		}
		if job.Status != adminJobStatusFailed {
			t.Fatalf("expected failed unsupported job, got %#v", job)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("expected Start to return promptly, but it deadlocked")
	}
}

type stubAdminRunner struct {
	calls []string
	job   *adminJob
}

func (s *stubAdminRunner) Start(action string, _ string) (*adminJob, error) {
	s.calls = append(s.calls, action)
	s.job = &adminJob{
		ID:       "job-test",
		Action:   action,
		Label:    action,
		Status:   adminJobStatusSucceeded,
		ExitCode: 0,
		Output:   "ok",
	}
	return s.job, nil
}

func (s *stubAdminRunner) Get(id string) (*adminJob, bool) {
	if s.job == nil || s.job.ID != id {
		return nil, false
	}
	return s.job, true
}

func (s *stubAdminRunner) Current() *adminJob {
	return s.job
}

func newAdminUITestServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	providersDir := filepath.Join(root, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers: %v", err)
	}
	rootEnv := strings.Join([]string{
		"# 管理界面测试配置",
		"LISTEN_ADDR=:21021",
		"PROXY_API_KEY=root-secret",
		fmt.Sprintf("PROVIDERS_DIR=%s", providersDir),
		fmt.Sprintf("OPENAI_COMPAT_DEBUG_ARCHIVE_DIR=%s", filepath.Join(root, "OPENAI_COMPAT_DEBUG_ARCHIVE_DIR")),
		"DEFAULT_PROVIDER=openai",
		"ENABLE_LEGACY_V1_ROUTES=true",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte(rootEnv), 0o644); err != nil {
		t.Fatalf("write root env: %v", err)
	}
	rootTemplate := strings.Join([]string{
		"# 根配置模板",
		"LISTEN_ADDR=:21021",
		"PROXY_API_KEY=",
		"PROVIDERS_DIR=./providers",
		"DEFAULT_PROVIDER=openai",
		"ENABLE_LEGACY_V1_ROUTES=true",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(root, ".env.example"), []byte(rootTemplate), 0o644); err != nil {
		t.Fatalf("write root env template: %v", err)
	}
	providerEnv := strings.Join([]string{
		"PROVIDER_ID=openai",
		"PROVIDER_ENABLED=true",
		"UPSTREAM_BASE_URL=https://example.com/v1",
		"UPSTREAM_API_KEY=upstream-secret",
		"UPSTREAM_ENDPOINT_TYPE=responses",
		"SUPPORTS_RESPONSES=true",
		"SUPPORTS_CHAT=true",
		"SUPPORTS_MODELS=true",
		"SUPPORTS_ANTHROPIC_MESSAGES=true",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(providersDir, "openai.env"), []byte(providerEnv), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}
	providerTemplate := strings.Join([]string{
		"# provider 模板",
		"PROVIDER_ID=openai",
		"PROVIDER_ENABLED=true",
		"UPSTREAM_BASE_URL=https://example.com/v1",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(providersDir, "openai.env.example"), []byte(providerTemplate), 0o644); err != nil {
		t.Fatalf("write provider env template: %v", err)
	}
	store, err := config.NewRuntimeStore(filepath.Join(root, ".env"))
	if err != nil {
		t.Fatalf("new runtime store: %v", err)
	}
	return NewServerWithStore(store, nil)
}

func adminLogin(t *testing.T, server *Server) (*http.Cookie, string) {
	t.Helper()
	rec := adminJSONRequest(t, server, http.MethodPost, "/_admin/api/login", map[string]any{
		"password": "root-secret",
		"remember": true,
	}, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("login failed: status=%d body=%s", rec.Code, rec.Body.String())
	}
	result := rec.Result()
	if len(result.Cookies()) == 0 {
		t.Fatalf("expected session cookie on login")
	}
	data := decodeAdminJSON(t, rec.Body.Bytes())
	csrf, _ := data["csrf_token"].(string)
	if csrf == "" {
		t.Fatalf("expected csrf token in login response")
	}
	return result.Cookies()[0], csrf
}

func adminJSONRequest(t *testing.T, server *Server, method, target string, body map[string]any, cookies []*http.Cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(payload)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		req.Header.Set("X-Admin-CSRF", csrf)
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

func decodeAdminJSON(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	var data map[string]any
	if err := json.Unmarshal(payload, &data); err != nil {
		t.Fatalf("decode json: %v payload=%s", err, string(payload))
	}
	return data
}
