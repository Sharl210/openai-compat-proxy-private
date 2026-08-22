package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestAdminModelListEndpoint(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[
				{"id":"quectel-github-copilot/GLM/glm-5.2","object":"model"},
				{"id":"quectel-github-copilot/QDeepseekV4/deepseek-v4-flash","object":"model"}
			]}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"r","object":"response","model":"x","choices":[]}`))
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.DefaultProvider = "copilot-work"
	cfg.ProxyAPIKey = "test-admin-key"
	cfg.Providers = []config.ProviderConfig{{
		ID:                       "copilot-work",
		Enabled:                  true,
		UpstreamBaseURL:          upstream.URL,
		UpstreamAPIKey:           "key",
		UpstreamEndpointType:     config.UpstreamEndpointTypeChat,
		SupportsChat:             true,
		SupportsModels:           true,
		ManualModels:             []string{"#re:.*deepseek.*", "#re:.*glm.*"},
		RawModelNameReplace:      "#re:.*/(.*):$1",
		RawModelNameReplaceRules: mustRawModelNameReplaceRules(t, "#re:.*/(.*):$1"),
	}}
	store := config.NewStaticRuntimeStore(cfg)
	a := &adminUI{store: store, runner: newNoopAdminActionRunner()}
	mux := http.NewServeMux()
	mux.HandleFunc("/_admin/api/model-list", allowMethods(a.handleModelList(), http.MethodGet))

	// 未登录应 401
	req := httptest.NewRequest(http.MethodGet, "/_admin/api/model-list?provider=copilot-work", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without session, got %d", rec.Code)
	}

	// 注入会话后验证
	injectAdminSession(t, a, mux, "copilot-work")
}

func injectAdminSession(t *testing.T, a *adminUI, mux *http.ServeMux, providerID string) {
	t.Helper()
	// 直接调用 handler 内部逻辑不便注入 session，改用 cookie 流程：先设置一个合法 session。
	// adminUI 的 session 校验用 hmac cookie；这里通过 issueSessionCookie 生成。
	cookie, _, err := a.issueSessionCookie(false)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/_admin/api/model-list?provider="+providerID, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with session, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp adminModelListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.RawModels) != 2 {
		t.Fatalf("expected 2 raw models, got %d: %v", len(resp.RawModels), resp.RawModels)
	}
	if len(resp.MappedModels) != 2 {
		t.Fatalf("expected 2 mapped entries, got %d", len(resp.MappedModels))
	}
	// 校验映射：原始名 → 伪原始名（提取最后一段）
	found := false
	for _, m := range resp.MappedModels {
		if m.Raw == "quectel-github-copilot/GLM/glm-5.2" && m.Pseudo == "glm-5.2" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected GLM mapping raw->pseudo, got %+v", resp.MappedModels)
	}
}

type noopAdminActionRunner struct{}

func newNoopAdminActionRunner() *noopAdminActionRunner { return &noopAdminActionRunner{} }

func (n *noopAdminActionRunner) Start(action string, label string) (*adminJob, error) {
	return &adminJob{ID: "noop", Action: action, Label: label, Status: "succeeded"}, nil
}
func (n *noopAdminActionRunner) Get(id string) (*adminJob, bool) {
	return &adminJob{ID: id, Status: "succeeded"}, true
}
func (n *noopAdminActionRunner) Current() *adminJob { return nil }
