package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/testutil"
)

var responseCreatedIDPattern = regexp.MustCompile(`event: response\.created\s+data: \{"response":\{"id":"([^"]+)"[^}]*\}`)
var responseCompletedIDPattern = regexp.MustCompile(`event: response\.completed\s+data: \{"response":\{[^\n]*?"id":"([^"]+)"`)

func firstResponseIDFromStreamBody(t *testing.T, body string) string {
	t.Helper()
	if matches := responseCompletedIDPattern.FindStringSubmatch(body); len(matches) == 2 {
		return matches[1]
	}
	matches := responseCreatedIDPattern.FindStringSubmatch(body)
	if len(matches) != 2 {
		t.Fatalf("expected stream body to expose a response id, got %s", body)
	}
	return matches[1]
}

func TestResponsesRouteUsesChatUpstreamEndpointType(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from chat upstream"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		DefaultProReasoningModeSet:  true,
		DefaultProReasoningMode:     false,
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeChat,
			SupportsResponses:    true,
			SupportsChat:         true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("expected chat upstream path, got %q", gotPath)
	}
	if !strings.Contains(rec.Body.String(), `"object":"response"`) || !strings.Contains(rec.Body.String(), `"hello from chat upstream"`) {
		t.Fatalf("expected responses output normalized from chat upstream, got %s", rec.Body.String())
	}
}

func TestResponsesRouteRejectsProgrammaticToolWhenProviderDoesNotSupportIt(t *testing.T) {
	// Given
	upstreamHits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_unexpected"}`))
	}))
	defer upstream.Close()
	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, Providers: []config.ProviderConfig{{
		ID:                   "openai",
		Enabled:              true,
		UpstreamBaseURL:      upstream.URL,
		UpstreamAPIKey:       "test-key",
		UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
		SupportsResponses:    true,
	}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5.6",
		"tools":[{"type":"programmatic_tool_calling"}],
		"input":"hello"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected preflight 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unsupported_upstream_feature") {
		t.Fatalf("expected stable unsupported feature error, got %s", rec.Body.String())
	}
	if upstreamHits != 0 {
		t.Fatalf("expected no upstream request, got %d", upstreamHits)
	}
}

func TestResponsesRouteRejectsMultiAgentWhenProviderDoesNotSupportIt(t *testing.T) {
	// Given
	upstreamHits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_unexpected"}`))
	}))
	defer upstream.Close()
	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, Providers: []config.ProviderConfig{{
		ID:                   "openai",
		Enabled:              true,
		UpstreamBaseURL:      upstream.URL,
		UpstreamAPIKey:       "test-key",
		UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
		SupportsResponses:    true,
	}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5.6",
		"multi_agent":{"enabled":true},
		"input":"hello"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected preflight 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unsupported_upstream_feature") {
		t.Fatalf("expected stable unsupported feature error, got %s", rec.Body.String())
	}
	if upstreamHits != 0 {
		t.Fatalf("expected no upstream request, got %d", upstreamHits)
	}
}

func TestResponsesRouteUltraSuffixSendsRealMultiAgentBeta(t *testing.T) {
	var receivedBeta string
	var receivedBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBeta = r.Header.Get("OpenAI-Beta")
		if err := json.NewDecoder(r.Body).Decode(&receivedBody); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		writeFeatureCompatibilityMatrixResponse(w, config.UpstreamEndpointTypeResponses)
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		DefaultProReasoningModeSet:  true,
		DefaultProReasoningMode:     false,
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                          "openai",
			Enabled:                     true,
			UpstreamBaseURL:             upstream.URL,
			UpstreamAPIKey:              "test-key",
			UpstreamEndpointType:        config.UpstreamEndpointTypeResponses,
			SupportsResponses:           true,
			SupportsResponsesMultiAgent: true,
			UltraMaxConcurrentSubagents: 5,
			EnableReasoningEffortSuffix: true,
			ManualModels:                []string{"gpt-5.6"},
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-high-ultra","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(receivedBeta, "responses_multi_agent=v1") {
		t.Fatalf("expected multi-agent beta header, got %q", receivedBeta)
	}
	multiAgent, _ := receivedBody["multi_agent"].(map[string]any)
	if multiAgent["enabled"] != true || multiAgent["max_concurrent_subagents"] != float64(5) {
		t.Fatalf("unexpected multi_agent payload: %#v", multiAgent)
	}
	reasoning, _ := receivedBody["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" {
		t.Fatalf("expected high effort alongside ultra, got %#v", reasoning)
	}
}

func TestResponsesRouteDropsSummaryOnlyReasoningReplayForAnthropicUpstream(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatalf("decode anthropic request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_replay","type":"message","role":"assistant","content":[{"type":"text","text":"continued"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "claude",
		DefaultProReasoningModeSet:  true,
		DefaultProReasoningMode:     false,
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                        "claude",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			UpstreamEndpointType:      config.UpstreamEndpointTypeAnthropic,
			SupportsResponses:         true,
			SupportsAnthropicMessages: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"claude-opus-4-8",
		"input":[
			{"role":"user","content":"first user"},
			{"type":"reasoning","id":"rs_chat_reasoning","summary":[{"type":"summary_text","text":"first reasoning"}]},
			{"role":"user","content":"second user"},
			{"type":"reasoning","id":"rs_chat_reasoning","summary":[{"type":"summary_text","text":"second reasoning"}]},
			{"type":"function_call","call_id":"call_1","name":"search","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"{}"}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected summary-only replay to reach Anthropic upstream, got %d body=%s", rec.Code, rec.Body.String())
	}
	messages, _ := upstreamBody["messages"].([]any)
	if len(messages) == 0 {
		t.Fatalf("expected Anthropic messages, got %#v", upstreamBody)
	}
	encoded, err := json.Marshal(upstreamBody)
	if err != nil {
		t.Fatalf("marshal upstream body: %v", err)
	}
	if strings.Contains(string(encoded), "first reasoning") || strings.Contains(string(encoded), "second reasoning") {
		t.Fatalf("expected incomplete reasoning replay to be removed from Anthropic request, got %s", encoded)
	}
	if !strings.Contains(string(encoded), "second user") || !strings.Contains(string(encoded), "tool_use") {
		t.Fatalf("expected later conversation and tool replay to remain intact, got %s", encoded)
	}
}

func TestResponsesRouteRejectsSemanticFeaturesBeforeNonResponsesUpstream(t *testing.T) {
	features := []struct {
		name string
		body string
	}{
		{
			name: "programmatic tool calling",
			body: `{"model":"gpt-5.6","tools":[{"type":"programmatic_tool_calling"}],"input":"hello"}`,
		},
		{
			name: "multi agent",
			body: `{"model":"gpt-5.6","multi_agent":{"enabled":true},"input":"hello"}`,
		},
		{
			name: "persisted reasoning item",
			body: `{"model":"gpt-5.6","input":[{"type":"reasoning","encrypted_content":"opaque","phase":"analysis","summary":[]}]}`,
		},
		{
			name: "reasoning context",
			body: `{"model":"gpt-5.6","reasoning":{"context":"opaque"},"input":"hello"}`,
		},
		{
			name: "prompt cache controls",
			body: `{"model":"gpt-5.6","prompt_cache_key":"stable-key","input":"hello"}`,
		},
		{
			name: "original image detail",
			body: `{"model":"gpt-5.6","input":[{"role":"user","content":[{"type":"input_image","image_url":{"url":"https://example.com/image.png","detail":"original"}}]}]}`,
		},
	}

	for _, endpointType := range []string{config.UpstreamEndpointTypeChat, config.UpstreamEndpointTypeAnthropic} {
		for _, feature := range features {
			t.Run(endpointType+"/"+feature.name, func(t *testing.T) {
				upstreamHits := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstreamHits++
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"id":"unexpected"}`))
				}))
				defer upstream.Close()

				server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, Providers: []config.ProviderConfig{{
					ID:                   "openai",
					Enabled:              true,
					UpstreamBaseURL:      upstream.URL,
					UpstreamAPIKey:       "test-key",
					UpstreamEndpointType: endpointType,
					SupportsResponses:    true,
				}}})
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(feature.body))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()

				server.ServeHTTP(rec, req)

				if feature.name == "persisted reasoning item" {
					if rec.Code != http.StatusOK || upstreamHits != 1 {
						t.Fatalf("expected client-owned persisted reasoning to reach upstream, status=%d calls=%d body=%s", rec.Code, upstreamHits, rec.Body.String())
					}
					return
				}
				if rec.Code != http.StatusBadRequest || upstreamHits != 0 {
					t.Fatalf("expected unsupported feature preflight rejection, status=%d calls=%d body=%s", rec.Code, upstreamHits, rec.Body.String())
				}
			})
		}
	}
}

func TestChatRouteRejectsResponsesOnlyFeaturesBeforeAnthropicUpstream(t *testing.T) {
	features := []struct {
		name string
		body string
	}{
		{
			name: "reasoning mode",
			body: `{"model":"gpt-5.6","reasoning":{"mode":"pro"},"messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			name: "original image detail",
			body: `{"model":"gpt-5.6","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/image.png","detail":"original"}}]}]}`,
		},
	}

	for _, feature := range features {
		t.Run(feature.name, func(t *testing.T) {
			upstreamHits := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamHits++
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"unexpected"}`))
			}))
			defer upstream.Close()

			server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DefaultProReasoningModeSet: true, DefaultProReasoningMode: false, Providers: []config.ProviderConfig{{
				ID:                        "openai",
				Enabled:                   true,
				UpstreamBaseURL:           upstream.URL,
				UpstreamAPIKey:            "test-key",
				UpstreamEndpointType:      config.UpstreamEndpointTypeAnthropic,
				SupportsChat:              true,
				SupportsAnthropicMessages: true,
			}}})
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(feature.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected preflight 400, got %d body=%s", rec.Code, rec.Body.String())
			}
			expectedCode := "unsupported_upstream_feature"
			if feature.name == "reasoning mode" {
				expectedCode = "unsupported_reasoning_mode"
			}
			if !strings.Contains(rec.Body.String(), expectedCode) {
				t.Fatalf("expected stable %s error, got %s", expectedCode, rec.Body.String())
			}
			if upstreamHits != 0 {
				t.Fatalf("expected no upstream request, got %d", upstreamHits)
			}
		})
	}
}

func TestResponsesRoutePreservesUnknownNativeResponsesEvent(t *testing.T) {
	// Given
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n" +
			"data: {\"response\":{\"id\":\"resp_1\",\"object\":\"response\"}}\n\n" +
			"event: response.program_output.delta\n" +
			"data: {\"type\":\"response.program_output.delta\",\"item_id\":\"prog_1\",\"delta\":\"console.log(1)\"}\n\n" +
			"event: response.completed\n" +
			"data: {\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\"}}\n\n"))
	}))
	defer upstream.Close()
	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, Providers: []config.ProviderConfig{{
		ID:                   "openai",
		Enabled:              true,
		UpstreamBaseURL:      upstream.URL,
		UpstreamAPIKey:       "test-key",
		UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
		SupportsResponses:    true,
	}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6","stream":true,"input":"hello"}`))
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("expected successful Responses stream, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "event: response.program_output.delta") {
		t.Fatalf("expected unknown native event passthrough, got %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "unexpected EOF") {
		t.Fatalf("expected terminal stream completion, got %s", rec.Body.String())
	}
}

func TestResponsesRouteMergesClientAndRequiredMultiAgentBetaHeaders(t *testing.T) {
	// Given
	var receivedBeta string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBeta = r.Header.Get("OpenAI-Beta")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[]}`))
	}))
	defer upstream.Close()
	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, Providers: []config.ProviderConfig{{
		ID:                          "openai",
		Enabled:                     true,
		UpstreamBaseURL:             upstream.URL,
		UpstreamAPIKey:              "test-key",
		UpstreamEndpointType:        config.UpstreamEndpointTypeResponses,
		SupportsResponses:           true,
		SupportsResponsesMultiAgent: true,
	}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6","multi_agent":{"enabled":true},"input":"hello"}`))
	req.Header.Set("OpenAI-Beta", "other_feature=v1,responses_multi_agent=v1")
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if receivedBeta != "other_feature=v1,responses_multi_agent=v1" {
		t.Fatalf("expected client and required beta headers merged once, got %q", receivedBeta)
	}
}

func TestResponsesRoutePreservesTopLevelFieldsAcrossChatUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from chat upstream"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsResponses: true, SupportsChat: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","previous_response_id":"resp_123","metadata":{"trace_id":"trace_123"},"parallel_tool_calls":true,"truncation":"auto","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"previous_response_id":"resp_123"`) || !strings.Contains(body, `"parallel_tool_calls":true`) || !strings.Contains(body, `"truncation":"auto"`) || !strings.Contains(body, `"trace_id":"trace_123"`) {
		t.Fatalf("expected preserved top-level fields in responses output, got %s", body)
	}
}

func TestResponsesRouteUsesRealChatUpstreamResponseID(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_123","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from chat upstream"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsResponses: true, SupportsChat: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response body: %v", err)
	}
	if got, _ := body["id"].(string); got != "chatcmpl_123" {
		t.Fatalf("expected responses output to keep upstream chat id chatcmpl_123, got %#v", body["id"])
	}
	if strings.HasPrefix(body["id"].(string), "resp_") {
		t.Fatalf("expected upstream chat id instead of synthesized proxy id, got %#v", body["id"])
	}
}

func TestResponsesRoutePreservesTopLevelFieldsAcrossAnthropicUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello from anthropic upstream"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsParallelToolCallsControl: true, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","previous_response_id":"resp_123","metadata":{"trace_id":"trace_123"},"parallel_tool_calls":false,"truncation":"auto","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"previous_response_id":"resp_123"`) || !strings.Contains(body, `"parallel_tool_calls":false`) || !strings.Contains(body, `"truncation":"auto"`) || !strings.Contains(body, `"trace_id":"trace_123"`) {
		t.Fatalf("expected preserved top-level fields in responses output, got %s", body)
	}
}

