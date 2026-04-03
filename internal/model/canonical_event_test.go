package model

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCanonicalEventFields(t *testing.T) {
	// Verify CanonicalEvent struct has all required fields per spec
	evt := CanonicalEvent{
		Seq:            1,
		Ts:             time.Now(),
		Type:           "message.delta",
		ItemID:         "item-123",
		CallID:         "call-456",
		Role:           "assistant",
		TextDelta:      "Hello",
		ReasoningDelta: "thinking...",
		ToolName:       "get_weather",
		ToolArgsDelta:  "{\"city\":",
		UsageDelta:     map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
		FinishReason:   "stop",
		Error:          map[string]any{"kind": "upstreamStreamBroken", "message": "stream ended"},
		RawEventName:   "response.output_text.delta",
		RawPayload:     json.RawMessage(`{"delta": "Hello"}`),
		ProviderMeta:   map[string]any{"provider_id": "openai", "synthetic": false},
	}

	if evt.Seq != 1 {
		t.Errorf("expected Seq=1, got %d", evt.Seq)
	}
	if evt.Type != "message.delta" {
		t.Errorf("expected Type='message.delta', got %s", evt.Type)
	}
	if evt.ItemID != "item-123" {
		t.Errorf("expected ItemID='item-123', got %s", evt.ItemID)
	}
	if evt.CallID != "call-456" {
		t.Errorf("expected CallID='call-456', got %s", evt.CallID)
	}
	if evt.Role != "assistant" {
		t.Errorf("expected Role='assistant', got %s", evt.Role)
	}
	if evt.TextDelta != "Hello" {
		t.Errorf("expected TextDelta='Hello', got %s", evt.TextDelta)
	}
	if evt.ReasoningDelta != "thinking..." {
		t.Errorf("expected ReasoningDelta='thinking...', got %s", evt.ReasoningDelta)
	}
	if evt.ToolName != "get_weather" {
		t.Errorf("expected ToolName='get_weather', got %s", evt.ToolName)
	}
	if evt.ToolArgsDelta != "{\"city\":" {
		t.Errorf("expected ToolArgsDelta='{\"city\":', got %s", evt.ToolArgsDelta)
	}
	if evt.FinishReason != "stop" {
		t.Errorf("expected FinishReason='stop', got %s", evt.FinishReason)
	}
	if evt.RawEventName != "response.output_text.delta" {
		t.Errorf("expected RawEventName='response.output_text.delta', got %s", evt.RawEventName)
	}
	if evt.ProviderMeta["synthetic"] != false {
		t.Errorf("expected ProviderMeta[synthetic]=false")
	}
}

func TestCanonicalEnvelopeFields(t *testing.T) {
	// Verify CanonicalEnvelope struct has all required fields per spec
	env := CanonicalEnvelope{
		RequestID:        "req-abc-123",
		UpstreamProtocol: "responses",
		ProviderID:       "openai",
		Model:            "gpt-4o",
		Events:           []CanonicalEvent{},
		ProviderMeta:     map[string]any{"endpoint": "/v1/responses"},
	}

	if env.RequestID != "req-abc-123" {
		t.Errorf("expected RequestID='req-abc-123', got %s", env.RequestID)
	}
	if env.UpstreamProtocol != "responses" {
		t.Errorf("expected UpstreamProtocol='responses', got %s", env.UpstreamProtocol)
	}
	if env.ProviderID != "openai" {
		t.Errorf("expected ProviderID='openai', got %s", env.ProviderID)
	}
	if env.Model != "gpt-4o" {
		t.Errorf("expected Model='gpt-4o', got %s", env.Model)
	}
	if env.Events == nil {
		t.Error("expected Events to be initialized slice, got nil")
	}
	if env.ProviderMeta["endpoint"] != "/v1/responses" {
		t.Errorf("expected ProviderMeta[endpoint]='/v1/responses'")
	}
}

func TestCanonicalEnvelopeAppendEvent(t *testing.T) {
	env := CanonicalEnvelope{}
	if len(env.Events) != 0 {
		t.Errorf("expected empty Events initially, got %d", len(env.Events))
	}

	// Append should work without panic
	env.AppendEvent(CanonicalEvent{Seq: 1, Type: "message.start"})
	if len(env.Events) != 1 {
		t.Errorf("expected 1 event after AppendEvent, got %d", len(env.Events))
	}
	if env.Events[0].Seq != 1 {
		t.Errorf("expected first event Seq=1, got %d", env.Events[0].Seq)
	}

	// Append more events
	env.AppendEvent(CanonicalEvent{Seq: 2, Type: "message.delta"})
	if len(env.Events) != 2 {
		t.Errorf("expected 2 events after second AppendEvent, got %d", len(env.Events))
	}
}

