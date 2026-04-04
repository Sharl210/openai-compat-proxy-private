package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/testutil"
)

func TestResponsesRouteExposesDirectionalObservabilityHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                          "openai",
			Enabled:                     true,
			UpstreamBaseURL:             upstream.URL,
			UpstreamAPIKey:              "test-key",
			UpstreamEndpointType:        config.UpstreamEndpointTypeChat,
			SupportsResponses:           true,
			SupportsChat:                true,
			EnableReasoningEffortSuffix: true,
			ModelMap:                    []config.ModelMapEntry{config.NewModelMapEntry("gpt-5", "claude-sonnet-4-5")},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5-high","input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	assertDirectionalObservabilityHeaders(t, rec, directionalHeaderExpectation{
		clientModel:           "gpt-5-high",
		clientReasoningEffort: "high",
		proxyUpstreamModel:    "claude-sonnet-4-5",
		proxyReasoningPayload: map[string]any{"reasoning": map[string]any{"effort": "high", "summary": "auto"}},
	})
	if !strings.Contains(rec.Body.String(), `"output_text"`) {
		t.Fatalf("expected responses payload, got %s", rec.Body.String())
	}
}

func TestChatStreamExposesDirectionalObservabilityHeaders(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                          "openai",
			Enabled:                     true,
			UpstreamBaseURL:             upstream.URL,
			UpstreamAPIKey:              "test-key",
			SupportsChat:                true,
			SupportsResponses:           true,
			EnableReasoningEffortSuffix: true,
			ModelMap:                    []config.ModelMapEntry{config.NewModelMapEntry("gpt-5", "gpt-5-mini")},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5-high","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	assertDirectionalObservabilityHeaders(t, rec, directionalHeaderExpectation{
		clientModel:           "gpt-5-high",
		clientReasoningEffort: "high",
		proxyUpstreamModel:    "gpt-5-mini",
		proxyReasoningPayload: map[string]any{"reasoning": map[string]any{"effort": "high", "summary": "auto"}},
	})
	if !strings.Contains(rec.Body.String(), `"object":"chat.completion.chunk"`) {
		t.Fatalf("expected chat stream body, got %s", rec.Body.String())
	}
}

func TestMessagesRouteExposesDirectionalObservabilityHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "anthropic",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                                    "anthropic",
			Enabled:                               true,
			UpstreamBaseURL:                       upstream.URL,
			UpstreamAPIKey:                        "test-key",
			UpstreamEndpointType:                  config.UpstreamEndpointTypeAnthropic,
			SupportsAnthropicMessages:             true,
			MapReasoningSuffixToAnthropicThinking: true,
			EnableReasoningEffortSuffix:           true,
			ModelMap:                              []config.ModelMapEntry{config.NewModelMapEntry("gpt-5", "claude-sonnet-4-5")},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5-high","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	assertDirectionalObservabilityHeaders(t, rec, directionalHeaderExpectation{
		clientModel:           "gpt-5-high",
		clientReasoningEffort: "high",
		proxyUpstreamModel:    "claude-sonnet-4-5",
		proxyReasoningPayload: map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": float64(128)}},
	})
}

type directionalHeaderExpectation struct {
	clientModel           string
	clientReasoningEffort string
	proxyUpstreamModel    string
	proxyReasoningPayload map[string]any
}

func assertDirectionalObservabilityHeaders(t *testing.T, rec *httptest.ResponseRecorder, expected directionalHeaderExpectation) {
	t.Helper()
	if got := rec.Header().Get(headerClientToProxyModel); got != expected.clientModel {
		t.Fatalf("expected %s %q, got %q", headerClientToProxyModel, expected.clientModel, got)
	}
	if got := rec.Header().Get(headerClientToProxyReasoningEffort); got != expected.clientReasoningEffort {
		t.Fatalf("expected %s %q, got %q", headerClientToProxyReasoningEffort, expected.clientReasoningEffort, got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != expected.proxyUpstreamModel {
		t.Fatalf("expected %s %q, got %q", headerProxyToUpstreamModel, expected.proxyUpstreamModel, got)
	}
	assertJSONHeaderEquals(t, rec.Header().Get(headerProxyToUpstreamReasoningParameters), expected.proxyReasoningPayload)
}

func assertJSONHeaderEquals(t *testing.T, raw string, expected map[string]any) {
	t.Helper()
	if raw == "" {
		t.Fatalf("expected non-empty JSON header")
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal header json: %v raw=%q", err, raw)
	}
	if diffExpected, err := json.Marshal(expected); err != nil {
		t.Fatalf("marshal expected: %v", err)
	} else if diffGot, err := json.Marshal(got); err != nil {
		t.Fatalf("marshal got: %v", err)
	} else if string(diffExpected) != string(diffGot) {
		t.Fatalf("expected header json %s, got %s", diffExpected, diffGot)
	}
}