func TestResponsesRouteDoesNotRestoreHistoryFromAnotherServer(t *testing.T) {
	// Given
	audioUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_shared","object":"response","status":"completed","output":[{"id":"msg_audio","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"stored separately"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer audioUpstream.Close()
	audioServer := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: audioUpstream.URL, UpstreamAPIKey: "test-key", SupportsResponses: true, SupportsChat: true}}})
	audioRequest := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"YWJj","format":"wav"}}]}]}`))
	audioRequest.Header.Set("Content-Type", "application/json")
	audioRecorder := httptest.NewRecorder()
	audioServer.ServeHTTP(audioRecorder, audioRequest)
	if audioRecorder.Code != http.StatusOK {
		t.Fatalf("expected audio source server to accept request, got %d body=%s", audioRecorder.Code, audioRecorder.Body.String())
	}

	var anthropicRequestBody string
	anthropicUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read anthropic upstream request: %v", err)
		}
		anthropicRequestBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_next","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"isolated"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer anthropicUpstream.Close()
	anthropicServer := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: anthropicUpstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})
	continuationRequest := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","previous_response_id":"resp_shared","input":"hello"}`))
	continuationRequest.Header.Set("Content-Type", "application/json")
	continuationRecorder := httptest.NewRecorder()

	// When
	anthropicServer.ServeHTTP(continuationRecorder, continuationRequest)

	// Then
	if continuationRecorder.Code != http.StatusOK {
		t.Fatalf("expected separate server continuation to succeed, got %d body=%s", continuationRecorder.Code, continuationRecorder.Body.String())
	}
	if strings.Contains(anthropicRequestBody, `"input_audio"`) {
		t.Fatalf("expected separate server not to restore audio history, got %s", anthropicRequestBody)
	}
}

func TestResponsesRouteDoesNotEchoArbitraryUnknownRequestFieldsBackIntoOutput(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from chat upstream"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsResponses: true, SupportsChat: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","custom_flag":"alpha","custom_config":{"trace_id":"trace_123"},"previous_response_id":"resp_123","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `"custom_flag":"alpha"`) || strings.Contains(body, `"trace_id":"trace_123"`) {
		t.Fatalf("expected unknown request-only fields to stay out of normalized responses output, got %s", body)
	}
	if !strings.Contains(body, `"previous_response_id":"resp_123"`) {
		t.Fatalf("expected explicitly preserved responses field to remain, got %s", body)
	}
}

func TestResponsesRouteDropsPersistedReasoningIncludeForChatUpstream(t *testing.T) {
	upstreamCalls := 0
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		body, _ := io.ReadAll(r.Body)
		upstreamBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from chat upstream"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsResponses: true, SupportsChat: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","store":false,"include":["reasoning.encrypted_content"],"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("expected one chat upstream call, got %d", upstreamCalls)
	}
	if strings.Contains(upstreamBody, "reasoning.encrypted_content") {
		t.Fatalf("expected Responses-only include omitted from chat request, got %s", upstreamBody)
	}
}

func TestResponsesRouteDropsPersistedReasoningIncludeForAnthropicUpstream(t *testing.T) {
	upstreamCalls := 0
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		body, _ := io.ReadAll(r.Body)
		upstreamBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello from anthropic upstream"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","store":false,"include":["reasoning.encrypted_content"],"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("expected one Anthropic upstream call, got %d", upstreamCalls)
	}
	if strings.Contains(upstreamBody, "reasoning.encrypted_content") {
		t.Fatalf("expected Responses-only include omitted from Anthropic request, got %s", upstreamBody)
	}
}

func TestResponsesRouteUsesResponsesUpstreamEndpointTypeForToolCompatibilityBaseline(t *testing.T) {
	var gotBody string
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","status":"completed","output":[{"id":"msg_123","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello from responses upstream"}]}]}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeResponses, SupportsResponses: true, SupportsChat: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello","tools":[{"type":"custom","name":"code_exec","description":"Run code"},{"type":"function","name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}},{"type":"web_search","name":"web_lookup","description":"Search the web"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/responses" {
		t.Fatalf("expected responses upstream path, got %q", gotPath)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal responses upstream payload: %v body=%s", err, gotBody)
	}
	tools, _ := payload["tools"].([]any)
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools in responses upstream payload, got %#v body=%s", payload["tools"], gotBody)
	}
	customTool, _ := tools[0].(map[string]any)
	if got, _ := customTool["type"].(string); got != "custom" {
		t.Fatalf("expected custom tool type preserved on responses upstream, got %#v body=%s", customTool, gotBody)
	}
	functionTool, _ := tools[1].(map[string]any)
	if got, _ := functionTool["type"].(string); got != "function" {
		t.Fatalf("expected function tool type preserved on responses upstream, got %#v body=%s", functionTool, gotBody)
	}
	webSearchTool, _ := tools[2].(map[string]any)
	if got, _ := webSearchTool["type"].(string); got != "web_search" {
		t.Fatalf("expected web_search tool type preserved on responses upstream, got %#v body=%s", webSearchTool, gotBody)
	}
}

func TestResponsesRouteCompatibilityModeUsesSelectedUpstreamEndpointType(t *testing.T) {
	requestBody := `{"model":"gpt-5","input":"hello","tools":[{"type":"custom","name":"code_exec","description":"Run code"},{"type":"function","name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}},{"type":"web_search","name":"web_lookup","description":"Search the web"}],"tool_choice":{"type":"function","name":"web_lookup"}}`

	decodePayload := func(t *testing.T, body string) map[string]any {
		t.Helper()
		var payload map[string]any
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			t.Fatalf("unmarshal upstream payload: %v body=%s", err, body)
		}
		return payload
	}
	toolEntries := func(t *testing.T, payload map[string]any) []map[string]any {
		t.Helper()
		rawTools, _ := payload["tools"].([]any)
		if len(rawTools) != 3 {
			t.Fatalf("expected 3 tools in upstream payload, got %#v", payload["tools"])
		}
		tools := make([]map[string]any, 0, len(rawTools))
		for _, rawTool := range rawTools {
			tool, _ := rawTool.(map[string]any)
			if tool == nil {
				t.Fatalf("expected tool entry to be object, got %#v", rawTool)
			}
			tools = append(tools, tool)
		}
		return tools
	}
	assertFallbackStringSchema := func(t *testing.T, raw any, field string) {
		t.Helper()
		schema, _ := raw.(map[string]any)
		if schema == nil {
			t.Fatalf("expected schema object, got %#v", raw)
		}
		if got, _ := schema["type"].(string); got != "object" {
			t.Fatalf("expected fallback schema type object, got %#v", schema)
		}
		properties, _ := schema["properties"].(map[string]any)
		fieldSchema, _ := properties[field].(map[string]any)
		if got, _ := fieldSchema["type"].(string); got != "string" {
			t.Fatalf("expected fallback string schema for %q, got %#v", field, schema)
		}
	}
	assertEmptySchema := func(t *testing.T, raw any, context string) {
		t.Helper()
		schema, _ := raw.(map[string]any)
		if schema == nil {
			t.Fatalf("expected %s schema object, got %#v", context, raw)
		}
		if len(schema) != 0 {
			t.Fatalf("expected %s schema to remain empty without responses compat fallback, got %#v", context, schema)
		}
	}

	t.Run("responses upstream rewrites non-function tools", func(t *testing.T) {
		var gotBody string
		var gotPath string
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			bodyBytes, _ := io.ReadAll(r.Body)
			gotBody = string(bodyBytes)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_compat","object":"response","status":"completed","output":[{"id":"msg_123","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello from responses upstream"}]}]}`))
		}))
		defer upstream.Close()

		server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeResponses, ResponsesToolCompatMode: config.ResponsesToolCompatModeFunctionOnly, SupportsResponses: true, SupportsChat: true}}})
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(requestBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		server.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
		}
		if gotPath != "/responses" {
			t.Fatalf("expected responses upstream path, got %q", gotPath)
		}
		tools := toolEntries(t, decodePayload(t, gotBody))
		customTool := tools[0]
		if got, _ := customTool["type"].(string); got != "function" {
			t.Fatalf("expected compat mode to rewrite custom tool on responses upstream, got %#v", customTool)
		}
		assertFallbackStringSchema(t, customTool["parameters"], "input")
		functionTool := tools[1]
		if got, _ := functionTool["type"].(string); got != "function" {
			t.Fatalf("expected native function tool to stay function on responses upstream, got %#v", functionTool)
		}
		assertFallbackStringSchema(t, functionTool["parameters"], "city")
		webSearchTool := tools[2]
		if got, _ := webSearchTool["type"].(string); got != "function" {
			t.Fatalf("expected compat mode to rewrite web_search tool on responses upstream, got %#v", webSearchTool)
		}
		if got, _ := webSearchTool["name"].(string); got != "web_lookup" {
			t.Fatalf("expected responses compat rewrite to preserve explicit web_search name, got %#v", webSearchTool)
		}
		assertFallbackStringSchema(t, webSearchTool["parameters"], "query")
	})

	t.Run("chat upstream ignores responses compat mode", func(t *testing.T) {
		var gotBody string
		var gotPath string
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			bodyBytes, _ := io.ReadAll(r.Body)
			gotBody = string(bodyBytes)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from chat upstream"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
		}))
		defer upstream.Close()

		server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, ResponsesToolCompatMode: config.ResponsesToolCompatModeFunctionOnly, SupportsResponses: true, SupportsChat: true}}})
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(requestBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		server.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
		}
		if gotPath != "/chat/completions" {
			t.Fatalf("expected chat upstream path, got %q", gotPath)
		}
		tools := toolEntries(t, decodePayload(t, gotBody))
		customTool := tools[0]
		if got, _ := customTool["type"].(string); got != "function" {
			t.Fatalf("expected chat upstream tools to keep their own function wrapper, got %#v", customTool)
		}
		customFunction, _ := customTool["function"].(map[string]any)
		assertEmptySchema(t, customFunction["parameters"], "chat custom tool")
		webSearchTool := tools[2]
		webSearchFunction, _ := webSearchTool["function"].(map[string]any)
		if got, _ := webSearchFunction["name"].(string); got != "web_lookup" {
			t.Fatalf("expected chat upstream builder to preserve original tool name, got %#v", webSearchFunction)
		}
		assertEmptySchema(t, webSearchFunction["parameters"], "chat web_search tool")
	})

	t.Run("anthropic upstream rewrites non-function responses tools", func(t *testing.T) {
		var gotBody string
		var gotPath string
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			bodyBytes, _ := io.ReadAll(r.Body)
			gotBody = string(bodyBytes)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello from anthropic upstream"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
		}))
		defer upstream.Close()

		server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, ResponsesToolCompatMode: config.ResponsesToolCompatModeFunctionOnly, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(requestBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		server.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
		}
		if gotPath != "/messages" {
			t.Fatalf("expected anthropic upstream path, got %q", gotPath)
		}
		tools := toolEntries(t, decodePayload(t, gotBody))
		customTool := tools[0]
		if got, _ := customTool["name"].(string); got != "code_exec" {
			t.Fatalf("expected anthropic upstream tool name preserved, got %#v", customTool)
		}
		assertEmptySchema(t, customTool["input_schema"], "anthropic custom tool")
		webSearchTool := tools[2]
		if got, _ := webSearchTool["name"].(string); got != "web_lookup" {
			t.Fatalf("expected anthropic upstream builder to preserve original web_search name, got %#v", webSearchTool)
		}
		assertFallbackStringSchema(t, webSearchTool["input_schema"], "query")
		toolChoice, _ := decodePayload(t, gotBody)["tool_choice"].(map[string]any)
		if got, _ := toolChoice["type"].(string); got != "tool" {
			t.Fatalf("expected anthropic tool_choice.type=tool, got %#v", toolChoice)
		}
		if got, _ := toolChoice["name"].(string); got != "web_lookup" {
			t.Fatalf("expected anthropic tool_choice name web_lookup, got %#v", toolChoice)
		}
	})
}

func TestChatRoutePreservesUnhandledTopLevelFieldsAcrossChatUpstream(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from chat upstream"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsResponses: true, SupportsChat: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","custom_flag":"alpha","custom_config":{"trace_id":"trace_123","nested":true},"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal chat upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["custom_flag"].(string); got != "alpha" {
		t.Fatalf("expected custom_flag passthrough, got %#v body=%s", payload["custom_flag"], gotBody)
	}
	customConfig, _ := payload["custom_config"].(map[string]any)
	if got, _ := customConfig["trace_id"].(string); got != "trace_123" {
		t.Fatalf("expected custom_config passthrough, got %#v body=%s", payload["custom_config"], gotBody)
	}
}

func TestChatRouteMapsServiceTierAliasForResponsesUpstream(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","status":"completed","output":[{"id":"msg_123","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}]}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeResponses, SupportsResponses: true, SupportsChat: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","serviceTier":"priority","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal responses upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["service_tier"].(string); got != "priority" {
		t.Fatalf("expected service_tier mapped from serviceTier alias, got %#v body=%s", payload["service_tier"], gotBody)
	}
	if _, exists := payload["serviceTier"]; exists {
		t.Fatalf("expected serviceTier alias to be removed from upstream payload, got %#v body=%s", payload, gotBody)
	}
}

func TestChatRouteProviderServiceTierOverrideWinsForResponsesUpstream(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","status":"completed","service_tier":"default","output":[{"id":"msg_123","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}]}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeResponses, SupportsResponses: true, SupportsChat: true, OpenAIServiceTier: config.OpenAIServiceTierPriority}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","serviceTier":"flex","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal responses upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["service_tier"].(string); got != "priority" {
		t.Fatalf("expected provider override service_tier priority, got %#v body=%s", payload["service_tier"], gotBody)
	}
	if got := rec.Header().Get(headerClientToProxyServiceTier); got != "flex" {
		t.Fatalf("expected client service tier header flex, got %q", got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamServiceTier); got != "priority" {
		t.Fatalf("expected upstream service tier header priority, got %q", got)
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal downstream response: %v body=%s", err, rec.Body.String())
	}
	if got, _ := response["service_tier"].(string); got != "default" {
		t.Fatalf("expected downstream service_tier default from upstream response, got %#v body=%s", response["service_tier"], rec.Body.String())
	}
}

