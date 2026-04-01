package upstream

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeAnthropicFrame_TextOnly(t *testing.T) {
	frames := []struct {
		event string
		data  string
	}{
		{`message_start`, `{"message":{"id":"msg_123","type":"message","role":"assistant"}}`},
		{`content_block_start`, `{"index":0,"content_block":{"type":"text","text":""}}`},
		{`content_block_delta`, `{"index":0,"delta":{"type":"text_delta","text":"Hello"}}`},
		{`content_block_delta`, `{"index":0,"delta":{"type":"text_delta","text":" world"}}`},
		{`message_delta`, `{"usage":{"input_tokens":10,"output_tokens":2},"delta":{"stop_reason":"end_turn"}}`},
		{`message_stop`, `{}`},
	}

	state := &anthropicNormalizationState{
		toolIDsByIndex: map[int]string{},
		usage:          map[string]any{},
	}

	var allEvents []Event
	for _, f := range frames {
		frame := &sseFrame{Event: f.event, Data: f.data}
		events, done, err := normalizeAnthropicFrame(frame, state)
		if err != nil {
			t.Fatalf("normalizeAnthropicFrame error: %v", err)
		}
		if done {
			break
		}
		allEvents = append(allEvents, events...)
	}

	t.Logf("=== Events (%d total) ===", len(allEvents))
	for i, evt := range allEvents {
		dataStr, _ := json.Marshal(evt.Data)
		t.Logf("  [%d] %s: %s", i, evt.Event, dataStr)
	}
}

func TestNormalizeAnthropicFrame_ToolUse(t *testing.T) {
	frames := []struct {
		event string
		data  string
	}{
		{`message_start`, `{"message":{"id":"msg_123","type":"message","role":"assistant"}}`},
		{`content_block_start`, `{"index":0,"content_block":{"type":"tool_use","id":"tool_0","name":"get_weather","input":{}}}`},
		{`content_block_delta`, `{"index":0,"delta":{"type":"input_json_delta","partial_json":""}}`},
		{`content_block_delta`, `{"index":0,"delta":{"type":"input_json_delta","partial_json":"{"}}`},
		{`content_block_delta`, `{"index":0,"delta":{"type":"input_json_delta","partial_json":"\"location\""}}`},
		{`content_block_delta`, `{"index":0,"delta":{"type":"input_json_delta","partial_json":"\":\"Los"}}`},
		{`content_block_delta`, `{"index":0,"delta":{"type":"input_json_delta","partial_json":" Angeles\""}}`},
		{`content_block_delta`, `{"index":0,"delta":{"type":"input_json_delta","partial_json":"}"}}`},
		{`message_delta`, `{"usage":{"input_tokens":10,"output_tokens":5},"delta":{"stop_reason":"tool_use"}}`},
		{`message_stop`, `{}`},
	}

	state := &anthropicNormalizationState{
		toolIDsByIndex: map[int]string{},
		usage:          map[string]any{},
	}

	var allEvents []Event
	for _, f := range frames {
		frame := &sseFrame{Event: f.event, Data: f.data}
		events, done, err := normalizeAnthropicFrame(frame, state)
		if err != nil {
			t.Fatalf("normalizeAnthropicFrame error: %v", err)
		}
		if done {
			break
		}
		allEvents = append(allEvents, events...)
	}

	t.Logf("=== Events (%d total) ===", len(allEvents))
	for i, evt := range allEvents {
		dataStr, _ := json.Marshal(evt.Data)
		t.Logf("  [%d] %s: %s", i, evt.Event, dataStr)
	}

	var outputItemDoneCount, argsDeltaCount, argsDoneCount int
	for _, evt := range allEvents {
		switch evt.Event {
		case "response.output_item.done":
			outputItemDoneCount++
			if item, ok := evt.Data["item"].(map[string]any); ok {
				t.Logf("  -> output_item.done: type=%v, name=%v, arguments=%v", item["type"], item["name"], item["arguments"])
			}
		case "response.function_call_arguments.delta":
			argsDeltaCount++
		case "response.function_call_arguments.done":
			argsDoneCount++
		}
	}
	t.Logf("\n=== Summary ===")
	t.Logf("output_item.done: %d", outputItemDoneCount)
	t.Logf("function_call_arguments.delta: %d", argsDeltaCount)
	t.Logf("function_call_arguments.done: %d", argsDoneCount)
}

func TestNormalizeAnthropicFrame_ToolUseWithInput(t *testing.T) {
	frames := []struct {
		event string
		data  string
	}{
		{`message_start`, `{"message":{"id":"msg_123","type":"message","role":"assistant"}}`},
		{`content_block_start`, `{"index":0,"content_block":{"type":"tool_use","id":"tool_0","name":"get_weather","input":{"location":"LA"}}}`},
		{`message_delta`, `{"usage":{"input_tokens":10,"output_tokens":5},"delta":{"stop_reason":"tool_use"}}`},
		{`message_stop`, `{}`},
	}

	state := &anthropicNormalizationState{
		toolIDsByIndex: map[int]string{},
		usage:          map[string]any{},
	}

	var allEvents []Event
	for _, f := range frames {
		frame := &sseFrame{Event: f.event, Data: f.data}
		events, done, err := normalizeAnthropicFrame(frame, state)
		if err != nil {
			t.Fatalf("normalizeAnthropicFrame error: %v", err)
		}
		if done {
			break
		}
		allEvents = append(allEvents, events...)
	}

	t.Logf("=== Events (%d total) ===", len(allEvents))
	for i, evt := range allEvents {
		dataStr, _ := json.Marshal(evt.Data)
		t.Logf("  [%d] %s: %s", i, evt.Event, dataStr)
	}
}

