package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

// 回归：根 V1_MODEL_MAP 映射 deepseek-v4-flash -> [copilot]deepseek-v4-flash，
// copilot-work 有 RAW 规则（伪原始名）与 MODEL_MAP，请求应还原全名发上游。
func TestRootV1MapDeepseekFlashReversesRawNameBeforeUpstream(t *testing.T) {
	var upstreamPayload map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"quectel-github-copilot/QDeepseekV4/deepseek-v4-flash","object":"model"}]}`))
			return
		}
		decodeJSONBody(r, &upstreamPayload)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"r","object":"response","model":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.DefaultProvider = "copilot-work"
	cfg.EnableLegacyV1Routes = true
	cfg.DownstreamNonStreamStrategy = config.DownstreamNonStreamStrategyUpstreamNonStream
	cfg.V1ModelMap = []config.ModelMapEntry{
		config.NewModelMapEntry("deepseek-v4-flash", "[copilot]deepseek-v4-flash"),
	}
	cfg.Providers = []config.ProviderConfig{{
		ID:                       "copilot-work",
		Enabled:                  true,
		UpstreamBaseURL:          upstream.URL,
		UpstreamAPIKey:           "key",
		UpstreamEndpointType:     config.UpstreamEndpointTypeChat,
		SupportsChat:             true,
		SupportsModels:           true,
		ModelIDTemplate:          "[copilot]{{model}}",
		ManualModels:             []string{"#re:.*deepseek.*"},
		ModelMap:                 []config.ModelMapEntry{config.NewModelMapEntry("#re:(.*)", "$1-max")},
		HiddenModels:             []string{"#re:.*"},
		RawModelNameReplace:      "#re:.*/(.*):$1",
		RawModelNameReplaceRules: mustRawModelNameReplaceRules(t, "#re:.*/(.*):$1"),
	}}
	store := config.NewStaticRuntimeStore(cfg)
	server := NewServerWithStore(store, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"max_tokens":64}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	t.Logf("status=%d", rec.Code)
	if upstreamPayload != nil {
		t.Logf("upstream model=%v", upstreamPayload["model"])
	}
	if upstreamPayload == nil || upstreamPayload["model"] != "quectel-github-copilot/QDeepseekV4/deepseek-v4-flash" {
		t.Fatalf("expected upstream to receive full raw name, got %#v", upstreamPayload)
	}
}

// 回归：实时 /models 拉取失败（上游 500）时，缓存索引 fallback 仍还原完整原始名发上游，
// 避免把伪原始名裸名直接发上游导致 400（对应 req-1787435120965015137-62 场景）。
func TestRootV1MapDeepseekFlashCachedIndexFallbackWhenModelsFail(t *testing.T) {
	var upstreamPayload map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"models unavailable"}`))
			return
		}
		decodeJSONBody(r, &upstreamPayload)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"r","object":"response","model":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.DefaultProvider = "copilot-work,chat-work"
	cfg.EnableLegacyV1Routes = true
	cfg.DownstreamNonStreamStrategy = config.DownstreamNonStreamStrategyUpstreamNonStream
	cfg.V1ModelMap = []config.ModelMapEntry{
		config.NewModelMapEntry("deepseek-v4-flash", "[copilot]deepseek-v4-flash"),
	}
	cfg.Providers = []config.ProviderConfig{{
		ID:                       "copilot-work",
		Enabled:                  true,
		UpstreamBaseURL:          upstream.URL,
		UpstreamAPIKey:           "key",
		UpstreamEndpointType:     config.UpstreamEndpointTypeChat,
		SupportsChat:             true,
		SupportsModels:           true,
		ModelIDTemplate:          "[copilot]{{model}}",
		ManualModels:             []string{"#re:.*deepseek.*"},
		HiddenModels:             []string{"#re:.*"},
		ModelMap:                 []config.ModelMapEntry{config.NewModelMapEntry("#re:(.*)", "$1-max")},
		RawModelNameReplace:      "#re:.*/(.*):$1",
		RawModelNameReplaceRules: mustRawModelNameReplaceRules(t, "#re:.*/(.*):$1"),
	}, {
		ID:              "chat-work",
		Enabled:         true,
		UpstreamBaseURL: "http://127.0.0.1:1",
		SupportsModels:  true,
	}}
	store := config.NewStaticRuntimeStore(cfg)
	// 模拟线上 refreshDefaultProviderOverlayCache 成功后的缓存索引。
	store.UpdateDefaultOverlayIndex(
		map[string]string{"[copilot]deepseek-v4-flash": "copilot-work"},
		[]string{"[copilot]deepseek-v4-flash"},
		map[string]string{"[copilot]deepseek-v4-flash": "copilot-work"},
		[]string{"[copilot]deepseek-v4-flash"},
		map[string]string{"[copilot]deepseek-v4-flash": "quectel-github-copilot/QDeepseekV4/deepseek-v4-flash"},
		map[string]string{"[copilot]deepseek-v4-flash": "quectel-github-copilot/QDeepseekV4/deepseek-v4-flash"},
	)
	server := NewServerWithStore(store, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"max_tokens":64}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer key")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if upstreamPayload == nil || upstreamPayload["model"] != "quectel-github-copilot/QDeepseekV4/deepseek-v4-flash" {
		t.Fatalf("expected cached-index fallback to restore full raw name, got %#v (status=%d)", upstreamPayload, rec.Code)
	}
}
