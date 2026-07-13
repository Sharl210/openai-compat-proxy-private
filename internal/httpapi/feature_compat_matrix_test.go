package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestFeatureCompatibilityMatrixAllowsPlainRequestsAcrossAllEndpoints(t *testing.T) {
	downstreams := []struct {
		name    string
		path    string
		body    string
		headers map[string]string
	}{
		{
			name: "responses",
			path: "/v1/responses",
			body: `{"model":"gpt-5.6","input":"hello"}`,
		},
		{
			name: "chat",
			path: "/v1/chat/completions",
			body: `{"model":"gpt-5.6","messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			name: "anthropic",
			path: "/v1/messages",
			body: `{"model":"gpt-5.6","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`,
			headers: map[string]string{
				"anthropic-version": "2023-06-01",
			},
		},
	}
	upstreamTypes := []string{
		config.UpstreamEndpointTypeResponses,
		config.UpstreamEndpointTypeChat,
		config.UpstreamEndpointTypeAnthropic,
	}

	for _, downstream := range downstreams {
		for _, upstreamType := range upstreamTypes {
			t.Run(downstream.name+"_to_"+upstreamType, func(t *testing.T) {
				upstreamHits := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstreamHits++
					writeFeatureCompatibilityMatrixResponse(w, upstreamType)
				}))
				defer upstream.Close()

				server := NewServer(config.Config{
					DefaultProvider:             "openai",
					DefaultProReasoningModeSet:  true,
					DefaultProReasoningMode:     false,
					EnableLegacyV1Routes:        true,
					DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
					Providers: []config.ProviderConfig{{
						ID:                        "openai",
						Enabled:                   true,
						UpstreamBaseURL:           upstream.URL,
						UpstreamAPIKey:            "test-key",
						UpstreamEndpointType:      upstreamType,
						SupportsResponses:         true,
						SupportsChat:              true,
						SupportsAnthropicMessages: true,
					}},
				})
				req := httptest.NewRequest(http.MethodPost, downstream.path, strings.NewReader(downstream.body))
				req.Header.Set("Content-Type", "application/json")
				for key, value := range downstream.headers {
					req.Header.Set(key, value)
				}
				rec := httptest.NewRecorder()

				server.ServeHTTP(rec, req)

				if rec.Code != http.StatusOK {
					t.Fatalf("expected successful %s -> %s request, got %d body=%s", downstream.name, upstreamType, rec.Code, rec.Body.String())
				}
				if upstreamHits != 1 {
					t.Fatalf("expected one upstream request for %s -> %s, got %d", downstream.name, upstreamType, upstreamHits)
				}
			})
		}
	}
}

func TestParallelToolCallsControlMatrixFailsClosedWithoutProviderCapability(t *testing.T) {
	downstreams := []struct {
		name    string
		path    string
		body    string
		headers map[string]string
	}{
		{name: "responses", path: "/v1/responses", body: `{"model":"gpt-5.6","input":"hello","parallel_tool_calls":false}`},
		{name: "chat", path: "/v1/chat/completions", body: `{"model":"gpt-5.6","messages":[{"role":"user","content":"hello"}],"parallel_tool_calls":false}`},
		{name: "anthropic", path: "/v1/messages", body: `{"model":"gpt-5.6","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"tool_choice":{"type":"auto","disable_parallel_tool_use":true}}`, headers: map[string]string{"anthropic-version": "2023-06-01"}},
	}
	upstreamTypes := []string{config.UpstreamEndpointTypeResponses, config.UpstreamEndpointTypeChat, config.UpstreamEndpointTypeAnthropic}

	for _, downstream := range downstreams {
		for _, upstreamType := range upstreamTypes {
			t.Run(downstream.name+"_to_"+upstreamType, func(t *testing.T) {
				upstreamHits := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstreamHits++
					writeFeatureCompatibilityMatrixResponse(w, upstreamType)
				}))
				defer upstream.Close()

				server := NewServer(config.Config{
					DefaultProvider:             "openai",
					DefaultProReasoningModeSet:  true,
					DefaultProReasoningMode:     false,
					EnableLegacyV1Routes:        true,
					DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
					Providers: []config.ProviderConfig{{
						ID:                        "openai",
						Enabled:                   true,
						UpstreamBaseURL:           upstream.URL,
						UpstreamAPIKey:            "test-key",
						UpstreamEndpointType:      upstreamType,
						SupportsResponses:         true,
						SupportsChat:              true,
						SupportsAnthropicMessages: true,
					}},
				})
				req := httptest.NewRequest(http.MethodPost, downstream.path, strings.NewReader(downstream.body))
				req.Header.Set("Content-Type", "application/json")
				for key, value := range downstream.headers {
					req.Header.Set(key, value)
				}
				rec := httptest.NewRecorder()

				server.ServeHTTP(rec, req)

				if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unsupported_upstream_feature") {
					t.Fatalf("expected unsupported_upstream_feature 400, got %d body=%s", rec.Code, rec.Body.String())
				}
				if upstreamHits != 0 {
					t.Fatalf("expected capability preflight to avoid upstream, got %d hits", upstreamHits)
				}
			})
		}
	}
}

