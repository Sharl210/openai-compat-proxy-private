package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	if strings.Contains(body, "代理层占位") || strings.Contains(body, "**推理中**") {
		t.Fatalf("expected anthropic stream not to expose proxy placeholder thinking text, got %s", body)
	}
	if strings.Contains(body, `"thinking":"`+invisibleSyntheticReasoningDelta+`"`) {
		t.Fatalf("expected anthropic stream not to emit invisible placeholder thinking delta, got %s", body)
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

func TestMessagesStreamFromAnthropicJSONUpstreamInjectsPlaceholderAndText(t *testing.T) {
	anthropicResponse := `{
		"id": "msg_json",
		"type": "message",
		"role": "assistant",
		"model": "claude-sonnet-4-5",
		"content": [{"type": "text", "text": "final answer"}],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 1, "output_tokens": 1}
	}`
	upstream := newAnthropicFormatUpstream(t, anthropicResponse)
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "anthropic",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                        "anthropic",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			UpstreamEndpointType:      config.UpstreamEndpointTypeAnthropic,
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
	if strings.Contains(body, "代理层占位") || strings.Contains(body, "**推理中**") {
		t.Fatalf("expected anthropic stream not to expose proxy placeholder thinking text, got %s", body)
	}
	if !strings.Contains(body, `"text":"final answer"`) {
		t.Fatalf("expected final answer text, got %s", body)
	}
	if !strings.Contains(body, `event: message_stop`) {
		t.Fatalf("expected message_stop, got %s", body)
	}
}

func TestMessagesStreamUsesRequestIdentityInMessageStart(t *testing.T) {
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
	requestID := rec.Header().Get("X-Request-Id")
	if requestID == "" {
		t.Fatalf("expected X-Request-Id header, got headers=%v", rec.Header())
	}
	if !strings.Contains(body, `"id":"`+requestID+`"`) {
		t.Fatalf("expected message_start to use request id %q, got %s", requestID, body)
	}
	if !strings.Contains(body, `"model":"gpt-5.4"`) {
		t.Fatalf("expected message_start to use downstream model, got %s", body)
	}
	if strings.Contains(body, `"id":"msg_proxy"`) || strings.Contains(body, `"model":"responses-upstream"`) {
		t.Fatalf("expected proxy placeholder identity to be absent, got %s", body)
	}
}

func TestMessagesStreamEmitsNumericUsageFieldsInMessageStart(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":12,\"output_tokens\":7}}}\n\n",
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
	if !strings.Contains(body, `"usage":{"input_tokens":0,"output_tokens":0}`) {
		t.Fatalf("expected message_start to include numeric usage placeholders, got %s", body)
	}
	if !strings.Contains(body, `"usage":{"input_tokens":12,"output_tokens":7}`) {
		t.Fatalf("expected final anthropic usage to carry real totals, got %s", body)
	}
}

func TestMessagesStreamDoesNotEmitNullUsageInMessageDelta(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{}}\n\n",
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
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"gpt-5.4",
		"stream":true,
		"max_tokens":64,
		"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]
	}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, request)
	body := rec.Body.String()
	if strings.Contains(body, `"usage":null`) {
		t.Fatalf("expected anthropic message_delta to avoid null usage, got %s", body)
	}
	if strings.Contains(body, `"stop_sequence":null`) {
		t.Fatalf("expected anthropic message_delta to omit null stop_sequence, got %s", body)
	}
	if !strings.Contains(body, "event: message_delta\n"+
		`data: {"delta":{"stop_reason":"end_turn"},"type":"message_delta","usage":{"input_tokens":0,"output_tokens":0}}`) {
		t.Fatalf("expected anthropic message_delta to emit numeric usage placeholders when totals are unavailable, got %s", body)
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

func TestMessagesStreamUsesOriginalSignatureFromReasoningBlocks(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.reasoning.delta\n" +
			"data: {\"summary\":\"alpha\",\"blocks\":[{\"type\":\"thinking\",\"thinking\":\"alpha\",\"signature\":\"sig_real\"}]}\n\n",
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

	if !strings.Contains(body, `"type":"signature_delta"`) || !strings.Contains(body, `"signature":"sig_real"`) {
		t.Fatalf("expected anthropic stream to forward original signature_delta, got %s", body)
	}
	if strings.Contains(body, `"signature":"proxy_signature"`) {
		t.Fatalf("expected anthropic stream to avoid proxy_signature when original signature exists, got %s", body)
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

func TestMessagesStreamPreservesToolArgumentDeltaArrivingBeforeToolStart(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.function_call_arguments.delta\n" +
			"data: {\"item_id\":\"fc_1\",\"delta\":\"{\\\"city\\\":\\\"Shanghai\\\"}\"}\n\n",
		"event: response.output_item.added\n" +
			"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"get_weather\"}}\n\n",
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

	if !strings.Contains(body, `"type":"input_json_delta"`) {
		t.Fatalf("expected pending tool arguments delta to be forwarded after tool block starts, got %s", body)
	}
	if !strings.Contains(body, `"partial_json":"{\"city\":\"Shanghai\"}"`) {
		t.Fatalf("expected pending tool arguments to be preserved, got %s", body)
	}
	toolStartIdx := strings.Index(body, `"content_block":{"id":"call_1","input":{},"name":"get_weather","type":"tool_use"}`)
	deltaIdx := strings.Index(body, `"partial_json":"{\"city\":\"Shanghai\"}"`)
	if toolStartIdx == -1 || deltaIdx == -1 || deltaIdx < toolStartIdx {
		t.Fatalf("expected tool arguments delta after tool block start, got %s", body)
	}
}

func TestMessagesStreamFailureClosesOpenBlocksBeforeTerminalStop(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: response.output_item.added\n"))
		_, _ = w.Write([]byte("data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"get_weather\"}}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("event: response.function_call_arguments.delta\n"))
		_, _ = w.Write([]byte("data: {broken json}\n\n"))
		flusher.Flush()
	}))
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

	toolStopIdx := strings.Index(body, `event: content_block_stop`+"\n"+`data: {"index":1,"type":"content_block_stop"}`)
	errorIdx := strings.Index(body, `event: error`)
	messageStopIdx := strings.Index(body, `event: message_stop`)
	if toolStopIdx == -1 {
		t.Fatalf("expected open tool block to be closed on terminal failure, got %s", body)
	}
	if errorIdx == -1 || messageStopIdx == -1 {
		t.Fatalf("expected error and message_stop terminal events, got %s", body)
	}
	if !(toolStopIdx < errorIdx && errorIdx < messageStopIdx) {
		t.Fatalf("expected block close before terminal failure events, got %s", body)
	}
}

func TestMessagesStreamUpstreamDisconnectClosesOpenBlocksBeforeTerminalStop(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: response.output_item.added\n"))
		_, _ = w.Write([]byte("data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"get_weather\"}}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("event: response.function_call_arguments.delta\n"))
		_, _ = w.Write([]byte("data: {\"item_id\":\"fc_1\",\"delta\":\"{\\\"city\\\":\\\"Shanghai\\\"}\"}\n\n"))
		flusher.Flush()
	}))
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

	toolStopIdx := strings.Index(body, `event: content_block_stop`+"\n"+`data: {"index":1,"type":"content_block_stop"}`)
	errorIdx := strings.Index(body, `event: error`)
	messageStopIdx := strings.Index(body, `event: message_stop`)
	if toolStopIdx == -1 {
		t.Fatalf("expected open tool block to be closed on upstream disconnect, got %s", body)
	}
	if errorIdx == -1 || messageStopIdx == -1 {
		t.Fatalf("expected error and message_stop terminal events after upstream disconnect, got %s", body)
	}
	if !(toolStopIdx < errorIdx && errorIdx < messageStopIdx) {
		t.Fatalf("expected block close before terminal failure events, got %s", body)
	}
	if !strings.Contains(body, `"health_flag":"upstreamStreamBroken"`) || !strings.Contains(body, `"message":"unexpected EOF"`) {
		t.Fatalf("expected upstream disconnect to surface unexpected EOF health details, got %s", body)
	}
}

func TestMessagesStreamReturnsHTTP400ForEarlyUpstreamContextOverflow(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: error\n" +
			"data: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"code\":\"context_length_exceeded\",\"message\":\"Your input exceeds the context window of this model. Please adjust your input and try again.\",\"param\":\"input\"},\"sequence_number\":2}\n\n",
		"event: response.failed\n" +
			"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"Your input exceeds the context window of this model. Please adjust your input and try again.\"}}}\n\n",
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
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected early upstream context overflow to return HTTP 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `event:`) || strings.Contains(body, `message_start`) {
		t.Fatalf("expected early context overflow not to start Anthropic SSE, got %s", body)
	}
	if !strings.Contains(body, `"type":"error"`) || !strings.Contains(body, `"code":"context_length_exceeded"`) {
		t.Fatalf("expected Anthropic context overflow envelope, got %s", body)
	}
	if !strings.Contains(body, `prompt is too long`) || !strings.Contains(body, `context_length_exceeded`) {
		t.Fatalf("expected client-recognized compaction signals, got %s", body)
	}
}

