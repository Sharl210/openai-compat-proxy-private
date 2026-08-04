package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"openai-compat-proxy/internal/model"
)

func TestImplicitSessionResolveDeduplicatesStatesWithinOneSession(t *testing.T) {
	store := newImplicitSessionStore()
	anchoredHistory := []model.CanonicalMessage{
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "one"}}},
		{Role: "assistant", Parts: []model.CanonicalContentPart{{Type: "text", Text: "ok"}}},
	}
	continuedHistory := append(append([]model.CanonicalMessage{}, anchoredHistory...), model.CanonicalMessage{
		Role:  "user",
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "two"}},
	})

	store.observe("request-1", "session-1", "/v1/chat/completions", "caller-1", anchoredHistory)
	store.markCompleted("request-1")
	store.observe("request-2", "session-1", "/v1/chat/completions", "caller-1", anchoredHistory)
	store.markCompleted("request-2")

	if got := store.resolve("/v1/chat/completions", "caller-1", continuedHistory); got != "session-1" {
		t.Fatalf("expected duplicate states from one session to resolve to session-1, got %q", got)
	}
}

func TestImplicitSessionResolveRefusesAmbiguousMatchingSessions(t *testing.T) {
	store := newImplicitSessionStore()
	history := []model.CanonicalMessage{
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "one"}}},
		{Role: "assistant", Parts: []model.CanonicalContentPart{{Type: "text", Text: "ok"}}},
	}
	continuedHistory := append(append([]model.CanonicalMessage{}, history...), model.CanonicalMessage{
		Role:  "user",
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "two"}},
	})

	store.observe("request-1", "session-1", "/v1/chat/completions", "caller-1", history)
	store.markCompleted("request-1")
	store.observe("request-2", "session-2", "/v1/chat/completions", "caller-1", history)
	store.markCompleted("request-2")

	if got := store.resolve("/v1/chat/completions", "caller-1", continuedHistory); got != "" {
		t.Fatalf("expected ambiguous matching sessions to remain unresolved, got %q", got)
	}
}

func TestImplicitSessionResponsesInputDoesNotReuseUnanchoredHistory(t *testing.T) {
	store := newImplicitSessionStore()
	history := newImplicitSessionHistory(nil, []map[string]any{{
		"type": "message",
		"role": "user",
		"content": []any{
			map[string]any{"type": "input_text", "text": "one"},
		},
	}})

	store.observeHistory("request-1", "session-1", "/v1/responses", "caller-1", history)
	store.markCompleted("request-1")
	if got := store.resolveHistory("/v1/responses", "caller-1", history); got != "" {
		t.Fatalf("expected an unanchored Responses history to remain unresolved, got %q", got)
	}
}

func TestImplicitSessionResolveAllowsReusableStateBeforeCompletion(t *testing.T) {
	store := newImplicitSessionStore()
	history := newImplicitSessionHistory(nil, []map[string]any{
		{"type": "message", "role": "user", "content": []any{"one"}},
		{"type": "message", "role": "assistant", "content": []any{"ok"}},
	})
	continuedHistory := newImplicitSessionHistory(nil, []map[string]any{
		{"type": "message", "role": "user", "content": []any{"one"}},
		{"type": "message", "role": "assistant", "content": []any{"ok"}},
		{"type": "message", "role": "user", "content": []any{"two"}},
	})

	store.observeHistory("request-1", "session-1", "/v1/responses", "caller-1", history)
	store.markReusable("request-1")

	if got := store.resolveHistoryDetailed("/v1/responses", "caller-1", continuedHistory); got.SessionID != "session-1" || got.RequestUID != "request-1" {
		t.Fatalf("expected reusable in-flight history to resolve to request-1 in session-1, got %#v", got)
	}
	if store.states["request-1"].completed {
		t.Fatal("expected reusable in-flight history to remain incomplete")
	}
}

func TestImplicitSessionResolveRejectsUnsuccessfulFinishedState(t *testing.T) {
	store := newImplicitSessionStore()
	history := newImplicitSessionHistory(nil, []map[string]any{
		{"type": "message", "role": "user", "content": []any{"one"}},
		{"type": "message", "role": "assistant", "content": []any{"ok"}},
	})
	continuedHistory := newImplicitSessionHistory(nil, []map[string]any{
		{"type": "message", "role": "user", "content": []any{"one"}},
		{"type": "message", "role": "assistant", "content": []any{"ok"}},
		{"type": "message", "role": "user", "content": []any{"two"}},
	})

	store.observeHistory("request-1", "session-1", "/v1/responses", "caller-1", history)
	store.markFinished("request-1", false)

	if got := store.resolveHistory("/v1/responses", "caller-1", continuedHistory); got != "" {
		t.Fatalf("expected unsuccessful history not to resolve, got %q", got)
	}
}