func TestParallelToolCallsControlMatrixMapsDisabledControlAcrossAllEndpoints(t *testing.T) {
	downstreams := []struct {
		name    string
		path    string
		body    string
		headers map[string]string
	}{
		{name: "responses", path: "/v1/responses", body: `{"model":"gpt-5.6","input":"hello","parallel_tool_calls":false}`},
		{name: "chat", path: "/v1/chat/completions", body: `{"model":"gpt-5.6","messages":[{"role":"user","content":"hello"}],"parallel_tool_calls":false}`},
		{name: "anthropic", path: "/v1/messages", body: `{"model":"gpt-5.6","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"tool_choice":{"type":"auto","disable_parallel_tool_use":true}}`, headers: map[string]string{"anthropic-version": "2023-06-01"}},
	}
	upstreamTypes := []string{config.UpstreamEndpointTypeResponses, config.UpstreamEndpointTypeChat, config.UpstreamEndpointTypeAnthropic}

	for _, downstream := range downstreams {
		for _, upstreamType := range upstreamTypes {
			t.Run(downstream.name+"_to_"+upstreamType, func(t *testing.T) {
				upstreamHits := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstreamHits++
					assertDisabledParallelToolCallsPayload(t, r, upstreamType)
					writeFeatureCompatibilityMatrixResponse(w, upstreamType)
				}))
				defer upstream.Close()

				server := NewServer(config.Config{
					DefaultProvider:             "openai",
					DefaultProReasoningModeSet:  true,
					DefaultProReasoningMode:     false,
					EnableLegacyV1Routes:        true,
					DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
					Providers: []config.ProviderConfig{{
						ID:                               "openai",
						Enabled:                          true,
						UpstreamBaseURL:                  upstream.URL,
						UpstreamAPIKey:                   "test-key",
						UpstreamEndpointType:             upstreamType,
						SupportsParallelToolCallsControl: true,
						SupportsResponses:                true,
						SupportsChat:                     true,
						SupportsAnthropicMessages:        true,
					}},
				})
				req := httptest.NewRequest(http.MethodPost, downstream.path, strings.NewReader(downstream.body))
				req.Header.Set("Content-Type", "application/json")
				for key, value := range downstream.headers {
					req.Header.Set(key, value)
				}
				rec := httptest.NewRecorder()

				server.ServeHTTP(rec, req)

				if rec.Code != http.StatusOK {
					t.Fatalf("expected successful request, got %d body=%s", rec.Code, rec.Body.String())
				}
				if upstreamHits != 1 {
					t.Fatalf("expected one upstream request, got %d", upstreamHits)
				}
			})
		}
	}
}

func TestAnthropicToolChoiceFailsClosedForUnknownShapes(t *testing.T) {
	tests := []struct {
		name       string
		toolChoice string
	}{
		{
			name:       "unknown type with disabled parallel tool use",
			toolChoice: `{"type":"unsupported","disable_parallel_tool_use":true}`,
		},
		{
			name:       "unknown string",
			toolChoice: `"unsupported"`,
		},
		{
			name:       "array",
			toolChoice: `["auto"]`,
		},
		{
			name:       "object without type",
			toolChoice: `{"disable_parallel_tool_use":true}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			upstreamHits := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamHits++
				writeFeatureCompatibilityMatrixResponse(w, config.UpstreamEndpointTypeAnthropic)
			}))
			defer upstream.Close()

			server := NewServer(config.Config{
				DefaultProvider:             "openai",
				DefaultProReasoningModeSet:  true,
				DefaultProReasoningMode:     false,
				EnableLegacyV1Routes:        true,
				DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
				Providers: []config.ProviderConfig{{
					ID:                               "openai",
					Enabled:                          true,
					UpstreamBaseURL:                  upstream.URL,
					UpstreamAPIKey:                   "test-key",
					UpstreamEndpointType:             config.UpstreamEndpointTypeAnthropic,
					SupportsParallelToolCallsControl: true,
					SupportsResponses:                true,
					SupportsChat:                     true,
					SupportsAnthropicMessages:        true,
				}},
			})
			body := `{"model":"gpt-5.6","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"tool_choice":` + tc.toolChoice + `}`
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("anthropic-version", "2023-06-01")
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unsupported_upstream_feature") {
				t.Fatalf("expected unsupported_upstream_feature 400, got %d body=%s", rec.Code, rec.Body.String())
			}
			if upstreamHits != 0 {
				t.Fatalf("expected invalid Anthropic tool_choice to avoid upstream, got %d hits", upstreamHits)
			}
		})
	}
}

