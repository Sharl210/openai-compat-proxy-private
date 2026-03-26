package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/testutil"
)

func TestMessagesStreamClosesThinkingBeforeTextAndEmitsSignature(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "anthropic",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                        "anthropic",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			SupportsAnthropicMessages: true,
			SupportsResponses:         true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"gpt-5.4",
		"stream":true,
		"max_tokens":64,
		"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `"thinking":"**推理中**\n\n代理层占位，以兼容不同上游情况，便于客户端记录推理时长\n"`) {
		t.Fatalf("expected anthropic placeholder thinking to use titled format, got %s", body)
	}

	sigIdx := strings.Index(body, `"type":"signature_delta"`)
	stopIdx := strings.Index(body, `event: content_block_stop`)
	textStartIdx := strings.Index(body, `"content_block":{"text":"","type":"text"}`)
	if sigIdx == -1 {
		t.Fatalf("expected signature_delta in anthropic stream, got %s", body)
	}
	if stopIdx == -1 {
		t.Fatalf("expected content_block_stop before text block, got %s", body)
	}
	if textStartIdx == -1 {
		t.Fatalf("expected text block start, got %s", body)
	}
	if !(sigIdx < stopIdx && stopIdx < textStartIdx) {
		t.Fatalf("expected signature_delta then thinking stop then text start, got %s", body)
	}
}

func TestMessagesStreamReopensThinkingBlockAcrossReasoningPhases(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.reasoning.delta\n" +
			"data: {\"summary\":\"alpha\"}\n\n",
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n",
		"event: response.reasoning.delta\n" +
			"data: {\"summary\":\"beta\"}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "anthropic",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                        "anthropic",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			SupportsAnthropicMessages: true,
			SupportsResponses:         true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"gpt-5.4",
		"stream":true,
		"max_tokens":64,
		"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()

	if !strings.Contains(body, `"thinking":"alpha"`) || !strings.Contains(body, `"thinking":"beta"`) {
		t.Fatalf("expected both reasoning deltas in anthropic stream, got %s", body)
	}
	if count := strings.Count(body, `"content_block":{"thinking":"","type":"thinking"}`); count != 2 {
		t.Fatalf("expected a reopened thinking block across reasoning phases, got %d body=%s", count, body)
	}
}

func TestMessagesStreamClosesToolBlockBeforeLaterTextAndForwardsArgumentDeltas(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.added\n" +
			"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"get_weather\"}}\n\n",
		"event: response.function_call_arguments.delta\n" +
			"data: {\"item_id\":\"fc_1\",\"delta\":\"{\\\"city\\\":\\\"Shanghai\\\"}\"}\n\n",
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"done\"}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "anthropic",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                        "anthropic",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			SupportsAnthropicMessages: true,
			SupportsResponses:         true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"gpt-5.4",
		"stream":true,
		"max_tokens":64,
		"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()

	if !strings.Contains(body, `"type":"tool_use"`) {
		t.Fatalf("expected tool_use block, got %s", body)
	}
	if !strings.Contains(body, `"type":"input_json_delta"`) {
		t.Fatalf("expected input_json_delta for tool arguments, got %s", body)
	}
	toolStopIdx := strings.Index(body, `event: content_block_stop`+"\n"+`data: {"index":1,"type":"content_block_stop"}`)
	textStartIdx := strings.Index(body, `"content_block":{"text":"","type":"text"}`)
	if toolStopIdx == -1 || textStartIdx == -1 {
		t.Fatalf("expected tool stop before text start, got %s", body)
	}
	if toolStopIdx > textStartIdx {
		t.Fatalf("expected tool block to close before later text starts, got %s", body)
	}
}
