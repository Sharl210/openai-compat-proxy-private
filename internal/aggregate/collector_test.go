package aggregate

import (
	"testing"

	"openai-compat-proxy/internal/upstream"
)

func TestCollectorFillsTextFromOutputItemDoneMessageOutputText(t *testing.T) {
	c := NewCollector()

	c.Accept(upstream.Event{
		Event: "response.created",
		Data: map[string]any{
			"response": map[string]any{"id": "resp_123"},
		},
	})

	c.Accept(upstream.Event{
		Event: "response.output_item.done",
		Data: map[string]any{
			"item": map[string]any{
				"id":   "msg_1",
				"type": "message",
				"content": []any{
					map[string]any{
						"type": "output_text",
						"text": "hello from message",
					},
				},
			},
		},
	})

	c.Accept(upstream.Event{
		Event: "response.completed",
		Data: map[string]any{
			"response": map[string]any{"finish_reason": "end_turn"},
		},
	})

	result, err := c.Result()
	if err != nil {
		t.Fatalf("Collector.Result() returned error: %v", err)
	}
	if result.Text != "hello from message" {
		t.Fatalf("expected Result.Text to be 'hello from message', got %q", result.Text)
	}
}

func TestCollectorFillsRefusalFromOutputItemDoneMessageRefusal(t *testing.T) {
	c := NewCollector()

	c.Accept(upstream.Event{
		Event: "response.created",
		Data: map[string]any{
			"response": map[string]any{"id": "resp_123"},
		},
	})

	c.Accept(upstream.Event{
		Event: "response.output_item.done",
		Data: map[string]any{
			"item": map[string]any{
				"id":   "msg_1",
				"type": "message",
				"content": []any{
					map[string]any{
						"type":    "refusal",
						"refusal": "I cannot help with that",
					},
				},
			},
		},
	})

	c.Accept(upstream.Event{
		Event: "response.completed",
		Data: map[string]any{
			"response": map[string]any{"finish_reason": "end_turn"},
		},
	})

	result, err := c.Result()
	if err != nil {
		t.Fatalf("Collector.Result() returned error: %v", err)
	}
	if result.Refusal != "I cannot help with that" {
		t.Fatalf("expected Result.Refusal to be 'I cannot help with that', got %q", result.Refusal)
	}
}

func TestCollectorFillsTextFromOutputTextDelta(t *testing.T) {
	c := NewCollector()

	c.Accept(upstream.Event{
		Event: "response.created",
		Data: map[string]any{
			"response": map[string]any{"id": "resp_123"},
		},
	})

	c.Accept(upstream.Event{
		Event: "response.output_text.delta",
		Data:  map[string]any{"delta": "hello from delta"},
	})

	c.Accept(upstream.Event{
		Event: "response.completed",
		Data: map[string]any{
			"response": map[string]any{"finish_reason": "end_turn"},
		},
	})

	result, err := c.Result()
	if err != nil {
		t.Fatalf("Collector.Result() returned error: %v", err)
	}
	if result.Text != "hello from delta" {
		t.Fatalf("expected Result.Text to be 'hello from delta', got %q", result.Text)
	}
}
