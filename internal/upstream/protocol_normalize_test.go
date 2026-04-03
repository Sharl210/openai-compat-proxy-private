package upstream

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

// normalizeChatFrame 的隔离测试

func TestNormalizeChatFrame_TextOnly(t *testing.T) {
	// 测试纯文本对话的转换
	frames := []string{
		`{"id":"chat-123","choices":[{"delta":{"content":"Hello"},"finish_reason":null}]}`,
		`{"id":"chat-123","choices":[{"delta":{"content":" world"},"finish_reason":"stop"}]}`,
	}

	state := &chatNormalizationState{
		toolIDsByIndex: map[int]string{},
		toolSent:       map[string]bool{},
	}

	var allEvents []Event
	for _, frameData := range frames {
		frame := &sseFrame{Event: "", Data: frameData}
		events, done, err := normalizeChatFrame(frame, state)
		if err != nil {
			t.Fatalf("normalizeChatFrame error: %v", err)
		}
		if done {
			t.Fatal("unexpected done")
		}
		allEvents = append(allEvents, events...)
	}

	// 验证事件序列
	t.Logf("Events: %d", len(allEvents))
	for i, evt := range allEvents {
		t.Logf("  [%d] %s: %s", i, evt.Event, mustMarshal(evt.Data))
	}

	// 应该有 response.created, response.output_text.delta x2, response.completed
	if len(allEvents) < 3 {
		t.Errorf("expected at least 3 events, got %d", len(allEvents))
	}
}

func TestNormalizeChatFrame_ToolCall(t *testing.T) {
	// 测试 tool call 的转换 - 模拟上游 chat SSE 事件序列
	// 1. tool_call 开始（index=0, id="tool_0", name="get_weather"）
	// 2. tool_call arguments 开始（部分参数）
	// 3. tool_call arguments 继续
	// 4. 完成

	frames := []struct {
		event string
		data  string
	}{
		// Tool call 开始 - normalizeChatFrame 会发送 output_item.added 暂缓
		{`chat`, `{"id":"chat-123","choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_0","function":{"name":"get_weather"}}]}}]}`},
		// Tool call arguments
		{`chat`, `{"id":"chat-123","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{"}}]}}]}`},
		{`chat`, `{"id":"chat-123","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"location\""}}]}}]}`},
		{`chat`, `{"id":"chat-123","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\":\"Los"}}]}}]}`},
		{`chat`, `{"id":"chat-123","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":" Angeles\"}"}}]}}]}`},
		// 完成 - 需要带 usage 才能触发 response.completed
		{`chat`, `{"id":"chat-123","usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15},"choices":[{"finish_reason":"tool_calls"}]}`},
	}

	state := &chatNormalizationState{
		toolIDsByIndex: map[int]string{},
		toolSent:       map[string]bool{},
	}

	var allEvents []Event
	for _, f := range frames {
		frame := &sseFrame{Event: f.event, Data: f.data}
		events, done, err := normalizeChatFrame(frame, state)
		if err != nil {
			t.Fatalf("normalizeChatFrame error: %v", err)
		}
		if done {
			t.Log("stream done")
			break
		}
		allEvents = append(allEvents, events...)
	}

	// 验证事件序列
	t.Logf("=== Events (%d total) ===", len(allEvents))
	for i, evt := range allEvents {
		dataStr, _ := json.Marshal(evt.Data)
		t.Logf("  [%d] %s: %s", i, evt.Event, dataStr)
	}

	// 分析 tool call 事件
	var toolItemDoneSeen, toolArgsDeltaSeen, toolArgsDoneSeen bool
	for _, evt := range allEvents {
		switch evt.Event {
		case "response.output_item.done":
			if item, ok := evt.Data["item"].(map[string]any); ok {
				if item["type"] == "function_call" {
					toolItemDoneSeen = true
					t.Logf("  -> output_item.done: name=%v, arguments=%v", item["name"], item["arguments"])
				}
			}
		case "response.function_call_arguments.delta":
			toolArgsDeltaSeen = true
			t.Logf("  -> function_call_arguments.delta: item_id=%v", evt.Data["item_id"])
		case "response.function_call_arguments.done":
			toolArgsDoneSeen = true
			t.Logf("  -> function_call_arguments.done: item_id=%v", evt.Data["item_id"])
		}
	}

	t.Logf("\n=== Analysis ===")
	t.Logf("output_item.done seen: %v", toolItemDoneSeen)
	t.Logf("function_call_arguments.delta seen: %v", toolArgsDeltaSeen)
	t.Logf("function_call_arguments.done seen: %v", toolArgsDoneSeen)

	// 关键问题：normalizeChatFrame 是否发送了 output_item.done？
	// 如果发送了，这个事件的 arguments 是空的吗？
}

