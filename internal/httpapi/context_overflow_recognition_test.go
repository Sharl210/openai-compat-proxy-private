package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/testutil"
)

type contextOverflowDownstreamCase struct {
	name       string
	path       string
	body       string
	streamBody string
	setHeaders func(*http.Request)
}

const upstreamChatContextOverflowEvent = "event: error\n" +
	"data: {\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"input tokens exceed maximum\"}}\n\n"

const upstreamChatDurableOutputEvent = "data: {\"id\":\"chat-overflow\",\"choices\":[{\"delta\":{\"content\":\"durable output\"},\"finish_reason\":null}]}\n\n"

func TestChatUpstreamContextOverflowPreOutput_returnsHTTP400(t *testing.T) {
	for _, tc := range contextOverflowDownstreamCases() {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			upstream := newChatStreamingUpstream(t, []string{upstreamChatContextOverflowEvent})
			defer upstream.Close()
			server := NewServer(testChatUpstreamContextOverflowConfig(upstream.URL))

			// When
			rec := performContextOverflowRequest(server, tc, true)

			// Then
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected pre-output chat-upstream overflow to return HTTP 400, got %d body=%s", rec.Code, rec.Body.String())
			}
			assertClientRecognizedContextOverflow(t, tc.name, rec.Body.String())
			if strings.Contains(rec.Body.String(), "event:") {
				t.Fatalf("expected pre-output overflow not to start SSE, got %s", rec.Body.String())
			}
		})
	}
}

func TestChatUpstreamContextOverflowAfterDurableOutput_staysTerminalSSE(t *testing.T) {
	for _, tc := range contextOverflowDownstreamCases() {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			upstream := newChatStreamingUpstream(t, []string{
				upstreamChatDurableOutputEvent,
				upstreamChatContextOverflowEvent,
			})
			defer upstream.Close()
			server := NewServer(testChatUpstreamContextOverflowConfig(upstream.URL))

			// When
			rec := performContextOverflowRequest(server, tc, true)

			// Then
			if rec.Code != http.StatusOK {
				t.Fatalf("expected post-output chat-upstream overflow to preserve SSE status, got %d body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "durable output") {
				t.Fatalf("expected durable output before terminal failure, got %s", rec.Body.String())
			}
			assertClientRecognizedContextOverflow(t, tc.name, rec.Body.String())
		})
	}
}

func TestChatUpstreamContextOverflowProxyBuffer_returnsHTTP400(t *testing.T) {
	for _, tc := range contextOverflowDownstreamCases() {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			upstream := newChatStreamingUpstream(t, []string{upstreamChatContextOverflowEvent})
			defer upstream.Close()
			cfg := testChatUpstreamContextOverflowConfig(upstream.URL)
			cfg.DownstreamNonStreamStrategy = config.DownstreamNonStreamStrategyProxyBuffer
			server := NewServer(cfg)

			// When
			rec := performContextOverflowRequest(server, tc, false)

			// Then
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected proxy-buffer chat-upstream overflow to return HTTP 400, got %d body=%s", rec.Code, rec.Body.String())
			}
			assertClientRecognizedContextOverflow(t, tc.name, rec.Body.String())
			if strings.Contains(rec.Body.String(), "invalid_upstream_stream") {
				t.Fatalf("expected chat-upstream overflow not to degrade into invalid_upstream_stream, got %s", rec.Body.String())
			}
		})
	}
}

func TestContextOverflowPreOutputProbe_normalizesInputTokensExceedMaximum(t *testing.T) {
	for _, tc := range contextOverflowDownstreamCases() {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			upstream := testutil.NewStreamingUpstream(t, []string{
				"event: error\n" +
					"data: {\"error\":{\"message\":\"input tokens exceed maximum\"}}\n\n",
			})
			defer upstream.Close()
			server := NewServer(testContextOverflowStreamConfig(upstream.URL))
			rec := performContextOverflowRequest(server, tc, true)

			// When
			body := rec.Body.String()

			// Then
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected pre-output overflow to return HTTP 400, got %d body=%s", rec.Code, body)
			}
			assertClientRecognizedContextOverflow(t, tc.name, body)
			if strings.Contains(body, "event:") {
				t.Fatalf("expected pre-output overflow not to start SSE, got %s", body)
			}
		})
	}
}

