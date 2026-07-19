package upstream

import (
	"bufio"
	"encoding/json"
	"fmt"
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

	if len(allEvents) < 2 {
		t.Errorf("expected at least 2 events, got %d", len(allEvents))
	}
	for _, evt := range allEvents {
		if evt.Event == "response.completed" {
			t.Fatalf("expected no synthetic response.completed before raw [DONE], got %#v", allEvents)
		}
	}
}

func TestNormalizeChatFrame_XMLToolCallTextDefaultPreservesText(t *testing.T) {
	frame := &sseFrame{Event: "chat", Data: `{"id":"chat-123","choices":[{"delta":{"content":"\n<tool_call>\n<function=mcp__mt_apk_outline_class>\n<parameter=className>Lacr/browser/lightning/activity/BrowserActivity$16;</parameter>\n<parameter=limit>50</parameter>\n<parameter=workspaceId>69jas4bi</parameter>\n</function>\n</tool_call>\n"}}]}`}
	state := &chatNormalizationState{toolIDsByIndex: map[int]string{}, toolSent: map[string]bool{}}

	events, done, err := normalizeChatFrame(frame, state)
	if err != nil {
		t.Fatalf("normalizeChatFrame error: %v", err)
	}
	if done {
		t.Fatal("unexpected done")
	}

	if len(events) != 2 {
		t.Fatalf("expected created and text events, got %#v", events)
	}
	if events[1].Event != "response.output_text.delta" {
		t.Fatalf("expected XML text to stay output_text by default, got %#v", events[1])
	}
	if got := stringValue(events[1].Data["delta"]); !strings.Contains(got, "<tool_call>") {
		t.Fatalf("expected XML text to be preserved, got %q", got)
	}
}

func TestNormalizeChatFrame_XMLToolCallTextLegacyConvertsToFunctionCall(t *testing.T) {
	frame := &sseFrame{Event: "chat", Data: `{"id":"chat-123","choices":[{"delta":{"content":"\n<tool_call>\n<function=mcp__mt_apk_outline_class>\n<parameter=className>Lacr/browser/lightning/activity/BrowserActivity$16;</parameter>\n<parameter=limit>50</parameter>\n<parameter=workspaceId>69jas4bi</parameter>\n</function>\n</tool_call>\n"},"finish_reason":"tool_calls"}]}`}
	state := &chatNormalizationState{
		toolIDsByIndex:       map[int]string{},
		toolSent:             map[string]bool{},
		upstreamXMLToolStyle: config.UpstreamXMLToolCallStyleLegacy,
	}

	events, done, err := normalizeChatFrame(frame, state)
	if err != nil {
		t.Fatalf("normalizeChatFrame error: %v", err)
	}
	if done {
		t.Fatal("unexpected done")
	}

	for _, evt := range events {
		if evt.Event == "response.output_text.delta" {
			t.Fatalf("expected XML tool call text to be consumed, got text event %#v", evt)
		}
	}
	if len(events) != 2 {
		t.Fatalf("expected created and function call events, got %#v", events)
	}
	if events[1].Event != "response.output_item.done" {
		t.Fatalf("expected function call item, got %#v", events[1])
	}
	item, _ := events[1].Data["item"].(map[string]any)
	if stringValue(item["type"]) != "function_call" {
		t.Fatalf("expected function_call item, got %#v", item)
	}
	if stringValue(item["name"]) != "mcp__mt_apk_outline_class" {
		t.Fatalf("unexpected function name: %#v", item)
	}
	if got := stringValue(item["arguments"]); got != `{"className":"Lacr/browser/lightning/activity/BrowserActivity$16;","limit":50,"workspaceId":"69jas4bi"}` {
		t.Fatalf("unexpected arguments: %s", got)
	}
}

