package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/testutil"
)

const upstreamContextOverflowEvent = "event: error\n" +
	"data: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"code\":\"context_length_exceeded\",\"message\":\"prompt is too long: context_length_exceeded from upstream\",\"param\":\"input\"}}\n\n"

func TestStreamingContextOverflow_returnsHTTP400BeforeDurableOutput(t *testing.T) {
	tests := []struct {
		name               string
		path               string
		body               string
		setHeaders         func(*http.Request)
		assertProtocolBody func(*testing.T, string)
	}{
		{
			name: "responses",
			path: "/v1/responses",
			body: `{"model":"gpt-5","stream":true,"input":[{"role":"user","content":"hello"}]}`,
			assertProtocolBody: func(t *testing.T, body string) {
				t.Helper()
				if !strings.Contains(body, `"error":{"code":"context_length_exceeded"`) {
					t.Fatalf("expected OpenAI error envelope, got %s", body)
				}
			},
		},
		{
			name: "chat completions",
			path: "/v1/chat/completions",
			body: `{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`,
			assertProtocolBody: func(t *testing.T, body string) {
				t.Helper()
				if !strings.Contains(body, `"error":{"code":"context_length_exceeded"`) {
					t.Fatalf("expected OpenAI error envelope, got %s", body)
				}
			},
		},
		{
			name: "anthropic messages",
			path: "/v1/messages",
			body: `{"model":"gpt-5","stream":true,"max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`,
			setHeaders: func(req *http.Request) {
				req.Header.Set("anthropic-version", "2023-06-01")
			},
			assertProtocolBody: func(t *testing.T, body string) {
				t.Helper()
				if !strings.Contains(body, `"type":"error"`) || !strings.Contains(body, `"error":{"code":"context_length_exceeded"`) {
					t.Fatalf("expected Anthropic error envelope, got %s", body)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			upstream := testutil.NewStreamingUpstream(t, []string{upstreamContextOverflowEvent})
			defer upstream.Close()
			server := NewServer(testContextOverflowStreamConfig(upstream.URL))
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.setHeaders != nil {
				tc.setHeaders(req)
			}
			rec := httptest.NewRecorder()

			// When
			server.ServeHTTP(rec, req)

			// Then
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected early context overflow to return HTTP 400, got %d body=%s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if !strings.Contains(body, "context_length_exceeded") || !strings.Contains(body, "prompt is too long") {
				t.Fatalf("expected client-recognized overflow signals, got %s", body)
			}
			if strings.Contains(body, "event:") || strings.Contains(body, `"id":"rs_proxy"`) || strings.Contains(body, `"type":"message_start"`) {
				t.Fatalf("expected no durable downstream SSE or assistant lifecycle before HTTP error, got %s", body)
			}
			if got := rec.Header().Get("X-Accel-Buffering"); got != "" {
				t.Fatalf("expected SSE headers to remain unset before HTTP error, got X-Accel-Buffering=%q", got)
			}
			tc.assertProtocolBody(t, body)
		})
	}
}

func TestStreamingContextOverflow_staysTerminalSSEAfterDurableOutput(t *testing.T) {
	tests := []struct {
		name              string
		path              string
		body              string
		setHeaders        func(*http.Request)
		assertTerminalSSE func(*testing.T, string)
	}{
		{
			name: "responses",
			path: "/v1/responses",
			body: `{"model":"gpt-5","stream":true,"input":[{"role":"user","content":"hello"}]}`,
			assertTerminalSSE: func(t *testing.T, body string) {
				t.Helper()
				if !strings.Contains(body, "event: response.failed") {
					t.Fatalf("expected Responses terminal SSE failure, got %s", body)
				}
			},
		},
		{
			name: "chat completions",
			path: "/v1/chat/completions",
			body: `{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`,
			assertTerminalSSE: func(t *testing.T, body string) {
				t.Helper()
				if !strings.Contains(body, `"finish_reason":"error"`) || !strings.Contains(body, "data: [DONE]") {
					t.Fatalf("expected Chat terminal error chunk and DONE marker, got %s", body)
				}
			},
		},
		{
			name: "anthropic messages",
			path: "/v1/messages",
			body: `{"model":"gpt-5","stream":true,"max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`,
			setHeaders: func(req *http.Request) {
				req.Header.Set("anthropic-version", "2023-06-01")
			},
			assertTerminalSSE: func(t *testing.T, body string) {
				t.Helper()
				if !strings.Contains(body, "event: error") || !strings.Contains(body, "event: message_stop") {
					t.Fatalf("expected Anthropic terminal error and message_stop events, got %s", body)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			upstream := testutil.NewStreamingUpstream(t, []string{
				"event: response.output_text.delta\n" +
					"data: {\"delta\":\"durable output\"}\n\n",
				upstreamContextOverflowEvent,
			})
			defer upstream.Close()
			server := NewServer(testContextOverflowStreamConfig(upstream.URL))
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.setHeaders != nil {
				tc.setHeaders(req)
			}
			rec := httptest.NewRecorder()

			// When
			server.ServeHTTP(rec, req)

			// Then
			if rec.Code != http.StatusOK {
				t.Fatalf("expected late context overflow to preserve SSE status, got %d body=%s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if !strings.Contains(body, "durable output") {
				t.Fatalf("expected durable output before terminal failure, got %s", body)
			}
			if !strings.Contains(body, "context_length_exceeded") || !strings.Contains(body, "prompt is too long") {
				t.Fatalf("expected client-recognized overflow signals in terminal SSE, got %s", body)
			}
			tc.assertTerminalSSE(t, body)
		})
	}
}

func TestStreamingContextOverflow_staysTerminalSSEAfterPartialReasoning(t *testing.T) {
	for _, tc := range contextOverflowRouteCases() {
		t.Run(tc.name, func(t *testing.T) {
			upstream := testutil.NewStreamingUpstream(t, []string{
				"event: response.reasoning.delta\n" +
					"data: {\"summary\":\"partial reasoning\"}\n\n",
				upstreamContextOverflowEvent,
			})
			defer upstream.Close()

			server := NewServer(testContextOverflowStreamConfig(upstream.URL))
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.streamBody))
			req.Header.Set("Content-Type", "application/json")
			if tc.setHeaders != nil {
				tc.setHeaders(req)
			}
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected partial reasoning to preserve SSE status, got %d body=%s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			partialReasoningIndex := strings.Index(body, "partial reasoning")
			terminalIndex := strings.Index(body, tc.terminalMarker)
			if partialReasoningIndex == -1 || terminalIndex == -1 || partialReasoningIndex >= terminalIndex {
				t.Fatalf("expected partial reasoning before terminal overflow, got %s", body)
			}
			assertContextOverflowSignals(t, body)

			switch tc.name {
			case "responses":
				partDoneIndex := strings.Index(body, "event: response.reasoning_summary_part.done")
				itemDoneIndex := strings.LastIndex(body, "event: response.output_item.done")
				if partDoneIndex == -1 || itemDoneIndex == -1 || !(partialReasoningIndex < partDoneIndex && partDoneIndex < itemDoneIndex && itemDoneIndex < terminalIndex) {
					t.Fatalf("expected Responses reasoning lifecycle to close before failure, got %s", body)
				}
				if strings.Count(body, "event: response.failed") != 1 || strings.Contains(body, "event: response.completed") || strings.Contains(body, "event: error\n") {
					t.Fatalf("expected exactly one normalized Responses failure terminal, got %s", body)
				}
			case "chat completions":
				if strings.Count(body, `"finish_reason":"error"`) != 1 || !strings.Contains(body, "data: [DONE]") || strings.Contains(body, `"finish_reason":"stop"`) {
					t.Fatalf("expected exactly one Chat error terminal and DONE marker, got %s", body)
				}
			case "anthropic messages":
				thinkingStopIndex := strings.LastIndex(body[:terminalIndex], "event: content_block_stop")
				messageStopIndex := strings.Index(body, "event: message_stop")
				if thinkingStopIndex == -1 || messageStopIndex == -1 || !(partialReasoningIndex < thinkingStopIndex && thinkingStopIndex < terminalIndex && terminalIndex < messageStopIndex) {
					t.Fatalf("expected Anthropic thinking lifecycle to close before error and message stop, got %s", body)
				}
				if strings.Count(body, "event: error") != 1 || strings.Contains(body, "event: message_delta") {
					t.Fatalf("expected exactly one Anthropic error terminal without completion delta, got %s", body)
				}
			}
		})
	}
}

func testContextOverflowStreamConfig(upstreamURL string) config.Config {
	return config.Config{
		DefaultProvider:      "test",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                        "test",
			Enabled:                   true,
			UpstreamBaseURL:           upstreamURL,
			UpstreamAPIKey:            "test-key",
			SupportsChat:              true,
			SupportsResponses:         true,
			SupportsAnthropicMessages: true,
		}},
	}
}