func TestContextOverflowPreOutputProbe_normalizesNestedAndTopLevelType(t *testing.T) {
	for _, overflow := range []struct {
		name string
		data string
	}{
		{
			name: "nested error type",
			data: `{"error":{"type":"context_too_large"}}`,
		},
		{
			name: "top level type",
			data: `{"type":"context_too_large"}`,
		},
	} {
		for _, tc := range contextOverflowDownstreamCases() {
			t.Run(overflow.name+"/"+tc.name, func(t *testing.T) {
				// Given
				upstream := testutil.NewStreamingUpstream(t, []string{
					"event: error\n" + "data: " + overflow.data + "\n\n",
				})
				defer upstream.Close()
				server := NewServer(testContextOverflowStreamConfig(upstream.URL))

				// When
				rec := performContextOverflowRequest(server, tc, true)

				// Then
				body := rec.Body.String()
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("expected pre-output overflow to return HTTP 400, got %d body=%s", rec.Code, body)
				}
				assertClientRecognizedContextOverflow(t, tc.name, body)
				if strings.Contains(body, "event:") {
					t.Fatalf("expected pre-output overflow not to start SSE, got %s", body)
				}
			})
		}
	}
}

func TestContextOverflowProxyBufferTerminalFailure_normalizesInputTokenLimitExceeded(t *testing.T) {
	for _, tc := range contextOverflowDownstreamCases() {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			upstream := testutil.NewStreamingUpstream(t, []string{
				"event: error\n" +
					"data: {\"error\":{\"message\":\"input token limit exceeded\"}}\n\n",
			})
			defer upstream.Close()
			cfg := testContextOverflowStreamConfig(upstream.URL)
			cfg.DownstreamNonStreamStrategy = config.DownstreamNonStreamStrategyProxyBuffer
			server := NewServer(cfg)
			rec := performContextOverflowRequest(server, tc, false)

			// When
			body := rec.Body.String()

			// Then
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected proxy-buffer overflow to return HTTP 400, got %d body=%s", rec.Code, body)
			}
			assertClientRecognizedContextOverflow(t, tc.name, body)
		})
	}
}

func TestContextOverflowHTTPStatusError_normalizesAcrossDownstreamProtocols(t *testing.T) {
	for _, tc := range contextOverflowDownstreamCases() {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":"context_too_large","message":"input tokens exceed maximum"}}`))
			}))
			defer upstream.Close()
			cfg := testContextOverflowStreamConfig(upstream.URL)
			cfg.DownstreamNonStreamStrategy = config.DownstreamNonStreamStrategyUpstreamNonStream
			server := NewServer(cfg)
			rec := performContextOverflowRequest(server, tc, false)

			// When
			body := rec.Body.String()

			// Then
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected upstream overflow to return HTTP 400, got %d body=%s", rec.Code, body)
			}
			assertClientRecognizedContextOverflow(t, tc.name, body)
		})
	}
}

func TestContextOverflowHTTPStatusError_normalizesNestedAndTopLevelType(t *testing.T) {
	for _, overflow := range []struct {
		name string
		body string
	}{
		{
			name: "nested error type",
			body: `{"error":{"type":"context_too_large"}}`,
		},
		{
			name: "top level type",
			body: `{"type":"context_too_large"}`,
		},
	} {
		for _, tc := range contextOverflowDownstreamCases() {
			t.Run(overflow.name+"/"+tc.name, func(t *testing.T) {
				// Given
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(overflow.body))
				}))
				defer upstream.Close()
				cfg := testContextOverflowStreamConfig(upstream.URL)
				cfg.DownstreamNonStreamStrategy = config.DownstreamNonStreamStrategyUpstreamNonStream
				server := NewServer(cfg)

				// When
				rec := performContextOverflowRequest(server, tc, false)

				// Then
				body := rec.Body.String()
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("expected upstream overflow to return HTTP 400, got %d body=%s", rec.Code, body)
				}
				assertClientRecognizedContextOverflow(t, tc.name, body)
			})
		}
	}
}

