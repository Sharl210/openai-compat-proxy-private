package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestRawModelNameReplaceAppliedBeforeUpstreamAcrossProtocols(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		body       string
		setHeaders func(*http.Request)
	}{
		{
			name: "chat",
			path: "/v1/chat/completions",
			body: `{"model":"client-model","messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			name: "responses",
			path: "/v1/responses",
			body: `{"model":"client-model","input":"hello"}`,
		},
		{
			name: "messages",
			path: "/v1/messages",
			body: `{"model":"client-model","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`,
			setHeaders: func(req *http.Request) {
				req.Header.Set("anthropic-version", "2023-06-01")
			},
		},
	}

	for _, entrypoint := range tests {
		t.Run(entrypoint.name, func(t *testing.T) {
			var upstreamPayload map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&upstreamPayload); err != nil {
					t.Fatalf("decode upstream payload: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","model":"replaced-model-v2","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
			}))
			defer upstream.Close()

			server := NewServer(config.Config{
				DefaultProvider:             "provider",
				EnableLegacyV1Routes:        true,
				DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
				Providers: []config.ProviderConfig{{
					ID:                        "provider",
					Enabled:                   true,
					UpstreamBaseURL:           upstream.URL,
					UpstreamAPIKey:            "test-key",
					UpstreamEndpointType:      config.UpstreamEndpointTypeChat,
					SupportsChat:              true,
					SupportsResponses:         true,
					SupportsAnthropicMessages: true,
					ManualModels:              []string{"client-model"},
					ModelMap: []config.ModelMapEntry{
						config.NewModelMapEntry("client-model", "middle-model"),
					},
					RawModelNameReplaceRules: mustRawModelNameReplaceRules(t, "middle:replaced,#re:model$:model-v2"),
				}},
			})
			req := httptest.NewRequest(http.MethodPost, entrypoint.path, strings.NewReader(entrypoint.body))
			req.Header.Set("Content-Type", "application/json")
			if entrypoint.setHeaders != nil {
				entrypoint.setHeaders(req)
			}
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			// 新语义：RAW_MODEL_NAME_REPLACE 只在响应/展示方向应用（原始名→伪原始名），
			// 请求方向不再应用替换；客户端发的是原始名 client-model，MODEL_MAP 映射为
			// middle-model 后直接发上游（RAW 规则 middle:replaced 是展示方向用的）。
			if got := rec.Header().Get(headerProxyToUpstreamModel); got != "middle-model" {
				t.Fatalf("expected MODEL_MAP target in transparency model, got %q", got)
			}
			if got := upstreamPayload["model"]; got != "middle-model" {
				t.Fatalf("expected upstream model unchanged by RAW replace, got %#v", upstreamPayload)
			}
		})
	}
}

func TestRawModelNameReplaceKeepsLimitRulesOnPreReplacementName(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:      "provider",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                      "provider",
			Enabled:                 true,
			UpstreamBaseURL:         "https://upstream.invalid/v1",
			UpstreamAPIKey:          "test-key",
			UpstreamEndpointType:    config.UpstreamEndpointTypeChat,
			SupportsChat:            true,
			ManualModels:            []string{"client-model"},
			ModelMap:                []config.ModelMapEntry{config.NewModelMapEntry("client-model", "middle-model")},
			ModelLimitContextTokens: -1,
			ModelLimitContextTokenRules: []config.ScopedIntRule{
				exactScopedRule("middle-model", 1),
			},
			// The replacement turns "middle-model" into "replaced-model", but the
			// context limit rule was configured on the pre-replacement name and must
			// still match and reject before any upstream call.
			RawModelNameReplaceRules: mustRawModelNameReplaceRules(t, "middle:replaced"),
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"client-model","messages":[{"role":"user","content":"hello world hello world hello world"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected context limit rejection on pre-replacement name, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "context_length_exceeded") {
		t.Fatalf("expected context_length_exceeded body, got %s", rec.Body.String())
	}
	// 透明度头显示实际发往上游的模型名（请求方向不再应用 RAW 替换，展示方向才替换）。
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "middle-model" {
		t.Fatalf("expected MODEL_MAP target in transparency model, got %q", got)
	}
}

func TestRawModelNameReplaceDisabledByIdentity(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:      "provider",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                   "provider",
			Enabled:              true,
			UpstreamBaseURL:      "https://upstream.invalid/v1",
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeChat,
			SupportsChat:         true,
			ManualModels:         []string{"client-model"},
			ModelMap:             []config.ModelMapEntry{config.NewModelMapEntry("client-model", "middle-model")},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"client-model","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	// No rules configured: the transparency header stays the MODEL_MAP target and
	// the request proceeds to the (unreachable) upstream.
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "middle-model" {
		t.Fatalf("expected unchanged transparency model, got %q", got)
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected upstream failure after no replacement, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func mustRawModelNameReplaceRules(t *testing.T, value string) []config.ModelNameReplaceRule {
	t.Helper()
	rules, err := config.ParseModelNameReplaceRules(value, "RAW_MODEL_NAME_REPLACE", "test")
	if err != nil {
		t.Fatalf("parse replace rules: %v", err)
	}
	return rules
}
