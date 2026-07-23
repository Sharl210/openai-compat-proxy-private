package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"openai-compat-proxy/internal/cacheinfo"
	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/tokenestimator"
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

func TestAdminUIStatusOmitsRuntimeMemory(t *testing.T) {
	// Given
	server := newAdminUITestServer(t)
	cookie, _ := adminLogin(t, server)
	req := httptest.NewRequest(http.MethodGet, "/_admin/api/status", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if _, found := decodeAdminJSON(t, rec.Body.Bytes())["runtime_memory"]; found {
		t.Fatalf("expected status payload without runtime_memory, got %s", rec.Body.String())
	}
}

func TestAdminUIMemoryDiagnosticsRequiresSession(t *testing.T) {
	// Given
	server := newAdminUITestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/_admin/api/diagnostics/memory", nil)
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated memory diagnostics, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminUIMemoryDiagnosticsReturnsAuthenticatedMetricWhitelist(t *testing.T) {
	// Given
	server := newAdminUITestServer(t)
	cookie, _ := adminLogin(t, server)
	req := httptest.NewRequest(http.MethodGet, "/_admin/api/diagnostics/memory", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("expected memory diagnostics 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	metrics := decodeAdminJSON(t, rec.Body.Bytes())
	expected := []string{"heap_alloc", "heap_inuse", "heap_idle", "heap_released", "sys", "stack_inuse", "num_gc", "goroutines", "vm_rss", "rss_anon"}
	if len(metrics) != len(expected) {
		t.Fatalf("expected fixed metric whitelist %v, got %#v", expected, metrics)
	}
	for _, key := range expected {
		if _, ok := metrics[key]; !ok {
			t.Fatalf("expected memory metric %q, got %#v", key, metrics)
		}
	}
	for _, sensitive := range []string{"proxy_api_key", "upstream_api_key", "profile", "heap_dump", "secret", "token"} {
		if _, ok := metrics[sensitive]; ok {
			t.Fatalf("expected no sensitive field %q, got %#v", sensitive, metrics)
		}
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

func TestAdminUIStatusRejectsStalePIDFile(t *testing.T) {
	server := newAdminUITestServer(t)
	server.admin.serviceState = func(context.Context) (adminServiceState, bool) {
		return adminServiceState{}, false
	}
	cookie, csrf := adminLogin(t, server)
	pidPath := filepath.Join(server.admin.rootDir(), ".proxy.pid")
	startedAt := time.Date(2026, time.April, 8, 10, 30, 0, 0, time.UTC)
	if err := os.WriteFile(pidPath, []byte("12345\n"), 0o644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}
	if err := os.Chtimes(pidPath, startedAt, startedAt); err != nil {
		t.Fatalf("set pid file time: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/_admin/api/status", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-Admin-CSRF", csrf)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	data := decodeAdminJSON(t, rec.Body.Bytes())
	status, ok := data["status"].(map[string]any)
	if !ok {
		t.Fatalf("expected status object, got %#v", data["status"])
	}
	if got := status["started_at"]; got != "" {
		t.Fatalf("expected stale pid file to omit started_at, got %#v", got)
	}
	if got := status["pid"]; got != "" {
		t.Fatalf("expected stale pid file to omit pid, got %#v", got)
	}
	if got := status["runtime_source"]; got != "" {
		t.Fatalf("expected stale pid file to omit runtime source, got %#v", got)
	}
}

func TestAdminUIStatusPrefersAuthoritativeSystemdServiceState(t *testing.T) {
	server := newAdminUITestServer(t)
	startedAt := time.Date(2026, time.July, 23, 9, 40, 56, 0, time.UTC)
	server.admin.serviceState = func(context.Context) (adminServiceState, bool) {
		return adminServiceState{PID: 4242, StartedAt: startedAt}, true
	}
	if err := os.WriteFile(filepath.Join(server.admin.rootDir(), ".proxy.pid"), []byte("12345\n"), 0o644); err != nil {
		t.Fatalf("write stale pid file: %v", err)
	}
	cookie, _ := adminLogin(t, server)
	req := httptest.NewRequest(http.MethodGet, "/_admin/api/status", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	status, ok := decodeAdminJSON(t, rec.Body.Bytes())["status"].(map[string]any)
	if !ok {
		t.Fatalf("expected status object, got %s", rec.Body.String())
	}
	if got := status["pid"]; got != "4242" {
		t.Fatalf("expected authoritative systemd pid, got %#v", got)
	}
	if got := status["started_at"]; got != startedAt.Format(time.RFC3339) {
		t.Fatalf("expected authoritative started_at %q, got %#v", startedAt.Format(time.RFC3339), got)
	}
	if got := status["runtime_source"]; got != "systemd" {
		t.Fatalf("expected systemd runtime source, got %#v", got)
	}
}

func TestParseAdminSystemdTimestampUSec(t *testing.T) {
	want := time.Date(2026, time.July, 23, 21, 57, 13, 0, time.FixedZone("CST", 8*60*60)).UTC()
	got := parseAdminSystemdTimestampUSec("1784815033000000")
	if !got.Equal(want) {
		t.Fatalf("parseAdminSystemdTimestampUSec() = %s, want %s", got, want)
	}
	if got := parseAdminSystemdTimestampUSec("Thu 2026-07-23 21:57:13 CST"); !got.IsZero() {
		t.Fatalf("expected display timestamp to be rejected, got %s", got)
	}
}

func TestAdminSystemdServiceNameUsesRuntimeUnit(t *testing.T) {
	t.Setenv("OPENAI_COMPAT_SYSTEMD_UNIT", "custom-proxy.service")
	if got := adminSystemdServiceName(); got != "custom-proxy.service" {
		t.Fatalf("expected runtime service name, got %q", got)
	}
}

func TestLookupAdminSystemdServiceStateUsesRuntimeUnitAndMicroseconds(t *testing.T) {
	root := t.TempDir()
	argsPath := filepath.Join(root, "systemctl.args")
	systemctl := filepath.Join(root, "systemctl")
	mustWriteAdminFile(t, systemctl, fmt.Sprintf("#!/usr/bin/env bash\nprintf '%%s\\n' \"$*\" > %q\nprintf 'ActiveState=active\\nMainPID=%%s\\nExecMainStartTimestampUSec=1784815033000000\\nActiveEnterTimestampUSec=1784815000000000\\n' \"$ADMIN_SYSTEMD_TEST_PID\"\n", argsPath))
	t.Setenv("ADMIN_SYSTEMD_TEST_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("OPENAI_COMPAT_SYSTEMD_UNIT", "custom-proxy.service")
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))

	service, ok := lookupAdminSystemdServiceState(context.Background())
	if !ok {
		t.Fatal("expected systemd service state")
	}
	if service.PID != os.Getpid() {
		t.Fatalf("expected current process pid, got %d", service.PID)
	}
	wantStartedAt := time.Date(2026, time.July, 23, 21, 57, 13, 0, time.FixedZone("CST", 8*60*60)).UTC()
	if !service.StartedAt.Equal(wantStartedAt) {
		t.Fatalf("expected started_at %s, got %s", wantStartedAt, service.StartedAt)
	}
	if args := mustReadAdminFile(t, argsPath); !strings.Contains(args, "custom-proxy.service") {
		t.Fatalf("expected custom service name in systemctl call, got %q", args)
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

func TestAdminUISearchFindsFilenamesFromCurrentDirectory(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)
	root := server.admin.rootDir()
	logsDir := filepath.Join(root, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logsDir, "req-root.txt"), []byte("root"), 0o644); err != nil {
		t.Fatalf("write root log: %v", err)
	}
	nestedDir := filepath.Join(logsDir, "nested")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedDir, "req-nested.txt"), []byte("nested"), 0o644); err != nil {
		t.Fatalf("write nested log: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "req-outside.txt"), []byte("outside"), 0o644); err != nil {
		t.Fatalf("write outside log: %v", err)
	}

	recursiveReq := httptest.NewRequest(http.MethodGet, "/_admin/api/search?path=logs&query=req-*.txt", nil)
	recursiveReq.AddCookie(cookie)
	recursiveReq.Header.Set("X-Admin-CSRF", csrf)
	recursiveRec := httptest.NewRecorder()
	server.ServeHTTP(recursiveRec, recursiveReq)
	if recursiveRec.Code != http.StatusOK {
		t.Fatalf("expected recursive search 200, got %d body=%s", recursiveRec.Code, recursiveRec.Body.String())
	}
	recursiveData := decodeAdminJSON(t, recursiveRec.Body.Bytes())
	if recursiveData["path"] != "logs" || recursiveData["query"] != "req-*.txt" || recursiveData["recursive"] != true {
		t.Fatalf("expected search metadata, got %#v", recursiveData)
	}
	recursiveNames := adminTreeItemNames(t, recursiveRec.Body.Bytes())
	if !slices.Contains(recursiveNames, "req-root.txt") || !slices.Contains(recursiveNames, "req-nested.txt") {
		t.Fatalf("expected recursive search to include direct and nested matches, got %v", recursiveNames)
	}
	if slices.Contains(recursiveNames, "req-outside.txt") {
		t.Fatalf("expected search to stay inside current directory, got %v", recursiveNames)
	}

	directReq := httptest.NewRequest(http.MethodGet, "/_admin/api/search?path=logs&query=req-*.txt&recursive=false", nil)
	directReq.AddCookie(cookie)
	directReq.Header.Set("X-Admin-CSRF", csrf)
	directRec := httptest.NewRecorder()
	server.ServeHTTP(directRec, directReq)
	if directRec.Code != http.StatusOK {
		t.Fatalf("expected direct search 200, got %d body=%s", directRec.Code, directRec.Body.String())
	}
	directData := decodeAdminJSON(t, directRec.Body.Bytes())
	if directData["recursive"] != false {
		t.Fatalf("expected recursive=false metadata, got %#v", directData)
	}
	directNames := adminTreeItemNames(t, directRec.Body.Bytes())
	if !slices.Contains(directNames, "req-root.txt") {
		t.Fatalf("expected direct search to include direct match, got %v", directNames)
	}
	if slices.Contains(directNames, "req-nested.txt") {
		t.Fatalf("expected direct search to exclude nested match, got %v", directNames)
	}
}

func TestAdminUISearchEmptyOrMissingQueryUsesWildcard(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)
	root := server.admin.rootDir()
	logsDir := filepath.Join(root, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logsDir, "req-root.txt"), []byte("root"), 0o644); err != nil {
		t.Fatalf("write root log: %v", err)
	}
	nestedDir := filepath.Join(logsDir, "nested")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedDir, "req-nested.txt"), []byte("nested"), 0o644); err != nil {
		t.Fatalf("write nested log: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "req-outside.txt"), []byte("outside"), 0o644); err != nil {
		t.Fatalf("write outside log: %v", err)
	}

	for _, tc := range []struct {
		name string
		url  string
	}{
		{name: "missing query", url: "/_admin/api/search?path=logs"},
		{name: "empty query", url: "/_admin/api/search?path=logs&query="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			req.AddCookie(cookie)
			req.Header.Set("X-Admin-CSRF", csrf)
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected wildcard search 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			data := decodeAdminJSON(t, rec.Body.Bytes())
			if data["query"] != "*" {
				t.Fatalf("expected wildcard query metadata, got %#v", data["query"])
			}
			if data["recursive"] != true {
				t.Fatalf("expected recursive search by default, got %#v", data["recursive"])
			}
			names := adminTreeItemNames(t, rec.Body.Bytes())
			if !slices.Contains(names, "req-root.txt") || !slices.Contains(names, "req-nested.txt") {
				t.Fatalf("expected wildcard search to include visible files in scope, got %v", names)
			}
			if slices.Contains(names, "req-outside.txt") {
				t.Fatalf("expected search to stay inside current directory, got %v", names)
			}
		})
	}
}

func TestAdminUISearchRegexDoesNotApplyToFilenameQuery(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)
	root := server.admin.rootDir()
	logsDir := filepath.Join(root, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logsDir, "req-root.txt"), []byte("Needle"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/_admin/api/search?path=logs&query=req-.*.txt&regex=true", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-Admin-CSRF", csrf)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected glob filename search 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	data := decodeAdminJSON(t, rec.Body.Bytes())
	if data["regex"] != true {
		t.Fatalf("expected regex metadata to stay true, got %#v", data["regex"])
	}
	names := adminTreeItemNames(t, rec.Body.Bytes())
	if len(names) != 0 {
		t.Fatalf("expected filename query to stay glob-based, got %v", names)
	}
}

func TestAdminUISearchInvalidContentRegexErrors(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)
	root := server.admin.rootDir()
	logsDir := filepath.Join(root, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logsDir, "alpha.txt"), []byte("Needle\n"), 0o644); err != nil {
		t.Fatalf("write alpha: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/_admin/api/search?path=logs&query=*.txt&regex=true&content_contains=(", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-Admin-CSRF", csrf)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid content regex 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid content regex") {
		t.Fatalf("expected invalid content regex error, got %s", rec.Body.String())
	}
}

func TestAdminUISearchAdvancedFiltersContentSizeCaseAndRegex(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)
	root := server.admin.rootDir()
	logsDir := filepath.Join(root, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logsDir, "alpha.txt"), []byte("Needle\n"), 0o644); err != nil {
		t.Fatalf("write alpha: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logsDir, "beta.txt"), []byte("needle with extra bytes\n"), 0o644); err != nil {
		t.Fatalf("write beta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logsDir, "gamma.json"), []byte(`{"kind":"needle"}`), 0o644); err != nil {
		t.Fatalf("write gamma: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/_admin/api/search?path=logs&query=*.txt&regex=true&case_sensitive=true&content_contains=Needle&min_size_bytes=1&max_size_bytes=16", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-Admin-CSRF", csrf)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected advanced search 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	data := decodeAdminJSON(t, rec.Body.Bytes())
	if data["case_sensitive"] != true || data["regex"] != true || data["content_contains"] != "Needle" {
		t.Fatalf("expected advanced metadata, got %#v", data)
	}
	names := adminTreeItemNames(t, rec.Body.Bytes())
	if !slices.Equal(names, []string{"alpha.txt"}) {
		t.Fatalf("expected only alpha.txt to match advanced filters, got %v", names)
	}

	invalidReq := httptest.NewRequest(http.MethodGet, "/_admin/api/search?path=logs&query=*&min_size_bytes=20&max_size_bytes=10", nil)
	invalidReq.AddCookie(cookie)
	invalidReq.Header.Set("X-Admin-CSRF", csrf)
	invalidRec := httptest.NewRecorder()
	server.ServeHTTP(invalidRec, invalidReq)
	if invalidRec.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid size range 400, got %d body=%s", invalidRec.Code, invalidRec.Body.String())
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

func TestAdminUITreeSortsLogDirectoryByModifiedDesc(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)
	logDir := filepath.Join(server.admin.rootDir(), "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir logs dir: %v", err)
	}
	oldPath := filepath.Join(logDir, "req-old.txt")
	midPath := filepath.Join(logDir, "req-mid.txt")
	newPath := filepath.Join(logDir, "req-new.txt")
	for _, p := range []string{oldPath, midPath, newPath} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("write log file %s: %v", p, err)
		}
	}
	base := time.Now().Add(-5 * time.Minute)
	if err := os.Chtimes(oldPath, base, base); err != nil {
		t.Fatalf("chtimes old: %v", err)
	}
	if err := os.Chtimes(midPath, base.Add(2*time.Minute), base.Add(2*time.Minute)); err != nil {
		t.Fatalf("chtimes mid: %v", err)
	}
	if err := os.Chtimes(newPath, base.Add(4*time.Minute), base.Add(4*time.Minute)); err != nil {
		t.Fatalf("chtimes new: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/_admin/api/tree?path=logs", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-Admin-CSRF", csrf)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected log tree 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	names := adminTreeItemNames(t, rec.Body.Bytes())
	if len(names) < 3 {
		t.Fatalf("expected >=3 log entries, got %v", names)
	}
	if names[0] != "req-new.txt" || names[1] != "req-mid.txt" || names[2] != "req-old.txt" {
		t.Fatalf("expected logs sorted by latest modified first, got %v", names)
	}
}

func TestAdminUITreeSortsArchiveRootByModifiedDesc(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)
	archiveRoot := filepath.Join(server.admin.rootDir(), "OPENAI_COMPAT_DEBUG_ARCHIVE_DIR")
	if err := os.MkdirAll(archiveRoot, 0o755); err != nil {
		t.Fatalf("mkdir archive root: %v", err)
	}
	oldDir := filepath.Join(archiveRoot, "req-old")
	midDir := filepath.Join(archiveRoot, "req-mid")
	newDir := filepath.Join(archiveRoot, "req-new")
	for _, p := range []string{oldDir, midDir, newDir} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir archive entry %s: %v", p, err)
		}
	}
	base := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(oldDir, base, base); err != nil {
		t.Fatalf("chtimes old dir: %v", err)
	}
	if err := os.Chtimes(midDir, base.Add(3*time.Minute), base.Add(3*time.Minute)); err != nil {
		t.Fatalf("chtimes mid dir: %v", err)
	}
	if err := os.Chtimes(newDir, base.Add(6*time.Minute), base.Add(6*time.Minute)); err != nil {
		t.Fatalf("chtimes new dir: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/_admin/api/tree?path=OPENAI_COMPAT_DEBUG_ARCHIVE_DIR", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-Admin-CSRF", csrf)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected archive root tree 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	names := adminTreeItemNames(t, rec.Body.Bytes())
	positions := map[string]int{}
	for i, name := range names {
		positions[name] = i
	}
	newPos, okNew := positions["req-new"]
	midPos, okMid := positions["req-mid"]
	oldPos, okOld := positions["req-old"]
	if !okNew || !okMid || !okOld {
		t.Fatalf("expected req-new/req-mid/req-old entries present, got %v", names)
	}
	if !(newPos < midPos && midPos < oldPos) {
		t.Fatalf("expected archive root sorted by latest modified first for target entries, got %v", names)
	}
}

func adminTreeItemNames(t *testing.T, payload []byte) []string {
	t.Helper()
	data := decodeAdminJSON(t, payload)
	items, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("expected tree items, got %#v", data["items"])
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.(map[string]any)["name"].(string))
	}
	return names
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