func TestNormalizeChatFrame_ToolCallWithArgumentsInSameFrame(t *testing.T) {
	// 测试当 tool_call 的 name 和 arguments 在同一个 frame 中时
	frame := &sseFrame{
		Event: "chat",
		Data:  `{"id":"chat-123","choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_0","function":{"name":"get_weather","arguments":"{\"location\":\"LA\"}"}}]}}]}`,
	}

	state := &chatNormalizationState{
		toolIDsByIndex: map[int]string{},
		toolSent:       map[string]bool{},
	}

	events, done, err := normalizeChatFrame(frame, state)
	if err != nil {
		t.Fatalf("normalizeChatFrame error: %v", err)
	}
	if done {
		t.Fatal("unexpected done")
	}

	t.Logf("=== Events (%d total) ===", len(events))
	for i, evt := range events {
		dataStr, _ := json.Marshal(evt.Data)
		t.Logf("  [%d] %s: %s", i, evt.Event, dataStr)
	}

	var doneCount, deltaCount int
	for _, evt := range events {
		switch evt.Event {
		case "response.output_item.done":
			doneCount++
		case "response.function_call_arguments.delta":
			deltaCount++
		}
	}
	if doneCount != 1 {
		t.Fatalf("expected exactly one output_item.done, got %d events=%#v", doneCount, events)
	}
	if deltaCount != 0 {
		t.Fatalf("expected same-frame complete tool call to avoid duplicate function_call_arguments.delta, got %d events=%#v", deltaCount, events)
	}
}