func TestUltraSuffixFailsClosedOutsideEnabledResponsesUpstreams(t *testing.T) {
	downstreams := []struct {
		name    string
		path    string
		body    string
		headers map[string]string
	}{
		{name: "responses", path: "/v1/responses", body: `{"model":"gpt-5.6-ultra","input":"hello"}`},
		{name: "chat", path: "/v1/chat/completions", body: `{"model":"gpt-5.6-ultra","messages":[{"role":"user","content":"hello"}]}`},
		{name: "anthropic", path: "/v1/messages", body: `{"model":"gpt-5.6-ultra","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`, headers: map[string]string{"anthropic-version": "2023-06-01"}},
	}
	for _, downstream := range downstreams {
		for _, upstreamType := range []string{config.UpstreamEndpointTypeResponses, config.UpstreamEndpointTypeChat, config.UpstreamEndpointTypeAnthropic} {
			t.Run(downstream.name+"_to_"+upstreamType, func(t *testing.T) {
				upstreamHits := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstreamHits++
					writeFeatureCompatibilityMatrixResponse(w, upstreamType)
				}))
				defer upstream.Close()
				provider := config.ProviderConfig{
					ID:                          "openai",
					Enabled:                     true,
					UpstreamBaseURL:             upstream.URL,
					UpstreamAPIKey:              "test-key",
					UpstreamEndpointType:        upstreamType,
					SupportsResponses:           true,
					SupportsChat:                true,
					SupportsAnthropicMessages:   true,
					UltraMaxConcurrentSubagents: 5,
					ManualModels:                []string{"gpt-5.6"},
				}
				if upstreamType == config.UpstreamEndpointTypeResponses {
					provider.SupportsResponsesMultiAgent = false
				}
				server := NewServer(config.Config{
					DefaultProvider:             "openai",
					DefaultProReasoningModeSet:  true,
					DefaultProReasoningMode:     false,
					EnableLegacyV1Routes:        true,
					DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
					Providers:                   []config.ProviderConfig{provider},
				})
				req := httptest.NewRequest(http.MethodPost, downstream.path, strings.NewReader(downstream.body))
				req.Header.Set("Content-Type", "application/json")
				for key, value := range downstream.headers {
					req.Header.Set(key, value)
				}
				rec := httptest.NewRecorder()

				server.ServeHTTP(rec, req)

				if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unsupported_upstream_feature") {
					t.Fatalf("expected fail-closed 400, got %d body=%s", rec.Code, rec.Body.String())
				}
				if upstreamHits != 0 {
					t.Fatalf("expected no upstream requests, got %d", upstreamHits)
				}
				if upstreamType == config.UpstreamEndpointTypeResponses {
					if !strings.Contains(rec.Body.String(), "set SUPPORTS_RESPONSES_MULTI_AGENT=true") {
						t.Fatalf("expected capability remediation, got %s", rec.Body.String())
					}
				} else if !strings.Contains(rec.Body.String(), "requires UPSTREAM_ENDPOINT_TYPE=responses") {
					t.Fatalf("expected endpoint remediation, got %s", rec.Body.String())
				}
			})
		}
	}
}

func assertDisabledParallelToolCallsPayload(t *testing.T, request *http.Request, upstreamType string) {
	t.Helper()
	var payload map[string]any
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		t.Fatalf("decode upstream payload: %v", err)
	}
	if upstreamType != config.UpstreamEndpointTypeAnthropic {
		if got := payload["parallel_tool_calls"]; got != false {
			t.Fatalf("expected parallel_tool_calls=false, got %#v", payload)
		}
		return
	}
	choice, _ := payload["tool_choice"].(map[string]any)
	if got := choice["type"]; got != "auto" {
		t.Fatalf("expected Anthropic auto tool choice, got %#v", choice)
	}
	if got := choice["disable_parallel_tool_use"]; got != true {
		t.Fatalf("expected Anthropic disabled parallel tool use, got %#v", choice)
	}
}

