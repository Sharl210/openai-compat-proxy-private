package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
)

func TestChatRoutePersistsTrustedAnthropicThinkingReplayAcrossCompletionPaths(t *testing.T) {
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
					t.Fatalf("decode anthropic request: %v", err)
				}
				stream, _ := request["stream"].(bool)
				requestCount++
				if requestCount == 1 {
					writeChatAnthropicThinkingToolResponse(w, stream, "msg_chat_first")
					return
				}
				secondBody = string(body)
				writeChatAnthropicCompletionResponse(w, stream, "msg_chat_second")
			}))
			defer upstream.Close()

			server := NewServer(config.Config{
				DefaultProvider:             "anthropic",
				DefaultProReasoningModeSet:  true,
				DefaultProReasoningMode:     false,
				EnableLegacyV1Routes:        true,
				DownstreamNonStreamStrategy: testCase.strategy,
				Providers: []config.ProviderConfig{{
					ID:                        "anthropic",
					Enabled:                   true,
					UpstreamBaseURL:           upstream.URL,
					UpstreamAPIKey:            "test-key",
					UpstreamEndpointType:      config.UpstreamEndpointTypeAnthropic,
					SupportsChat:              true,
					SupportsResponses:         true,
					SupportsAnthropicMessages: true,
				}},
			})

			firstBody := `{"model":"gpt-5","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`
			if testCase.stream {
				firstBody = `{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`
			}
			firstReq := httptest.NewRequest(http.MethodPost, canonicalV1ChatCompletionsPath, strings.NewReader(firstBody))
			firstReq.Header.Set("Content-Type", "application/json")
			firstRec := httptest.NewRecorder()
			server.ServeHTTP(firstRec, firstReq)
			if firstRec.Code != http.StatusOK {
				t.Fatalf("expected first status 200, got %d body=%s", firstRec.Code, firstRec.Body.String())
			}
			if testCase.stream && !strings.Contains(firstRec.Body.String(), "data: [DONE]") {
				t.Fatalf("expected completed Chat SSE response, got %s", firstRec.Body.String())
			}

			secondReq := httptest.NewRequest(http.MethodPost, canonicalV1ChatCompletionsPath, strings.NewReader(`{"model":"gpt-5","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"/tmp/a\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"file contents"}]}`))
			secondReq.Header.Set("Content-Type", "application/json")
			secondRec := httptest.NewRecorder()
			server.ServeHTTP(secondRec, secondReq)
			if secondRec.Code != http.StatusOK {
				t.Fatalf("expected replay request to succeed, got %d body=%s", secondRec.Code, secondRec.Body.String())
			}
			if requestCount != 2 {
				t.Fatalf("expected two upstream requests, got %d", requestCount)
			}
			for _, expected := range []string{
				`"type":"tool_use"`,
				`"id":"call_1"`,
				`"type":"tool_result"`,
				`"tool_use_id":"call_1"`,
			} {
				if !strings.Contains(secondBody, expected) {
					t.Fatalf("expected replayed Anthropic request to contain %s, got %s", expected, secondBody)
				}
			}
			if strings.Contains(secondBody, `"type":"thinking"`) || strings.Contains(secondBody, `"signature":"sig_chat_1"`) {
				t.Fatalf("expected replayed Chat request to avoid server-held thinking, got %s", secondBody)
			}
		})
	}
}

