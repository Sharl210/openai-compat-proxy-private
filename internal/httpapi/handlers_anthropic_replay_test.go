package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestMessagesRoutePersistsDeepSeekAnthropicThinkingToolHistoryAcrossCompletionPaths(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		strategy string
		stream   bool
	}{
		{name: "upstream non-stream", strategy: config.DownstreamNonStreamStrategyUpstreamNonStream},
		{name: "proxy buffered stream", strategy: config.DownstreamNonStreamStrategyProxyBuffer},
		{name: "live SSE", strategy: config.DownstreamNonStreamStrategyProxyBuffer, stream: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			requestCount := 0
			var secondBody string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var request map[string]any
				if err := json.Unmarshal(body, &request); err != nil {
					t.Fatalf("decode DeepSeek Anthropic request: %v", err)
				}
				stream, _ := request["stream"].(bool)
				requestCount++
				if requestCount == 1 {
					writeDeepSeekAnthropicThinkingToolResponse(w, stream, "msg_deepseek_first")
					return
				}
				secondBody = string(body)
				writeDeepSeekAnthropicCompletionResponse(w, stream, "msg_deepseek_second")
			}))
			defer upstream.Close()

			server := NewServer(config.Config{
				DefaultProvider:             "deepseek",
				DefaultProReasoningModeSet:  true,
				DefaultProReasoningMode:     false,
				EnableLegacyV1Routes:        true,
				DownstreamNonStreamStrategy: testCase.strategy,
				Providers: []config.ProviderConfig{{
					ID:                        "deepseek",
					Enabled:                   true,
					UpstreamBaseURL:           upstream.URL,
					UpstreamAPIKey:            "test-key",
					UpstreamEndpointType:      config.UpstreamEndpointTypeAnthropic,
					SupportsAnthropicMessages: true,
					SupportsResponses:         true,
					ManualModels:              []string{"deepseek-chat"},
				}},
			})

			firstBody := `{"model":"deepseek-chat","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"find the file"}]}],"tools":[{"name":"read_file","description":"Read a file","input_schema":{"type":"object"}}]}`
			if testCase.stream {
				firstBody = strings.Replace(firstBody, `{"model"`, `{"stream":true,"model"`, 1)
			}
			firstReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(firstBody))
			firstReq.Header.Set("Content-Type", "application/json")
			firstReq.Header.Set("anthropic-version", "2023-06-01")
			firstReq.Header.Set("X-Proxy-Session-ID", "deepseek-follow-up-session")
			firstRec := httptest.NewRecorder()
			server.ServeHTTP(firstRec, firstReq)
			if firstRec.Code != http.StatusOK {
				t.Fatalf("expected first status 200, got %d body=%s", firstRec.Code, firstRec.Body.String())
			}
			if testCase.stream && !strings.Contains(firstRec.Body.String(), "event: message_stop") {
				t.Fatalf("expected completed Anthropic SSE response, got %s", firstRec.Body.String())
			}
			secondReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"deepseek-chat","max_tokens":64,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"read_file","input":{"path":"/tmp/a"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"file contents"},{"type":"text","text":"continue"}]}]}`))
			secondReq.Header.Set("Content-Type", "application/json")
			secondReq.Header.Set("anthropic-version", "2023-06-01")
			secondReq.Header.Set("X-Proxy-Session-ID", "deepseek-follow-up-session")
			secondRec := httptest.NewRecorder()
			server.ServeHTTP(secondRec, secondReq)
			if secondRec.Code != http.StatusOK {
				t.Fatalf("expected follow-up status 200, got %d body=%s", secondRec.Code, secondRec.Body.String())
			}
			if requestCount != 2 {
				t.Fatalf("expected first request to save history and one follow-up upstream request, got %d", requestCount)
			}
			for _, expected := range []string{`"type":"thinking"`, `"signature":"sig_deepseek_1"`, `"type":"tool_use"`, `"id":"call_1"`, `"type":"tool_result"`, `"tool_use_id":"call_1"`} {
				if !strings.Contains(secondBody, expected) {
					t.Fatalf("expected DeepSeek Anthropic follow-up to restore %s, got %s", expected, secondBody)
				}
			}
		})
	}
}

func TestMessagesLiveSSETerminalFailureDoesNotEmitSecondTerminal(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_failure\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n")
		_, _ = io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy\"}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "deepseek",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyProxyBuffer,
		Providers: []config.ProviderConfig{{
			ID:                        "deepseek",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			UpstreamEndpointType:      config.UpstreamEndpointTypeAnthropic,
			SupportsAnthropicMessages: true,
			ManualModels:              []string{"deepseek-chat"},
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"deepseek-chat","stream":true,"max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	if strings.Count(body, "event: error") != 1 {
		t.Fatalf("expected exactly one terminal error after upstream terminal failure, got %s", body)
	}
	if strings.Count(body, "event: message_stop") != 1 {
		t.Fatalf("expected exactly one message_stop after upstream terminal failure, got %s", body)
	}
}

func writeDeepSeekAnthropicThinkingToolResponse(w http.ResponseWriter, stream bool, responseID string) {
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"`+responseID+`","type":"message","role":"assistant","content":[{"type":"thinking","thinking":"need tool result","signature":"sig_deepseek_1"},{"type":"tool_use","id":"call_1","name":"read_file","input":{"path":"/tmp/a"}}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":3}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, "event: message_start\n"+
		`data: {"type":"message_start","message":{"id":"`+responseID+`","type":"message","role":"assistant","content":[]}}`+"\n\n"+
		"event: content_block_start\n"+
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n"+
		"event: content_block_delta\n"+
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"need tool result\"}}\n\n"+
		"event: content_block_delta\n"+
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig_deepseek_1\"}}\n\n"+
		"event: content_block_stop\n"+
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
		"event: content_block_start\n"+
		"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"read_file\",\"input\":{\"path\":\"/tmp/a\"}}}\n\n"+
		"event: content_block_stop\n"+
		"data: {\"type\":\"content_block_stop\",\"index\":1}\n\n"+
		"event: message_delta\n"+
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"input_tokens\":2,\"output_tokens\":3}}\n\n"+
		"event: message_stop\n"+
		"data: {\"type\":\"message_stop\"}\n\n")
}

func writeDeepSeekAnthropicCompletionResponse(w http.ResponseWriter, stream bool, responseID string) {
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"`+responseID+`","type":"message","role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\""+responseID+"\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n"+
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n"+
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":2,\"output_tokens\":3}}\n\n"+
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
}