func TestResponsesRouteProviderServiceTierOverrideWinsForResponsesUpstream(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","status":"completed","service_tier":"default","output":[{"id":"msg_123","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}]}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeResponses, SupportsResponses: true, SupportsChat: true, OpenAIServiceTier: config.OpenAIServiceTierPriority}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","serviceTier":"flex","input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal responses upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["service_tier"].(string); got != "priority" {
		t.Fatalf("expected provider override service_tier priority, got %#v body=%s", payload["service_tier"], gotBody)
	}
	if got := rec.Header().Get(headerClientToProxyServiceTier); got != "flex" {
		t.Fatalf("expected client service tier header flex, got %q", got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamServiceTier); got != "priority" {
		t.Fatalf("expected upstream service tier header priority, got %q", got)
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal downstream response: %v body=%s", err, rec.Body.String())
	}
	if got, _ := response["service_tier"].(string); got != "default" {
		t.Fatalf("expected downstream service_tier default from upstream response, got %#v body=%s", response["service_tier"], rec.Body.String())
	}
}

func TestResponsesRouteMapsServiceTierAliasForChatUpstream(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from chat upstream"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsResponses: true, SupportsChat: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","serviceTier":"flex","input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal chat upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["service_tier"].(string); got != "flex" {
		t.Fatalf("expected service_tier mapped from serviceTier alias, got %#v body=%s", payload["service_tier"], gotBody)
	}
	if _, exists := payload["serviceTier"]; exists {
		t.Fatalf("expected serviceTier alias removed from chat upstream payload, got %#v body=%s", payload, gotBody)
	}
}

func TestAnthropicRoutePreservesUnhandledTopLevelFieldsAcrossAnthropicUpstream(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello from anthropic upstream"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":128,"service_tier":"flex","custom_config":{"trace_id":"trace_123","mode":"x"},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal anthropic upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["service_tier"].(string); got != "flex" {
		t.Fatalf("expected service_tier passthrough, got %#v body=%s", payload["service_tier"], gotBody)
	}
	customConfig, _ := payload["custom_config"].(map[string]any)
	if got, _ := customConfig["trace_id"].(string); got != "trace_123" {
		t.Fatalf("expected custom_config passthrough, got %#v body=%s", payload["custom_config"], gotBody)
	}
}

func TestAnthropicRoutePreservesCacheControlAcrossAnthropicUpstream(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello from anthropic upstream"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal anthropic upstream payload: %v body=%s", err, gotBody)
	}
	messages, _ := payload["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected one anthropic upstream message, got %#v body=%s", payload["messages"], gotBody)
	}
	message, _ := messages[0].(map[string]any)
	content, _ := message["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected one content block, got %#v body=%s", message["content"], gotBody)
	}
	block, _ := content[0].(map[string]any)
	cacheControl, _ := block["cache_control"].(map[string]any)
	if got, _ := cacheControl["type"].(string); got != "ephemeral" {
		t.Fatalf("expected cache_control.type ephemeral, got %#v body=%s", block["cache_control"], gotBody)
	}
}

func TestAnthropicRouteDropsCacheControlForChatUpstream(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsAnthropicMessages: true, SupportsChat: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5","max_tokens":128,"cache_control":{"type":"ephemeral"},"system":[{"type":"text","text":"system prompt","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(gotBody, "cache_control") || strings.Contains(gotBody, "ephemeral") {
		t.Fatalf("expected cache_control to be dropped for anthropic-to-chat upstream, got %s", gotBody)
	}
}

func TestAnthropicRouteDropsCacheControlForResponsesUpstream(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","status":"completed","output":[{"id":"msg_123","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeResponses, SupportsAnthropicMessages: true, SupportsResponses: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5","max_tokens":128,"cache_control":{"type":"ephemeral"},"system":[{"type":"text","text":"system prompt","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(gotBody, "cache_control") || strings.Contains(gotBody, "ephemeral") {
		t.Fatalf("expected cache_control to be dropped for anthropic-to-responses upstream, got %s", gotBody)
	}
}

func TestChatRoutePrependsProviderPromptIntoChatUpstreamSystemMessage(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from chat upstream"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsResponses: true, SupportsChat: true, SystemPromptText: "provider system", SystemPromptPosition: config.SystemPromptPositionPrepend}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"system","content":"chat system"},{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal chat upstream payload: %v body=%s", err, gotBody)
	}
	messages, _ := payload["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("expected merged system message plus user message, got %#v", payload["messages"])
	}
	first, _ := messages[0].(map[string]any)
	if role, _ := first["role"].(string); role != "system" {
		t.Fatalf("expected first chat upstream message role system, got %#v", first)
	}
	if content, _ := first["content"].(string); content != "provider system\n\nchat system" {
		t.Fatalf("expected merged system content string, got %#v body=%s", first["content"], gotBody)
	}
}

func TestResponsesRouteAppendsProviderPromptIntoResponsesUpstreamInstructions(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","output":[{"id":"msg_123","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello from responses upstream"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeResponses, SupportsResponses: true, SupportsChat: true, SystemPromptText: "provider system", SystemPromptPosition: config.SystemPromptPositionAppend}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","instructions":"user instructions","input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal responses upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["instructions"].(string); got != "user instructions\n\nprovider system" {
		t.Fatalf("expected appended provider prompt in responses instructions, got %#v body=%s", payload["instructions"], gotBody)
	}
	input, _ := payload["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("expected user input items to remain unchanged, got %#v", payload["input"])
	}
}

