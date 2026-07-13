package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestProxyModelIntentRouting_preservesAllAxisOrdersAcrossExternalEntrypoints(t *testing.T) {
	orders := []string{
		"low-pro-noprompt",
		"low-noprompt-pro",
		"pro-low-noprompt",
		"pro-noprompt-low",
		"noprompt-low-pro",
		"noprompt-pro-low",
	}
	entrypoints := []struct {
		name            string
		tagEnabled      bool
		modelIDTemplate string
		model           func(string) string
	}{
		{name: "bare overlay after root map", model: func(tail string) string { return "root-vendor-" + tail }},
		{name: "tagged", tagEnabled: true, model: func(tail string) string { return "[packy]vendor-" + tail }},
		{name: "templated", modelIDTemplate: "packy-{{model}}-vip", model: func(tail string) string { return "packy-vendor-" + tail + "-vip" }},
		{name: "literal alias", model: func(tail string) string { return "literal-vendor-" + tail }},
	}

	for _, entrypoint := range entrypoints {
		for _, order := range orders {
			t.Run(entrypoint.name+"/"+order, func(t *testing.T) {
				// Given
				upstream := newResponsesProviderUpstream(t, "packy")
				defer upstream.Close()
				server := NewServer(config.Config{
					DefaultProvider:                   "packy",
					EnableLegacyV1Routes:              true,
					EnableDefaultProviderModelTags:    entrypoint.tagEnabled,
					EnableAllDefaultProviderModelTags: entrypoint.tagEnabled,
					EnableNoPromptModelSuffix:         true,
					DownstreamNonStreamStrategy:       config.DownstreamNonStreamStrategyUpstreamNonStream,
					V1ModelMap: []config.ModelMapEntry{
						config.NewModelMapEntry("root-vendor", "vendor"),
					},
					Providers: []config.ProviderConfig{{
						ID:                          "packy",
						Enabled:                     true,
						UpstreamBaseURL:             upstream.URL,
						UpstreamAPIKey:              "test-key",
						UpstreamEndpointType:        config.UpstreamEndpointTypeResponses,
						SupportsResponses:           true,
						EnableReasoningEffortSuffix: true,
						EnableNoPromptModelSuffix:   true,
						ModelIDTemplate:             entrypoint.modelIDTemplate,
						ManualModels:                []string{"vendor"},
						ModelMap: []config.ModelMapEntry{
							config.NewModelMapEntry("vendor", "upstream"),
							config.NewModelMapEntry("literal-vendor", "upstream"),
						},
					}},
				})
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"`+entrypoint.model(order)+`","input":"hello"}`))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()

				// When
				server.ServeHTTP(rec, req)

				// Then
				if rec.Code != http.StatusOK {
					t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
				}
				if got := rec.Header().Get("X-Provider-Name"); got != "packy" {
					t.Fatalf("expected provider packy, got %q", got)
				}
				if got := rec.Header().Get(headerProxyToUpstreamModel); got != "upstream" {
					t.Fatalf("expected upstream model upstream, got %q", got)
				}
				if got := rec.Header().Get(headerClientToProxyNoPrompt); got != "true" {
					t.Fatalf("expected noprompt marker to remain effective, got %q", got)
				}
			})
		}
	}
}

func TestProxyModelIntentRouting_skipsProviderPromptAcrossProtocolsAndAxisOrders(t *testing.T) {
	orders := []string{
		"low-pro-noprompt",
		"low-noprompt-pro",
		"pro-low-noprompt",
		"pro-noprompt-low",
		"noprompt-low-pro",
		"noprompt-pro-low",
	}
	protocols := []struct {
		name       string
		path       string
		requestFor func(string) string
		setHeaders func(*http.Request)
	}{
		{
			name: "responses",
			path: "/v1/responses",
			requestFor: func(modelName string) string {
				return `{"model":"` + modelName + `","input":"hello"}`
			},
			setHeaders: func(*http.Request) {},
		},
		{
			name: "chat",
			path: "/v1/chat/completions",
			requestFor: func(modelName string) string {
				return `{"model":"` + modelName + `","messages":[{"role":"user","content":"hello"}]}`
			},
			setHeaders: func(*http.Request) {},
		},
		{
			name: "anthropic",
			path: "/v1/messages",
			requestFor: func(modelName string) string {
				return `{"model":"` + modelName + `","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`
			},
			setHeaders: func(req *http.Request) {
				req.Header.Set("anthropic-version", "2023-06-01")
			},
		},
	}

	for _, protocol := range protocols {
		for _, order := range orders {
			t.Run(protocol.name+"/"+order, func(t *testing.T) {
				// Given
				var gotBody string
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Fatalf("read upstream request: %v", err)
					}
					gotBody = string(body)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`))
				}))
				defer upstream.Close()
				server := NewServer(config.Config{
					DefaultProvider:             "packy",
					EnableLegacyV1Routes:        true,
					EnableNoPromptModelSuffix:   true,
					DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
					Providers: []config.ProviderConfig{{
						ID:                          "packy",
						Enabled:                     true,
						UpstreamBaseURL:             upstream.URL,
						UpstreamAPIKey:              "test-key",
						UpstreamEndpointType:        config.UpstreamEndpointTypeResponses,
						SupportsResponses:           true,
						SupportsChat:                true,
						SupportsAnthropicMessages:   true,
						EnableReasoningEffortSuffix: true,
						EnableNoPromptModelSuffix:   true,
						ManualModels:                []string{"vendor"},
						SystemPromptText:            "provider system",
					}},
				})
				modelName := "vendor-" + order
				req := httptest.NewRequest(http.MethodPost, protocol.path, strings.NewReader(protocol.requestFor(modelName)))
				req.Header.Set("Content-Type", "application/json")
				protocol.setHeaders(req)
				rec := httptest.NewRecorder()

				// When
				server.ServeHTTP(rec, req)

				// Then
				if rec.Code != http.StatusOK {
					t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
				}
				if got := rec.Header().Get(headerClientToProxyNoPrompt); got != "true" {
					t.Fatalf("expected noprompt observability header true, got %q", got)
				}
				if strings.Contains(gotBody, "provider system") {
					t.Fatalf("expected provider prompt to be skipped, got upstream body=%s", gotBody)
				}
			})
		}
	}
}