func TestChatRouteDoesNotReplayAnthropicThinkingForUntrustedToolSourceOrScope(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		storedProvider string
		storedModel    string
		storedCaller   string
		requestModel   string
		toolName       string
		toolArguments  string
		storedAuth     string
		requestAuth    string
	}{
		{
			name:           "assistant tool call source changed",
			storedProvider: "anthropic",
			storedModel:    "gpt-5",
			storedCaller:   "anonymous",
			requestModel:   "gpt-5",
			toolName:       "read_file",
			toolArguments:  `{"path":"/tmp/changed"}`,
		},
		{
			name:           "final model scope changed",
			storedProvider: "anthropic",
			storedModel:    "gpt-5",
			storedCaller:   "anonymous",
			requestModel:   "gpt-5-other",
			toolName:       "read_file",
			toolArguments:  `{"path":"/tmp/a"}`,
		},
		{
			name:           "inbound caller scope changed",
			storedProvider: "anthropic",
			storedModel:    "gpt-5",
			storedCaller:   "different-caller",
			requestModel:   "gpt-5",
			toolName:       "read_file",
			toolArguments:  `{"path":"/tmp/a"}`,
		},
		{
			name:           "provider identity changed",
			storedProvider: "other",
			storedModel:    "gpt-5",
			storedCaller:   "anonymous",
			requestModel:   "gpt-5",
			toolName:       "read_file",
			toolArguments:  `{"path":"/tmp/a"}`,
		},
		{
			name:           "upstream credential scope changed",
			storedProvider: "anthropic",
			storedModel:    "gpt-5",
			storedCaller:   "anonymous",
			requestModel:   "gpt-5",
			toolName:       "read_file",
			toolArguments:  `{"path":"/tmp/a"}`,
			storedAuth:     "Bearer scope-a",
			requestAuth:    "Bearer scope-b",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var upstreamBody string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				upstreamBody = string(body)
				writeChatAnthropicCompletionResponse(w, false, "msg_chat_untrusted")
			}))
			defer upstream.Close()

			server := NewServer(config.Config{
				DefaultProvider:             "anthropic",
				DefaultProReasoningModeSet:  true,
				DefaultProReasoningMode:     false,
				EnableLegacyV1Routes:        true,
				DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
				Providers: []config.ProviderConfig{{
					ID:                        "anthropic",
					Enabled:                   true,
					UpstreamBaseURL:           upstream.URL,
					UpstreamAPIKey:            "test-key",
					UpstreamEndpointType:      config.UpstreamEndpointTypeAnthropic,
					SupportsChat:              true,
					SupportsResponses:         true,
					SupportsAnthropicMessages: true,
				}},
			})

			storedAuth := testCase.storedAuth
			if storedAuth == "" {
				storedAuth = "Bearer test-key"
			}
			scope := responsesHistoryReplayScope(responsesHistoryReplayProvenance{
				ProviderID:                testCase.storedProvider,
				DownstreamEndpoint:        canonicalV1ChatCompletionsPath,
				UpstreamEndpointType:      config.UpstreamEndpointTypeAnthropic,
				NormalizedUpstreamBaseURL: upstream.URL,
				FinalUpstreamModel:        testCase.storedModel,
				CredentialFingerprint:     authorizationFingerprint(storedAuth),
				InboundCallerFingerprint:  testCase.storedCaller,
			})
			server.history.Save(testCase.storedProvider, "msg_chat_server", []model.CanonicalMessage{{
				Role: "assistant",
				ReasoningBlocks: []map[string]any{{
					"type":      "thinking",
					"thinking":  "server-held thinking",
					"signature": "sig_chat_server",
				}},
				ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "read_file", Arguments: `{"path":"/tmp/a"}`}},
			}}, scope)

			req := httptest.NewRequest(http.MethodPost, canonicalV1ChatCompletionsPath, strings.NewReader(`{"model":"`+testCase.requestModel+`","messages":[{"role":"assistant","content":null,"reasoning_content":"untrusted client reasoning","tool_calls":[{"id":"call_1","type":"function","function":{"name":"`+testCase.toolName+`","arguments":"`+strings.ReplaceAll(testCase.toolArguments, `"`, `\"`)+`"}}]},{"role":"tool","tool_call_id":"call_1","content":"file contents"}]}`))
			req.Header.Set("Content-Type", "application/json")
			if testCase.requestAuth != "" {
				req.Header.Set("X-Upstream-Authorization", testCase.requestAuth)
			}
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected non-replay request to remain valid, got %d body=%s", rec.Code, rec.Body.String())
			}
			if strings.Contains(upstreamBody, `"signature":"sig_chat_server"`) || strings.Contains(upstreamBody, `"thinking":"server-held thinking"`) {
				t.Fatalf("expected no server-held thinking replay for %s, got %s", testCase.name, upstreamBody)
			}
			if !strings.Contains(upstreamBody, `"type":"tool_use"`) || !strings.Contains(upstreamBody, `"id":"call_1"`) {
				t.Fatalf("expected current assistant tool call to remain available without replay, got %s", upstreamBody)
			}
		})
	}
}