func TestNormalizeChatFrame_LegacyXMLToolCallSplitAcrossContentDeltasConvertsOnce(t *testing.T) {
	frames := []string{
		`{"id":"chat-123","choices":[{"delta":{"content":"<tool_call>\n<function=lookup_record>\n<parameter=query>"}}]}`,
		`{"id":"chat-123","choices":[{"delta":{"content":"alpha"}}]}`,
		`{"id":"chat-123","choices":[{"delta":{"content":"</parameter>\n<parameter=limit>3</parameter>\n</function>\n</tool_call>"},"finish_reason":"tool_calls"}]}`,
	}
	state := &chatNormalizationState{
		toolIDsByIndex:       map[int]string{},
		toolSent:             map[string]bool{},
		upstreamXMLToolStyle: config.UpstreamXMLToolCallStyleLegacy,
	}

	var allEvents []Event
	for _, frameData := range frames {
		events, done, err := normalizeChatFrame(&sseFrame{Event: "chat", Data: frameData}, state)
		if err != nil {
			t.Fatalf("normalizeChatFrame error: %v", err)
		}
		if done {
			t.Fatal("unexpected done")
		}
		allEvents = append(allEvents, events...)
	}

	var toolDone []map[string]any
	for _, evt := range allEvents {
		if evt.Event == "response.output_text.delta" && strings.Contains(stringValue(evt.Data["delta"]), "<tool_call>") {
			t.Fatalf("expected split XML tool text to be buffered, got %#v from %#v", evt, allEvents)
		}
		if evt.Event != "response.output_item.done" {
			continue
		}
		item, _ := evt.Data["item"].(map[string]any)
		if stringValue(item["type"]) == "function_call" {
			toolDone = append(toolDone, item)
		}
	}
	if len(toolDone) != 1 {
		t.Fatalf("expected exactly one structured function call, got %d events=%#v", len(toolDone), allEvents)
	}
	if got := stringValue(toolDone[0]["name"]); got != "lookup_record" {
		t.Fatalf("unexpected function name %q from %#v", got, toolDone[0])
	}
	if got := stringValue(toolDone[0]["arguments"]); got != `{"limit":3,"query":"alpha"}` {
		t.Fatalf("unexpected arguments %s from %#v", got, toolDone[0])
	}
}

func TestNormalizeChatFrame_LegacyXMLToolTextCompletesPendingToolArguments(t *testing.T) {
	frames := []struct {
		event string
		data  string
	}{
		{`chat`, `{"id":"chat-123","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"mcp__mt_apk_read_text"}}]}}]}`},
		{`chat`, `{"id":"chat-123","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"locator\": "}}]}}]}`},
		{`chat`, `{"id":"chat-123","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"kind\": \"dex_method\", \"target\": \"Lacr/browser/lightning/activity/BrowserActivity$16;->x()V\"}"}}]}}]}`},
		{`chat`, `{"id":"chat-123","choices":[{"delta":{"content":"<tool_call>\n<function=mcp__mt_apk_read_text>\n<parameter=locator>{\"kind\": \"dex_method\", \"target\": \"Lacr/browser/lightning/activity/BrowserActivity$16;->x()V\"}</parameter>\n<parameter=maxChars>80000</parameter>\n<parameter=workspaceId>69jas4bi</parameter>\n<parameter=includeLineNumbers>False</parameter>\n<parameter=limit>2000</parameter>\n<parameter=startColumn>1</parameter>\n<parameter=startLine>1</parameter>\n</function>\n</tool_call>"}}]}`},
		{`chat`, `{"id":"chat-123","usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15},"choices":[{"finish_reason":"tool_calls"}]}`},
	}
	state := &chatNormalizationState{
		toolIDsByIndex:       map[int]string{},
		toolSent:             map[string]bool{},
		upstreamXMLToolStyle: config.UpstreamXMLToolCallStyleLegacy,
	}

	var allEvents []Event
	for _, f := range frames {
		events, done, err := normalizeChatFrame(&sseFrame{Event: f.event, Data: f.data}, state)
		if err != nil {
			t.Fatalf("normalizeChatFrame error: %v", err)
		}
		if done {
			break
		}
		allEvents = append(allEvents, events...)
	}

	for _, evt := range allEvents {
		if evt.Event == "response.output_text.delta" && strings.Contains(stringValue(evt.Data["delta"]), "<tool_call>") {
			t.Fatalf("expected XML tool text to be consumed, got %#v", evt)
		}
	}
	var doneItem map[string]any
	for _, evt := range allEvents {
		if evt.Event != "response.output_item.done" {
			continue
		}
		item, _ := evt.Data["item"].(map[string]any)
		if stringValue(item["id"]) == "call_1" {
			doneItem = item
		}
	}
	if doneItem == nil {
		t.Fatalf("expected final tool item, got %#v", allEvents)
	}
	expected := `{"includeLineNumbers":false,"limit":2000,"locator":{"kind":"dex_method","target":"Lacr/browser/lightning/activity/BrowserActivity$16;->x()V"},"maxChars":80000,"startColumn":1,"startLine":1,"workspaceId":"69jas4bi"}`
	if got := stringValue(doneItem["arguments"]); got != expected {
		t.Fatalf("unexpected final arguments:\n got: %s\nwant: %s", got, expected)
	}
}

