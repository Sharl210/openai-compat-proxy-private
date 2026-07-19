package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/upstream"
)

func TestAnthropicProjectionDropsOnlyTerminalTextLineEndings(t *testing.T) {
	rec := httptest.NewRecorder()
	flusher := flushRecorder{rec}
	state := &anthropicStreamState{}

	for _, delta := range []string{"\n", "123", "456", "78", "\n\n"} {
		if err := writeAnthropicEvent(rec, flusher, state, upstream.Event{Event: "response.output_text.delta", Data: map[string]any{"delta": delta}}, nil); err != nil {
			t.Fatalf("writeAnthropicEvent(%q): %v", delta, err)
		}
	}
	if err := writeAnthropicEvent(rec, flusher, state, upstream.Event{Event: "response.completed", Data: map[string]any{}}, nil); err != nil {
		t.Fatalf("writeAnthropicEvent(completed): %v", err)
	}

	if got := strings.Join(anthropicTextDeltasFromSSE(t, rec.Body.String()), ""); got != "\n12345678" {
		t.Fatalf("expected leading line ending preserved and only terminal line endings omitted, got %q; body=%s", got, rec.Body.String())
	}
}

func TestAnthropicProjectionPreservesMiddleBlankTextDeltas(t *testing.T) {
	rec := httptest.NewRecorder()
	flusher := flushRecorder{rec}
	state := &anthropicStreamState{}

	for _, delta := range []string{"code", "\n\n", "next"} {
		if err := writeAnthropicEvent(rec, flusher, state, upstream.Event{Event: "response.output_text.delta", Data: map[string]any{"delta": delta}}, nil); err != nil {
			t.Fatalf("writeAnthropicEvent(%q): %v", delta, err)
		}
	}
	if err := writeAnthropicEvent(rec, flusher, state, upstream.Event{Event: "response.completed", Data: map[string]any{}}, nil); err != nil {
		t.Fatalf("writeAnthropicEvent(completed): %v", err)
	}

	if got := strings.Join(anthropicTextDeltasFromSSE(t, rec.Body.String()), ""); got != "code\n\nnext" {
		t.Fatalf("expected middle blank text to be preserved, got %q; body=%s", got, rec.Body.String())
	}
}

func TestAnthropicProjectionDropsBlankOnlyTerminalText(t *testing.T) {
	rec := httptest.NewRecorder()
	flusher := flushRecorder{rec}
	state := &anthropicStreamState{}

	for _, delta := range []string{"\n", "\n\n"} {
		if err := writeAnthropicEvent(rec, flusher, state, upstream.Event{Event: "response.output_text.delta", Data: map[string]any{"delta": delta}}, nil); err != nil {
			t.Fatalf("writeAnthropicEvent(%q): %v", delta, err)
		}
	}
	if err := writeAnthropicEvent(rec, flusher, state, upstream.Event{Event: "response.completed", Data: map[string]any{}}, nil); err != nil {
		t.Fatalf("writeAnthropicEvent(completed): %v", err)
	}

	if got := strings.Join(anthropicTextDeltasFromSSE(t, rec.Body.String()), ""); got != "" {
		t.Fatalf("expected blank-only terminal text to be omitted, got %q; body=%s", got, rec.Body.String())
	}
}

type flushRecorder struct{ *httptest.ResponseRecorder }

func (f flushRecorder) Flush() {}

func anthropicTextDeltasFromSSE(t *testing.T, body string) []string {
	t.Helper()
	var out []string
	for _, frame := range strings.Split(body, "\n\n") {
		frame = strings.TrimSpace(frame)
		if frame == "" || !strings.Contains(frame, "event: content_block_delta") {
			continue
		}
		for _, line := range strings.Split(frame, "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload); err != nil {
				t.Fatalf("decode SSE payload: %v; line=%s", err, line)
			}
			delta, _ := payload["delta"].(map[string]any)
			if delta == nil || delta["type"] != "text_delta" {
				continue
			}
			text, _ := delta["text"].(string)
			out = append(out, text)
		}
	}
	return out
}