func TestNormalizeAnthropicFrame_Thinking(t *testing.T) {
	frames := []struct {
		event string
		data  string
	}{
		{`message_start`, `{"message":{"id":"msg_123","type":"message","role":"assistant"}}`},
		{`content_block_start`, `{"index":0,"content_block":{"type":"thinking","thinking":""}}`},
		{`content_block_delta`, `{"index":0,"delta":{"type":"thinking_delta","thinking":"thinking..."}}`},
		{`content_block_start`, `{"index":1,"content_block":{"type":"text","text":""}}`},
		{`content_block_delta`, `{"index":1,"delta":{"type":"text_delta","text":"answer"}}`},
		{`message_delta`, `{"usage":{"input_tokens":10,"output_tokens":5},"delta":{"stop_reason":"end_turn"}}`},
		{`message_stop`, `{}`},
	}

	state := &anthropicNormalizationState{
		toolIDsByIndex: map[int]string{},
		usage:          map[string]any{},
	}

	var allEvents []Event
	for _, f := range frames {
		frame := &sseFrame{Event: f.event, Data: f.data}
		events, done, err := normalizeAnthropicFrame(frame, state)
		if err != nil {
			t.Fatalf("normalizeAnthropicFrame error: %v", err)
		}
		if done {
			break
		}
		allEvents = append(allEvents, events...)
	}

	t.Logf("=== Events (%d total) ===", len(allEvents))
	for i, evt := range allEvents {
		dataStr, _ := json.Marshal(evt.Data)
		t.Logf("  [%d] %s: %s", i, evt.Event, dataStr)
	}
}

func TestNormalizeAnthropicFrame_Error(t *testing.T) {
	frame := &sseFrame{Event: "error", Data: `{"error":{"type":"invalid_request","message":"bad request"}}`}
	state := &anthropicNormalizationState{}

	events, done, err := normalizeAnthropicFrame(frame, state)
	if err != nil {
		t.Fatalf("normalizeAnthropicFrame error: %v", err)
	}
	if done {
		t.Fatal("unexpected done")
	}

	t.Logf("=== Error Event ===")
	for i, evt := range events {
		dataStr, _ := json.Marshal(evt.Data)
		t.Logf("  [%d] %s: %s", i, evt.Event, dataStr)
	}
}

func TestNormalizeAnthropicFrame_FullStream(t *testing.T) {
	rawSSE := `event: message_start
data: {"message":{"id":"msg_123","type":"message","role":"assistant"}}

event: content_block_start
data: {"index":0,"content_block":{"type":"tool_use","id":"tool_0","name":"get_weather","input":{}}}

event: content_block_delta
data: {"index":0,"delta":{"type":"input_json_delta","partial_json":"{\"location\":"}}

event: content_block_delta
data: {"index":0,"delta":{"type":"input_json_delta","partial_json":"\"Los Angeles\""}}

event: content_block_delta
data: {"index":0,"delta":{"type":"input_json_delta","partial_json":"}"}}

event: message_delta
data: {"usage":{"input_tokens":10,"output_tokens":5},"delta":{"stop_reason":"tool_use"}}

event: message_stop
data: {}
`

	reader := bufio.NewScanner(strings.NewReader(rawSSE))
	readNext := newAnthropicEventBatchReader(nil)

	var allEvents []Event
	for {
		events, err := readNext(reader)
		if err != nil {
			t.Fatalf("readNext error: %v", err)
		}
		if events == nil {
			break
		}
		allEvents = append(allEvents, events...)
	}

	t.Logf("=== Full Stream Events (%d total) ===", len(allEvents))
	for i, evt := range allEvents {
		dataStr, _ := json.Marshal(evt.Data)
		t.Logf("  [%d] %s: %s", i, evt.Event, dataStr)
	}

	var outputItemDoneCount, argsDeltaCount, argsDoneCount int
	for _, evt := range allEvents {
		switch evt.Event {
		case "response.output_item.done":
			outputItemDoneCount++
			if item, ok := evt.Data["item"].(map[string]any); ok {
				t.Logf("  -> output_item.done: type=%v, name=%v, arguments=%v", item["type"], item["name"], item["arguments"])
			}
		case "response.function_call_arguments.delta":
			argsDeltaCount++
		case "response.function_call_arguments.done":
			argsDoneCount++
		}
	}
	t.Logf("\n=== Summary ===")
	t.Logf("output_item.done: %d", outputItemDoneCount)
	t.Logf("function_call_arguments.delta: %d", argsDeltaCount)
	t.Logf("function_call_arguments.done: %d", argsDoneCount)
}
