package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestAdaptiveModelSuffixUsesFinalAnthropicMappingAcrossEntrypoints(t *testing.T) {
	entrypoints := []struct {
		name       string
		path       string
		body       string
		setHeaders func(*http.Request)
	}{
		{
			name:       "responses",
			path:       "/v1/responses",
			body:       `{"model":"client-noprompt-adaptive-pro-high","max_output_tokens":128,"input":"hello"}`,
			setHeaders: func(*http.Request) {},
		},
		{
			name:       "chat",
			path:       "/v1/chat/completions",
			body:       `{"model":"client-high-pro-noprompt-adaptive","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`,
			setHeaders: func(*http.Request) {},
		},
		{
			name: "messages",
			path: "/v1/messages",
			body: `{"model":"client-pro-adaptive-high-noprompt","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`,
			setHeaders: func(req *http.Request) {
				req.Header.Set("anthropic-version", "2023-06-01")
			},
		},
	}

	for _, entrypoint := range entrypoints {
		t.Run(entrypoint.name, func(t *testing.T) {
			upstreamHits := 0
			var upstreamPayload map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamHits++
				if r.URL.Path != "/messages" {
					t.Fatalf("expected final Anthropic upstream path, got %q", r.URL.Path)
				}
				if err := json.NewDecoder(r.Body).Decode(&upstreamPayload); err != nil {
					t.Fatalf("decode upstream payload: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`))
			}))
			defer upstream.Close()

			server := NewServer(config.Config{
				DefaultProvider:             "provider",
				EnableLegacyV1Routes:        true,
				EnableNoPromptModelSuffix:   true,
				DefaultProReasoningModeSet:  true,
				DefaultProReasoningMode:     false,
				DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
				V1ModelMap: []config.ModelMapEntry{
					config.NewModelMapEntry("client", "provider-target"),
				},
				Providers: []config.ProviderConfig{{
					ID:                                    "provider",
					Enabled:                               true,
					UpstreamBaseURL:                       upstream.URL,
					UpstreamAPIKey:                        "test-key",
					UpstreamEndpointType:                  config.UpstreamEndpointTypeAnthropic,
					SupportsResponses:                     true,
					SupportsChat:                          true,
					SupportsAnthropicMessages:             true,
					EnableReasoningEffortSuffix:           true,
					EnableNoPromptModelSuffix:             true,
					MapReasoningSuffixToAnthropicThinking: false,
					ManualModels:                          []string{"provider-target"},
					ModelMap: []config.ModelMapEntry{
						config.NewModelMapEntry("provider-target", "claude-opus-4-6"),
					},
				}},
			})
			req := httptest.NewRequest(http.MethodPost, entrypoint.path, strings.NewReader(entrypoint.body))
			req.Header.Set("Content-Type", "application/json")
			entrypoint.setHeaders(req)
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			if upstreamHits != 1 {
				t.Fatalf("expected one upstream request, got %d", upstreamHits)
			}
			if got := upstreamPayload["model"]; got != "claude-opus-4-6" {
				t.Fatalf("expected final mapped model without adaptive suffix, got %#v", upstreamPayload)
			}
			thinking, _ := upstreamPayload["thinking"].(map[string]any)
			if thinking["type"] != "adaptive" {
				t.Fatalf("expected native adaptive thinking, got %#v", upstreamPayload)
			}
			if _, exists := thinking["budget_tokens"]; exists {
				t.Fatalf("adaptive suffix must not fall back to manual thinking, got %#v", upstreamPayload)
			}
			outputConfig, _ := upstreamPayload["output_config"].(map[string]any)
			if outputConfig["effort"] != "high" {
				t.Fatalf("expected composed reasoning effort high, got %#v", upstreamPayload)
			}
			if got := rec.Header().Get(headerClientToProxyNoPrompt); got != "true" {
				t.Fatalf("expected composed noprompt suffix to remain active, got %q", got)
			}
		})
	}
}

func TestAdaptiveModelSuffixRejectsUnsupportedFinalTargetsBeforeUpstream(t *testing.T) {
	tests := []struct {
		name             string
		endpointType     string
		mappedModel      string
		path             string
		body             string
		setHeaders       func(*http.Request)
		expectedFragment string
	}{
		{
			name:             "messages to responses upstream",
			endpointType:     config.UpstreamEndpointTypeResponses,
			mappedModel:      "claude-opus-4-6",
			path:             "/v1/messages",
			body:             `{"model":"client-adaptive","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`,
			setHeaders:       func(req *http.Request) { req.Header.Set("anthropic-version", "2023-06-01") },
			expectedFragment: "requires UPSTREAM_ENDPOINT_TYPE=anthropic",
		},
		{
			name:             "responses to chat upstream",
			endpointType:     config.UpstreamEndpointTypeChat,
			mappedModel:      "claude-opus-4-6",
			path:             "/v1/responses",
			body:             `{"model":"client-adaptive","input":"hello"}`,
			setHeaders:       func(*http.Request) {},
			expectedFragment: "requires UPSTREAM_ENDPOINT_TYPE=anthropic",
		},
		{
			name:             "unsupported final anthropic model",
			endpointType:     config.UpstreamEndpointTypeAnthropic,
			mappedModel:      "claude-sonnet-4-5",
			path:             "/v1/chat/completions",
			body:             `{"model":"client-adaptive","messages":[{"role":"user","content":"hello"}]}`,
			setHeaders:       func(*http.Request) {},
			expectedFragment: "not supported by final Anthropic model",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstreamHits := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamHits++
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"unexpected"}`))
			}))
			defer upstream.Close()

			server := NewServer(config.Config{
				DefaultProvider:      "provider",
				EnableLegacyV1Routes: true,
				Providers: []config.ProviderConfig{{
					ID:                        "provider",
					Enabled:                   true,
					UpstreamBaseURL:           upstream.URL,
					UpstreamAPIKey:            "test-key",
					UpstreamEndpointType:      test.endpointType,
					SupportsResponses:         true,
					SupportsChat:              true,
					SupportsAnthropicMessages: true,
					ManualModels:              []string{test.mappedModel},
					ModelMap: []config.ModelMapEntry{
						config.NewModelMapEntry("client", test.mappedModel),
					},
				}},
			})
			req := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			req.Header.Set("Content-Type", "application/json")
			test.setHeaders(req)
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected local 400, got %d body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "unsupported_upstream_feature") || !strings.Contains(rec.Body.String(), test.expectedFragment) {
				t.Fatalf("expected clear adaptive preflight error, got %s", rec.Body.String())
			}
			if upstreamHits != 0 {
				t.Fatalf("expected rejected request to make no upstream calls, got %d", upstreamHits)
			}
		})
	}
}

func TestAdaptiveModelSuffixPreservesExactLiteralModelPrecedence(t *testing.T) {
	upstreamHits := 0
	var upstreamPayload map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		if err := json.NewDecoder(r.Body).Decode(&upstreamPayload); err != nil {
			t.Fatalf("decode upstream payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[]}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "provider",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "provider",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			ManualModels:         []string{"vendor-adaptive", "vendor"},
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"vendor-adaptive","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected exact literal model to remain routable, got %d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamHits != 1 || upstreamPayload["model"] != "vendor-adaptive" {
		t.Fatalf("expected exact literal model to bypass adaptive parsing, calls=%d payload=%#v", upstreamHits, upstreamPayload)
	}
	if _, exists := upstreamPayload["thinking"]; exists {
		t.Fatalf("exact literal model must not synthesize adaptive thinking, got %#v", upstreamPayload)
	}
}