func TestNormalizeChatFrame_LegacyXMLToolTextParsesLooseParameters(t *testing.T) {
	frame := &sseFrame{Event: "chat", Data: `{"id":"chat-123","choices":[{"delta":{"content":"<tool_call>\n<function=mcp__mt_apk_read_text>\n<parameter=includeLineNumbers>False\n<parameter=limit>520\n<parameter=locator>{\"kind\": \"dex_class\", \"target\": \"Lacr/browser/lightning/view/IDMDownloadListener;\"}\n<parameter=maxChars>80000\n<parameter=startColumn>1\n<parameter=startLine>500\n<parameter=workspaceId>69jas4bi\n</tool_call>"},"finish_reason":"tool_calls"}]}`}
	state := &chatNormalizationState{
		toolIDsByIndex:       map[int]string{},
		toolSent:             map[string]bool{},
		upstreamXMLToolStyle: config.UpstreamXMLToolCallStyleLegacy,
	}

	events, done, err := normalizeChatFrame(frame, state)
	if err != nil {
		t.Fatalf("normalizeChatFrame error: %v", err)
	}
	if done {
		t.Fatal("unexpected done")
	}
	if len(events) != 2 {
		t.Fatalf("expected created and function call events, got %#v", events)
	}
	item, _ := events[1].Data["item"].(map[string]any)
	if stringValue(item["name"]) != "mcp__mt_apk_read_text" {
		t.Fatalf("unexpected function name: %#v", item)
	}
	expected := `{"includeLineNumbers":false,"limit":520,"locator":{"kind":"dex_class","target":"Lacr/browser/lightning/view/IDMDownloadListener;"},"maxChars":80000,"startColumn":1,"startLine":500,"workspaceId":"69jas4bi"}`
	if got := stringValue(item["arguments"]); got != expected {
		t.Fatalf("unexpected arguments:\n got: %s\nwant: %s", got, expected)
	}
}

func TestNormalizeChatFrame_LegacyXMLToolCallWithPrefixTextPreservesTextAndConvertsTool(t *testing.T) {
	frame := &sseFrame{Event: "chat", Data: `{"id":"chat-123","choices":[{"delta":{"content":"我先查一下：\n<tool_call>\n<function=search_web>\n<parameter=query>weather</parameter>\n</function>\n</tool_call>"},"finish_reason":"tool_calls"}]}`}
	state := &chatNormalizationState{
		toolIDsByIndex:       map[int]string{},
		toolSent:             map[string]bool{},
		upstreamXMLToolStyle: config.UpstreamXMLToolCallStyleLegacy,
	}

	events, done, err := normalizeChatFrame(frame, state)
	if err != nil {
		t.Fatalf("normalizeChatFrame error: %v", err)
	}
	if done {
		t.Fatal("unexpected done")
	}

	var text string
	var tool map[string]any
	for _, evt := range events {
		switch evt.Event {
		case "response.output_text.delta":
			text += stringValue(evt.Data["delta"])
		case "response.output_item.done":
			item, _ := evt.Data["item"].(map[string]any)
			if stringValue(item["type"]) == "function_call" {
				tool = item
			}
		}
	}
	if strings.Contains(text, "<tool_call>") {
		t.Fatalf("expected XML tool text to be consumed, got text %q from %#v", text, events)
	}
	if !strings.Contains(text, "我先查一下") {
		t.Fatalf("expected prefix text to be preserved, got %q from %#v", text, events)
	}
	if tool == nil {
		t.Fatalf("expected function_call item, got %#v", events)
	}
	if got := stringValue(tool["name"]); got != "search_web" {
		t.Fatalf("unexpected tool name %q from %#v", got, tool)
	}
	if got := stringValue(tool["arguments"]); got != `{"query":"weather"}` {
		t.Fatalf("unexpected tool arguments %s from %#v", got, tool)
	}
}