func TestNormalizeChatFrame_UsageAndCompletion(t *testing.T) {
	// 测试 usage 和 completion
	frames := []string{
		`{"id":"chat-123","choices":[{"delta":{"content":"Hi"}}]}`,
		`{"id":"chat-123","usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12},"choices":[{"finish_reason":"stop"}]}`,
	}

	state := &chatNormalizationState{
		toolIDsByIndex: map[int]string{},
		toolSent:       map[string]bool{},
	}

	var allEvents []Event
	for _, frameData := range frames {
		frame := &sseFrame{Event: "", Data: frameData}
		events, done, err := normalizeChatFrame(frame, state)
		if err != nil {
			t.Fatalf("normalizeChatFrame error: %v", err)
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

func TestChatEventBatchReader_FinalizesOnEOFWithoutCompletedEvent(t *testing.T) {
	rawSSE := strings.Join([]string{
		"event: chat",
		`data: {"id":"chat-123","choices":[{"delta":{"reasoning_content":"thinking"}}]}`,
		"",
		"event: chat",
		`data: {"id":"chat-123","choices":[{"delta":{"content":"final answer"}}]}`,
		"",
	}, "\n")

	scanner := bufio.NewScanner(strings.NewReader(rawSSE))
	readNext := newChatEventBatchReader(config.UpstreamThinkingTagStyleOff, nil, "req-test")

	var allEvents []Event
	for {
		events, err := readNext(scanner)
		if err != nil {
			t.Fatalf("readNext error: %v", err)
		}
		if len(events) == 0 {
			break
		}
		allEvents = append(allEvents, events...)
	}

	if len(allEvents) < 4 {
		t.Fatalf("expected created + reasoning + text + completed events, got %d %#v", len(allEvents), allEvents)
	}
	if allEvents[len(allEvents)-1].Event != "response.completed" {
		t.Fatalf("expected final event response.completed, got %#v", allEvents[len(allEvents)-1])
	}
	response, _ := allEvents[len(allEvents)-1].Data["response"].(map[string]any)
	if id, _ := response["id"].(string); id != "chat-123" {
		t.Fatalf("expected completed response id chat-123, got %#v", response)
	}
}

func TestNormalizeChatFrame_ReasoningContent(t *testing.T) {
	// 测试 reasoning_content
	frames := []string{
		`{"id":"chat-123","choices":[{"delta":{"reasoning_content":"thinking..."}}]}`,
		`{"id":"chat-123","choices":[{"delta":{"content":"answer"}}]}`,
	}

	state := &chatNormalizationState{
		toolIDsByIndex: map[int]string{},
		toolSent:       map[string]bool{},
	}

	var allEvents []Event
	for _, frameData := range frames {
		frame := &sseFrame{Event: "", Data: frameData}
		events, done, err := normalizeChatFrame(frame, state)
		if err != nil {
			t.Fatalf("normalizeChatFrame error: %v", err)
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

func TestNormalizeChatFrame_DoesNotExtractThinkTagsWhenStyleDisabled(t *testing.T) {
	frame := &sseFrame{Event: "chat", Data: `{"id":"chat-123","choices":[{"delta":{"content":"<think>internal reasoning</think>final answer"}}]}`}
	state := &chatNormalizationState{
		toolIDsByIndex:   map[int]string{},
		toolSent:         map[string]bool{},
		thinkingTagStyle: config.UpstreamThinkingTagStyleOff,
	}

	events, done, err := normalizeChatFrame(frame, state)
	if err != nil {
		t.Fatalf("normalizeChatFrame error: %v", err)
	}
	if done {
		t.Fatal("unexpected done")
	}
	if len(events) < 2 {
		t.Fatalf("expected created + output_text events, got %#v", events)
	}
	for _, evt := range events {
		if evt.Event == "response.reasoning.delta" {
			t.Fatalf("expected disabled style to avoid extracting think tags, got %#v", events)
		}
	}
	last := events[len(events)-1]
	if last.Event != "response.output_text.delta" {
		t.Fatalf("expected final event output_text.delta, got %#v", events)
	}
	if delta := stringValue(last.Data["delta"]); delta != "<think>internal reasoning</think>final answer" {
		t.Fatalf("expected original text preserved, got %#v", events)
	}
}

func TestNormalizeChatFrame_Done(t *testing.T) {
	frame := &sseFrame{Event: "", Data: "[DONE]"}
	state := &chatNormalizationState{}

	events, done, err := normalizeChatFrame(frame, state)
	if err != nil {
		t.Fatalf("normalizeChatFrame error: %v", err)
	}
	if !done {
		t.Fatal("expected done=true for [DONE]")
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events for [DONE] (response.completed comes from finish_reason+usage frame, not [DONE]), got %d", len(events))
	}
}

// 测试 normalizeChatFrame 的完整流程（模拟 SSE scanner）
func TestNormalizeChatFrame_FullStream(t *testing.T) {
	rawSSE := `event: chat
data: {"id":"chat-123","choices":[{"delta":{"role":"assistant"}}]}

event: chat
data: {"id":"chat-123","choices":[{"delta":{"content":"I"}}]}

event: chat
data: {"id":"chat-123","choices":[{"delta":{"content":"'ll "}}]}

event: chat
data: {"id":"chat-123","choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_0","function":{"name":"get_weather"}}]}}]}

event: chat
data: {"id":"chat-123","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{"}}]}}]}

event: chat
data: {"id":"chat-123","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"location\""}}]}}]}

event: chat
data: {"id":"chat-123","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\":\"LA\""}}]}}]}

event: chat
data: {"id":"chat-123","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"}"}}]}}]}

event: chat
data: {"id":"chat-123","usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15},"choices":[{"finish_reason":"tool_calls"}]}

event: chat
data: [DONE]
`

	reader := bufio.NewScanner(strings.NewReader(rawSSE))
	readNext := newChatEventBatchReader(config.UpstreamThinkingTagStyleOff, nil, "")

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

	// 分析关键事件
	var outputItemDoneCount, argsDeltaCount, argsDoneCount int
	for _, evt := range allEvents {
		switch evt.Event {
		case "response.output_item.done":
			outputItemDoneCount++
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

// Test that shadow recording captures raw upstream events alongside canonical events.
// The normalize functions should preserve raw frame data in Event.Raw
// and track provider metadata (provider type, original event name).
func TestNormalizeChatFrame_ShadowRecording_RawPreserved(t *testing.T) {
	// Simulate an upstream chat SSE frame with known content
	rawFrameData := `{"id":"chat-123","choices":[{"delta":{"content":"Hello"}}]}`
	frame := &sseFrame{Event: "chat", Data: rawFrameData}

	state := &chatNormalizationState{
		toolIDsByIndex: map[int]string{},
		toolSent:       map[string]bool{},
		provider:       "chat",
	}

	events, done, err := normalizeChatFrame(frame, state)
	if err != nil {
		t.Fatalf("normalizeChatFrame error: %v", err)
	}
	if done {
		t.Fatal("unexpected done")
	}

	if len(events) == 0 {
		t.Fatal("expected at least one canonical event")
	}

	foundRawEvent := false
	noLeakage := true
	for _, evt := range events {
		if _, ok := evt.Data["_providerMeta"]; ok {
			noLeakage = false
		}
		if len(evt.Raw) > 0 {
			foundRawEvent = true
			var envelope map[string]any
			if err := json.Unmarshal(evt.Raw, &envelope); err != nil {
				t.Errorf("Event.Raw cannot be unmarshaled: %v", err)
			} else {
				rawFrame, ok := envelope["_raw"].(map[string]any)
				if !ok {
					t.Error("envelope missing _raw field")
					continue
				}
				choices, _ := rawFrame["choices"].([]any)
				if len(choices) > 0 {
					if choice, ok := choices[0].(map[string]any); ok {
						if delta, ok := choice["delta"].(map[string]any); ok {
							if content, _ := delta["content"].(string); content != "Hello" {
								t.Errorf("Raw does not preserve original content: got %q, want %q", content, "Hello")
							}
						}
					}
				}
				if provider, _ := envelope["provider"].(string); provider != "chat" {
					t.Errorf("envelope provider = %q, want %q", provider, "chat")
				}
				if originalEvent, _ := envelope["originalEvent"].(string); originalEvent != "chat" {
					t.Errorf("envelope originalEvent = %q, want %q", originalEvent, "chat")
				}
			}
		}
	}

	if !noLeakage {
		t.Error("shadow recording FAILED: _providerMeta leaked into Event.Data")
	}
	if !foundRawEvent {
		t.Error("shadow recording FAILED: no Event.Raw preserved after normalization; expected raw frame data to be retained in Event.Raw")
	}
}

func TestNormalizeChatFrame_ShadowRecording_ProviderMeta(t *testing.T) {
	// Test that provider metadata (provider type, original event name) is captured
	frames := []string{
		`{"id":"chat-123","choices":[{"delta":{"content":"Hi"}}]}`,
		`{"id":"chat-123","usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12},"choices":[{"finish_reason":"stop"}]}`,
	}

	state := &chatNormalizationState{
		toolIDsByIndex: map[int]string{},
		toolSent:       map[string]bool{},
		provider:       "chat",
	}

	var allEvents []Event
	for _, frameData := range frames {
		frame := &sseFrame{Event: "chat", Data: frameData}
		events, done, err := normalizeChatFrame(frame, state)
		if err != nil {
			t.Fatalf("normalizeChatFrame error: %v", err)
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
			if provider, _ := envelope["provider"].(string); provider == "chat" {
				if originalEvent, _ := envelope["originalEvent"].(string); originalEvent == "chat" {
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

func TestNormalizeChatFrame_ShadowRecording_AllEventTypes(t *testing.T) {
	// Test shadow recording across different canonical event types from chat upstream
	frames := []struct {
		event string
		data  string
	}{
		// response.created
		{`chat`, `{"id":"chat-123","choices":[{"delta":{"role":"assistant"}}]}`},
		// text delta
		{`chat`, `{"id":"chat-123","choices":[{"delta":{"content":"Hello"}}]}`},
		// reasoning delta
		{`chat`, `{"id":"chat-123","choices":[{"delta":{"reasoning_content":"thinking..."}}]}`},
		// tool call
		{`chat`, `{"id":"chat-123","choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_0","function":{"name":"get_weather"}}]}}]}`},
		// completion
		{`chat`, `{"id":"chat-123","usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15},"choices":[{"finish_reason":"stop"}]}`},
	}

	state := &chatNormalizationState{
		toolIDsByIndex: map[int]string{},
		toolSent:       map[string]bool{},
		provider:       "chat",
	}

	var allEvents []Event
	for _, f := range frames {
		frame := &sseFrame{Event: f.event, Data: f.data}
		events, done, err := normalizeChatFrame(frame, state)
		if err != nil {
			t.Fatalf("normalizeChatFrame error: %v", err)
		}
		if done {
			break
		}
		allEvents = append(allEvents, events...)
	}

	t.Logf("=== Shadow Recording Test: %d events ===", len(allEvents))
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
				if provider, _ := envelope["provider"].(string); provider != "chat" {
					t.Errorf("  [%d] %s: envelope provider = %q, want %q", i, evt.Event, provider, "chat")
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
		t.Error("shadow recording FAILED: no events have raw frame data or provider metadata captured")
	}
	if !noLeakage {
		t.Error("shadow recording FAILED: _providerMeta leaked into Event.Data")
	}
}

func TestReadNextResponsesEventBatch_ShadowRecording_RawPreserved(t *testing.T) {
	rawSSE := "event: response.output_text.delta\n" +
		"data: {\"delta\":\"hello\"}\n\n"

	scanner := bufio.NewScanner(strings.NewReader(rawSSE))
	events, err := readNextResponsesEventBatch(scanner)
	if err != nil {
		t.Fatalf("readNextResponsesEventBatch error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if _, ok := events[0].Data["_providerMeta"]; ok {
		t.Fatal("shadow recording FAILED: _providerMeta leaked into Event.Data")
	}
	if len(events[0].Raw) == 0 {
		t.Fatal("shadow recording FAILED: responses event Raw is empty")
	}

	var envelope map[string]any
	if err := json.Unmarshal(events[0].Raw, &envelope); err != nil {
		t.Fatalf("unmarshal Raw envelope: %v", err)
	}
	if provider, _ := envelope["provider"].(string); provider != "responses" {
		t.Fatalf("provider = %q, want %q", provider, "responses")
	}
	if originalEvent, _ := envelope["originalEvent"].(string); originalEvent != "response.output_text.delta" {
		t.Fatalf("originalEvent = %q, want %q", originalEvent, "response.output_text.delta")
	}
	rawFrame, ok := envelope["_raw"].(map[string]any)
	if !ok {
		t.Fatalf("expected _raw payload in envelope, got %#v", envelope["_raw"])
	}
	if delta, _ := rawFrame["delta"].(string); delta != "hello" {
		t.Fatalf("delta = %q, want %q", delta, "hello")
	}
}

func TestReadNextResponsesEventBatch_ShadowRecording_PreservesOriginalDoneBehavior(t *testing.T) {
	rawSSE := "event: response.completed\n" +
		"data: {\"response\":{\"id\":\"resp_123\"}}\n\n"

	scanner := bufio.NewScanner(strings.NewReader(rawSSE))
	events, err := readNextResponsesEventBatch(scanner)
	if err != nil {
		t.Fatalf("readNextResponsesEventBatch error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Event != "response.completed" {
		t.Fatalf("event = %q, want %q", events[0].Event, "response.completed")
	}
	response, _ := events[0].Data["response"].(map[string]any)
	if id, _ := response["id"].(string); id != "resp_123" {
		t.Fatalf("response.id = %q, want %q", id, "resp_123")
	}
}

func mustMarshal(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
