package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