func TestMessagesStreamSeparatesMultipleToolCallsIntoDistinctBlocks(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.added\n" +
			"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"search_web\"}}\n\n",
		"event: response.function_call_arguments.delta\n" +
			"data: {\"item_id\":\"fc_1\",\"delta\":\"{\\\"query\\\":\\\"A\\\",\\\"topic\\\":\\\"general\\\"}\"}\n\n",
		"event: response.output_item.added\n" +
			"data: {\"item\":{\"id\":\"fc_2\",\"type\":\"function_call\",\"call_id\":\"call_2\",\"name\":\"search_web\"}}\n\n",
		"event: response.function_call_arguments.delta\n" +
			"data: {\"item_id\":\"fc_2\",\"delta\":\"{\\\"query\\\":\\\"B\\\"}\"}\n\n",
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

	if count := strings.Count(body, `"type":"tool_use"`); count != 2 {
		t.Fatalf("expected two tool_use blocks, got %d body=%s", count, body)
	}
	if !strings.Contains(body, `"partial_json":"{\"query\":\"A\",\"topic\":\"general\"}"`) {
		t.Fatalf("expected first tool arguments in stream, got %s", body)
	}
	if !strings.Contains(body, `"partial_json":"{\"query\":\"B\"}"`) {
		t.Fatalf("expected second tool arguments in stream, got %s", body)
	}
	firstStopIdx := strings.Index(body, `event: content_block_stop`+"\n"+`data: {"index":1,"type":"content_block_stop"}`)
	secondStartIdx := strings.LastIndex(body, `"content_block":{"id":"call_2","input":{},"name":"search_web","type":"tool_use"}`)
	if firstStopIdx == -1 || secondStartIdx == -1 || firstStopIdx > secondStartIdx {
		t.Fatalf("expected first tool block to stop before second starts, got %s", body)
	}
}

