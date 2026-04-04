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
	if err := os.WriteFile(filepath.Join(server.admin.rootDir(), "visible.txt"), []byte("shown"), 0o644); err != nil {
		t.Fatalf("write txt file: %v", err)
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
	if !slices.Contains(names, "visible.txt") {
		t.Fatalf("expected visible.txt shown in tree, got %v", names)
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
	if err := os.WriteFile(filepath.Join(providersDir, ".env.example"), []byte(providerTemplate), 0o644); err != nil {
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