func TestResponsesRouteNoPromptSuffixSkipsProviderPromptAndKeepsReasoningSuffix(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","output":[{"id":"msg_123","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello from responses upstream"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		DefaultProReasoningModeSet:  true,
		DefaultProReasoningMode:     false,
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		EnableNoPromptModelSuffix:   true,
		Providers: []config.ProviderConfig{{
			ID:                          "openai",
			Enabled:                     true,
			UpstreamBaseURL:             upstream.URL,
			UpstreamAPIKey:              "test-key",
			UpstreamEndpointType:        config.UpstreamEndpointTypeResponses,
			SupportsResponses:           true,
			SupportsChat:                true,
			ManualModels:                []string{"gpt-5.5"},
			EnableReasoningEffortSuffix: true,
			SystemPromptText:            "provider system",
			SystemPromptPosition:        config.SystemPromptPositionAppend,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.5-low-noprompt","instructions":"user instructions","input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "gpt-5.5" {
		t.Fatalf("expected noprompt suffix to be stripped before upstream model resolution, got %q", got)
	}
	if got := rec.Header().Get(headerClientToProxyModel); got != "gpt-5.5-low-noprompt" {
		t.Fatalf("expected client model observability header to preserve raw client model, got %q", got)
	}
	if got := rec.Header().Get(headerClientToProxyNoPrompt); got != "true" {
		t.Fatalf("expected noprompt observability header true, got %q", got)
	}
	if got := rec.Header().Get("X-Client-To-Proxy-Reasoning-Effort"); got != "low" {
		t.Fatalf("expected reasoning suffix effort low to be preserved, got %q", got)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal responses upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["instructions"].(string); got != "user instructions" {
		t.Fatalf("expected provider prompt to be skipped while client instructions remain, got %#v body=%s", payload["instructions"], gotBody)
	}
}

func TestResponsesRouteNoPromptSuffixWorksWithoutReasoningSuffix(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","output":[{"id":"msg_123","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello from responses upstream"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		DefaultProReasoningModeSet:  true,
		DefaultProReasoningMode:     false,
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		EnableNoPromptModelSuffix:   true,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			SupportsChat:         true,
			ManualModels:         []string{"gpt-5.5"},
			SystemPromptText:     "provider system",
			SystemPromptPosition: config.SystemPromptPositionAppend,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.5-noprompt","instructions":"user instructions","input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "gpt-5.5" {
		t.Fatalf("expected noprompt suffix to be stripped before upstream model resolution, got %q", got)
	}
	if got := rec.Header().Get(headerClientToProxyModel); got != "gpt-5.5-noprompt" {
		t.Fatalf("expected client model observability header to preserve raw client model, got %q", got)
	}
	if got := rec.Header().Get(headerClientToProxyNoPrompt); got != "true" {
		t.Fatalf("expected noprompt observability header true, got %q", got)
	}
	if got := rec.Header().Get("X-Client-To-Proxy-Reasoning-Effort"); got != "" {
		t.Fatalf("expected no reasoning effort when request only has noprompt suffix, got %q", got)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal responses upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["instructions"].(string); got != "user instructions" {
		t.Fatalf("expected provider prompt to be skipped while client instructions remain, got %#v body=%s", payload["instructions"], gotBody)
	}
}

func TestResponsesRouteNoPromptSuffixWorksBeforeReasoningSuffix(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","output":[{"id":"msg_123","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello from responses upstream"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		DefaultProReasoningModeSet:  true,
		DefaultProReasoningMode:     false,
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		EnableNoPromptModelSuffix:   true,
		Providers: []config.ProviderConfig{{
			ID:                          "openai",
			Enabled:                     true,
			UpstreamBaseURL:             upstream.URL,
			UpstreamAPIKey:              "test-key",
			UpstreamEndpointType:        config.UpstreamEndpointTypeResponses,
			SupportsResponses:           true,
			SupportsChat:                true,
			ManualModels:                []string{"gpt-5.5"},
			EnableReasoningEffortSuffix: true,
			SystemPromptText:            "provider system",
			SystemPromptPosition:        config.SystemPromptPositionAppend,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.5-noprompt-low","instructions":"user instructions","input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "gpt-5.5" {
		t.Fatalf("expected proxy suffix markers to be stripped before upstream model resolution, got %q", got)
	}
	if got := rec.Header().Get(headerClientToProxyModel); got != "gpt-5.5-noprompt-low" {
		t.Fatalf("expected client model observability header to preserve raw client model, got %q", got)
	}
	if got := rec.Header().Get(headerClientToProxyNoPrompt); got != "true" {
		t.Fatalf("expected noprompt observability header true, got %q", got)
	}
	if got := rec.Header().Get("X-Client-To-Proxy-Reasoning-Effort"); got != "low" {
		t.Fatalf("expected reasoning suffix effort low to be preserved, got %q", got)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal responses upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["instructions"].(string); got != "user instructions" {
		t.Fatalf("expected provider prompt to be skipped while client instructions remain, got %#v body=%s", payload["instructions"], gotBody)
	}
	if !strings.Contains(gotBody, `"reasoning":{"effort":"low","summary":"auto"}`) {
		t.Fatalf("expected reasoning suffix to remain effective after noprompt stripping, got %s", gotBody)
	}
}

func TestChatRouteNoPromptSuffixWorksWithoutReasoningSuffix(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from chat upstream"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		DefaultProReasoningModeSet:  true,
		DefaultProReasoningMode:     false,
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		EnableNoPromptModelSuffix:   true,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeChat,
			SupportsResponses:    true,
			SupportsChat:         true,
			ManualModels:         []string{"gpt-5.5"},
			SystemPromptText:     "provider system",
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.5-noprompt","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "gpt-5.5" {
		t.Fatalf("expected noprompt suffix to be stripped before upstream model resolution, got %q", got)
	}
	if got := rec.Header().Get(headerClientToProxyNoPrompt); got != "true" {
		t.Fatalf("expected noprompt observability header true, got %q", got)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal chat upstream payload: %v body=%s", err, gotBody)
	}
	messages, _ := payload["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected provider prompt to be skipped and only user message to remain, got %#v", payload["messages"])
	}
	first, _ := messages[0].(map[string]any)
	if role, _ := first["role"].(string); role != "user" {
		t.Fatalf("expected user message to remain first when provider prompt is skipped, got %#v", first)
	}
}

func TestAnthropicRouteNoPromptSuffixWorksWithoutReasoningSuffix(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello from anthropic upstream"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "anthropic",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		EnableNoPromptModelSuffix:   true,
		Providers: []config.ProviderConfig{{
			ID:                        "anthropic",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			UpstreamEndpointType:      config.UpstreamEndpointTypeAnthropic,
			SupportsResponses:         true,
			SupportsChat:              true,
			SupportsAnthropicMessages: true,
			ManualModels:              []string{"claude-sonnet-4-5"},
			SystemPromptText:          "provider system",
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5-noprompt","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "claude-sonnet-4-5" {
		t.Fatalf("expected noprompt suffix to be stripped before upstream model resolution, got %q", got)
	}
	if got := rec.Header().Get(headerClientToProxyNoPrompt); got != "true" {
		t.Fatalf("expected noprompt observability header true, got %q", got)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal anthropic upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["system"].(string); got != "" {
		t.Fatalf("expected provider prompt to be skipped for anthropic system field, got %#v body=%s", payload["system"], gotBody)
	}
}

func TestExplicitResponsesRouteManualNoPromptModelStillActsAsProxySuffix(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.5","object":"model"}]}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","output":[],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		EnableNoPromptModelSuffix: true,
		Providers: []config.ProviderConfig{{
			ID:                   "test",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			SupportsModels:       true,
			ManualModels:         []string{"gpt-5.5-noprompt"},
			SystemPromptText:     "provider system",
			SystemPromptPosition: config.SystemPromptPositionAppend,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/test/v1/responses", strings.NewReader(`{"model":"gpt-5.5-noprompt","instructions":"user instructions","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerClientToProxyNoPrompt); got != "true" {
		t.Fatalf("expected manual noprompt model to still set noprompt header true, got %q", got)
	}
	if got := rec.Header().Get(headerClientToProxyModel); got != "gpt-5.5-noprompt" {
		t.Fatalf("expected client model header to preserve raw client model, got %q", got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "gpt-5.5" {
		t.Fatalf("expected upstream model to strip noprompt suffix, got %q", got)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal responses upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["instructions"].(string); got != "user instructions" {
		t.Fatalf("expected provider prompt to be skipped while client instructions remain, got %#v body=%s", payload["instructions"], gotBody)
	}
}

func TestResponsesRouteNoneReasoningSuffixDisablesReasoning(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","output":[],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		DefaultProReasoningModeSet:  true,
		DefaultProReasoningMode:     false,
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                          "openai",
			Enabled:                     true,
			UpstreamBaseURL:             upstream.URL,
			UpstreamAPIKey:              "test-key",
			UpstreamEndpointType:        config.UpstreamEndpointTypeResponses,
			SupportsResponses:           true,
			SupportsChat:                true,
			ManualModels:                []string{"gpt-5.5"},
			EnableReasoningEffortSuffix: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.5-none","reasoning":{"effort":"high","summary":"auto"},"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerClientToProxyReasoningEffort); got != "none" {
		t.Fatalf("expected client reasoning effort none, got %q", got)
	}
	assertJSONHeaderEquals(t, rec.Header().Get(headerProxyToUpstreamReasoningParameters), map[string]any{"reasoning": map[string]any{"effort": "none", "summary": "auto"}})
	if !strings.Contains(gotBody, `"reasoning":{"effort":"none","summary":"auto"}`) {
		t.Fatalf("expected none suffix to send explicit reasoning none upstream, got %s", gotBody)
	}
}

func TestResponsesRouteNoPromptSuffixIsLiteralWhenDisabled(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","output":[],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		EnableNoPromptModelSuffix:   false,
		Providers: []config.ProviderConfig{{
			ID:                          "openai",
			Enabled:                     true,
			UpstreamBaseURL:             upstream.URL,
			UpstreamAPIKey:              "test-key",
			UpstreamEndpointType:        config.UpstreamEndpointTypeResponses,
			SupportsResponses:           true,
			SupportsChat:                true,
			ManualModels:                []string{"gpt-5.5"},
			EnableReasoningEffortSuffix: true,
			SystemPromptText:            "provider system",
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.5-low-noprompt","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected disabled noprompt suffix to be treated as a literal unavailable model, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerClientToProxyNoPrompt); got != "false" {
		t.Fatalf("expected disabled noprompt suffix to report false, got %q", got)
	}
}

func TestResponsesRouteProviderNoPromptSuffixOverrideDisablesRootEnabledSuffix(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","output":[],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		EnableNoPromptModelSuffix:   true,
		Providers: []config.ProviderConfig{{
			ID:                           "openai",
			Enabled:                      true,
			UpstreamBaseURL:              upstream.URL,
			UpstreamAPIKey:               "test-key",
			UpstreamEndpointType:         config.UpstreamEndpointTypeResponses,
			SupportsResponses:            true,
			SupportsChat:                 true,
			ManualModels:                 []string{"gpt-5.5"},
			EnableReasoningEffortSuffix:  true,
			EnableNoPromptModelSuffixSet: true,
			EnableNoPromptModelSuffix:    false,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.5-noprompt","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected provider noprompt override to keep suffix literal and unavailable, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerClientToProxyNoPrompt); got != "false" {
		t.Fatalf("expected provider noprompt override to report false, got %q", got)
	}
}

func TestResponsesRouteHiddenNoPromptSuffixDisablesVariant(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","output":[],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		EnableNoPromptModelSuffix:   true,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			SupportsChat:         true,
			ManualModels:         []string{"gpt-5.5"},
			HiddenModels:         []string{"gpt-5.5-noprompt"},
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.5-noprompt","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected hidden noprompt variant to be rejected, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestChatRouteUsesProviderPromptAsSystemMessageWhenClientHasNoInstructionRole(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from chat upstream"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsResponses: true, SupportsChat: true, SystemPromptText: "provider system"}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal chat upstream payload: %v body=%s", err, gotBody)
	}
	messages, _ := payload["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("expected provider system message plus user message, got %#v", payload["messages"])
	}
	first, _ := messages[0].(map[string]any)
	if role, _ := first["role"].(string); role != "system" {
		t.Fatalf("expected first chat upstream message role system, got %#v", first)
	}
	if text, _ := first["content"].(string); text != "provider system" {
		t.Fatalf("expected provider prompt to become standalone system message, got %#v body=%s", first["content"], gotBody)
	}
}

func TestResponsesRouteUsesProviderPromptAsInstructionsWhenClientHasNoInstructions(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","output":[{"id":"msg_123","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello from responses upstream"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeResponses, SupportsResponses: true, SupportsChat: true, SystemPromptText: "provider system"}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal responses upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["instructions"].(string); got != "provider system" {
		t.Fatalf("expected provider prompt to become responses instructions, got %#v body=%s", payload["instructions"], gotBody)
	}
}

func TestAnthropicRouteAppendsProviderPromptIntoAnthropicSystemField(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello from anthropic upstream"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true, SystemPromptText: "provider system", SystemPromptPosition: config.SystemPromptPositionAppend}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":128,"system":"anthropic system","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal anthropic upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["system"].(string); got != "anthropic system\n\nprovider system" {
		t.Fatalf("expected appended provider prompt in anthropic system field, got %#v body=%s", payload["system"], gotBody)
	}
	messages, _ := payload["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected anthropic user messages to remain intact, got %#v", payload["messages"])
	}
}

func TestAnthropicRouteUsesProviderPromptAsSystemWhenClientHasNoSystem(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello from anthropic upstream"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true, SystemPromptText: "provider system"}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal anthropic upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["system"].(string); got != "provider system" {
		t.Fatalf("expected provider prompt to become anthropic system field, got %#v body=%s", payload["system"], gotBody)
	}
}

func TestAnthropicRouteMapsReasoningSuffixToThinkingWhenEnabled(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true, EnableReasoningEffortSuffix: true, MapReasoningSuffixToAnthropicThinking: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-opus-4-6-high","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(gotBody, `"thinking":{"type":"adaptive"}`) {
		t.Fatalf("expected anthropic upstream payload to include adaptive thinking config, got %s", gotBody)
	}
	if !strings.Contains(gotBody, `"output_config":{"effort":"high"}`) {
		t.Fatalf("expected anthropic upstream payload to include output_config effort, got %s", gotBody)
	}
}

func TestChatRouteMapsReasoningSuffixToAnthropicThinkingWhenEnabled(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true, EnableReasoningEffortSuffix: true, MapReasoningSuffixToAnthropicThinking: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"claude-opus-4-6-high","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(gotBody, `"thinking":{"type":"adaptive"}`) {
		t.Fatalf("expected anthropic upstream payload to include adaptive thinking config, got %s", gotBody)
	}
	if !strings.Contains(gotBody, `"output_config":{"effort":"high"}`) {
		t.Fatalf("expected anthropic upstream payload to include output_config effort, got %s", gotBody)
	}
}

func TestResponsesRouteMapsReasoningSuffixToAnthropicThinkingWhenEnabled(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true, EnableReasoningEffortSuffix: true, MapReasoningSuffixToAnthropicThinking: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"claude-opus-4-6-high","max_output_tokens":128,"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(gotBody, `"thinking":{"type":"adaptive"}`) {
		t.Fatalf("expected anthropic upstream payload to include adaptive thinking config, got %s", gotBody)
	}
	if !strings.Contains(gotBody, `"output_config":{"effort":"high"}`) {
		t.Fatalf("expected anthropic upstream payload to include output_config effort, got %s", gotBody)
	}
}

func TestChatRouteMapsExplicitReasoningEffortToAnthropicThinkingWhenEnabled(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true, EnableReasoningEffortSuffix: true, MapReasoningSuffixToAnthropicThinking: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"claude-opus-4-6","max_tokens":128,"reasoning_effort":"high","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(gotBody, `"thinking":{"type":"adaptive"}`) {
		t.Fatalf("expected explicit reasoning_effort to map into anthropic adaptive thinking config, got %s", gotBody)
	}
	if !strings.Contains(gotBody, `"output_config":{"effort":"high"}`) {
		t.Fatalf("expected explicit reasoning_effort to map into anthropic output_config effort, got %s", gotBody)
	}
}

func TestResponsesRouteMapsExplicitReasoningEffortToAnthropicThinkingWhenEnabled(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true, EnableReasoningEffortSuffix: true, MapReasoningSuffixToAnthropicThinking: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"claude-opus-4-6","max_output_tokens":128,"reasoning":{"effort":"high"},"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(gotBody, `"thinking":{"type":"adaptive"}`) {
		t.Fatalf("expected explicit responses reasoning.effort to map into anthropic adaptive thinking config, got %s", gotBody)
	}
	if !strings.Contains(gotBody, `"output_config":{"effort":"high"}`) {
		t.Fatalf("expected explicit responses reasoning.effort to map into anthropic output_config effort, got %s", gotBody)
	}
}

func TestChatRouteKeepsUpstreamReasoningVisibleWhenAnthropicMappingDisabled(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_passthrough_1","type":"message","role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true, EnableReasoningEffortSuffix: true, MapReasoningSuffixToAnthropicThinking: false}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"claude-opus-4-6","max_tokens":128,"reasoning_effort":"high","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(gotBody, `"reasoning":{"effort":"high","summary":"auto"}`) {
		t.Fatalf("expected disabled mapping to keep reasoning visible in anthropic upstream payload, got %s", gotBody)
	}
	if strings.Contains(gotBody, `"thinking":`) || strings.Contains(gotBody, `"output_config":`) {
		t.Fatalf("expected disabled mapping not to synthesize anthropic thinking fields, got %s", gotBody)
	}
}

func TestResponsesRouteKeepsUpstreamReasoningVisibleWhenAnthropicMappingDisabled(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_passthrough_2","type":"message","role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true, EnableReasoningEffortSuffix: true, MapReasoningSuffixToAnthropicThinking: false}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"claude-opus-4-6","max_output_tokens":128,"reasoning":{"effort":"high"},"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(gotBody, `"reasoning":{"effort":"high","summary":"auto"}`) {
		t.Fatalf("expected disabled mapping to keep responses reasoning visible in anthropic upstream payload, got %s", gotBody)
	}
	if strings.Contains(gotBody, `"thinking":`) || strings.Contains(gotBody, `"output_config":`) {
		t.Fatalf("expected disabled mapping not to synthesize anthropic thinking fields, got %s", gotBody)
	}
}

func TestChatNonStreamPreservesContextLengthExceededFromUpstreamFailure(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: error\n" +
			"data: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"code\":\"context_length_exceeded\",\"message\":\"Your input exceeds the context window of this model. Please adjust your input and try again.\",\"param\":\"input\"},\"sequence_number\":2}\n\n",
		"event: response.failed\n" +
			"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"Your input exceeds the context window of this model. Please adjust your input and try again.\",\"param\":\"input\",\"type\":\"invalid_request_error\"}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", SupportsChat: true, SupportsResponses: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `context_length_exceeded`) || !strings.Contains(body, `Your input exceeds the context window of this model`) {
		t.Fatalf("expected non-stream chat path to preserve upstream context overflow details, got %s", body)
	}
	if strings.Contains(body, `invalid_upstream_stream`) {
		t.Fatalf("expected non-stream chat path not to degrade into invalid_upstream_stream, got %s", body)
	}
	if !strings.Contains(body, `"type":"proxy_error"`) {
		t.Fatalf("expected proxy-stable error envelope in non-stream chat path, got %s", body)
	}
}

func TestMessagesNonStreamPreservesContextLengthExceededFromUpstreamFailure(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: error\n" +
			"data: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"code\":\"context_length_exceeded\",\"message\":\"Your input exceeds the context window of this model. Please adjust your input and try again.\",\"param\":\"input\"},\"sequence_number\":2}\n\n",
		"event: response.failed\n" +
			"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"Your input exceeds the context window of this model. Please adjust your input and try again.\",\"param\":\"input\",\"type\":\"invalid_request_error\"}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", SupportsAnthropicMessages: true, SupportsResponses: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `context_length_exceeded`) || !strings.Contains(body, `Your input exceeds the context window of this model`) {
		t.Fatalf("expected non-stream messages path to preserve upstream context overflow details, got %s", body)
	}
	if strings.Contains(body, `invalid_upstream_stream`) {
		t.Fatalf("expected non-stream messages path not to degrade into invalid_upstream_stream, got %s", body)
	}
	if !strings.Contains(body, `"type":"error"`) || !strings.Contains(body, `"type":"invalid_request_error"`) {
		t.Fatalf("expected Anthropic context overflow envelope in non-stream messages path, got %s", body)
	}
}

func TestAnthropicRouteForwardsSuccessfulToolResultWithNullErrorToResponsesUpstream(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeResponses, SupportsAnthropicMessages: true, SupportsResponses: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"open apk"}]},{"role":"assistant","content":[{"type":"text","text":"I will open it."},{"type":"tool_use","id":"call_1","name":"mcp__mt_apk_open","input":{"path":"mt://current-apk"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"{\"ok\":true,\"data\":{\"workspaceId\":\"c9m8dlnh\"},\"error\":null}"}]}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal responses upstream payload: %v body=%s", err, gotBody)
	}
	input, _ := payload["input"].([]any)
	var sawFunctionCall bool
	functionOutputCount := 0
	for _, raw := range input {
		item, _ := raw.(map[string]any)
		switch item["type"] {
		case "function_call":
			if item["call_id"] == "call_1" && item["name"] == "mcp__mt_apk_open" {
				sawFunctionCall = true
			}
		case "function_call_output":
			if item["call_id"] == "call_1" && strings.Contains(stringValue(item["output"]), `"error":null`) {
				functionOutputCount++
			}
		}
	}
	if !sawFunctionCall || functionOutputCount != 1 {
		t.Fatalf("expected function_call and function_call_output in responses upstream input, got %s", gotBody)
	}
}

func TestAnthropicRouteMapsThinkingConfigToResponsesReasoningWhenUpstreamIsResponses(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", DefaultProReasoningModeSet: true, DefaultProReasoningMode: false, EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeResponses, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":4096,"thinking":{"type":"enabled","budget_tokens":2048},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(gotBody, `"reasoning":{"effort":"minimal","summary":"auto"}`) {
		t.Fatalf("expected anthropic thinking config to map into responses reasoning payload, got %s", gotBody)
	}
	if strings.Contains(gotBody, `"thinking":`) {
		t.Fatalf("expected responses upstream payload to avoid anthropic thinking field, got %s", gotBody)
	}
}

func TestAnthropicRouteDropsMetadataWhenUpstreamIsResponses(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeResponses, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":4096,"metadata":{"user_id":"abc"},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(gotBody, `"metadata":`) {
		t.Fatalf("expected messages-to-responses upstream payload to drop metadata, got %s", gotBody)
	}
}

func TestAnthropicRouteMapsThinkingConfigToChatReasoningWhenUpstreamIsChat(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":4096,"thinking":{"type":"enabled","budget_tokens":2048},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(gotBody, `"reasoning":{"effort":"minimal","summary":"auto"}`) {
		t.Fatalf("expected anthropic thinking config to map into chat reasoning payload, got %s", gotBody)
	}
	if strings.Contains(gotBody, `"thinking":`) {
		t.Fatalf("expected chat upstream payload to avoid anthropic thinking field, got %s", gotBody)
	}
}