func TestMessagesStreamDoesNotReopenCompletedToolItems(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.added\n" +
			"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"search_web\"}}\n\n",
		"event: response.function_call_arguments.delta\n" +
			"data: {\"item_id\":\"fc_1\",\"delta\":\"{\\\"query\\\":\\\"A\\\"}\"}\n\n",
		"event: response.output_item.added\n" +
			"data: {\"item\":{\"id\":\"fc_2\",\"type\":\"function_call\",\"call_id\":\"call_2\",\"name\":\"search_web\"}}\n\n",
		"event: response.function_call_arguments.delta\n" +
			"data: {\"item_id\":\"fc_2\",\"delta\":\"{\\\"query\\\":\\\"B\\\"}\"}\n\n",
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"search_web\",\"arguments\":\"{\\\"query\\\":\\\"A\\\"}\"}}\n\n",
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"fc_2\",\"type\":\"function_call\",\"call_id\":\"call_2\",\"name\":\"search_web\",\"arguments\":\"{\\\"query\\\":\\\"B\\\"}\"}}\n\n",
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

	if count := strings.Count(body, `"type":"tool_use"`); count != 2 {
		t.Fatalf("expected completed tool items not to reopen tool blocks, got %d body=%s", count, body)
	}
	if count := strings.Count(body, `"name":"search_web"`); count != 2 {
		t.Fatalf("expected each tool name exactly once, got %d body=%s", count, body)
	}
}