func TestNormalizeChatPayload_LegacyXMLToolCallsInterleaveTextAndPreserveInvalidMarkup(t *testing.T) {
	legacyPayload := func(content string) map[string]any {
		return normalizeChatPayload(map[string]any{
			"id": "chat-legacy",
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": content},
			}},
		}, config.UpstreamThinkingTagStyleOff, config.UpstreamXMLToolCallStyleLegacy)
	}
	outputItem := func(t *testing.T, output []any, index int) map[string]any {
		t.Helper()
		item, _ := output[index].(map[string]any)
		if item == nil {
			t.Fatalf("expected output item %d to be a map, got %#v", index, output)
		}
		return item
	}
	messageText := func(t *testing.T, item map[string]any) string {
		t.Helper()
		content, _ := item["content"].([]any)
		if len(content) != 1 {
			t.Fatalf("expected one message content part, got %#v", item)
		}
		part, _ := content[0].(map[string]any)
		if stringValue(part["type"]) != "output_text" {
			t.Fatalf("expected output_text part, got %#v", item)
		}
		return stringValue(part["text"])
	}

	t.Run("interleaves every complete tool call", func(t *testing.T) {
		payload := legacyPayload("prefix <tool_call><function=lookup><parameter=query>first</parameter></function></tool_call> between <tool_call><function=lookup><parameter=query>second</parameter></function></tool_call> suffix")
		output, _ := payload["output"].([]any)
		if len(output) != 5 {
			t.Fatalf("expected text, tool, text, tool, text output ordering, got %#v", output)
		}
		for _, index := range []int{0, 2, 4} {
			if got := stringValue(outputItem(t, output, index)["type"]); got != "message" {
				t.Fatalf("expected message at index %d, got %#v", index, output)
			}
		}
		for _, index := range []int{1, 3} {
			if got := stringValue(outputItem(t, output, index)["type"]); got != "function_call" {
				t.Fatalf("expected function_call at index %d, got %#v", index, output)
			}
		}
		if got := messageText(t, outputItem(t, output, 0)); got != "prefix " {
			t.Fatalf("expected prefix preserved, got %q", got)
		}
		if got := messageText(t, outputItem(t, output, 2)); got != " between " {
			t.Fatalf("expected middle text preserved, got %q", got)
		}
		if got := messageText(t, outputItem(t, output, 4)); got != " suffix" {
			t.Fatalf("expected suffix preserved, got %q", got)
		}
		firstTool := outputItem(t, output, 1)
		secondTool := outputItem(t, output, 3)
		if got := stringValue(firstTool["arguments"]); got != `{"query":"first"}` {
			t.Fatalf("unexpected first tool arguments %q", got)
		}
		if got := stringValue(secondTool["arguments"]); got != `{"query":"second"}` {
			t.Fatalf("unexpected second tool arguments %q", got)
		}
		if firstTool["id"] == secondTool["id"] || firstTool["call_id"] == secondTool["call_id"] {
			t.Fatalf("expected distinct legacy tool identifiers, got %#v", output)
		}
	})

	t.Run("converts standalone complete tool call", func(t *testing.T) {
		payload := legacyPayload("<tool_call><function=lookup><parameter=query>weather</parameter></function></tool_call>")
		output, _ := payload["output"].([]any)
		if len(output) != 1 || stringValue(outputItem(t, output, 0)["type"]) != "function_call" {
			t.Fatalf("expected standalone XML call to become one function_call, got %#v", output)
		}
	})

	t.Run("preserves malformed and incomplete XML as text", func(t *testing.T) {
		text := "prefix <tool_call><function=lookup><parameter=query>missing close"
		payload := legacyPayload(text)
		output, _ := payload["output"].([]any)
		if len(output) != 1 || stringValue(outputItem(t, output, 0)["type"]) != "message" || messageText(t, outputItem(t, output, 0)) != text {
			t.Fatalf("expected malformed XML to remain output text, got %#v", output)
		}
	})

	t.Run("preserves malformed XML while converting a later valid call", func(t *testing.T) {
		malformed := "prefix <tool_call>not a function</tool_call> then "
		payload := legacyPayload(malformed + "<tool_call><function=lookup><parameter=query>weather</parameter></function></tool_call>")
		output, _ := payload["output"].([]any)
		if len(output) != 2 || stringValue(outputItem(t, output, 0)["type"]) != "message" || stringValue(outputItem(t, output, 1)["type"]) != "function_call" {
			t.Fatalf("expected malformed text followed by a native function_call, got %#v", output)
		}
		if got := messageText(t, outputItem(t, output, 0)); got != malformed {
			t.Fatalf("expected malformed XML to stay intact, got %q", got)
		}
		if got := stringValue(outputItem(t, output, 1)["arguments"]); got != `{"query":"weather"}` {
			t.Fatalf("expected later valid tool call arguments, got %q", got)
		}
	})

	t.Run("keeps XML text when compatibility is disabled", func(t *testing.T) {
		text := "<tool_call><function=lookup><parameter=query>weather</parameter></function></tool_call>"
		payload := normalizeChatPayload(map[string]any{
			"id": "chat-legacy-off",
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": text},
			}},
		}, config.UpstreamThinkingTagStyleOff, config.UpstreamXMLToolCallStyleOff)
		output, _ := payload["output"].([]any)
		if len(output) != 1 || stringValue(outputItem(t, output, 0)["type"]) != "message" || messageText(t, outputItem(t, output, 0)) != text {
			t.Fatalf("expected disabled XML compatibility to retain text, got %#v", output)
		}
	})
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