func TestChatRouteDoesNotMapThinkingWhenUpstreamIsNotAnthropic(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsResponses: true, SupportsChat: true, EnableReasoningEffortSuffix: true, MapReasoningSuffixToAnthropicThinking: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"claude-opus-4-6-high","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(gotBody, `"thinking":`) || strings.Contains(gotBody, `"output_config":`) {
		t.Fatalf("expected non-anthropic chat upstream payload to avoid anthropic thinking fields, got %s", gotBody)
	}
}

func TestResponsesRouteDoesNotMapThinkingWhenUpstreamIsNotAnthropic(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsResponses: true, SupportsChat: true, EnableReasoningEffortSuffix: true, MapReasoningSuffixToAnthropicThinking: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"claude-opus-4-6-high","max_output_tokens":128,"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(gotBody, `"thinking":`) || strings.Contains(gotBody, `"output_config":`) {
		t.Fatalf("expected non-anthropic responses upstream payload to avoid anthropic thinking fields, got %s", gotBody)
	}
}

func TestChatRouteUsesAnthropicUpstreamEndpointType(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Header.Get("x-api-key") != "test-key" {
			t.Fatalf("expected x-api-key header, got %q", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Fatalf("expected anthropic-version header, got %q", r.Header.Get("anthropic-version"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello from anthropic upstream"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "anthropic",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                        "anthropic",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			UpstreamEndpointType:      config.UpstreamEndpointTypeAnthropic,
			SupportsResponses:         true,
			SupportsChat:              true,
			SupportsAnthropicMessages: true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/messages" {
		t.Fatalf("expected anthropic upstream path, got %q", gotPath)
	}
	if !strings.Contains(rec.Body.String(), `"object":"chat.completion"`) || !strings.Contains(rec.Body.String(), `"hello from anthropic upstream"`) {
		t.Fatalf("expected chat output normalized from anthropic upstream, got %s", rec.Body.String())
	}
}

func TestChatRouteHoistsInstructionRolesIntoAnthropicSystem(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello from anthropic upstream"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "anthropic",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                        "anthropic",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			UpstreamEndpointType:      config.UpstreamEndpointTypeAnthropic,
			SupportsResponses:         true,
			SupportsChat:              true,
			SupportsAnthropicMessages: true,
			SystemPromptText:          "provider system",
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"system","content":"chat system"},{"role":"developer","content":"chat developer"},{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["system"].(string); got != "provider system\n\nchat system\n\nchat developer" {
		t.Fatalf("expected anthropic system to include provider + chat instruction roles, got %#v body=%s", payload["system"], gotBody)
	}
	messages, _ := payload["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected only non-instruction messages to remain, got %#v", payload["messages"])
	}
	message, _ := messages[0].(map[string]any)
	if role, _ := message["role"].(string); role != "user" {
		t.Fatalf("expected remaining anthropic message role user, got %#v", message)
	}
}

func TestAnthropicRouteUsesChatUpstreamReasoningAsThinking(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"final answer","reasoning_content":"think first"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "anthropic",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                        "anthropic",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			UpstreamEndpointType:      config.UpstreamEndpointTypeChat,
			SupportsResponses:         true,
			SupportsChat:              true,
			SupportsAnthropicMessages: true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"thinking"`) || !strings.Contains(body, `"thinking":"think first"`) {
		t.Fatalf("expected reasoning_content to map into anthropic thinking block, got %s", body)
	}
	if !strings.Contains(body, `"type":"text"`) || !strings.Contains(body, `"text":"final answer"`) {
		t.Fatalf("expected final text to remain present, got %s", body)
	}
}

func TestAnthropicRouteUsesChatUpstreamToolCallsAsToolUse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"search_web","arguments":"{\"query\":\"weather\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "anthropic",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                        "anthropic",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			UpstreamEndpointType:      config.UpstreamEndpointTypeChat,
			SupportsResponses:         true,
			SupportsChat:              true,
			SupportsAnthropicMessages: true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"tool_use"`) || !strings.Contains(body, `"name":"search_web"`) {
		t.Fatalf("expected chat tool_calls to map into anthropic tool_use, got %s", body)
	}
	if !strings.Contains(body, `"query":"weather"`) {
		t.Fatalf("expected tool arguments preserved, got %s", body)
	}
}

func TestChatRouteUsesAnthropicUpstreamToolUseAsToolCalls(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"call_1","name":"search_web","input":{"query":"weather"}}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "anthropic",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                        "anthropic",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			UpstreamEndpointType:      config.UpstreamEndpointTypeAnthropic,
			SupportsResponses:         true,
			SupportsChat:              true,
			SupportsAnthropicMessages: true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"tool_calls"`) || !strings.Contains(body, `"name":"search_web"`) {
		t.Fatalf("expected anthropic tool_use to map into chat tool_calls, got %s", body)
	}
	if !strings.Contains(body, `"arguments":"{\"query\":\"weather\"}"`) {
		t.Fatalf("expected tool arguments preserved in chat output, got %s", body)
	}
}

func TestResponsesRouteRestoresPreviousToolUseForAnthropicFollowUp(t *testing.T) {
	requestCount := 0
	var secondBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		bodyBytes, _ := io.ReadAll(r.Body)
		if requestCount == 2 {
			secondBody = string(bodyBytes)
		}
		w.Header().Set("Content-Type", "application/json")
		if requestCount == 1 {
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"call_1","name":"search_web","input":{"query":"weather"}}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":3}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})

	firstReq := httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstRec := httptest.NewRecorder()
	server.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("expected first status 200, got %d body=%s", firstRec.Code, firstRec.Body.String())
	}
	var firstResp map[string]any
	if err := json.Unmarshal(firstRec.Body.Bytes(), &firstResp); err != nil {
		t.Fatalf("unmarshal first response: %v", err)
	}
	responseID, _ := firstResp["id"].(string)
	if responseID != "msg_1" {
		t.Fatalf("expected first responses output to keep upstream anthropic message id msg_1, got %#v", firstResp["id"])
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","previous_response_id":"`+responseID+`","input":[{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}]}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("expected second status 200, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	if !strings.Contains(secondBody, `"type":"tool_use"`) || !strings.Contains(secondBody, `"id":"call_1"`) {
		t.Fatalf("expected second anthropic request to restore previous assistant tool_use, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"type":"tool_result"`) || !strings.Contains(secondBody, `"tool_use_id":"call_1"`) {
		t.Fatalf("expected second anthropic request to include current tool_result, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"role":"user"`) || !strings.Contains(secondBody, `"hello"`) {
		t.Fatalf("expected second anthropic request to preserve original user question context, got %s", secondBody)
	}
}

func TestResponsesRouteRecoversAnthropicToolUseByCallIDWithoutPreviousResponseID(t *testing.T) {
	requestCount := 0
	var secondBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		bodyBytes, _ := io.ReadAll(r.Body)
		if requestCount == 2 {
			secondBody = string(bodyBytes)
		}
		w.Header().Set("Content-Type", "application/json")
		if requestCount == 1 {
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"call_1","name":"run_in_terminal","input":{"cmd":"pwd"}}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":3}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstRec := httptest.NewRecorder()
	server.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("expected first status 200, got %d body=%s", firstRec.Code, firstRec.Body.String())
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}]}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("expected second status 200, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	if !strings.Contains(secondBody, `"type":"tool_use"`) || !strings.Contains(secondBody, `"id":"call_1"`) || !strings.Contains(secondBody, `"name":"run_in_terminal"`) {
		t.Fatalf("expected second anthropic request to recover previous tool_use by call_id, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"type":"tool_result"`) || !strings.Contains(secondBody, `"tool_use_id":"call_1"`) {
		t.Fatalf("expected second anthropic request to include current tool_result, got %s", secondBody)
	}
	if strings.Contains(secondBody, `"previous_response_id"`) {
		t.Fatalf("expected recovery without previous_response_id passthrough, got %s", secondBody)
	}
}

func TestResponsesRouteRecoversAnthropicThinkingByCallIDWithoutPreviousResponseID(t *testing.T) {
	requestCount := 0
	var secondBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		bodyBytes, _ := io.ReadAll(r.Body)
		if requestCount == 2 {
			secondBody = string(bodyBytes)
		}
		w.Header().Set("Content-Type", "application/json")
		if requestCount == 1 {
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"thinking","thinking":"need tool result","signature":"sig_1"},{"type":"tool_use","id":"call_1","name":"read_file","input":{"filePath":"/tmp/a"}}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":3}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstRec := httptest.NewRecorder()
	server.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("expected first status 200, got %d body=%s", firstRec.Code, firstRec.Body.String())
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"type":"function_call_output","call_id":"call_1","output":"file contents"}]}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("expected second status 200, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	if !strings.Contains(secondBody, `"type":"thinking"`) || !strings.Contains(secondBody, `"thinking":"need tool result"`) || !strings.Contains(secondBody, `"signature":"sig_1"`) {
		t.Fatalf("expected recovered anthropic request to restore trusted thinking and signature, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"type":"tool_use"`) || !strings.Contains(secondBody, `"id":"call_1"`) || !strings.Contains(secondBody, `"name":"read_file"`) {
		t.Fatalf("expected recovered anthropic request to replay previous tool_use, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"type":"tool_result"`) || !strings.Contains(secondBody, `"tool_use_id":"call_1"`) {
		t.Fatalf("expected recovered anthropic request to include current tool_result, got %s", secondBody)
	}
}

func TestResponsesRouteRecoversAnthropicToolUsesByCallIDAfterHistoryStoreRestart(t *testing.T) {
	providersDir := t.TempDir()

	requestCount := 0
	var secondBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		bodyBytes, _ := io.ReadAll(r.Body)
		if requestCount == 2 {
			secondBody = string(bodyBytes)
		}
		w.Header().Set("Content-Type", "application/json")
		if requestCount == 1 {
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"thinking","thinking":"need three tool results","signature":"sig_1"},{"type":"tool_use","id":"call_1","name":"read_file","input":{"filePath":"/tmp/a"}},{"type":"tool_use","id":"call_2","name":"glob","input":{"pattern":"*.go"}},{"type":"tool_use","id":"call_3","name":"grep","input":{"pattern":"TODO"}}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":3}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	serverConfig := config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, ProvidersDir: providersDir, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}}
	server := NewServer(serverConfig)

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstRec := httptest.NewRecorder()
	server.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("expected first status 200, got %d body=%s", firstRec.Code, firstRec.Body.String())
	}

	server = NewServer(serverConfig)

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"type":"function_call_output","call_id":"call_1","output":"file contents"},{"type":"function_call_output","call_id":"call_2","output":"glob output"},{"type":"function_call_output","call_id":"call_3","output":"grep output"}]}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("expected second status 200, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	if !strings.Contains(secondBody, `"type":"thinking"`) || !strings.Contains(secondBody, `"thinking":"need three tool results"`) || !strings.Contains(secondBody, `"signature":"sig_1"`) {
		t.Fatalf("expected restarted recovery to restore persisted thinking and signature, got %s", secondBody)
	}
	for _, want := range []string{`"id":"call_1"`, `"id":"call_2"`, `"id":"call_3"`} {
		if !strings.Contains(secondBody, `"type":"tool_use"`) || !strings.Contains(secondBody, want) {
			t.Fatalf("expected restarted recovery to replay tool_use %s, got %s", want, secondBody)
		}
	}
	for _, want := range []string{`"tool_use_id":"call_1"`, `"tool_use_id":"call_2"`, `"tool_use_id":"call_3"`} {
		if !strings.Contains(secondBody, `"type":"tool_result"`) || !strings.Contains(secondBody, want) {
			t.Fatalf("expected second request to include tool_result %s, got %s", want, secondBody)
		}
	}
	if strings.Contains(secondBody, "hello") {
		t.Fatalf("expected restarted tool-call recovery not to persist full prior prompt context, got %s", secondBody)
	}
}