func TestFeatureCompatibilityMatrixPreservesOrRejectsSemanticRequests(t *testing.T) {
	tests := []struct {
		name                              string
		path                              string
		body                              string
		headers                           map[string]string
		supportsProgrammaticToolCalling   bool
		responsesToolCompatibilityMode    string
		allowedUpstreamEndpointTypes      map[string]bool
		assertProgrammaticToolPassthrough bool
	}{
		{
			name:                            "responses programmatic tool calling",
			path:                            "/v1/responses",
			body:                            `{"model":"gpt-5.6","input":"hello","tools":[{"type":"programmatic_tool_calling"}]}`,
			supportsProgrammaticToolCalling: true,
			responsesToolCompatibilityMode:  config.ResponsesToolCompatModeFunctionOnly,
			allowedUpstreamEndpointTypes: map[string]bool{
				config.UpstreamEndpointTypeResponses: true,
			},
			assertProgrammaticToolPassthrough: true,
		},
		{
			name: "chat reasoning mode",
			path: "/v1/chat/completions",
			body: `{"model":"gpt-5.6","reasoning":{"mode":"pro"},"messages":[{"role":"user","content":"hello"}]}`,
			allowedUpstreamEndpointTypes: map[string]bool{
				config.UpstreamEndpointTypeResponses: true,
			},
		},
		{
			name: "anthropic thinking",
			path: "/v1/messages",
			body: `{"model":"gpt-5.6","max_tokens":2048,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"hello"}]}`,
			headers: map[string]string{
				"anthropic-version": "2023-06-01",
			},
			allowedUpstreamEndpointTypes: map[string]bool{
				config.UpstreamEndpointTypeResponses: true,
				config.UpstreamEndpointTypeChat:      true,
				config.UpstreamEndpointTypeAnthropic: true,
			},
		},
	}
	upstreamTypes := []string{
		config.UpstreamEndpointTypeResponses,
		config.UpstreamEndpointTypeChat,
		config.UpstreamEndpointTypeAnthropic,
	}

	for _, test := range tests {
		for _, upstreamType := range upstreamTypes {
			t.Run(test.name+"_to_"+upstreamType, func(t *testing.T) {
				upstreamHits := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstreamHits++
					if test.assertProgrammaticToolPassthrough {
						assertProgrammaticToolCallingPassthrough(t, r)
					}
					writeFeatureCompatibilityMatrixResponse(w, upstreamType)
				}))
				defer upstream.Close()

				server := NewServer(config.Config{
					DefaultProvider:             "openai",
					DefaultProReasoningModeSet:  true,
					DefaultProReasoningMode:     false,
					EnableLegacyV1Routes:        true,
					DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
					Providers: []config.ProviderConfig{{
						ID:                              "openai",
						Enabled:                         true,
						UpstreamBaseURL:                 upstream.URL,
						UpstreamAPIKey:                  "test-key",
						UpstreamEndpointType:            upstreamType,
						ResponsesToolCompatMode:         test.responsesToolCompatibilityMode,
						SupportsProgrammaticToolCalling: test.supportsProgrammaticToolCalling,
						SupportsResponses:               true,
						SupportsChat:                    true,
						SupportsAnthropicMessages:       true,
					}},
				})
				req := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
				req.Header.Set("Content-Type", "application/json")
				for key, value := range test.headers {
					req.Header.Set(key, value)
				}
				rec := httptest.NewRecorder()

				server.ServeHTTP(rec, req)

				if test.allowedUpstreamEndpointTypes[upstreamType] {
					if rec.Code != http.StatusOK {
						t.Fatalf("expected successful %s -> %s request, got %d body=%s", test.name, upstreamType, rec.Code, rec.Body.String())
					}
					if upstreamHits != 1 {
						t.Fatalf("expected one upstream request for %s -> %s, got %d", test.name, upstreamType, upstreamHits)
					}
					return
				}

				if rec.Code != http.StatusBadRequest {
					t.Fatalf("expected preflight 400 for %s -> %s, got %d body=%s", test.name, upstreamType, rec.Code, rec.Body.String())
				}
				if upstreamHits != 0 {
					t.Fatalf("expected no upstream request for %s -> %s, got %d", test.name, upstreamType, upstreamHits)
				}
			})
		}
	}
}

func assertProgrammaticToolCallingPassthrough(t *testing.T, request *http.Request) {
	t.Helper()
	var payload struct {
		Tools []struct {
			Type string `json:"type"`
		} `json:"tools"`
	}
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		t.Fatalf("decode upstream request: %v", err)
	}
	if len(payload.Tools) != 1 || payload.Tools[0].Type != "programmatic_tool_calling" {
		t.Fatalf("expected programmatic tool calling to bypass function_only, got %#v", payload.Tools)
	}
}

func writeFeatureCompatibilityMatrixResponse(w http.ResponseWriter, upstreamType string) {
	w.Header().Set("Content-Type", "application/json")
	switch upstreamType {
	case config.UpstreamEndpointTypeResponses:
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))
	case config.UpstreamEndpointTypeChat:
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	case config.UpstreamEndpointTypeAnthropic:
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"gpt-5.6","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}
}