func TestAnthropicThinkingReplayRequiresCompleteAssistantToolCallSequence(t *testing.T) {
	history := newResponsesHistoryStore(10, "")
	scope := responsesHistoryReplayScope(responsesHistoryReplayProvenance{
		ProviderID:                "anthropic",
		DownstreamEndpoint:        canonicalV1ChatCompletionsPath,
		UpstreamEndpointType:      config.UpstreamEndpointTypeAnthropic,
		NormalizedUpstreamBaseURL: "https://example.test",
		FinalUpstreamModel:        "claude-test",
		CredentialFingerprint:     "credential",
		InboundCallerFingerprint:  "caller",
	})
	toolCalls := []model.CanonicalToolCall{
		{ID: "call_1", Type: "function", Name: "read_file", Arguments: `{"path":"/tmp/a"}`},
		{ID: "call_2", Type: "function", Name: "list_files", Arguments: `{"path":"/tmp"}`},
	}
	history.Save("anthropic", "msg_chat_sequence", []model.CanonicalMessage{{
		Role:            "assistant",
		ToolCalls:       toolCalls,
		ReasoningBlocks: []map[string]any{{"type": "thinking", "thinking": "server-held thinking", "signature": "sig_chat_sequence"}},
	}}, scope)

	partial := recoverAnthropicThinkingForAssistantToolCalls(history, []model.CanonicalMessage{{
		Role:      "assistant",
		ToolCalls: toolCalls[:1],
	}}, "anthropic", scope)
	if len(partial[0].ReasoningBlocks) != 0 {
		t.Fatalf("expected partial assistant tool-call sequence not to replay thinking, got %#v", partial[0].ReasoningBlocks)
	}

	complete := recoverAnthropicThinkingForAssistantToolCalls(history, []model.CanonicalMessage{{
		Role:      "assistant",
		ToolCalls: append([]model.CanonicalToolCall(nil), toolCalls...),
	}}, "anthropic", scope)
	if len(complete[0].ReasoningBlocks) != 1 || complete[0].ReasoningBlocks[0]["signature"] != "sig_chat_sequence" {
		t.Fatalf("expected complete assistant tool-call sequence to replay thinking, got %#v", complete[0].ReasoningBlocks)
	}
}

func writeChatAnthropicThinkingToolResponse(w http.ResponseWriter, stream bool, responseID string) {
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"`+responseID+`","type":"message","role":"assistant","content":[{"type":"thinking","thinking":"need tool result","signature":"sig_chat_1"},{"type":"tool_use","id":"call_1","name":"read_file","input":{"path":"/tmp/a"}}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":3}}`)
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
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig_chat_1\"}}\n\n"+
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

func writeChatAnthropicCompletionResponse(w http.ResponseWriter, stream bool, responseID string) {
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"`+responseID+`","type":"message","role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, "event: message_start\n"+
		`data: {"type":"message_start","message":{"id":"`+responseID+`","type":"message","role":"assistant","content":[]}}`+"\n\n"+
		"event: content_block_start\n"+
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
		"event: content_block_delta\n"+
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n"+
		"event: content_block_stop\n"+
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
		"event: message_delta\n"+
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":2,\"output_tokens\":3}}\n\n"+
		"event: message_stop\n"+
		"data: {\"type\":\"message_stop\"}\n\n")
}
