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

// TestPayloadToSyntheticCanonicalEvents_Text demonstrates that a non-stream payload's
// text can be represented as synthetic canonical events and fed to the Collector,
// producing the same text as ResultFromResponsePayload.
//
// Currently this test FAILS because PayloadToSyntheticCanonicalEvents does not exist
// and ResultFromResponsePayload does not route through Collector.
func TestPayloadToSyntheticCanonicalEvents_Text(t *testing.T) {
	payload := map[string]any{
		"id":            "resp_payload_123",
		"finish_reason": "end_turn",
		"usage": map[string]any{
			"output_tokens": 10,
			"input_tokens":  5,
		},
		"output": []any{
			map[string]any{
				"type": "message",
				"id":   "msg_1",
				"content": []any{
					map[string]any{
						"type": "output_text",
						"text": "Hello from non-stream payload",
					},
				},
			},
		},
	}

	// The payload path already produces a result directly
	directResult, err := ResultFromResponsePayload(payload)
	if err != nil {
		t.Fatalf("ResultFromResponsePayload failed: %v", err)
	}

	// Synthetic canonical events should be convertible from the payload
	syntheticEvents := PayloadToSyntheticCanonicalEvents(payload)

	// Feed synthetic events to the same Collector that streaming uses
	c := NewCollector()
	for _, evt := range syntheticEvents {
		c.Accept(evt)
	}

	eventResult, err := c.Result()
	if err != nil {
		t.Fatalf("Collector.Result() from synthetic events failed: %v", err)
	}

	// Both paths must produce the same text
	if eventResult.Text != directResult.Text {
		t.Fatalf("Text mismatch: direct=%q, via synthetic events=%q", directResult.Text, eventResult.Text)
	}
	// Both paths must produce the same finish reason
	if eventResult.FinishReason != directResult.FinishReason {
		t.Fatalf("FinishReason mismatch: direct=%q, via synthetic events=%q", directResult.FinishReason, eventResult.FinishReason)
	}
	// Both paths must produce the same response ID
	if eventResult.ResponseID != directResult.ResponseID {
		t.Fatalf("ResponseID mismatch: direct=%q, via synthetic events=%q", directResult.ResponseID, eventResult.ResponseID)
	}
}

// TestPayloadToSyntheticCanonicalEvents_Refusal demonstrates that a non-stream payload's
// refusal can be represented as synthetic canonical events.
func TestPayloadToSyntheticCanonicalEvents_Refusal(t *testing.T) {
	payload := map[string]any{
		"id":            "resp_refusal_456",
		"finish_reason": "content_filter",
		"output": []any{
			map[string]any{
				"type": "message",
				"id":   "msg_2",
				"content": []any{
					map[string]any{
						"type":    "refusal",
						"refusal": "I cannot comply",
					},
				},
			},
		},
	}

	directResult, err := ResultFromResponsePayload(payload)
	if err != nil {
		t.Fatalf("ResultFromResponsePayload failed: %v", err)
	}

	syntheticEvents := PayloadToSyntheticCanonicalEvents(payload)

	c := NewCollector()
	for _, evt := range syntheticEvents {
		c.Accept(evt)
	}

	eventResult, err := c.Result()
	if err != nil {
		t.Fatalf("Collector.Result() from synthetic events failed: %v", err)
	}

	if eventResult.Refusal != directResult.Refusal {
		t.Fatalf("Refusal mismatch: direct=%q, via synthetic events=%q", directResult.Refusal, eventResult.Refusal)
	}
}

// TestPayloadToSyntheticCanonicalEvents_ToolCalls demonstrates that a non-stream payload's
// tool calls can be represented as synthetic canonical events.
func TestPayloadToSyntheticCanonicalEvents_ToolCalls(t *testing.T) {
	payload := map[string]any{
		"id":            "resp_tool_789",
		"finish_reason": "tool_calls",
		"output": []any{
			map[string]any{
				"type":      "function_call",
				"id":        "call_abc",
				"name":      "get_weather",
				"arguments": `{"location":"Shanghai"}`,
			},
		},
	}

	directResult, err := ResultFromResponsePayload(payload)
	if err != nil {
		t.Fatalf("ResultFromResponsePayload failed: %v", err)
	}

	syntheticEvents := PayloadToSyntheticCanonicalEvents(payload)

	c := NewCollector()
	for _, evt := range syntheticEvents {
		c.Accept(evt)
	}

	eventResult, err := c.Result()
	if err != nil {
		t.Fatalf("Collector.Result() from synthetic events failed: %v", err)
	}

	if len(eventResult.ToolCalls) != len(directResult.ToolCalls) {
		t.Fatalf("ToolCalls count mismatch: direct=%d, via synthetic events=%d", len(directResult.ToolCalls), len(eventResult.ToolCalls))
	}
	if len(eventResult.ToolCalls) > 0 {
		if eventResult.ToolCalls[0].Name != directResult.ToolCalls[0].Name {
			t.Fatalf("ToolCall[0].Name mismatch: direct=%q, via synthetic events=%q", directResult.ToolCalls[0].Name, eventResult.ToolCalls[0].Name)
		}
		if eventResult.ToolCalls[0].Arguments != directResult.ToolCalls[0].Arguments {
			t.Fatalf("ToolCall[0].Arguments mismatch: direct=%q, via synthetic events=%q", directResult.ToolCalls[0].Arguments, eventResult.ToolCalls[0].Arguments)
		}
	}
}