func TestAdminUISaveProviderEnvReportsInvalidOpenAIServiceTier(t *testing.T) {
	server := newAdminUITestServer(t)
	cookie, csrf := adminLogin(t, server)
	rec := adminJSONRequest(t, server, http.MethodPut, "/_admin/api/file", map[string]any{
		"path": "providers/openai.env",
		"mode": "text",
		"content": strings.Join([]string{
			"PROVIDER_ID=openai",
			"PROVIDER_ENABLED=true",
			"UPSTREAM_BASE_URL=https://example.com/v1",
			"UPSTREAM_API_KEY=upstream-secret",
			"UPSTREAM_ENDPOINT_TYPE=responses",
			"OPENAI_SERVICE_TIER=fast",
			"SUPPORTS_RESPONSES=true",
			"SUPPORTS_CHAT=true",
			"SUPPORTS_MODELS=true",
			"SUPPORTS_ANTHROPIC_MESSAGES=true",
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
	restartError, _ := validation["restart_error"].(string)
	if !strings.Contains(restartError, "invalid OPENAI_SERVICE_TIER") {
		t.Fatalf("expected restart error to mention OPENAI_SERVICE_TIER, got %#v", validation)
	}

	bootstrapReq := httptest.NewRequest(http.MethodGet, "/_admin/api/bootstrap", nil)
	bootstrapReq.AddCookie(cookie)
	bootstrapRec := httptest.NewRecorder()
	server.ServeHTTP(bootstrapRec, bootstrapReq)
	if bootstrapRec.Code != http.StatusOK {
		t.Fatalf("expected UI to remain available after invalid provider env save, got %d body=%s", bootstrapRec.Code, bootstrapRec.Body.String())
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

func TestAdminCommandRunnerUsesTransientSystemdUnit(t *testing.T) {
	root := t.TempDir()
	systemdLog := filepath.Join(root, "systemd-run.log")
	systemdRun := filepath.Join(root, "fake-bin", "systemd-run")
	mustWriteAdminFile(t, systemdRun, "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> \"$ADMIN_SYSTEMD_RUN_LOG\"\nargs=(\"$@\")\nfor ((i = 0; i < ${#args[@]}; i++)); do\n  if [[ \"${args[$i]}\" != --* ]]; then\n    \"${args[@]:$i}\" &\n    exit 0\n  fi\ndone\nexit 64\n")
	mustWriteAdminFile(t, filepath.Join(root, "scripts", "restart-linux.sh"), "#!/usr/bin/env bash\nprintf 'restart-body\\n'\n")
	runner := &adminCommandRunner{rootDir: root, systemdRunBin: systemdRun}
	t.Setenv("ADMIN_SYSTEMD_RUN_LOG", systemdLog)

	job, err := runner.Start("restart", "重启")
	if err != nil {
		t.Fatalf("start transient job: %v", err)
	}
	if job.Status != adminJobStatusRunning {
		t.Fatalf("expected running job after launch, got %#v", job)
	}
	completed := mustWaitForAdminJob(t, runner, job.ID)
	if completed.Status != adminJobStatusSucceeded {
		t.Fatalf("expected transient job to persist success, got %#v", completed)
	}
	if !strings.Contains(completed.Output, "restart-body") {
		t.Fatalf("expected transient job output, got %q", completed.Output)
	}
	command := mustReadAdminFile(t, systemdLog)
	for _, want := range []string{
		"--collect",
		"--no-block",
		"--service-type=exec",
		"--unit=" + adminTransientJobUnitName(job.ID),
		"--working-directory=" + root,
		filepath.Join(root, ".admin-ui", "jobs", job.ID+".runner.sh"),
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("expected transient systemd command to contain %q, got %q", want, command)
		}
	}
}

func TestAdminCommandRunnerFallsBackWithoutTransientSystemd(t *testing.T) {
	root := t.TempDir()
	mustWriteAdminFile(t, filepath.Join(root, "scripts", "restart-linux.sh"), "#!/usr/bin/env bash\nprintf 'fallback-body\\n'\n")
	runner := &adminCommandRunner{
		rootDir:        root,
		systemdRunBin:  filepath.Join(root, "missing-systemd-run"),
		systemdManaged: func() bool { return false },
	}

	job, err := runner.Start("restart", "重启")
	if err != nil {
		t.Fatalf("start direct fallback job: %v", err)
	}
	completed := mustWaitForAdminJob(t, runner, job.ID)
	if completed.Status != adminJobStatusSucceeded {
		t.Fatalf("expected fallback job to persist success, got %#v", completed)
	}
	for _, want := range []string{"isolated systemd runner unavailable; using direct fallback", "fallback-body"} {
		if !strings.Contains(completed.Output, want) {
			t.Fatalf("expected fallback output to contain %q, got %q", want, completed.Output)
		}
	}
}

func TestAdminCommandRunnerDoesNotUseDirectFallbackWhenSystemdManaged(t *testing.T) {
	root := t.TempDir()
	systemdRun := filepath.Join(root, "fake-bin", "systemd-run")
	mustWriteAdminFile(t, systemdRun, "#!/usr/bin/env bash\nexit 23\n")
	mustWriteAdminFile(t, filepath.Join(root, "scripts", "restart-linux.sh"), "#!/usr/bin/env bash\nprintf 'must-not-run\\n'\n")
	runner := &adminCommandRunner{
		rootDir:        root,
		systemdRunBin:  systemdRun,
		systemdManaged: func() bool { return true },
	}

	job, err := runner.Start("restart", "重启")
	if err != nil {
		t.Fatalf("start managed job: %v", err)
	}
	if job.Status != adminJobStatusFailed || job.ExitCode != 1 {
		t.Fatalf("expected managed launch failure, got %#v", job)
	}
	if strings.Contains(job.Output, "must-not-run") || strings.Contains(job.Output, "using direct fallback") {
		t.Fatalf("managed job must not use direct fallback, got %q", job.Output)
	}
	if !strings.Contains(job.Output, "cannot start isolated systemd runner") {
		t.Fatalf("expected isolated runner failure, got %q", job.Output)
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
	server := NewServerWithStore(store, nil, nil)
	server.admin.serviceState = func(context.Context) (adminServiceState, bool) {
		return adminServiceState{}, false
	}
	return server
}

func mustWaitForAdminJob(t *testing.T, runner *adminCommandRunner, id string) *adminJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, ok := runner.Get(id)
		if ok && job.Status != adminJobStatusRunning {
			return job
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("admin job %s did not reach a terminal state", id)
	return nil
}

func mustWriteAdminFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustReadAdminFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
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

func TestWithTokenEstimatorManagerRoundTrip(t *testing.T) {
	mgr := tokenestimator.NewManager(t.TempDir(), time.UTC, func() []string { return []string{"openai"} })
	ctx := withTokenEstimatorManager(context.Background(), mgr)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil).WithContext(ctx)
	if got := tokenEstimatorManagerFromRequest(req); got == nil {
		t.Fatal("expected token estimator manager from request")
	}
}

func TestAdminUIAllowsDeletingTokenEstimatorFiles(t *testing.T) {
	server := newAdminUITestServer(t)
	providersDir := server.store.Active().Config.ProvidersDir
	key := tokenestimator.BucketKey{ProviderID: "openai", EndpointType: "responses", Model: "gpt-5.4"}
	state := &tokenestimator.BucketState{SchemaVersion: 1, EstimatorVersion: 1, ProviderID: key.ProviderID, EndpointType: key.EndpointType, FinalUpstreamRawModel: key.Model, SafeModelName: tokenestimator.SafeModelName(key.Model)}
	if err := tokenestimator.SaveBucketState(providersDir, key, state); err != nil {
		t.Fatalf("SaveBucketState error: %v", err)
	}
	jsonPath, _ := tokenestimator.BucketPaths(providersDir, key)
	adminPath := "/providers/Token_Estimator/SYSTEM_JSON_FILES/openai/responses/" + filepath.Base(jsonPath)
	if err := server.admin.deleteAdminFile(adminPath); err != nil {
		t.Fatalf("deleteAdminFile error: %v", err)
	}
}
