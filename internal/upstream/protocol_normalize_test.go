package upstream

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"
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

	// 当 name 和 arguments 都在同一个 frame 中时
	// normalizeChatFrame 应该发送 output_item.done（带 name 和 arguments？）
	// 还是 output_item.done（带 name）+ function_call_arguments.delta？
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
	if len(events) != 1 {
		t.Errorf("expected 1 event (response.completed) for [DONE], got %d", len(events))
	}
	if events[0].Event != "response.completed" {
		t.Errorf("expected event type 'response.completed', got '%s'", events[0].Event)
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
	readNext := newChatEventBatchReader(false, nil, "")

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

func mustMarshal(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