// TestPayloadToSyntheticCanonicalEvents_Usage demonstrates that a non-stream payload's
// usage can be represented as synthetic canonical events.
func TestPayloadToSyntheticCanonicalEvents_Usage(t *testing.T) {
	payload := map[string]any{
		"id":            "resp_usage_999",
		"finish_reason": "end_turn",
		"usage": map[string]any{
			"output_tokens": 42,
			"input_tokens":  17,
		},
		"output": []any{
			map[string]any{
				"type": "message",
				"id":   "msg_3",
				"content": []any{
					map[string]any{
						"type": "output_text",
						"text": "Done",
					},
				},
			},
		},
	}

	directResult, err := ResultFromResponsePayload(payload)
	if err != nil {
		t.Fatalf("ResultFromResponsePayload failed: %v", err)
	}

	syntheticEvents := PayloadToSyntheticCanonicalEvents(payload)

	c := NewCollector()
	for _, evt := range syntheticEvents {
		c.Accept(evt)
	}

	eventResult, err := c.Result()
	if err != nil {
		t.Fatalf("Collector.Result() from synthetic events failed: %v", err)
	}

	if len(eventResult.Usage) != len(directResult.Usage) {
		t.Fatalf("Usage length mismatch: direct=%v, via synthetic events=%v", directResult.Usage, eventResult.Usage)
	}
}

// TestPayloadToSyntheticCanonicalEvents_Reasoning demonstrates that a non-stream payload's
// reasoning summary can be represented as synthetic canonical events.
func TestPayloadToSyntheticCanonicalEvents_Reasoning(t *testing.T) {
	payload := map[string]any{
		"id":            "resp_reasoning_555",
		"finish_reason": "end_turn",
		"reasoning": map[string]any{
			"summary": "I considered the options",
		},
		"output": []any{
			map[string]any{
				"type": "message",
				"id":   "msg_4",
				"content": []any{
					map[string]any{
						"type": "output_text",
						"text": "Final answer",
					},
				},
			},
		},
	}

	directResult, err := ResultFromResponsePayload(payload)
	if err != nil {
		t.Fatalf("ResultFromResponsePayload failed: %v", err)
	}

	syntheticEvents := PayloadToSyntheticCanonicalEvents(payload)

	c := NewCollector()
	for _, evt := range syntheticEvents {
		c.Accept(evt)
	}

	eventResult, err := c.Result()
	if err != nil {
		t.Fatalf("Collector.Result() from synthetic events failed: %v", err)
	}

	if eventResult.Reasoning == nil {
		t.Fatal("Reasoning should not be nil via synthetic events")
	}
	directSummary := ""
	if s, ok := directResult.Reasoning["summary"].(string); ok {
		directSummary = s
	}
	eventSummary := ""
	if s, ok := eventResult.Reasoning["summary"].(string); ok {
		eventSummary = s
	}
	if eventSummary != directSummary {
		t.Fatalf("Reasoning summary mismatch: direct=%q, via synthetic events=%q", directSummary, eventSummary)
	}
}

// TestPayloadToSyntheticCanonicalEvents_SyntheticFlag verifies that synthetic events
// carry ProviderMeta["synthetic"]=true so they can be distinguished from real upstream events.
func TestPayloadToSyntheticCanonicalEvents_SyntheticFlag(t *testing.T) {
	payload := map[string]any{
		"id":            "resp_synth_666",
		"finish_reason": "end_turn",
		"output": []any{
			map[string]any{
				"type": "message",
				"id":   "msg_5",
				"content": []any{
					map[string]any{
						"type": "output_text",
						"text": "Synthetic test",
					},
				},
			},
		},
	}

	syntheticEvents := PayloadToSyntheticCanonicalEvents(payload)

	if len(syntheticEvents) == 0 {
		t.Fatal("PayloadToSyntheticCanonicalEvents returned no events")
	}

	// At least one event should carry the synthetic flag in ProviderMeta
	foundSynthetic := false
	for _, evt := range syntheticEvents {
		providerMeta, ok := evt.Data["provider_meta"].(map[string]any)
		if !ok {
			continue
		}
		if synthetic, ok := providerMeta["synthetic"].(bool); ok && synthetic {
			foundSynthetic = true
			break
		}
	}
	if !foundSynthetic {
		t.Error("No synthetic canonical event found with ProviderMeta[synthetic]=true")
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