func TestResponsesRouteRestoresPreviousAnthropicThinkingBlocksOnFollowUp(t *testing.T) {
	requestCount := 0
	var secondBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		bodyBytes, _ := io.ReadAll(r.Body)
		if requestCount == 2 {
			secondBody = string(bodyBytes)
		}
		w.Header().Set("Content-Type", "application/json")
		if requestCount == 1 {
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"thinking","thinking":"internal reasoning","signature":"sig_123"},{"type":"tool_use","id":"call_1","name":"search_web","input":{"query":"weather"}}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":3}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "anthropic",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		V1ModelMap:                  []config.ModelMapEntry{config.NewModelMapEntry("gpt-5", "provider-alias")},
		Providers: []config.ProviderConfig{{
			ID:                        "anthropic",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			UpstreamEndpointType:      config.UpstreamEndpointTypeAnthropic,
			SupportsResponses:         true,
			SupportsChat:              true,
			SupportsAnthropicMessages: true,
			ModelMap:                  []config.ModelMapEntry{config.NewModelMapEntry("provider-alias", "deepseek-v4")},
		}},
	})

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstRec := httptest.NewRecorder()
	server.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("expected first status 200, got %d body=%s", firstRec.Code, firstRec.Body.String())
	}
	var firstResp map[string]any
	if err := json.Unmarshal(firstRec.Body.Bytes(), &firstResp); err != nil {
		t.Fatalf("unmarshal first response: %v", err)
	}
	responseID, _ := firstResp["id"].(string)

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","previous_response_id":"`+responseID+`","input":[{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}]}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("expected second status 200, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	if got := secondRec.Header().Get(headerProxyToUpstreamModel); got != "deepseek-v4" {
		t.Fatalf("expected restored native thinking to use final mapped model, got %q", got)
	}
	if !strings.Contains(secondBody, `"model":"deepseek-v4"`) {
		t.Fatalf("expected restored native thinking payload to use final mapped model, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"type":"thinking"`) || !strings.Contains(secondBody, `"thinking":"internal reasoning"`) {
		t.Fatalf("expected restored anthropic request to keep previous thinking block, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"signature":"sig_123"`) {
		t.Fatalf("expected restored anthropic request to keep thinking signature, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"type":"tool_use"`) || !strings.Contains(secondBody, `"id":"call_1"`) {
		t.Fatalf("expected restored anthropic request to keep previous tool_use, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"type":"tool_result"`) || !strings.Contains(secondBody, `"tool_use_id":"call_1"`) {
		t.Fatalf("expected restored anthropic request to include current tool_result, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"role":"user"`) || !strings.Contains(secondBody, `"hello"`) {
		t.Fatalf("expected restored anthropic request to preserve original user question context, got %s", secondBody)
	}
}

func TestResponsesRouteRehydratesServerIssuedAnthropicThinkingReplay(t *testing.T) {
	requestCount := 0
	var secondBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount == 2 {
			body, _ := io.ReadAll(r.Body)
			secondBody = string(body)
		}
		w.Header().Set("Content-Type", "application/json")
		if requestCount == 1 {
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"thinking","thinking":"internal reasoning","signature":"sig_123"},{"type":"tool_use","id":"call_1","name":"search_web","input":{"query":"weather"}}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":3}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstRec := httptest.NewRecorder()
	server.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("expected first status 200, got %d body=%s", firstRec.Code, firstRec.Body.String())
	}
	var firstResp map[string]any
	if err := json.Unmarshal(firstRec.Body.Bytes(), &firstResp); err != nil {
		t.Fatalf("unmarshal first response: %v", err)
	}
	output, _ := firstResp["output"].([]any)
	if len(output) < 2 {
		t.Fatalf("expected first response output to expose reasoning and function_call items, got %#v", output)
	}
	encodedOutput, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal output: %v", err)
	}
	if !strings.Contains(string(encodedOutput), `"type":"reasoning"`) || !strings.Contains(string(encodedOutput), `"encrypted_content":"sig_123"`) || strings.Contains(string(encodedOutput), `"signature":`) {
		t.Fatalf("expected output to expose opaque reasoning without an internal signature field, got %s", encodedOutput)
	}

	var opaqueReasoning map[string]any
	for _, raw := range output {
		item, _ := raw.(map[string]any)
		if item["type"] == "reasoning" {
			opaqueReasoning = item
			break
		}
	}
	if opaqueReasoning == nil {
		t.Fatalf("expected server-issued opaque reasoning output, got %#v", output)
	}
	opaqueJSON, err := json.Marshal(opaqueReasoning)
	if err != nil {
		t.Fatalf("marshal opaque reasoning: %v", err)
	}
	secondBodyJSON := `{"model":"gpt-5","input":[{"role":"user","content":"hello"},` + string(opaqueJSON) + `,{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}]}`
	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(secondBodyJSON))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("expected server-issued opaque replay to succeed, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	if requestCount != 2 || strings.Contains(secondBody, `"signature":"sig_123"`) {
		t.Fatalf("expected second upstream request to project client thinking without a server-held signature, count=%d body=%s", requestCount, secondBody)
	}
	nestedBody := `{"model":"gpt-5","input":[{"role":"assistant","content":[` + string(opaqueJSON) + `]},{"role":"user","content":"continue"}]}`
	nestedReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(nestedBody))
	nestedReq.Header.Set("Content-Type", "application/json")
	nestedRec := httptest.NewRecorder()
	server.ServeHTTP(nestedRec, nestedReq)
	if nestedRec.Code != http.StatusOK || requestCount != 3 || strings.Contains(secondBody, `"signature":"sig_123"`) {
		t.Fatalf("expected nested client thinking replay without a server-held signature, status=%d calls=%d body=%s", nestedRec.Code, requestCount, secondBody)
	}
	for _, testCase := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "unknown", mutate: func(item map[string]any) { item["encrypted_content"] = "unknown" }},
		{name: "tampered", mutate: func(item map[string]any) { item["thinking"] = "tampered" }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			beforeRequests := requestCount
			item := cloneJSONValueForResponse(opaqueReasoning).(map[string]any)
			testCase.mutate(item)
			encoded, err := json.Marshal(item)
			if err != nil {
				t.Fatalf("marshal %s opaque item: %v", testCase.name, err)
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[`+string(encoded)+`]}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || requestCount != beforeRequests+1 {
				t.Fatalf("expected %s client opaque replay to reach upstream, status=%d calls=%d body=%s", testCase.name, rec.Code, requestCount, rec.Body.String())
			}
		})
	}
	for _, testCase := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "nested unknown", mutate: func(item map[string]any) { item["encrypted_content"] = "unknown" }},
		{name: "nested tampered", mutate: func(item map[string]any) { item["thinking"] = "tampered" }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			beforeRequests := requestCount
			item := cloneJSONValueForResponse(opaqueReasoning).(map[string]any)
			testCase.mutate(item)
			encoded, err := json.Marshal(item)
			if err != nil {
				t.Fatalf("marshal %s opaque item: %v", testCase.name, err)
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"assistant","content":[`+string(encoded)+`]}]}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || requestCount != beforeRequests+1 {
				t.Fatalf("expected %s nested client opaque replay to reach upstream, status=%d calls=%d body=%s", testCase.name, rec.Code, requestCount, rec.Body.String())
			}
		})
	}
}

func TestResponsesRouteProjectsVerifiedOpaqueThinkingAcrossProviderAndModelScope(t *testing.T) {
	for _, endpointType := range []string{config.UpstreamEndpointTypeResponses, config.UpstreamEndpointTypeChat, config.UpstreamEndpointTypeAnthropic} {
		for _, scopeChange := range []string{"provider", "model"} {
			t.Run(scopeChange+"/"+endpointType, func(t *testing.T) {
				var upstreamBody string
				var upstreamHits atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstreamHits.Add(1)
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Fatalf("read projected upstream request: %v", err)
					}
					upstreamBody = string(body)
					w.Header().Set("Content-Type", "application/json")
					if endpointType == config.UpstreamEndpointTypeChat {
						_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"continued"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
						return
					}
					if endpointType == config.UpstreamEndpointTypeResponses {
						_, _ = w.Write([]byte(`{"id":"resp_projected","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"continued"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
						return
					}
					_, _ = w.Write([]byte(`{"id":"msg_projected","type":"message","role":"assistant","content":[{"type":"text","text":"continued"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
				}))
				defer upstream.Close()

				storedProviderID := "source"
				requestPath := "/target/v1/responses"
				storedModel := "model-a"
				requestModel := "model-b"
				providers := []config.ProviderConfig{
					{ID: "source", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "source-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true},
					{ID: "target", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "target-key", UpstreamEndpointType: endpointType, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true},
				}
				if scopeChange == "model" {
					requestPath = "/source/v1/responses"
					providers = []config.ProviderConfig{{
						ID:                        "source",
						Enabled:                   true,
						UpstreamBaseURL:           upstream.URL,
						UpstreamAPIKey:            "source-key",
						UpstreamEndpointType:      endpointType,
						SupportsResponses:         true,
						SupportsChat:              true,
						SupportsAnthropicMessages: true,
						ModelMap: []config.ModelMapEntry{
							config.NewModelMapEntry("client-a", storedModel),
							config.NewModelMapEntry("client-b", requestModel),
						},
					}}
					requestModel = "client-b"
				}
				server := NewServer(config.Config{DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: providers})
				storedProvider, err := server.store.Active().Config.ProviderByID(storedProviderID)
				if err != nil {
					t.Fatalf("lookup stored provider: %v", err)
				}
				nativeScope := responsesHistoryReplayScope(responsesHistoryReplayProvenance{
					ProviderID:                storedProviderID,
					DownstreamEndpoint:        canonicalV1ResponsesPath,
					UpstreamEndpointType:      storedProvider.UpstreamEndpointType,
					NormalizedUpstreamBaseURL: storedProvider.UpstreamBaseURL,
					FinalUpstreamModel:        storedModel,
					CredentialFingerprint:     authorizationFingerprint("Bearer " + storedProvider.UpstreamAPIKey),
					InboundCallerFingerprint:  "anonymous",
				})
				native := map[string]any{
					"type":              "reasoning",
					"encrypted_content": "enc_server",
					"summary": []any{
						map[string]any{"type": "summary_text", "text": "first reasoning"},
						map[string]any{"type": "summary_text", "text": "second reasoning"},
					},
				}
				server.history.SaveWithPortableScope(storedProviderID, "resp_server", []model.CanonicalMessage{{Role: "assistant", ReasoningBlocks: []map[string]any{native}}}, nativeScope, responsesHistoryPortableScope("anonymous"))
				public := responsesOpaqueThinkingPublicBlock(native, 0)
				encoded, err := json.Marshal(public)
				if err != nil {
					t.Fatalf("marshal public opaque thinking: %v", err)
				}
				req := httptest.NewRequest(http.MethodPost, requestPath, strings.NewReader(`{"model":"`+requestModel+`","input":[{"role":"user","content":"continue"},`+string(encoded)+`]}`))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				server.ServeHTTP(rec, req)

				if rec.Code != http.StatusOK || upstreamHits.Load() != 1 {
					t.Fatalf("expected %s/%s portable replay success, status=%d calls=%d body=%s", scopeChange, endpointType, rec.Code, upstreamHits.Load(), rec.Body.String())
				}
				if endpointType == config.UpstreamEndpointTypeResponses {
					if !strings.Contains(upstreamBody, `"encrypted_content":"enc_server"`) {
						t.Fatalf("expected responses upstream to preserve client opaque state, got %s", upstreamBody)
					}
				} else if strings.Contains(upstreamBody, `"encrypted_content"`) || strings.Contains(upstreamBody, `"signature"`) {
					t.Fatalf("expected cross-protocol conversion to strip native state, got %s", upstreamBody)
				}
				if endpointType != config.UpstreamEndpointTypeResponses && (strings.Contains(upstreamBody, "first reasoning") || strings.Contains(upstreamBody, "second reasoning")) {
					t.Fatalf("expected cross-protocol replay to omit unverifiable reasoning history, got %s", upstreamBody)
				}
				if endpointType == config.UpstreamEndpointTypeResponses && (!strings.Contains(upstreamBody, "first reasoning") || !strings.Contains(upstreamBody, "second reasoning")) {
					t.Fatalf("expected responses upstream to receive canonical portable reasoning, got %s", upstreamBody)
				}
			})
		}
	}
}

func TestResponsesRouteRejectsRawOpaqueReasoningFieldsBeforeNonResponsesUpstream(t *testing.T) {
	inputs := []struct {
		name string
		item string
	}{
		{name: "empty signature", item: `{"type":"reasoning","thinking":"internal reasoning","signature":""}`},
		{name: "empty encrypted content", item: `{"type":"reasoning","thinking":"internal reasoning","encrypted_content":""}`},
		{name: "non string encrypted content", item: `{"type":"reasoning","thinking":"internal reasoning","encrypted_content":{"opaque":true}}`},
	}
	for _, endpointType := range []string{config.UpstreamEndpointTypeChat, config.UpstreamEndpointTypeAnthropic} {
		for _, input := range inputs {
			t.Run(endpointType+"/"+input.name, func(t *testing.T) {
				upstreamHits := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstreamHits++
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"id":"unexpected"}`))
				}))
				defer upstream.Close()

				server := NewServer(config.Config{Providers: []config.ProviderConfig{{
					ID:                        "target",
					Enabled:                   true,
					UpstreamBaseURL:           upstream.URL,
					UpstreamAPIKey:            "test-key",
					UpstreamEndpointType:      endpointType,
					SupportsResponses:         true,
					SupportsChat:              true,
					SupportsAnthropicMessages: true,
				}}})

				request := httptest.NewRequest(http.MethodPost, "/target/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"},`+input.item+`]}`))
				request.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()
				server.ServeHTTP(recorder, request)

				if recorder.Code != http.StatusOK || upstreamHits != 1 {
					t.Fatalf("expected client opaque field to reach upstream, status=%d calls=%d body=%s", recorder.Code, upstreamHits, recorder.Body.String())
				}
			})
		}
	}
}

func TestResponsesRouteDropsSummaryOnlyReasoningForMappedChatUpstream(t *testing.T) {
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "chat",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeChat,
			SupportsResponses:    true,
			SupportsChat:         true,
			ModelMap:             []config.ModelMapEntry{config.NewModelMapEntry("client-alias", "deepseek-v4-flash")},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/chat/v1/responses", strings.NewReader(`{
		"model":"client-alias",
		"input":[
			{"role":"user","content":"hello"},
			{"type":"reasoning","id":"rs_1","phase":"analysis","summary":[{"type":"summary_text","text":"need tool output"}]},
			{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"query\":\"weather\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "deepseek-v4-flash" {
		t.Fatalf("expected provider-mapped final model in observability header, got %q", got)
	}
	for _, want := range []string{`"model":"deepseek-v4-flash"`, `"id":"call_1"`, `"name":"lookup"`, `"role":"tool"`, `"tool_call_id":"call_1"`} {
		if !strings.Contains(upstreamBody, want) {
			t.Fatalf("expected mapped chat payload to preserve %s, got %s", want, upstreamBody)
		}
	}
	if strings.Contains(upstreamBody, "need tool output") || strings.Contains(upstreamBody, `"reasoning_content"`) {
		t.Fatalf("expected mapped chat payload to omit unverifiable reasoning history, got %s", upstreamBody)
	}
}

func TestResponsesRouteTransformsPlaintextPersistedHistoryForMappedNonResponsesUpstreams(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		mappingScope string
		endpointType string
	}{
		{name: "root map to chat", mappingScope: "root", endpointType: config.UpstreamEndpointTypeChat},
		{name: "provider map to chat", mappingScope: "provider", endpointType: config.UpstreamEndpointTypeChat},
		{name: "root map to anthropic", mappingScope: "root", endpointType: config.UpstreamEndpointTypeAnthropic},
		{name: "provider map to anthropic", mappingScope: "provider", endpointType: config.UpstreamEndpointTypeAnthropic},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			const clientModel = "client-model"
			const rootTargetModel = "root-target-model"
			const providerTargetModel = "provider-target-model"
			targetModel := rootTargetModel
			if testCase.mappingScope == "provider" {
				targetModel = providerTargetModel
			}

			var upstreamBody string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("read upstream body: %v", err)
				}
				upstreamBody = string(body)
				w.Header().Set("Content-Type", "application/json")
				if testCase.endpointType == config.UpstreamEndpointTypeChat {
					_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
					return
				}
				_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"` + targetModel + `","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
			}))
			defer upstream.Close()

			provider := config.ProviderConfig{
				ID:                        "provider",
				Enabled:                   true,
				UpstreamBaseURL:           upstream.URL,
				UpstreamAPIKey:            "test-key",
				UpstreamEndpointType:      testCase.endpointType,
				SupportsResponses:         true,
				SupportsChat:              true,
				SupportsAnthropicMessages: true,
			}
			cfg := config.Config{
				DefaultProvider:             provider.ID,
				DefaultProReasoningModeSet:  true,
				DefaultProReasoningMode:     false,
				EnableLegacyV1Routes:        true,
				DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
				Providers:                   []config.ProviderConfig{provider},
			}
			if testCase.mappingScope == "root" {
				cfg.V1ModelMap = []config.ModelMapEntry{config.NewModelMapEntry(clientModel, targetModel)}
			} else {
				cfg.Providers[0].ModelMap = []config.ModelMapEntry{config.NewModelMapEntry(clientModel, targetModel)}
			}
			server := NewServer(cfg)

			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
				"model":"`+clientModel+`",
				"input":[
					{"role":"user","content":"find the answer"},
					{"type":"reasoning","id":"reasoning_1","thinking":"need the tool result"},
					{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"query\":\"weather\"}"},
					{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}
				]
			}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get(headerProxyToUpstreamModel); got != targetModel {
				t.Fatalf("expected final mapped model %q, got %q", targetModel, got)
			}
			if !strings.Contains(upstreamBody, `"model":"`+targetModel+`"`) {
				t.Fatalf("expected upstream payload model %q, got %s", targetModel, upstreamBody)
			}
			if testCase.endpointType == config.UpstreamEndpointTypeChat {
				for _, want := range []string{`"reasoning_content":"need the tool result"`, `"id":"call_1"`, `"name":"lookup"`, `"role":"tool"`, `"tool_call_id":"call_1"`} {
					if !strings.Contains(upstreamBody, want) {
						t.Fatalf("expected chat history payload to preserve %s, got %s", want, upstreamBody)
					}
				}
				return
			}
			for _, want := range []string{`"type":"thinking"`, `"thinking":"need the tool result"`, `"type":"tool_use"`, `"id":"call_1"`, `"type":"tool_result"`, `"tool_use_id":"call_1"`} {
				if !strings.Contains(upstreamBody, want) {
					t.Fatalf("expected anthropic history payload to preserve %s, got %s", want, upstreamBody)
				}
			}
			if strings.Contains(upstreamBody, `"signature":`) {
				t.Fatalf("expected plaintext thinking replay not to invent a signature, got %s", upstreamBody)
			}
		})
	}
}