func TestCanonicalEventClone(t *testing.T) {
	evt := CanonicalEvent{
		Seq:            1,
		Type:           "message.delta",
		ItemID:         "item-123",
		TextDelta:      "Hello",
		ReasoningDelta: "thinking...",
		ToolName:       "get_weather",
		ToolArgsDelta:  "{\"city\":",
		UsageDelta:     map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
		FinishReason:   "stop",
		Error:          map[string]any{"kind": "upstreamStreamBroken", "status": 500},
		RawEventName:   "response.output_text.delta",
		RawPayload:     json.RawMessage(`{"delta": "Hello"}`),
		ProviderMeta:   map[string]any{"provider_id": "openai", "synthetic": false},
	}

	clone := evt.Clone()

	if clone.Seq != evt.Seq {
		t.Errorf("clone Seq mismatch: got %d, want %d", clone.Seq, evt.Seq)
	}
	if clone.Type != evt.Type {
		t.Errorf("clone Type mismatch: got %s, want %s", clone.Type, evt.Type)
	}
	if clone.TextDelta != evt.TextDelta {
		t.Errorf("clone TextDelta mismatch: got %s, want %s", clone.TextDelta, evt.TextDelta)
	}
	if clone.ItemID != evt.ItemID {
		t.Errorf("clone ItemID mismatch: got %s, want %s", clone.ItemID, evt.ItemID)
	}

	clone.Seq = 999
	clone.TextDelta = "modified"
	if evt.Seq == 999 {
		t.Error("clone is not a deep clone: modifying clone affected original Seq")
	}
	if evt.TextDelta == "modified" {
		t.Error("clone is not a deep clone: modifying clone affected original TextDelta")
	}

	clone.UsageDelta["prompt_tokens"] = 999
	if evt.UsageDelta["prompt_tokens"] == 999 {
		t.Error("clone is not a deep clone: modifying clone UsageDelta affected original")
	}

	clone.Error["kind"] = "modified"
	if evt.Error["kind"] == "modified" {
		t.Error("clone is not a deep clone: modifying clone Error affected original")
	}

	clone.ProviderMeta["provider_id"] = "modified"
	if evt.ProviderMeta["provider_id"] == "modified" {
		t.Error("clone is not a deep clone: modifying clone ProviderMeta affected original")
	}

	clone.RawPayload[0] = 'X'
	if evt.RawPayload[0] == 'X' {
		t.Error("clone is not a deep clone: modifying clone RawPayload affected original")
	}
}

func TestCanonicalEnvelopeClone(t *testing.T) {
	env := CanonicalEnvelope{
		RequestID:        "req-123",
		UpstreamProtocol: "chat",
		ProviderID:       "openai",
		Model:            "gpt-4o",
		Events: []CanonicalEvent{
			{Seq: 1, Type: "message.start", TextDelta: "Hello"},
			{Seq: 2, Type: "message.delta", TextDelta: " world"},
		},
		ProviderMeta: map[string]any{"provider_id": "openai"},
	}

	clone := env.Clone()

	// Verify clone has same values
	if clone.RequestID != env.RequestID {
		t.Errorf("clone RequestID mismatch: got %s, want %s", clone.RequestID, env.RequestID)
	}
	if clone.UpstreamProtocol != env.UpstreamProtocol {
		t.Errorf("clone UpstreamProtocol mismatch: got %s, want %s", clone.UpstreamProtocol, env.UpstreamProtocol)
	}
	if len(clone.Events) != len(env.Events) {
		t.Errorf("clone Events length mismatch: got %d, want %d", len(clone.Events), len(env.Events))
	}

	// Verify it's a deep clone
	clone.Events[0].Seq = 999
	clone.Events[0].TextDelta = "modified"
	clone.RequestID = "modified"
	if env.Events[0].Seq == 999 {
		t.Error("clone is not a deep clone: modifying clone affected original Events")
	}
	if env.Events[0].TextDelta == "modified" {
		t.Error("clone is not a deep clone: modifying clone affected original TextDelta")
	}
	if env.RequestID == "modified" {
		t.Error("clone is not a deep clone: modifying clone affected original RequestID")
	}

	clone.ProviderMeta["provider_id"] = "modified"
	if env.ProviderMeta["provider_id"] == "modified" {
		t.Error("clone is not a deep clone: modifying clone ProviderMeta affected original")
	}
}