func TestContextOverflowHTTPStatusError_preservesTokenLimitQuotaErrors(t *testing.T) {
	const upstreamBody = `{"error":{"code":"USAGE_LIMIT_EXCEEDED","type":"insufficient_quota","message":"input tokens exceed maximum for this API key quota"}}`

	for _, tc := range contextOverflowDownstreamCases() {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(upstreamBody))
			}))
			defer upstream.Close()
			cfg := testContextOverflowStreamConfig(upstream.URL)
			cfg.DownstreamNonStreamStrategy = config.DownstreamNonStreamStrategyUpstreamNonStream
			server := NewServer(cfg)

			// When
			rec := performContextOverflowRequest(server, tc, false)

			// Then
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("expected quota status to pass through, got %d body=%s", rec.Code, rec.Body.String())
			}
			if got := rec.Body.String(); got != upstreamBody {
				t.Fatalf("expected quota body to pass through unchanged, got %s", got)
			}
			if strings.Contains(rec.Body.String(), "context_length_exceeded") {
				t.Fatalf("expected quota error not to be normalized as context overflow, got %s", rec.Body.String())
			}
		})
	}
}

func TestChatUpstreamQuotaErrorBeforeOutput_staysQuotaTerminalSSE(t *testing.T) {
	for _, tc := range contextOverflowDownstreamCases() {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			upstream := newChatStreamingUpstream(t, []string{
				"event: error\n" +
					"data: {\"error\":{\"code\":\"USAGE_LIMIT_EXCEEDED\",\"type\":\"insufficient_quota\",\"message\":\"input tokens exceed maximum for this API key quota\"}}\n\n",
			})
			defer upstream.Close()
			server := NewServer(testChatUpstreamContextOverflowConfig(upstream.URL))

			// When
			rec := performContextOverflowRequest(server, tc, true)

			// Then
			if rec.Code != http.StatusOK {
				t.Fatalf("expected quota error to preserve stream status, got %d body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "insufficient_quota") {
				t.Fatalf("expected quota error in terminal stream, got %s", rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "context_length_exceeded") || strings.Contains(rec.Body.String(), "prompt is too long") {
				t.Fatalf("expected quota error not to normalize as context overflow, got %s", rec.Body.String())
			}
		})
	}
}

func contextOverflowDownstreamCases() []contextOverflowDownstreamCase {
	return []contextOverflowDownstreamCase{
		{
			name:       "responses",
			path:       "/v1/responses",
			body:       `{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`,
			streamBody: `{"model":"gpt-5","stream":true,"input":[{"role":"user","content":"hello"}]}`,
		},
		{
			name:       "chat",
			path:       "/v1/chat/completions",
			body:       `{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`,
			streamBody: `{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			name:       "anthropic",
			path:       "/v1/messages",
			body:       `{"model":"gpt-5","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`,
			streamBody: `{"model":"gpt-5","stream":true,"max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`,
			setHeaders: func(req *http.Request) {
				req.Header.Set("anthropic-version", "2023-06-01")
			},
		},
	}
}

func testChatUpstreamContextOverflowConfig(upstreamURL string) config.Config {
	cfg := testContextOverflowStreamConfig(upstreamURL)
	cfg.Providers[0].UpstreamEndpointType = config.UpstreamEndpointTypeChat
	return cfg
}

func performContextOverflowRequest(server http.Handler, tc contextOverflowDownstreamCase, stream bool) *httptest.ResponseRecorder {
	reqBody := tc.body
	if stream {
		reqBody = tc.streamBody
	}
	req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	if tc.setHeaders != nil {
		tc.setHeaders(req)
	}
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

func assertClientRecognizedContextOverflow(t *testing.T, protocol string, body string) {
	t.Helper()
	if !strings.Contains(body, `"code":"context_length_exceeded"`) {
		t.Fatalf("expected normalized context overflow code, got %s", body)
	}
	if !strings.Contains(body, "prompt is too long") || !strings.Contains(body, "context_length_exceeded") {
		t.Fatalf("expected client-recognized overflow signals, got %s", body)
	}
	if protocol == "anthropic" && !strings.Contains(body, `"type":"error"`) {
		t.Fatalf("expected Anthropic error envelope, got %s", body)
	}
}
