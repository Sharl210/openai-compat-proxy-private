package integration_test

import (
	"testing"

	"openai-compat-proxy/internal/aggregate"
	"openai-compat-proxy/internal/upstream"
)

func TestCollectorBuildsTextAndToolCalls(t *testing.T) {
	collector := aggregate.NewCollector()
	collector.Accept(sampleTextDeltaEvent("hel"))
	collector.Accept(sampleTextDeltaEvent("lo"))
	collector.Accept(sampleToolDeltaEvent("call_1", "{\"city\":\"sh"))
	collector.Accept(sampleToolDeltaEvent("call_1", "anghai\"}"))
	collector.Accept(sampleToolDoneEvent("call_1", "get_weather", "call_abc", "{\"city\":\"shanghai\"}"))
	collector.Accept(sampleCompletedEvent())

	result, err := collector.Result()
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "hello" || len(result.ToolCalls) != 1 {
		t.Fatal("expected aggregated text and tool call")
	}
	if result.ToolCalls[0].Arguments != "{\"city\":\"shanghai\"}" {
		t.Fatalf("unexpected tool arguments: %s", result.ToolCalls[0].Arguments)
	}
	if result.ToolCalls[0].Name != "get_weather" || result.ToolCalls[0].CallID != "call_abc" {
		t.Fatalf("unexpected tool metadata: %#v", result.ToolCalls[0])
	}
}

func sampleTextDeltaEvent(delta string) upstream.Event {
	return upstream.Event{Event: "response.output_text.delta", Data: map[string]any{"delta": delta}}
}

func sampleToolDeltaEvent(itemID, delta string) upstream.Event {
	return upstream.Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": itemID, "delta": delta}}
}

func sampleCompletedEvent() upstream.Event {
	return upstream.Event{Event: "response.completed", Data: map[string]any{}}
}

func sampleToolDoneEvent(itemID, name, callID, arguments string) upstream.Event {
	return upstream.Event{Event: "response.output_item.done", Data: map[string]any{
		"item": map[string]any{
			"id":        itemID,
			"type":      "function_call",
			"name":      name,
			"call_id":   callID,
			"arguments": arguments,
		},
	}}
}