func TestCanonicalEventTypes(t *testing.T) {
	// Verify canonical event type vocabulary per spec
	validTypes := []string{
		"response.start",
		"message.start",
		"message.delta",
		"message.done",
		"reasoning.start",
		"reasoning.delta",
		"reasoning.done",
		"tool_call.start",
		"tool_call.arguments.delta",
		"tool_call.done",
		"tool_result.start",
		"tool_result.delta",
		"tool_result.done",
		"usage.update",
		"response.completed",
		"response.incomplete",
		"response.error",
	}

	for _, typ := range validTypes {
		evt := CanonicalEvent{Type: typ}
		if evt.Type != typ {
			t.Errorf("expected Type=%s, got %s", typ, evt.Type)
		}
	}
}

func TestCanonicalEnvelopeWithEvents(t *testing.T) {
	env := CanonicalEnvelope{
		RequestID:        "req-test",
		UpstreamProtocol: "responses",
		ProviderID:       "test-provider",
		Model:            "test-model",
		Events:           make([]CanonicalEvent, 0, 10),
	}

	// Simulate a streaming sequence
	events := []CanonicalEvent{
		{Seq: 1, Type: "response.start", RawEventName: "response.created"},
		{Seq: 2, Type: "message.start", ItemID: "msg-1", Role: "assistant"},
		{Seq: 3, Type: "message.delta", ItemID: "msg-1", TextDelta: "Hello"},
		{Seq: 4, Type: "message.delta", ItemID: "msg-1", TextDelta: " world"},
		{Seq: 5, Type: "message.done", ItemID: "msg-1"},
		{Seq: 6, Type: "usage.update", UsageDelta: map[string]any{"completion_tokens": 5}},
		{Seq: 7, Type: "response.completed", FinishReason: "stop"},
	}

	for _, e := range events {
		env.AppendEvent(e)
	}

	if len(env.Events) != 7 {
		t.Errorf("expected 7 events, got %d", len(env.Events))
	}

	// Verify sequence
	for i, expectedSeq := range []int64{1, 2, 3, 4, 5, 6, 7} {
		if env.Events[i].Seq != expectedSeq {
			t.Errorf("event[%d] expected Seq=%d, got %d", i, expectedSeq, env.Events[i].Seq)
		}
	}

	// Verify text accumulation
	text := ""
	for _, e := range env.Events {
		text += e.TextDelta
	}
	if text != "Hello world" {
		t.Errorf("expected accumulated text 'Hello world', got '%s'", text)
	}
}

func TestCanonicalEventJSONRoundtrip(t *testing.T) {
	evt := CanonicalEvent{
		Seq:            42,
		Ts:             time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		Type:           "message.delta",
		ItemID:         "item-abc",
		CallID:         "call-123",
		Role:           "assistant",
		TextDelta:      "Hi there!",
		ReasoningDelta: "Let me think...",
		ToolName:       "calculator",
		ToolArgsDelta:  "{\"expr\":",
		UsageDelta:     map[string]any{"prompt_tokens": float64(100), "completion_tokens": float64(20)},
		FinishReason:   "stop",
		Error:          nil,
		RawEventName:   "response.output_text.delta",
		RawPayload:     json.RawMessage(`{"delta": "Hi there!"}`),
		ProviderMeta:   map[string]any{"provider": "test", "region": "us-east"},
	}

	// Marshal
	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("failed to marshal CanonicalEvent: %v", err)
	}

	// Unmarshal
	var decoded CanonicalEvent
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("failed to unmarshal CanonicalEvent: %v", err)
	}

	// Verify
	if decoded.Seq != evt.Seq {
		t.Errorf("Seq mismatch after roundtrip: got %d, want %d", decoded.Seq, evt.Seq)
	}
	if decoded.Type != evt.Type {
		t.Errorf("Type mismatch after roundtrip: got %s, want %s", decoded.Type, evt.Type)
	}
	if decoded.TextDelta != evt.TextDelta {
		t.Errorf("TextDelta mismatch after roundtrip: got %s, want %s", decoded.TextDelta, evt.TextDelta)
	}
}

