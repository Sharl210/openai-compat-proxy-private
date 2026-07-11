package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

const upstreamContextOverflowCreatedEvent = "event: response.created\n" +
	"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_overflow\",\"status\":\"in_progress\"}}\n\n"

const upstreamContextOverflowInProgressEvent = "event: response.in_progress\n" +
	"data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_overflow\",\"status\":\"in_progress\"}}\n\n"

const upstreamContextOverflowFailedEvent = "event: response.failed\n" +
	"data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_overflow\",\"status\":\"failed\",\"error\":{\"type\":\"invalid_request_error\",\"code\":\"context_length_exceeded\",\"message\":\"prompt is too long: context_length_exceeded from upstream\",\"param\":\"input\"}}}\n\n"

type contextOverflowRouteCase struct {
	name            string
	path            string
	streamBody      string
	nonStreamBody   string
	setHeaders      func(*http.Request)
	terminalMarker  string
	successMarker   string
	nonStreamMarker string
}

func contextOverflowRouteCases() []contextOverflowRouteCase {
	return []contextOverflowRouteCase{
		{
			name:            "responses",
			path:            "/v1/responses",
			streamBody:      `{"model":"gpt-5","stream":true,"input":[{"role":"user","content":"hello"}]}`,
			nonStreamBody:   `{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`,
			terminalMarker:  "event: response.failed",
			successMarker:   "event: response.completed",
			nonStreamMarker: `"type":"proxy_error"`,
		},
		{
			name:            "chat completions",
			path:            "/v1/chat/completions",
			streamBody:      `{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`,
			nonStreamBody:   `{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`,
			terminalMarker:  `"finish_reason":"error"`,
			successMarker:   "data: [DONE]",
			nonStreamMarker: `"type":"proxy_error"`,
		},
		{
			name:          "anthropic messages",
			path:          "/v1/messages",
			streamBody:    `{"model":"gpt-5","stream":true,"max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`,
			nonStreamBody: `{"model":"gpt-5","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`,
			setHeaders: func(req *http.Request) {
				req.Header.Set("anthropic-version", "2023-06-01")
			},
			terminalMarker:  "event: error",
			successMarker:   "event: message_stop",
			nonStreamMarker: `"type":"invalid_request_error"`,
		},
	}
}

func assertContextOverflowSignals(t *testing.T, body string) {
	t.Helper()
	if !strings.Contains(body, "context_length_exceeded") || !strings.Contains(body, "prompt is too long") {
		t.Fatalf("expected client-recognized overflow signals, got %s", body)
	}
}
