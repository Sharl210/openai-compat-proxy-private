package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"openai-compat-proxy/internal/testutil"
)

const upstreamCompletedEvent = "event: response.completed\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_completed\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"

func TestStreamingContextOverflow_returnsHTTP400BeforeSSE_whenUpstreamSendsLifecycleOverflow(t *testing.T) {
	for _, tc := range contextOverflowRouteCases() {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			upstream := testutil.NewStreamingUpstream(t, []string{
				upstreamContextOverflowCreatedEvent,
				upstreamContextOverflowInProgressEvent,
				upstreamContextOverflowEvent,
				upstreamContextOverflowFailedEvent,
			})
			defer upstream.Close()
			server := NewServer(testContextOverflowStreamConfig(upstream.URL))
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.streamBody))
			req.Header.Set("Content-Type", "application/json")
			if tc.setHeaders != nil {
				tc.setHeaders(req)
			}
			rec := httptest.NewRecorder()

			// When
			server.ServeHTTP(rec, req)

			// Then
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected lifecycle context overflow to return HTTP 400 before SSE, got %d body=%s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if !json.Valid([]byte(body)) {
				t.Fatalf("expected JSON error body before SSE, got %s", body)
			}
			assertContextOverflowSignals(t, body)
			for _, marker := range []string{
				"event:",
				"response.created",
				"response.in_progress",
				"response.failed",
				"\"id\":\"rs_proxy\"",
				"\"type\":\"message_start\"",
				tc.terminalMarker,
			} {
				if strings.Contains(body, marker) {
					t.Fatalf("expected no downstream SSE or lifecycle marker %q before HTTP error, got %s", marker, body)
				}
			}
			if got := rec.Header().Get("X-Accel-Buffering"); got != "" {
				t.Fatalf("expected SSE headers to remain unset before HTTP error, got X-Accel-Buffering=%q", got)
			}
		})
	}
}

func TestStreamingFirstTerminalWins_whenSuccessPrecedesFailure(t *testing.T) {
	for _, tc := range contextOverflowRouteCases() {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			upstream := testutil.NewStreamingUpstream(t, []string{
				upstreamContextOverflowCreatedEvent,
				upstreamContextOverflowInProgressEvent,
				upstreamCompletedEvent,
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

			// When
			server.ServeHTTP(rec, req)

			// Then
			if rec.Code != http.StatusOK {
				t.Fatalf("expected successful first terminal to preserve SSE status, got %d body=%s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if strings.Contains(body, "context_length_exceeded") || strings.Contains(body, "prompt is too long") {
				t.Fatalf("expected events after the first successful terminal to be ignored, got %s", body)
			}
		})
	}
}

func TestStreamingStopsHeartbeat_whenUpstreamStaysOpenAfterTerminal(t *testing.T) {
	oldTickInterval := syntheticReasoningTickInterval
	oldHeartbeatInterval := sseHeartbeatInterval
	syntheticReasoningTickInterval = time.Hour
	sseHeartbeatInterval = 5 * time.Millisecond
	defer func() {
		syntheticReasoningTickInterval = oldTickInterval
		sseHeartbeatInterval = oldHeartbeatInterval
	}()

	for _, tc := range contextOverflowRouteCases() {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(upstreamCompletedEvent))
				w.(http.Flusher).Flush()
				time.Sleep(30 * time.Millisecond)
			}))
			defer upstream.Close()
			server := NewServer(testContextOverflowStreamConfig(upstream.URL))
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.streamBody))
			req.Header.Set("Content-Type", "application/json")
			if tc.setHeaders != nil {
				tc.setHeaders(req)
			}
			rec := httptest.NewRecorder()

			// When
			server.ServeHTTP(rec, req)

			// Then
			postTerminal := bytesAfterSSETerminal(t, rec.Body.String(), tc.successMarker)
			if postTerminal != "" {
				t.Fatalf("expected no bytes after terminal, got %q", postTerminal)
			}
		})
	}
}

func TestStreamingStopsOutput_whenMalformedSSEFollowsTerminal(t *testing.T) {
	for _, tc := range contextOverflowRouteCases() {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(upstreamCompletedEvent))
				_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\n\n"))
				w.(http.Flusher).Flush()
			}))
			defer upstream.Close()
			server := NewServer(testContextOverflowStreamConfig(upstream.URL))
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.streamBody))
			req.Header.Set("Content-Type", "application/json")
			if tc.setHeaders != nil {
				tc.setHeaders(req)
			}
			rec := httptest.NewRecorder()

			// When
			server.ServeHTTP(rec, req)

			// Then
			postTerminal := bytesAfterSSETerminal(t, rec.Body.String(), tc.successMarker)
			if postTerminal != "" {
				t.Fatalf("expected malformed post-terminal SSE not to produce downstream bytes, got %q", postTerminal)
			}
		})
	}
}

func bytesAfterSSETerminal(t *testing.T, body, marker string) string {
	t.Helper()
	terminalIndex := strings.Index(body, marker)
	if terminalIndex < 0 {
		t.Fatalf("expected terminal marker %q, got %s", marker, body)
	}
	frameEndOffset := strings.Index(body[terminalIndex:], "\n\n")
	if frameEndOffset < 0 {
		t.Fatalf("expected terminal frame boundary after %q, got %s", marker, body)
	}
	return body[terminalIndex+frameEndOffset+2:]
}
