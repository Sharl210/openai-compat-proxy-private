package upstream

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeAnthropicUsageConvertsDiffInputToCanonicalTotals(t *testing.T) {
	usage := normalizeAnthropicUsage(map[string]any{
		"input_tokens":                20,
		"output_tokens":               5,
		"cache_read_input_tokens":     6,
		"cache_creation_input_tokens": 4,
	})
	if got := usage["input_tokens"]; got != float64(30) {
		t.Fatalf("expected canonical total input_tokens 30, got %#v", got)
	}
	if got := usage["total_tokens"]; got != float64(35) {
		t.Fatalf("expected canonical total_tokens 35, got %#v", got)
	}
	details, _ := usage["input_tokens_details"].(map[string]any)
	if got := details["cached_tokens"]; got != 6 {
		t.Fatalf("expected cached_tokens 6, got %#v", got)
	}
	if got := details["cache_creation_tokens"]; got != 4 {
		t.Fatalf("expected cache_creation_tokens 4, got %#v", got)
	}
}

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

func TestNormalizeAnthropicFrame_ShadowRecording_RawPreserved(t *testing.T) {
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
		provider:       "anthropic",
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

	foundRawEvent := false
	noLeakage := true
	for _, evt := range allEvents {
		if _, ok := evt.Data["_providerMeta"]; ok {
			noLeakage = false
		}
		if len(evt.Raw) > 0 {
			foundRawEvent = true
			var envelope map[string]any
			if err := json.Unmarshal(evt.Raw, &envelope); err != nil {
				t.Errorf("Event.Raw cannot be unmarshaled: %v", err)
			} else {
				if _, ok := envelope["_raw"]; !ok {
					t.Error("envelope missing _raw field")
				}
				if provider, _ := envelope["provider"].(string); provider != "anthropic" {
					t.Errorf("envelope provider = %q, want %q", provider, "anthropic")
				}
			}
		}
	}

	if !noLeakage {
		t.Error("shadow recording FAILED: _providerMeta leaked into Event.Data")
	}
	if !foundRawEvent {
		t.Error("shadow recording FAILED: no Event.Raw preserved after anthropic normalization; expected raw frame data to be retained in Event.Raw")
	}
}

func TestNormalizeAnthropicFrame_ShadowRecording_ProviderMeta(t *testing.T) {
	frames := []struct {
		event string
		data  string
	}{
		{`message_start`, `{"message":{"id":"msg_123","type":"message","role":"assistant"}}`},
		{`content_block_start`, `{"index":0,"content_block":{"type":"text","text":""}}`},
		{`content_block_delta`, `{"index":0,"delta":{"type":"text_delta","text":"Hi"}}`},
		{`message_delta`, `{"usage":{"input_tokens":10,"output_tokens":2},"delta":{"stop_reason":"end_turn"}}`},
		{`message_stop`, `{}`},
	}

	state := &anthropicNormalizationState{
		toolIDsByIndex: map[int]string{},
		usage:          map[string]any{},
		provider:       "anthropic",
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

	providerMetaFound := false
	noLeakage := true
	for _, evt := range allEvents {
		if _, ok := evt.Data["_providerMeta"]; ok {
			noLeakage = false
		}
		var envelope map[string]any
		if err := json.Unmarshal(evt.Raw, &envelope); err == nil {
			if provider, _ := envelope["provider"].(string); provider == "anthropic" {
				if originalEvent, _ := envelope["originalEvent"].(string); originalEvent != "" {
					providerMetaFound = true
				}
			}
		}
	}

	if !noLeakage {
		t.Error("shadow recording FAILED: _providerMeta leaked into Event.Data; metadata should only be in Raw envelope")
	}
	if !providerMetaFound {
		t.Error("shadow recording FAILED: provider metadata not found in Event.Raw envelope")
	}
}

func TestNormalizeAnthropicFrame_ShadowRecording_AllEventTypes(t *testing.T) {
	frames := []struct {
		event string
		data  string
	}{
		{`message_start`, `{"message":{"id":"msg_123","type":"message","role":"assistant"}}`},
		{`content_block_start`, `{"index":0,"content_block":{"type":"thinking","thinking":""}}`},
		{`content_block_delta`, `{"index":0,"delta":{"type":"thinking_delta","thinking":"thinking..."}}`},
		{`content_block_start`, `{"index":1,"content_block":{"type":"text","text":""}}`},
		{`content_block_delta`, `{"index":1,"delta":{"type":"text_delta","text":"answer"}}`},
		{`content_block_start`, `{"index":2,"content_block":{"type":"tool_use","id":"tool_0","name":"get_weather","input":{}}}`},
		{`content_block_delta`, `{"index":2,"delta":{"type":"input_json_delta","partial_json":"{}"}}`},
		{`message_delta`, `{"usage":{"input_tokens":10,"output_tokens":5},"delta":{"stop_reason":"tool_use"}}`},
		{`message_stop`, `{}`},
	}

	state := &anthropicNormalizationState{
		toolIDsByIndex: map[int]string{},
		usage:          map[string]any{},
		provider:       "anthropic",
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

	noLeakage := true
	for i, evt := range allEvents {
		t.Logf("  [%d] canonical=%s raw=%s", i, evt.Event, string(evt.Raw))
		if _, ok := evt.Data["_providerMeta"]; ok {
			noLeakage = false
		}
		if len(evt.Raw) == 0 {
			t.Errorf("  [%d] %s: no shadow recording (Raw is empty)", i, evt.Event)
		} else {
			var envelope map[string]any
			if err := json.Unmarshal(evt.Raw, &envelope); err != nil {
				t.Errorf("  [%d] %s: Raw is not valid envelope JSON: %v", i, evt.Event, err)
			} else {
				if _, ok := envelope["_raw"]; !ok {
					t.Errorf("  [%d] %s: envelope missing _raw field", i, evt.Event)
				}
				if provider, _ := envelope["provider"].(string); provider != "anthropic" {
					t.Errorf("  [%d] %s: envelope provider = %q, want %q", i, evt.Event, provider, "anthropic")
				}
			}
		}
	}

	eventsWithShadow := 0
	for _, evt := range allEvents {
		if len(evt.Raw) > 0 {
			eventsWithShadow++
		}
	}
	if eventsWithShadow == 0 {
		t.Error("shadow recording FAILED: no events have raw frame data captured for anthropic normalization")
	}
	if !noLeakage {
		t.Error("shadow recording FAILED: _providerMeta leaked into Event.Data")
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
