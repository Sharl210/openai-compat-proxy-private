package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestConfiguredProviderSelectionRoutesReplacedDisplayName(t *testing.T) {
	alpha := newOverlaySuffixUpstream(t, []string{"realtime-base"})
	defer alpha.Close()
	beta := newOverlaySuffixUpstream(t, []string{"other-model"})
	defer beta.Close()
	cfg := defaultOverlaySuffixConfig(alpha.URL, beta.URL)
	// MANUAL_MODELS 按新语义配置伪原始名（替换后的名）。
	cfg.Providers[0].ManualModels = []string{"QDeepseekV4/deepseek-v4-flash"}
	cfg.Providers[0].RawModelNameReplace = "#re:.*quectel-github-copilot/(.*):$1"
	cfg.Providers[0].RawModelNameReplaceRules = mustRawModelNameReplaceRules(t, "#re:.*quectel-github-copilot/(.*):$1")
	store := config.NewStaticRuntimeStore(cfg)

	// 展示名（= MANUAL_MODELS 伪原始名）请求应路由到 alpha。
	providerID, resolvedModel, _, ok := configuredDefaultProviderSelection(store.Active(), "QDeepseekV4/deepseek-v4-flash", "")
	if !ok || providerID != "alpha" {
		t.Fatalf("expected pseudo-name display to route to alpha, got provider=%q ok=%t", providerID, ok)
	}
	if resolvedModel != "QDeepseekV4/deepseek-v4-flash" {
		t.Fatalf("expected resolved model to be the pseudo-original name, got %q", resolvedModel)
	}
}

func TestConfiguredProviderSelectionRoutesPseudoNameWithTemplate(t *testing.T) {
	alpha := newOverlaySuffixUpstream(t, []string{"realtime-base"})
	defer alpha.Close()
	beta := newOverlaySuffixUpstream(t, []string{"other-model"})
	defer beta.Close()
	cfg := defaultOverlaySuffixConfig(alpha.URL, beta.URL)
	cfg.Providers[0].ManualModels = []string{"QDeepseekV4/deepseek-v4-flash"}
	cfg.Providers[0].ModelIDTemplate = "packy-{{model}}"
	store := config.NewStaticRuntimeStore(cfg)

	// 带模板前缀的展示名请求应还原后路由到 alpha。
	providerID, resolvedModel, _, ok := configuredDefaultProviderSelection(store.Active(), "packy-QDeepseekV4/deepseek-v4-flash", "")
	if !ok || providerID != "alpha" {
		t.Fatalf("expected templated pseudo-name to route to alpha, got provider=%q ok=%t", providerID, ok)
	}
	if resolvedModel != "QDeepseekV4/deepseek-v4-flash" {
		t.Fatalf("expected resolved model to be pseudo-original name, got %q", resolvedModel)
	}
}

func TestRawModelNameReplaceEndToEndChatRequest(t *testing.T) {
	var upstreamPayload map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"realtime-base","object":"model"}]}`))
			return
		}
		if err := decodeJSONBody(r, &upstreamPayload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","model":"QDeepseekV4/deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.DefaultProvider = "alpha"
	cfg.EnableLegacyV1Routes = true
	cfg.DownstreamNonStreamStrategy = config.DownstreamNonStreamStrategyUpstreamNonStream
	cfg.Providers = []config.ProviderConfig{{
		ID:                       "alpha",
		Enabled:                  true,
		UpstreamBaseURL:          upstream.URL,
		UpstreamAPIKey:           "alpha-key",
		UpstreamEndpointType:     config.UpstreamEndpointTypeChat,
		SupportsChat:             true,
		ManualModels:             []string{"QDeepseekV4/deepseek-v4-flash"},
		RawModelNameReplace:      "#re:.*quectel-github-copilot/(.*):$1",
		RawModelNameReplaceRules: mustRawModelNameReplaceRules(t, "#re:.*quectel-github-copilot/(.*):$1"),
	}}
	store := config.NewStaticRuntimeStore(cfg)
	server := NewServerWithStore(store, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"QDeepseekV4/deepseek-v4-flash","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "QDeepseekV4/deepseek-v4-flash" {
		t.Fatalf("expected replaced upstream model in header, got %q", got)
	}
	if got := upstreamPayload["model"]; got != "QDeepseekV4/deepseek-v4-flash" {
		t.Fatalf("expected replaced model sent upstream, got %#v", upstreamPayload["model"])
	}
}

func decodeJSONBody(r *http.Request, target any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(target)
}