func TestChatEventBatchReaderDoesNotFinalizeOnEOFWithoutTerminalEvent(t *testing.T) {
	rawSSE := strings.Join([]string{
		"event: chat",
		`data: {"id":"chat-123","choices":[{"delta":{"reasoning_content":"thinking"}}]}`,
		"",
		"event: chat",
		`data: {"id":"chat-123","choices":[{"delta":{"content":"final answer"}}]}`,
		"",
	}, "\n")

	scanner := bufio.NewScanner(strings.NewReader(rawSSE))
	readNext := newChatEventBatchReader(config.UpstreamThinkingTagStyleOff, config.UpstreamXMLToolCallStyleOff, nil, "req-test", false)

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

	if len(allEvents) < 3 {
		t.Fatalf("expected created + reasoning + text events, got %d %#v", len(allEvents), allEvents)
	}
	for _, evt := range allEvents {
		if evt.Event == "response.completed" {
			t.Fatalf("expected EOF without raw terminal event to avoid response.completed, got %#v", allEvents)
		}
	}
}

func TestChatEventBatchReaderFinalizesOnEOFWhenAllowedAndFinishReasonSeen(t *testing.T) {
	rawSSE := strings.Join([]string{
		"event: chat",
		`data: {"id":"chat-123","choices":[{"delta":{"content":"hello"}}]}`,
		"",
		"event: chat",
		`data: {"id":"chat-123","choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		"",
	}, "\n")

	scanner := bufio.NewScanner(strings.NewReader(rawSSE))
	readNext := newChatEventBatchReader(config.UpstreamThinkingTagStyleOff, config.UpstreamXMLToolCallStyleOff, nil, "req-test", true)

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

	if len(allEvents) < 3 {
		t.Fatalf("expected created + text + completed events, got %#v", allEvents)
	}
	completed := allEvents[len(allEvents)-1]
	if completed.Event != "response.completed" {
		t.Fatalf("expected EOF completion when allowed, got %#v", allEvents)
	}
	response, _ := completed.Data["response"].(map[string]any)
	if got := response["finish_reason"]; got != "stop" {
		t.Fatalf("expected finish_reason stop, got %#v", completed)
	}
	usage, _ := response["usage"].(map[string]any)
	if got := usage["input_tokens"]; got != float64(3) {
		t.Fatalf("expected usage to survive EOF completion, got %#v", completed)
	}
}

func TestChatEventBatchReaderDoesNotFinalizePartialToolCallOnTruncationFinishReasons(t *testing.T) {
	for _, finishReason := range []string{"max_tokens", "length"} {
		t.Run(finishReason, func(t *testing.T) {
			rawSSE := strings.Join([]string{
				"event: chat",
				`data: {"id":"chat-123","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_partial","function":{"name":"lookup_record"}}]}}]}`,
				"",
				"event: chat",
				`data: {"id":"chat-123","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"query\":"}}]}}]}`,
				"",
				"event: chat",
				fmt.Sprintf(`data: {"id":"chat-123","choices":[{"finish_reason":%q}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`, finishReason),
				"",
			}, "\n")

			scanner := bufio.NewScanner(strings.NewReader(rawSSE))
			readNext := newChatEventBatchReader(config.UpstreamThinkingTagStyleOff, config.UpstreamXMLToolCallStyleOff, nil, "req-test", true)

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

			for _, evt := range allEvents {
				switch evt.Event {
				case "response.output_item.done":
					item, _ := evt.Data["item"].(map[string]any)
					if stringValue(item["type"]) == "function_call" {
						t.Fatalf("expected partial function_call to stay unfinished on %s, got %#v from %#v", finishReason, evt, allEvents)
					}
				case "response.function_call_arguments.done":
					t.Fatalf("expected no synthesized arguments.done for partial args, got %#v from %#v", evt, allEvents)
				}
			}
			terminal := allEvents[len(allEvents)-1]
			if terminal.Event != "response.incomplete" {
				t.Fatalf("expected %s terminal event to be incomplete, got %#v from %#v", finishReason, terminal, allEvents)
			}
		})
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

	var reasoningEvents []Event
	for _, evt := range allEvents {
		if evt.Event == "response.reasoning.delta" {
			reasoningEvents = append(reasoningEvents, evt)
		}
	}
	if len(reasoningEvents) != 1 {
		t.Fatalf("expected exactly one reasoning event, got %#v", allEvents)
	}
	if got := stringValue(reasoningEvents[0].Data["reasoning_content"]); got != "thinking..." {
		t.Fatalf("expected reasoning_content field to carry upstream reasoning body, got %#v", reasoningEvents[0].Data)
	}
	if _, ok := reasoningEvents[0].Data["summary"]; ok {
		t.Fatalf("expected upstream reasoning_content not to be downgraded into summary field, got %#v", reasoningEvents[0].Data)
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

func TestNormalizeChatFrame_SuppressesWhitespaceOnlyFrameAfterThinkExtraction(t *testing.T) {
	frames := []string{
		`{"id":"chat-123","choices":[{"delta":{"content":"<think>internal reasoning</think>"}}]}`,
		`{"id":"chat-123","choices":[{"delta":{"content":"\n\n"}}]}`,
		`{"id":"chat-123","choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_0","function":{"name":"fetch_webpage"}}]}}]}`,
	}

	state := &chatNormalizationState{
		toolIDsByIndex:   map[int]string{},
		toolSent:         map[string]bool{},
		thinkingTagStyle: config.UpstreamThinkingTagStyleLegacy,
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

	for _, evt := range allEvents {
		if evt.Event == "response.output_text.delta" {
			t.Fatalf("expected split whitespace-only frame after think extraction to be suppressed, got %#v", allEvents)
		}
	}
	if len(allEvents) < 3 {
		t.Fatalf("expected created, reasoning, and tool events, got %#v", allEvents)
	}
}

func TestNormalizeChatFrame_StreamsThinkReasoningBeforeClosingTag(t *testing.T) {
	frames := []string{
		`{"id":"chat-123","choices":[{"delta":{"content":"<think>abc"}}]}`,
		`{"id":"chat-123","choices":[{"delta":{"content":"def"}}]}`,
		`{"id":"chat-123","choices":[{"delta":{"content":"</think>final"}}]}`,
	}

	state := &chatNormalizationState{
		toolIDsByIndex:   map[int]string{},
		toolSent:         map[string]bool{},
		thinkingTagStyle: config.UpstreamThinkingTagStyleLegacy,
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

	var reasoning []string
	var text []string
	for _, evt := range allEvents {
		switch evt.Event {
		case "response.reasoning.delta":
			reasoning = append(reasoning, stringValue(evt.Data["reasoning_content"]))
		case "response.output_text.delta":
			text = append(text, stringValue(evt.Data["delta"]))
		}
	}

	if len(reasoning) < 2 {
		t.Fatalf("expected progressive reasoning deltas before closing tag, got %#v", allEvents)
	}
	if reasoning[0] != "abc" || reasoning[1] != "def" {
		t.Fatalf("expected reasoning deltas [abc def], got %#v from %#v", reasoning, allEvents)
	}
	if strings.Join(text, "") != "final" {
		t.Fatalf("expected trailing answer text preserved after closing tag, got %#v from %#v", text, allEvents)
	}
}

func TestNormalizeChatFrame_StreamsReasoningTagBeforeClosingTag(t *testing.T) {
	frames := []string{
		`{"id":"chat-reasoning","choices":[{"delta":{"content":"<reasoning>abc"}}]}`,
		`{"id":"chat-reasoning","choices":[{"delta":{"content":"def"}}]}`,
		`{"id":"chat-reasoning","choices":[{"delta":{"content":"</reasoning>final"}}]}`,
	}

	state := &chatNormalizationState{
		toolIDsByIndex:   map[int]string{},
		toolSent:         map[string]bool{},
		thinkingTagStyle: config.UpstreamThinkingTagStyleLegacy,
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

	var reasoning []string
	var text []string
	for _, evt := range allEvents {
		switch evt.Event {
		case "response.reasoning.delta":
			reasoning = append(reasoning, stringValue(evt.Data["reasoning_content"]))
		case "response.output_text.delta":
			text = append(text, stringValue(evt.Data["delta"]))
		}
	}

	if len(reasoning) < 2 {
		t.Fatalf("expected progressive reasoning deltas for <reasoning> tag, got %#v", allEvents)
	}
	if reasoning[0] != "abc" || reasoning[1] != "def" {
		t.Fatalf("expected reasoning deltas [abc def] from <reasoning> tag, got %#v from %#v", reasoning, allEvents)
	}
	if strings.Join(text, "") != "final" {
		t.Fatalf("expected trailing answer text preserved after </reasoning>, got %#v from %#v", text, allEvents)
	}
	for _, evt := range allEvents {
		data := mustMarshal(evt.Data)
		if strings.Contains(data, "<reasoning>") || strings.Contains(data, "</reasoning>") {
			t.Fatalf("expected <reasoning> tags to be stripped from normalized events, got %#v", allEvents)
		}
	}
}

func TestNormalizeChatFrame_DefaultsToReasoningUntilClosingTagWhenStyleEnabled(t *testing.T) {
	frames := []string{
		`{"id":"chat-implicit-think","choices":[{"delta":{"content":"abc"}}]}`,
		`{"id":"chat-implicit-think","choices":[{"delta":{"content":"def"}}]}`,
		`{"id":"chat-implicit-think","choices":[{"delta":{"content":"</think>final"}}]}`,
	}

	state := &chatNormalizationState{
		toolIDsByIndex:   map[int]string{},
		toolSent:         map[string]bool{},
		thinkingTagStyle: config.UpstreamThinkingTagStyleLegacy,
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

	var reasoning []string
	var text []string
	for _, evt := range allEvents {
		switch evt.Event {
		case "response.reasoning.delta":
			reasoning = append(reasoning, stringValue(evt.Data["reasoning_content"]))
		case "response.output_text.delta":
			text = append(text, stringValue(evt.Data["delta"]))
		}
	}

	if strings.Join(reasoning, "") != "abcdef" {
		t.Fatalf("expected content before closing tag to be emitted as reasoning, got %#v from %#v", reasoning, allEvents)
	}
	if strings.Join(text, "") != "final" {
		t.Fatalf("expected content after closing tag to remain output text, got %#v from %#v", text, allEvents)
	}
}

func TestNormalizeChatFrame_DoesNotReenterImplicitReasoningAfterFirstClose(t *testing.T) {
	frames := []string{
		`{"id":"chat-implicit-think-once","choices":[{"delta":{"content":"alpha"}}]}`,
		`{"id":"chat-implicit-think-once","choices":[{"delta":{"content":"</think>final"}}]}`,
		`{"id":"chat-implicit-think-once","choices":[{"delta":{"content":" trailing text"}}]}`,
	}

	state := &chatNormalizationState{
		toolIDsByIndex:   map[int]string{},
		toolSent:         map[string]bool{},
		thinkingTagStyle: config.UpstreamThinkingTagStyleLegacy,
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

	var reasoning []string
	var text []string
	for _, evt := range allEvents {
		switch evt.Event {
		case "response.reasoning.delta":
			reasoning = append(reasoning, stringValue(evt.Data["reasoning_content"]))
		case "response.output_text.delta":
			text = append(text, stringValue(evt.Data["delta"]))
		}
	}

	if strings.Join(reasoning, "") != "alpha" {
		t.Fatalf("expected only pre-close content to stay in reasoning, got %#v from %#v", reasoning, allEvents)
	}
	if strings.Join(text, "") != "final trailing text" {
		t.Fatalf("expected post-close deltas to remain output text without re-entering reasoning, got %#v from %#v", text, allEvents)
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
		t.Fatalf("expected bare [DONE] without stream state to emit no events, got %#v", events)
	}
}

func TestNormalizeChatFrame_DoneFlushesPendingToolItems(t *testing.T) {
	frame := &sseFrame{Event: "", Data: "[DONE]"}
	state := &chatNormalizationState{
		createdSent: true,
		responseID:  "chat-123",
		pendingItems: map[string]map[string]any{
			"call_3": {
				"type":      "function_call",
				"id":        "call_3",
				"call_id":   "call_3",
				"name":      "search_web",
				"arguments": `{"query":"q"}`,
			},
		},
		toolSent: map[string]bool{},
	}

	events, done, err := normalizeChatFrame(frame, state)
	if err != nil {
		t.Fatalf("normalizeChatFrame error: %v", err)
	}
	if !done {
		t.Fatal("expected done=true for [DONE]")
	}
	if len(events) != 2 {
		t.Fatalf("expected pending tool item and terminal response on [DONE], got %#v", events)
	}
	if events[0].Event != "response.output_item.done" {
		t.Fatalf("expected first event response.output_item.done, got %#v", events[0])
	}
	if events[1].Event != "response.completed" {
		t.Fatalf("expected second event response.completed, got %#v", events[1])
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
	readNext := newChatEventBatchReader(config.UpstreamThinkingTagStyleOff, config.UpstreamXMLToolCallStyleOff, nil, "", false)

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

func TestReadNextResponsesEventBatch_PreservesRawFrame(t *testing.T) {
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

	var rawFrame map[string]any
	if err := json.Unmarshal(events[0].Raw, &rawFrame); err != nil {
		t.Fatalf("unmarshal Raw frame: %v", err)
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