func TestMessagesStreamResetsStopReasonAfterToolUseFollowedByText(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.added\n" +
			"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"search_web\"}}\n\n",
		"event: response.function_call_arguments.delta\n" +
			"data: {\"item_id\":\"fc_1\",\"delta\":\"{\\\"query\\\":\\\"Quectel\\\"}\"}\n\n",
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
	if !strings.Contains(body, `"stop_reason":"end_turn"`) {
		t.Fatalf("expected final stop_reason end_turn after later text, got %s", body)
	}
}

func TestMessagesStreamFormatsReasoningSummaryDoneSuffix(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.reasoning_summary_text.delta\n" +
			"data: {\"item_id\":\"rs_1\",\"summary_index\":0,\"delta\":\"**标题**\"}\n\n",
		"event: response.reasoning_summary_text.done\n" +
			"data: {\"item_id\":\"rs_1\",\"summary_index\":0,\"text\":\"**标题****正文**\"}\n\n",
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"**标题****正文**\"}]}}\n\n",
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
	if !strings.Contains(body, `"thinking":"**标题**"`) || !strings.Contains(body, `"thinking":"\n\n**正文**"`) {
		t.Fatalf("expected formatted append-only thinking chunks, got %s", body)
	}
	if strings.Contains(body, `"thinking":"**标题**\n\n**正文**"`) {
		t.Fatalf("expected completed title buffer not to be replayed as a thinking delta, got %s", body)
	}
}

func TestMessagesStreamCompletesReasoningSummaryFromItemDone(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.reasoning_summary_text.delta\n" +
			"data: {\"item_id\":\"rs_1\",\"summary_index\":0,\"delta\":\"**标题**\"}\n\n",
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"**标题****正文**\"}]}}\n\n",
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
	if !strings.Contains(body, `"thinking":"**标题**"`) || !strings.Contains(body, `"thinking":"\n\n**正文**"`) {
		t.Fatalf("expected item.done to append the formatted thinking suffix, got %s", body)
	}
}

func TestMessagesStreamEmitsMissingReasoningSummaryIndexes(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.reasoning_summary_text.done\n" +
			"data: {\"item_id\":\"rs_1\",\"summary_index\":0,\"text\":\"**标题****正文**\"}\n\n",
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"**标题****正文**\"},{\"type\":\"summary_text\",\"text\":\"second\"}]}}\n\n",
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
	if strings.Count(body, `"thinking":"**标题**\n\n**正文**"`) != 1 || strings.Count(body, `"thinking":"second"`) != 1 {
		t.Fatalf("expected each summary index exactly once, got %s", body)
	}
}

func TestReasoningContentValueFormatsThinkingText(t *testing.T) {
	if got := reasoningContentValue(map[string]any{"reasoning_content": "**重点**正文"}); got != "**重点**\n正文" {
		t.Fatalf("expected thinking title to be separated, got %q", got)
	}
}

func TestReasoningSummaryFromItemSeparatesBoldTitleFromFollowingContent(t *testing.T) {
	item := map[string]any{
		"type": "reasoning",
		"summary": []any{
			map[string]any{"type": "summary_text", "text": "**标题****后续**"},
		},
	}
	if got := reasoningSummaryFromItem(item); got != "**标题**\n\n**后续**" {
		t.Fatalf("expected adjacent reasoning titles to be separated, got %q", got)
	}
}

func TestMessagesStreamSlowUpstreamDoesNotEmitInvisibleThinkingPlaceholderDelta(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_slow\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(600 * time.Millisecond)
		_, _ = w.Write([]byte("event: content_block_start\n"))
		_, _ = w.Write([]byte("data: {\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte("data: {\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"real thinking\"}}\n\n"))
		_, _ = w.Write([]byte("event: message_delta\n"))
		_, _ = w.Write([]byte("data: {\"usage\":{\"input_tokens\":1,\"output_tokens\":1},\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte("data: {}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}))
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
	if strings.Contains(body, `"thinking":"`+invisibleSyntheticReasoningDelta+`"`) {
		t.Fatalf("expected no invisible thinking placeholder delta during slow upstream stream, got %s", body)
	}
	if !strings.Contains(body, `event: content_block_start`) {
		t.Fatalf("expected thinking lifecycle to still start for slow upstream stream, got %s", body)
	}
}