func TestResponsesRouteFiltersSyntheticProxyReasoningBeforeAnthropicReplay(t *testing.T) {
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		upstreamBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyProxyBuffer, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true, EnableReasoningEffortSuffix: true, MapReasoningSuffixToAnthropicThinking: true}}})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"reasoning":{"effort":"high","summary":"auto"},
		"input":[
			{"role":"user","content":"hello"},
			{"type":"reasoning","id":"rs_proxy","summary":[{"type":"summary_text","text":"**推理中**\n\n代理层占位，以兼容不同上游情况，便于客户端记录推理时长"}]},
			{"type":"function_call","call_id":"call_1","name":"search_web","arguments":"{\"query\":\"weather\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	if strings.Contains(upstreamBody, "代理层占位") || strings.Contains(upstreamBody, `"id":"rs_proxy"`) {
		t.Fatalf("expected synthetic rs_proxy reasoning to stay out of anthropic upstream payload, got %s", upstreamBody)
	}
	if strings.Contains(upstreamBody, `"type":"thinking"`) {
		t.Fatalf("expected synthetic rs_proxy reasoning not to become anthropic thinking content, got %s", upstreamBody)
	}
	if strings.Contains(upstreamBody, `"thinking":`) {
		t.Fatalf("expected synthetic-only replay not to enable anthropic thinking mode, got %s", upstreamBody)
	}
	if strings.Contains(upstreamBody, `"effort":"high"`) || strings.Contains(upstreamBody, `"summary":"auto"`) {
		t.Fatalf("expected OpenAI-style reasoning controls to stay out of anthropic replay without real thinking history, got %s", upstreamBody)
	}
	if !strings.Contains(upstreamBody, `"type":"tool_use"`) || !strings.Contains(upstreamBody, `"id":"call_1"`) || !strings.Contains(upstreamBody, `"name":"search_web"`) {
		t.Fatalf("expected synthetic reasoning to be filtered without downgrading real function_call history, got %s", upstreamBody)
	}
	if !strings.Contains(upstreamBody, `"type":"tool_result"`) || !strings.Contains(upstreamBody, `"tool_use_id":"call_1"`) || !strings.Contains(upstreamBody, `\"ok\":true`) {
		t.Fatalf("expected synthetic reasoning to be filtered without downgrading real function_call_output history, got %s", upstreamBody)
	}
}

func TestResponsesRouteKeepsClientProvidedFunctionHistoryForAnthropicDespiteSyntheticReasoning(t *testing.T) {
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		upstreamBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyProxyBuffer, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true, EnableReasoningEffortSuffix: true, MapReasoningSuffixToAnthropicThinking: true}}})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"reasoning":{"effort":"high","summary":"auto"},
		"input":[
			{"role":"user","content":"hello"},
			{"type":"reasoning","id":"rs_proxy","summary":[{"type":"summary_text","text":"**推理中**\n\n代理层占位，以兼容不同上游情况，便于客户端记录推理时长"}]},
			{"type":"function_call","call_id":"call_1","name":"search_web","arguments":"{\"query\":\"weather\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	if strings.Contains(upstreamBody, "代理层占位") || strings.Contains(upstreamBody, `"id":"rs_proxy"`) {
		t.Fatalf("expected synthetic rs_proxy reasoning to stay out of anthropic upstream payload, got %s", upstreamBody)
	}
	if !strings.Contains(upstreamBody, `"type":"tool_use"`) || !strings.Contains(upstreamBody, `"id":"call_1"`) || !strings.Contains(upstreamBody, `"name":"search_web"`) {
		t.Fatalf("expected client-provided function_call to stay structured as anthropic tool_use, got %s", upstreamBody)
	}
	if !strings.Contains(upstreamBody, `"type":"tool_result"`) || !strings.Contains(upstreamBody, `"tool_use_id":"call_1"`) || !strings.Contains(upstreamBody, `\"ok\":true`) {
		t.Fatalf("expected client-provided function_call_output to stay structured as anthropic tool_result, got %s", upstreamBody)
	}
	if strings.Contains(upstreamBody, "工具调用 search_web") {
		t.Fatalf("expected real function history not to be downgraded into plain text, got %s", upstreamBody)
	}
}

func TestResponsesRouteRestoresAnthropicThinkingBlocksAcrossMultipleToolFollowUps(t *testing.T) {
	requestCount := 0
	var secondBody string
	var thirdBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		bodyBytes, _ := io.ReadAll(r.Body)
		bodyText := string(bodyBytes)
		switch requestCount {
		case 1:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"thinking","thinking":"step one reasoning","signature":"sig_1"},{"type":"tool_use","id":"call_1","name":"search_web","input":{"query":"one"}}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":3}}`))
		case 2:
			secondBody = bodyText
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"thinking","thinking":"step two reasoning","signature":"sig_2"},{"type":"tool_use","id":"call_2","name":"search_web","input":{"query":"two"}}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":3}}`))
		default:
			thirdBody = bodyText
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"msg_3","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
		}
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstRec := httptest.NewRecorder()
	server.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("expected first status 200, got %d body=%s", firstRec.Code, firstRec.Body.String())
	}
	var firstResp map[string]any
	if err := json.Unmarshal(firstRec.Body.Bytes(), &firstResp); err != nil {
		t.Fatalf("unmarshal first response: %v", err)
	}
	firstResponseID, _ := firstResp["id"].(string)

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","previous_response_id":"`+firstResponseID+`","input":[{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}]}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("expected second status 200, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	var secondResp map[string]any
	if err := json.Unmarshal(secondRec.Body.Bytes(), &secondResp); err != nil {
		t.Fatalf("unmarshal second response: %v", err)
	}
	secondResponseID, _ := secondResp["id"].(string)

	thirdReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","previous_response_id":"`+secondResponseID+`","input":[{"type":"function_call_output","call_id":"call_2","output":"{\"ok\":true}"}]}`))
	thirdReq.Header.Set("Content-Type", "application/json")
	thirdRec := httptest.NewRecorder()
	server.ServeHTTP(thirdRec, thirdReq)
	if thirdRec.Code != http.StatusOK {
		t.Fatalf("expected third status 200, got %d body=%s", thirdRec.Code, thirdRec.Body.String())
	}

	if !strings.Contains(secondBody, `"thinking":"step one reasoning"`) || !strings.Contains(secondBody, `"signature":"sig_1"`) {
		t.Fatalf("expected second request to restore first anthropic thinking block, got %s", secondBody)
	}
	if !strings.Contains(thirdBody, `"thinking":"step one reasoning"`) || !strings.Contains(thirdBody, `"signature":"sig_1"`) {
		t.Fatalf("expected third request to keep first anthropic thinking block across multiple tool follow-ups, got %s", thirdBody)
	}
	if !strings.Contains(thirdBody, `"thinking":"step two reasoning"`) || !strings.Contains(thirdBody, `"signature":"sig_2"`) {
		t.Fatalf("expected third request to keep second anthropic thinking block across multiple tool follow-ups, got %s", thirdBody)
	}
	if !strings.Contains(thirdBody, `"id":"call_1"`) || !strings.Contains(thirdBody, `"id":"call_2"`) {
		t.Fatalf("expected third request to keep both previous tool_use blocks, got %s", thirdBody)
	}
	if strings.Count(thirdBody, `"type":"tool_result"`) != 2 {
		t.Fatalf("expected third request to contain both prior tool results in history, got %s", thirdBody)
	}
}

func TestResponsesRouteRestoresPreviousConversationForAnthropicNewUserTurn(t *testing.T) {
	requestCount := 0
	var secondBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		bodyBytes, _ := io.ReadAll(r.Body)
		if requestCount == 2 {
			secondBody = string(bodyBytes)
		}
		w.Header().Set("Content-Type", "application/json")
		if requestCount == 1 {
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"first answer"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"second answer"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstRec := httptest.NewRecorder()
	server.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("expected first status 200, got %d body=%s", firstRec.Code, firstRec.Body.String())
	}
	var firstResp map[string]any
	if err := json.Unmarshal(firstRec.Body.Bytes(), &firstResp); err != nil {
		t.Fatalf("unmarshal first response: %v", err)
	}
	responseID, _ := firstResp["id"].(string)

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","previous_response_id":"`+responseID+`","input":"follow up"}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("expected second status 200, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	if !strings.Contains(secondBody, `"hello"`) {
		t.Fatalf("expected restored anthropic request to include original user turn, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"first answer"`) {
		t.Fatalf("expected restored anthropic request to include previous assistant turn, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"follow up"`) {
		t.Fatalf("expected restored anthropic request to include new user turn, got %s", secondBody)
	}
}