func TestCanonicalEnvelopeJSONRoundtrip(t *testing.T) {
	env := CanonicalEnvelope{
		RequestID:        "req-json-test",
		UpstreamProtocol: "chat",
		ProviderID:       "provider-xyz",
		Model:            "model-abc",
		Events: []CanonicalEvent{
			{Seq: 1, Type: "message.start"},
			{Seq: 2, Type: "message.delta", TextDelta: "test"},
		},
		ProviderMeta: map[string]any{"key": "value"},
	}

	// Marshal
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("failed to marshal CanonicalEnvelope: %v", err)
	}

	// Unmarshal
	var decoded CanonicalEnvelope
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("failed to unmarshal CanonicalEnvelope: %v", err)
	}

	// Verify
	if decoded.RequestID != env.RequestID {
		t.Errorf("RequestID mismatch after roundtrip: got %s, want %s", decoded.RequestID, env.RequestID)
	}
	if len(decoded.Events) != len(env.Events) {
		t.Errorf("Events length mismatch after roundtrip: got %d, want %d", len(decoded.Events), len(env.Events))
	}
}

func TestToolCallEventFields(t *testing.T) {
	// Test tool_call lifecycle events per spec
	events := []CanonicalEvent{
		{
			Seq:      1,
			Type:     "tool_call.start",
			ItemID:   "tc-1",
			CallID:   "call-abc",
			ToolName: "get_weather",
		},
		{
			Seq:           2,
			Type:          "tool_call.arguments.delta",
			ItemID:        "tc-1",
			CallID:        "call-abc",
			ToolArgsDelta: "{\"city\":",
		},
		{
			Seq:           3,
			Type:          "tool_call.arguments.delta",
			ItemID:        "tc-1",
			CallID:        "call-abc",
			ToolArgsDelta: "\"Boston\"}",
		},
		{
			Seq:    4,
			Type:   "tool_call.done",
			ItemID: "tc-1",
			CallID: "call-abc",
		},
	}

	if len(events) != 4 {
		t.Errorf("expected 4 tool call events, got %d", len(events))
	}

	// Verify first event has tool name
	if events[0].ToolName != "get_weather" {
		t.Errorf("expected ToolName='get_weather' in tool_call.start")
	}

	// Verify deltas accumulate
	args := ""
	for _, e := range events[1:3] {
		args += e.ToolArgsDelta
	}
	if args != "{\"city\":\"Boston\"}" {
		t.Errorf("expected accumulated args '{\"city\":\"Boston\"}', got '%s'", args)
	}
}

func TestErrorEventFields(t *testing.T) {
	// Test error event structure per spec
	evt := CanonicalEvent{
		Seq:          1,
		Type:         "response.error",
		Error:        map[string]any{"kind": "upstreamHTTPError", "status_code": 500, "message": "internal error"},
		RawEventName: "error",
	}

	if evt.Error["kind"] != "upstreamHTTPError" {
		t.Errorf("expected Error['kind']='upstreamHTTPError', got %v", evt.Error["kind"])
	}
	if evt.Error["status_code"] != 500 {
		t.Errorf("expected Error['status_code']=500, got %v", evt.Error["status_code"])
	}
	if evt.Type != "response.error" {
		t.Errorf("expected Type='response.error', got %s", evt.Type)
	}
}

func TestSyntheticEventFlag(t *testing.T) {
	// Per spec: synthetic events must be distinguishable via ProviderMeta["synthetic"]=true
	syntheticEvt := CanonicalEvent{
		Seq:          1,
		Type:         "response.completed",
		ProviderMeta: map[string]any{"synthetic": true},
	}

	realEvt := CanonicalEvent{
		Seq:          1,
		Type:         "response.completed",
		ProviderMeta: map[string]any{"synthetic": false},
	}

	if syntheticEvt.ProviderMeta["synthetic"] != true {
		t.Error("expected synthetic event to have ProviderMeta['synthetic']=true")
	}
	if realEvt.ProviderMeta["synthetic"] != false {
		t.Error("expected real event to have ProviderMeta['synthetic']=false")
	}
}

func TestReasoningEventFields(t *testing.T) {
	// Test reasoning event structure per spec
	events := []CanonicalEvent{
		{Seq: 1, Type: "reasoning.start", ItemID: "reasoning-1"},
		{Seq: 2, Type: "reasoning.delta", ItemID: "reasoning-1", ReasoningDelta: "Let me think about this"},
		{Seq: 3, Type: "reasoning.delta", ItemID: "reasoning-1", ReasoningDelta: "... solving the problem"},
		{Seq: 4, Type: "reasoning.done", ItemID: "reasoning-1"},
	}

	if len(events) != 4 {
		t.Errorf("expected 4 reasoning events, got %d", len(events))
	}

	// Verify reasoning deltas accumulate
	reasoning := ""
	for _, e := range events[1:3] {
		reasoning += e.ReasoningDelta
	}
	expected := "Let me think about this... solving the problem"
	if reasoning != expected {
		t.Errorf("expected reasoning '%s', got '%s'", expected, reasoning)
	}
}