func TestImplicitSessionFinalFailureClearsEarlyReusableStateAcrossProtocols(t *testing.T) {
	tests := []struct {
		name      string
		route     string
		event     string
		data      map[string]any
		chatDelta map[string]any
	}{
		{
			name:  "responses",
			route: "/v1/responses",
			event: "response.output_text.delta",
			data:  map[string]any{"delta": "partial response"},
		},
		{
			name:      "chat",
			route:     "/v1/chat/completions",
			chatDelta: map[string]any{"content": "partial response"},
		},
		{
			name:  "anthropic",
			route: "/v1/messages",
			event: "content_block_delta",
			data: map[string]any{
				"delta": map[string]any{"type": "text_delta", "text": "partial response"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newRequestLineageStore()
			history := newImplicitSessionHistory([]model.CanonicalMessage{
				{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "one"}}},
				{Role: "assistant", Parts: []model.CanonicalContentPart{{Type: "text", Text: "ok"}}},
			}, nil)
			continuedHistory := newImplicitSessionHistory([]model.CanonicalMessage{
				{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "one"}}},
				{Role: "assistant", Parts: []model.CanonicalContentPart{{Type: "text", Text: "ok"}}},
				{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "two"}}},
			}, nil)
			store.implicitSessions.observeHistory("request-1", "session-1", test.route, "caller-1", history)

			writer := &responseCaptureWriter{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
			if test.chatDelta != nil {
				markSuccessfulChatChunk(writer, test.chatDelta)
			} else {
				markSuccessfulDownstreamEvent(writer, test.event, test.data)
			}
			if !writer.hasSuccessfulDownstreamOutput() {
				t.Fatal("expected partial output to make the in-flight state reusable")
			}
			store.implicitSessions.markReusable("request-1")
			if got := store.implicitSessions.resolveHistoryDetailed(test.route, "caller-1", continuedHistory); got.SessionID != "session-1" || got.RequestUID != "request-1" {
				t.Fatalf("expected in-flight state to resolve before final status, got %#v", got)
			}

			store.implicitSessions.markFinished("request-1", false)
			if got := store.implicitSessions.resolveHistory(test.route, "caller-1", continuedHistory); got != "" {
				t.Fatalf("expected failed final status to clear reusable state, got %q", got)
			}
		})
	}
}

func TestImplicitSessionStreamingLifecycleAndErrorEventsDoNotMakeStateReusable(t *testing.T) {
	tests := []struct {
		name   string
		event  string
		data   map[string]any
		commit bool
	}{
		{name: "responses created", event: "response.created"},
		{name: "responses in progress", event: "response.in_progress"},
		{name: "responses failed", event: "response.failed"},
		{name: "responses incomplete", event: "response.incomplete"},
		{name: "responses empty message item", event: "response.output_item.added", data: map[string]any{
			"item": map[string]any{"type": "message", "content": []any{}},
		}},
		{name: "responses empty reasoning item", event: "response.output_item.added", data: map[string]any{
			"item": map[string]any{"type": "reasoning", "summary": []any{}},
		}},
		{name: "responses function arguments delta", event: "response.function_call_arguments.delta", data: map[string]any{
			"delta": `{"query":`,
		}},
		{name: "responses empty content part", event: "response.content_part.done", data: map[string]any{
			"part": map[string]any{"type": "output_text", "text": " \n"},
		}},
		{name: "anthropic message start", event: "message_start"},
		{name: "anthropic content block start", event: "content_block_start"},
		{name: "anthropic content block stop", event: "content_block_stop"},
		{name: "anthropic message delta", event: "message_delta"},
		{name: "anthropic message stop", event: "message_stop"},
		{name: "anthropic error", event: "error"},
		{name: "anthropic ping", event: "ping"},
		{name: "responses text delta", event: "response.output_text.delta", data: map[string]any{
			"delta": "answer",
		}, commit: true},
		{name: "responses function arguments done", event: "response.function_call_arguments.done", data: map[string]any{
			"arguments": `{"query":"weather"}`,
		}, commit: true},
		{name: "anthropic content block delta", event: "content_block_delta", data: map[string]any{
			"delta": map[string]any{"type": "text_delta", "text": "answer"},
		}, commit: true},
		{name: "anthropic signature delta", event: "content_block_delta", data: map[string]any{
			"delta": map[string]any{"type": "signature_delta", "signature": "opaque-signature"},
		}},
		{name: "anthropic empty thinking delta", event: "content_block_delta", data: map[string]any{
			"delta": map[string]any{"type": "thinking_delta", "thinking": " \n"},
		}},
		{name: "anthropic partial input json delta", event: "content_block_delta", data: map[string]any{
			"delta": map[string]any{"type": "input_json_delta", "partial_json": `{"query":`},
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writer := &responseCaptureWriter{
				ResponseWriter: httptest.NewRecorder(),
				status:         200,
			}
			markSuccessfulDownstreamEvent(writer, test.event, test.data)
			if got := writer.hasSuccessfulDownstreamOutput(); got != test.commit {
				t.Fatalf("event %q committed output = %t, want %t", test.event, got, test.commit)
			}
		})
	}
}

func TestImplicitSessionChatLifecycleChunksDoNotMakeStateReusable(t *testing.T) {
	tests := []struct {
		name   string
		delta  map[string]any
		finish string
		want   bool
	}{
		{name: "role-only chunk", delta: map[string]any{"role": "assistant"}},
		{name: "empty terminal chunk", delta: map[string]any{}, finish: "stop"},
		{name: "error chunk", delta: map[string]any{"error": map[string]any{"message": "failed"}}, finish: "error"},
		{name: "text chunk", delta: map[string]any{"content": "answer"}, want: true},
		{name: "tool metadata chunk", delta: map[string]any{"tool_calls": []map[string]any{{"id": "call_1"}}}, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writer := &responseCaptureWriter{
				ResponseWriter: httptest.NewRecorder(),
				status:         200,
			}
			writer.Header().Set("Content-Type", "text/event-stream")
			if err := writeChatChunk(writer, writer, nil, test.delta, test.finish, nil); err != nil {
				t.Fatalf("write chat chunk: %v", err)
			}
			if got := writer.hasSuccessfulDownstreamOutput(); got != test.want {
				t.Fatalf("chat delta %#v finish %q committed output = %t, want %t", test.delta, test.finish, got, test.want)
			}
		})
	}
}