func TestResponsesRouteUsesResponsesUpstreamForFunctionCallFollowUp(t *testing.T) {
	requestCount := 0
	var secondBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		bodyBytes, _ := io.ReadAll(r.Body)
		if requestCount == 2 {
			secondBody = string(bodyBytes)
		}
		w.Header().Set("Content-Type", "application/json")
		if requestCount == 1 {
			_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","output":[{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"search_web","arguments":"{\"query\":\"weather\"}"}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"resp_2","object":"response","output":[{"id":"msg_2","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "resp", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "resp", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeResponses, SupportsResponses: true, SupportsChat: true}}})

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello","tools":[{"type":"function","name":"search_web","description":"Search","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}]}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstRec := httptest.NewRecorder()
	server.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("expected first status 200, got %d body=%s", firstRec.Code, firstRec.Body.String())
	}
	var firstResp map[string]any
	if err := json.Unmarshal(firstRec.Body.Bytes(), &firstResp); err != nil {
		t.Fatalf("unmarshal first response: %v", err)
	}
	responseID, _ := firstResp["id"].(string)
	if responseID != "resp_1" {
		t.Fatalf("expected first responses output to keep upstream response id resp_1, got %#v", firstResp["id"])
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","previous_response_id":"`+responseID+`","input":[{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}]}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("expected second status 200, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	if !strings.Contains(secondBody, `"previous_response_id":"resp_1"`) {
		t.Fatalf("expected responses upstream follow-up to preserve previous_response_id, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"type":"function_call_output"`) || !strings.Contains(secondBody, `"call_id":"call_1"`) {
		t.Fatalf("expected responses upstream follow-up to preserve function_call_output, got %s", secondBody)
	}
}

func TestResponsesRouteUsesItemReferenceForOpenCodeMasqueradeFunctionCallFollowUp(t *testing.T) {
	requestCount := 0
	var secondBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		bodyBytes, _ := io.ReadAll(r.Body)
		if requestCount == 2 {
			secondBody = string(bodyBytes)
		}
		w.Header().Set("Content-Type", "application/json")
		if requestCount == 1 {
			_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","output":[{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"search_web","arguments":"{\"query\":\"weather\"}"}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"resp_2","object":"response","output":[{"id":"msg_2","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "resp", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "resp", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeResponses, MasqueradeTarget: config.MasqueradeTargetOpenCode, SupportsResponses: true, SupportsChat: true, SystemPromptText: "provider prompt", SystemPromptPosition: config.SystemPromptPositionAppend}}})

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello","tools":[{"type":"function","name":"search_web","description":"Search","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}]}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstRec := httptest.NewRecorder()
	server.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("expected first status 200, got %d body=%s", firstRec.Code, firstRec.Body.String())
	}
	var firstResp map[string]any
	if err := json.Unmarshal(firstRec.Body.Bytes(), &firstResp); err != nil {
		t.Fatalf("unmarshal first response: %v", err)
	}
	responseID, _ := firstResp["id"].(string)

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","previous_response_id":"`+responseID+`","instructions":"follow-up instructions","input":[{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}]}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("expected second status 200, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	if strings.Contains(secondBody, `"previous_response_id"`) {
		t.Fatalf("expected opencode masquerade follow-up to drop previous_response_id, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"type":"item_reference"`) || !strings.Contains(secondBody, `"id":"fc_1"`) {
		t.Fatalf("expected opencode masquerade follow-up to inject item_reference fc_1, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"type":"function_call_output"`) || !strings.Contains(secondBody, `"call_id":"call_1"`) {
		t.Fatalf("expected opencode masquerade follow-up to preserve function_call_output, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"instructions":"follow-up instructions\n\nprovider prompt"`) {
		t.Fatalf("expected provider prompt to preserve follow-up item-reference ordering, got %s", secondBody)
	}
}

func TestResponsesRouteUsesResponsesUpstreamForComplexFunctionCallFollowUp(t *testing.T) {
	requestCount := 0
	var secondBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		bodyBytes, _ := io.ReadAll(r.Body)
		if requestCount == 2 {
			secondBody = string(bodyBytes)
		}
		w.Header().Set("Content-Type", "application/json")
		if requestCount == 1 {
			_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","output":[{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"search_web","arguments":"{\"query\":\"weather\"}"}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"resp_2","object":"response","output":[{"id":"msg_2","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "resp", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "resp", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeResponses, SupportsResponses: true, SupportsChat: true}}})

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello","tools":[{"type":"function","name":"search_web","description":"Search","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}]}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstRec := httptest.NewRecorder()
	server.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("expected first status 200, got %d body=%s", firstRec.Code, firstRec.Body.String())
	}
	var firstResp map[string]any
	if err := json.Unmarshal(firstRec.Body.Bytes(), &firstResp); err != nil {
		t.Fatalf("unmarshal first response: %v", err)
	}
	responseID, _ := firstResp["id"].(string)

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","previous_response_id":"`+responseID+`","parallel_tool_calls":true,"metadata":{"trace_id":"trace_123"},"input":[{"role":"user","content":"hello"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"search_web","arguments":"{\"query\":\"weather\"}"}}]},{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}]}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("expected second status 200, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	if !strings.Contains(secondBody, `"previous_response_id":"resp_1"`) {
		t.Fatalf("expected complex responses follow-up to preserve previous_response_id, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"parallel_tool_calls":true`) || !strings.Contains(secondBody, `"trace_id":"trace_123"`) {
		t.Fatalf("expected complex responses follow-up to preserve top-level stateful fields, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"type":"function_call"`) || !strings.Contains(secondBody, `"name":"search_web"`) || !strings.Contains(secondBody, `"arguments":"{\"query\":\"weather\"}"`) {
		t.Fatalf("expected complex responses follow-up to preserve assistant tool_call history as responses function_call item, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"type":"function_call_output"`) || !strings.Contains(secondBody, `"call_id":"call_1"`) {
		t.Fatalf("expected complex responses follow-up to preserve function_call_output, got %s", secondBody)
	}
}

func TestResponsesStreamRouteRestoresPreviousToolUseForAnthropicFollowUp(t *testing.T) {
	var secondBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		bodyText := string(bodyBytes)
		if strings.Contains(bodyText, `"tool_result"`) {
			secondBody = string(bodyBytes)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: message_start\n" +
			"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
			"event: content_block_start\n" +
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"search_web\"}}\n\n" +
			"event: content_block_delta\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"query\\\":\\\"weather\\\"}\"}}\n\n" +
			"event: message_delta\n" +
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"input_tokens\":2,\"output_tokens\":3}}\n\n" +
			"event: message_stop\n" +
			"data: {\"type\":\"message_stop\"}\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","stream":true,"input":"hello"}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstRec := httptest.NewRecorder()
	server.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("expected first status 200, got %d body=%s", firstRec.Code, firstRec.Body.String())
	}
	responseID := firstResponseIDFromStreamBody(t, firstRec.Body.String())
	if !strings.Contains(firstRec.Body.String(), `"call_id":"call_1"`) {
		t.Fatalf("expected first stream to include tool call call_1, got %s", firstRec.Body.String())
	}
	if responseID != "msg_1" {
		t.Fatalf("expected first stream to expose real upstream anthropic id msg_1, got %q body=%s", responseID, firstRec.Body.String())
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","previous_response_id":"`+responseID+`","input":[{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}]}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("expected second status 200, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	if !strings.Contains(secondBody, `"type":"tool_use"`) || !strings.Contains(secondBody, `"id":"call_1"`) {
		t.Fatalf("expected second anthropic request to restore previous streamed tool_use, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"type":"tool_result"`) || !strings.Contains(secondBody, `"tool_use_id":"call_1"`) {
		t.Fatalf("expected second anthropic request to include tool_result after streamed first round, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"role":"user"`) || !strings.Contains(secondBody, `"hello"`) {
		t.Fatalf("expected second anthropic streamed follow-up to preserve original user question context, got %s", secondBody)
	}
}

func TestResponsesStreamRouteRestoresPreviousAnthropicThinkingBlocksOnFollowUp(t *testing.T) {
	var secondBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		bodyText := string(bodyBytes)
		if strings.Contains(bodyText, `"tool_result"`) {
			secondBody = bodyText
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: message_start\n" +
			"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
			"event: content_block_start\n" +
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"sig_123\"}}\n\n" +
			"event: content_block_delta\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"internal reasoning\"}}\n\n" +
			"event: content_block_start\n" +
			"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"search_web\"}}\n\n" +
			"event: content_block_delta\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"query\\\":\\\"weather\\\"}\"}}\n\n" +
			"event: message_delta\n" +
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"input_tokens\":2,\"output_tokens\":3}}\n\n" +
			"event: message_stop\n" +
			"data: {\"type\":\"message_stop\"}\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","stream":true,"input":"hello"}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstRec := httptest.NewRecorder()
	server.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("expected first status 200, got %d body=%s", firstRec.Code, firstRec.Body.String())
	}
	responseID := firstResponseIDFromStreamBody(t, firstRec.Body.String())
	if responseID != "msg_1" {
		t.Fatalf("expected first stream to expose real upstream id msg_1, got %q body=%s", responseID, firstRec.Body.String())
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","previous_response_id":"`+responseID+`","input":[{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}]}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("expected second status 200, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	if !strings.Contains(secondBody, `"type":"thinking"`) || !strings.Contains(secondBody, `"thinking":"internal reasoning"`) {
		t.Fatalf("expected streamed follow-up to restore previous thinking block, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"signature":"sig_123"`) {
		t.Fatalf("expected streamed follow-up to restore thinking signature, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"type":"tool_use"`) || !strings.Contains(secondBody, `"id":"call_1"`) {
		t.Fatalf("expected streamed follow-up to restore previous tool_use, got %s", secondBody)
	}
	if !strings.Contains(secondBody, `"type":"tool_result"`) || !strings.Contains(secondBody, `"tool_use_id":"call_1"`) {
		t.Fatalf("expected streamed follow-up to include current tool_result, got %s", secondBody)
	}
}

func TestResponsesRouteSkipsPreviousHistoryRestoreWhenClientAlreadySendsHistory(t *testing.T) {
	requestCount := 0
	var secondBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		bodyBytes, _ := io.ReadAll(r.Body)
		if requestCount == 2 {
			secondBody = string(bodyBytes)
		}
		w.Header().Set("Content-Type", "application/json")
		if requestCount == 1 {
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"call_1","name":"search_web","input":{"query":"weather"}}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":3}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstRec := httptest.NewRecorder()
	server.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("expected first status 200, got %d body=%s", firstRec.Code, firstRec.Body.String())
	}
	var firstResp map[string]any
	if err := json.Unmarshal(firstRec.Body.Bytes(), &firstResp); err != nil {
		t.Fatalf("unmarshal first response: %v", err)
	}
	responseID, _ := firstResp["id"].(string)
	if responseID == "" {
		t.Fatalf("expected first responses output to include id, got %#v", firstResp)
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","previous_response_id":"`+responseID+`","input":[{"role":"user","content":"hello"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"search_web","arguments":"{\"query\":\"weather\"}"}}]},{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}]}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("expected second status 200, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	if strings.Count(secondBody, `"type":"tool_use"`) != 1 {
		t.Fatalf("expected client-provided history to avoid duplicate restored tool_use, got %s", secondBody)
	}
	if strings.Count(secondBody, `"role":"user"`) != 2 {
		t.Fatalf("expected one original user plus current tool_result wrapper, got %s", secondBody)
	}
}

func TestResponsesRouteDedupesDuplicateToolResultsFromClientFollowUp(t *testing.T) {
	requestCount := 0
	var secondBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		bodyBytes, _ := io.ReadAll(r.Body)
		if requestCount == 2 {
			secondBody = string(bodyBytes)
		}
		w.Header().Set("Content-Type", "application/json")
		if requestCount == 1 {
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"call_1","name":"search_web","input":{"query":"weather"}}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":3}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstRec := httptest.NewRecorder()
	server.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("expected first status 200, got %d body=%s", firstRec.Code, firstRec.Body.String())
	}
	var firstResp map[string]any
	if err := json.Unmarshal(firstRec.Body.Bytes(), &firstResp); err != nil {
		t.Fatalf("unmarshal first response: %v", err)
	}
	responseID, _ := firstResp["id"].(string)

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","previous_response_id":"`+responseID+`","input":[{"role":"user","content":"hello"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"search_web","arguments":"{\"query\":\"weather\"}"}}]},{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"},{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}]}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("expected second status 200, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	if strings.Count(secondBody, `"tool_use_id":"call_1"`) != 1 {
		t.Fatalf("expected duplicate tool_result to be deduped before upstream request, got %s", secondBody)
	}
}

func TestResponsesRouteHoistsInstructionsAndDeveloperIntoAnthropicSystem(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello from anthropic upstream"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true, SystemPromptText: "provider system"}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","instructions":"response instructions","input":[{"role":"developer","content":[{"type":"input_text","text":"response developer"}]},{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["system"].(string); got != "provider system\n\nresponse developer\n\nresponse instructions" {
		t.Fatalf("expected anthropic system to include instructions + developer + provider prompt, got %#v body=%s", payload["system"], gotBody)
	}
	messages, _ := payload["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected only user message to remain after hoisting instruction roles, got %#v", payload["messages"])
	}
	message, _ := messages[0].(map[string]any)
	if role, _ := message["role"].(string); role != "user" {
		t.Fatalf("expected remaining anthropic message role user, got %#v", message)
	}
}

func TestResponsesRoutePreservesChatFinishReasonLength(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"partial"},"finish_reason":"length"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsResponses: true, SupportsChat: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"status":"incomplete"`) || !strings.Contains(body, `"reason":"length"`) {
		t.Fatalf("expected responses output to preserve length reason, got %s", body)
	}
}

func TestChatRoutePreservesAnthropicStopReasonMaxTokens(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"partial"}],"stop_reason":"max_tokens","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"finish_reason":"length"`) {
		t.Fatalf("expected chat output to map anthropic max_tokens into length, got %s", rec.Body.String())
	}
}

func TestAnthropicRouteDropsOrphanAssistantToolUseBeforeAnthropicUpstream(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsAnthropicMessages: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"search"}]},{"role":"assistant","content":[{"type":"tool_use","id":"call_01_D8v6uYpgei39lTvLwbs79207","name":"search_web","input":{"query":"weather"}}]},{"role":"assistant","content":[{"type":"text","text":"继续回答"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(gotBody, `"type":"tool_use"`) || strings.Contains(gotBody, `call_01_D8v6uYpgei39lTvLwbs79207`) {
		t.Fatalf("expected orphan tool_use to be dropped before anthropic upstream, got %s", gotBody)
	}
	if !strings.Contains(gotBody, `"继续回答"`) {
		t.Fatalf("expected assistant text after orphan tool_use to remain, got %s", gotBody)
	}
}

func TestChatRouteForwardsToolResultToChatUpstream(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsChat: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"search"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"search_web","arguments":"{\"query\":\"weather\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"{\"ok\":true}"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(gotBody, `"role":"tool"`) || !strings.Contains(gotBody, `"tool_call_id":"call_1"`) || !strings.Contains(gotBody, `{\"ok\":true}`) {
		t.Fatalf("expected chat tool result to reach chat upstream as role=tool message, got %s", gotBody)
	}
}

func TestAnthropicRouteForwardsToolResultToAnthropicUpstream(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsAnthropicMessages: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"search"}]},{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"search_web","input":{"query":"weather"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"{\"ok\":true}"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(gotBody, `"type":"tool_result"`) || !strings.Contains(gotBody, `"tool_use_id":"call_1"`) || !strings.Contains(gotBody, `{\"ok\":true}`) {
		t.Fatalf("expected anthropic tool_result to reach anthropic upstream, got %s", gotBody)
	}
}

func TestAnthropicRouteRejectsAudioWhenUpstreamIsAnthropic(t *testing.T) {
	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: "https://example.com", UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true, SupportsAnthropicMessages: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"input_audio","input_audio":{"data":"YWJj","format":"mp3"}}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `unsupported anthropic content type: input_audio`) {
		t.Fatalf("expected explicit anthropic audio rejection, got %s", rec.Body.String())
	}
}

func TestResponsesRouteMapsChatRefusal(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":null,"refusal":"nope"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()
	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsResponses: true, SupportsChat: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"type":"refusal"`) || !strings.Contains(rec.Body.String(), `"refusal":"nope"`) {
		t.Fatalf("expected chat refusal to map into responses payload, got %s", rec.Body.String())
	}
}

func TestChatUpstreamToResponsesDownstreamUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		_, _ = w.Write([]byte("data: {\"id\":\"chat-123\",\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"))
		flusher.Flush()

		_, _ = w.Write([]byte("data: {\"id\":\"chat-123\",\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n"))
		flusher.Flush()

		_, _ = w.Write([]byte("data: {\"id\":\"chat-123\",\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15},\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n"))
		flusher.Flush()

		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeChat,
			SupportsResponses:    true,
			SupportsChat:         true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	body := rec.Body.String()
	t.Logf("Response body:\n%s", body)
	if count := strings.Count(body, `event: response.created`); count != 1 {
		t.Fatalf("expected exactly one response.created event, got count=%d body=\n%s", count, body)
	}
	if responseID := firstResponseIDFromStreamBody(t, body); responseID != "chat-123" {
		t.Fatalf("expected response.created to use upstream chat id chat-123, got %q body=\n%s", responseID, body)
	}

	if !strings.Contains(body, "input_tokens") {
		t.Errorf("expected response to contain input_tokens, got:\n%s", body)
	}
	if !strings.Contains(body, "output_tokens") {
		t.Errorf("expected response to contain output_tokens, got:\n%s", body)
	}
	if !strings.Contains(body, "total_tokens") {
		t.Errorf("expected response to contain total_tokens, got:\n%s", body)
	}
	if !strings.Contains(body, "event: response.completed") {
		t.Errorf("expected response to contain response.completed event, got:\n%s", body)
	}
}
