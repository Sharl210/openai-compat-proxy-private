package adapter

import (
	"testing"

	"openai-compat-proxy/internal/adapter/anthropic"
	"openai-compat-proxy/internal/adapter/chat"
	"openai-compat-proxy/internal/adapter/responses"
	"openai-compat-proxy/internal/aggregate"
)

// ---------------------------------------------------------------------------
// Test case: text-only result
// ---------------------------------------------------------------------------

// TestParity_TextOnly_Content checks that all three adapters surface result.Text
// in a semantically equivalent position.
func TestParity_TextOnly_Content(t *testing.T) {
	result := aggregate.Result{
		Text: "Hello, world!",
	}

	chatResp := chat.BuildResponse(result)
	anthropicResp := anthropic.BuildResponse(result, "req_1", "gpt-4o")
	responsesResp := responses.BuildResponse(result)

	// Chat: message.content should be the text string
	chatChoices := chatResp["choices"].([]map[string]any)
	chatMsg := chatChoices[0]["message"].(map[string]any)
	if chatMsg["content"] != "Hello, world!" {
		t.Errorf("[chat] content = %#v, want text string", chatMsg["content"])
	}

	// Anthropic: content[0].text should equal result.Text
	anthropicContent := anthropicResp["content"].([]map[string]any)
	if len(anthropicContent) == 0 {
		t.Errorf("[anthropic] content is empty")
	} else if anthropicContent[0]["text"] != "Hello, world!" {
		t.Errorf("[anthropic] content[0].text = %#v, want result.Text", anthropicContent[0]["text"])
	}

	// Responses: output[0].content[0].text should equal result.Text
	responsesOutput := responsesResp["output"].([]map[string]any)
	if len(responsesOutput) == 0 {
		t.Errorf("[responses] output is empty")
	} else {
		msgContent := responsesOutput[0]["content"].([]map[string]any)
		if msgContent[0]["text"] != "Hello, world!" {
			t.Errorf("[responses] output[0].content[0].text = %#v, want result.Text", msgContent[0]["text"])
		}
	}
}

// ---------------------------------------------------------------------------
// Test case: tool calls only (no text, no reasoning)
// ---------------------------------------------------------------------------

// TestParity_ToolCallsOnly_Content checks that tool-call-only results set
// content to nil in chat but produce a tool_use block in anthropic and
// a function_call block in responses.
func TestParity_ToolCallsOnly_Content(t *testing.T) {
	result := aggregate.Result{
		ToolCalls: []aggregate.ToolCall{{
			CallID:    "call_0",
			Name:      "get_weather",
			Arguments: `{"city":"Shanghai"}`,
		}},
	}

	chatResp := chat.BuildResponse(result)
	anthropicResp := anthropic.BuildResponse(result, "req_2", "gpt-4o")
	responsesResp := responses.BuildResponse(result)

	// Chat: message.content should be nil
	chatChoices := chatResp["choices"].([]map[string]any)
	chatMsg := chatChoices[0]["message"].(map[string]any)
	if chatMsg["content"] != nil {
		t.Errorf("[chat] content = %#v, want nil", chatMsg["content"])
	}

	// Chat: message.tool_calls should be populated
	if chatMsg["tool_calls"] == nil {
		t.Errorf("[chat] tool_calls is nil, want populated")
	}

	// Anthropic: content should NOT include a text block, only tool_use
	anthropicContent := anthropicResp["content"].([]map[string]any)
	hasText := false
	hasToolUse := false
	for _, block := range anthropicContent {
		if block["type"] == "text" {
			hasText = true
		}
		if block["type"] == "tool_use" {
			hasToolUse = true
		}
	}
	if hasText {
		t.Errorf("[anthropic] content should NOT include text block for tool-call-only result")
	}
	if !hasToolUse {
		t.Errorf("[anthropic] content should include tool_use block")
	}

	// Responses: output should include function_call block (not message block)
	responsesOutput := responsesResp["output"].([]map[string]any)
	hasFunctionCall := false
	hasMessage := false
	for _, item := range responsesOutput {
		if item["type"] == "function_call" {
			hasFunctionCall = true
		}
		if item["type"] == "message" {
			hasMessage = true
		}
	}
	if !hasFunctionCall {
		t.Errorf("[responses] output should include function_call block")
	}
	if hasMessage {
		t.Errorf("[responses] output should NOT include message block for tool-call-only result")
	}
}

// ---------------------------------------------------------------------------
// Test case: text + tool calls
// ---------------------------------------------------------------------------

// TestParity_TextAndToolCalls checks that when both text and tool calls are
// present, chat surfaces text as content, anthropic emits both text and
// tool_use blocks, and responses emits both message and function_call blocks.
func TestParity_TextAndToolCalls(t *testing.T) {
	result := aggregate.Result{
		Text: "Let me check that for you.",
		ToolCalls: []aggregate.ToolCall{{
			CallID:    "call_1",
			Name:      "get_weather",
			Arguments: `{"city":"Beijing"}`,
		}},
	}

	chatResp := chat.BuildResponse(result)
	anthropicResp := anthropic.BuildResponse(result, "req_3", "gpt-4o")
	responsesResp := responses.BuildResponse(result)

	// Chat: content should be the text string (not nil)
	chatChoices := chatResp["choices"].([]map[string]any)
	chatMsg := chatChoices[0]["message"].(map[string]any)
	if chatMsg["content"] != "Let me check that for you." {
		t.Errorf("[chat] content = %#v, want text string", chatMsg["content"])
	}
	if chatMsg["tool_calls"] == nil {
		t.Errorf("[chat] tool_calls should be populated")
	}

	// Anthropic: content should have text block first, then tool_use
	anthropicContent := anthropicResp["content"].([]map[string]any)
	if len(anthropicContent) != 2 {
		t.Errorf("[anthropic] content length = %d, want 2 (text + tool_use)", len(anthropicContent))
	} else {
		if anthropicContent[0]["type"] != "text" {
			t.Errorf("[anthropic] content[0].type = %s, want text", anthropicContent[0]["type"])
		}
		if anthropicContent[1]["type"] != "tool_use" {
			t.Errorf("[anthropic] content[1].type = %s, want tool_use", anthropicContent[1]["type"])
		}
	}

	// Responses: output should have both message and function_call blocks
	responsesOutput := responsesResp["output"].([]map[string]any)
	if len(responsesOutput) != 2 {
		t.Errorf("[responses] output length = %d, want 2", len(responsesOutput))
	}
}

// ---------------------------------------------------------------------------
// Test case: reasoning / thinking block
// ---------------------------------------------------------------------------

// TestParity_ReasoningPresence checks that reasoning is surfaced via the
// appropriate field in each adapter.
func TestParity_ReasoningPresence(t *testing.T) {
	result := aggregate.Result{
		Text:      "The answer is 42.",
		Reasoning: map[string]any{"summary": "I computed it step by step."},
	}

	chatResp := chat.BuildResponse(result)
	anthropicResp := anthropic.BuildResponse(result, "req_4", "claude-sonnet-4")
	responsesResp := responses.BuildResponse(result)

	// Chat: message.reasoning_content should contain the summary
	chatChoices := chatResp["choices"].([]map[string]any)
	chatMsg := chatChoices[0]["message"].(map[string]any)
	if chatMsg["reasoning_content"] != "I computed it step by step." {
		t.Errorf("[chat] reasoning_content = %#v, want summary text", chatMsg["reasoning_content"])
	}

	// Anthropic: content[0] should be a thinking block
	anthropicContent := anthropicResp["content"].([]map[string]any)
	if len(anthropicContent) < 2 {
		t.Errorf("[anthropic] content length = %d, want at least 2 (thinking + text)", len(anthropicContent))
	} else {
		if anthropicContent[0]["type"] != "thinking" {
			t.Errorf("[anthropic] content[0].type = %s, want thinking", anthropicContent[0]["type"])
		}
		if anthropicContent[0]["thinking"] != "I computed it step by step." {
			t.Errorf("[anthropic] content[0].thinking = %#v, want summary text", anthropicContent[0]["thinking"])
		}
	}

	// Responses: reasoning field should be the raw map (with summary key)
	reasoning := responsesResp["reasoning"].(map[string]any)
	if reasoning["summary"] != "I computed it step by step." {
		t.Errorf("[responses] reasoning.summary = %#v, want summary text", reasoning["summary"])
	}
}

// TestParity_ReasoningWithDifferentKeys checks that reasoning content is
// extracted from different key names across adapters.
func TestParity_ReasoningWithDifferentKeys(t *testing.T) {
	// The raw reasoning map may contain keys like "thinking", "content",
	// "delta", etc. Each adapter extracts from different priority keys.
	t.Run("summary key", func(t *testing.T) {
		result := aggregate.Result{
			Text:      "Final.",
			Reasoning: map[string]any{"summary": "reasoning text"},
		}
		chatResp := chat.BuildResponse(result)
		chatChoices := chatResp["choices"].([]map[string]any)
		chatMsg := chatChoices[0]["message"].(map[string]any)
		if chatMsg["reasoning_content"] != "reasoning text" {
			t.Errorf("[chat] reasoning_content = %#v, want \"reasoning text\"", chatMsg["reasoning_content"])
		}
	})

	t.Run("reasoning_content key", func(t *testing.T) {
		result := aggregate.Result{
			Text:      "Final.",
			Reasoning: map[string]any{"reasoning_content": "rc text"},
		}
		chatResp := chat.BuildResponse(result)
		chatChoices := chatResp["choices"].([]map[string]any)
		chatMsg := chatChoices[0]["message"].(map[string]any)
		if chatMsg["reasoning_content"] != "rc text" {
			t.Errorf("[chat] reasoning_content = %#v, want \"rc text\"", chatMsg["reasoning_content"])
		}
	})

	t.Run("content key", func(t *testing.T) {
		result := aggregate.Result{
			Text:      "Final.",
			Reasoning: map[string]any{"content": "content text"},
		}
		chatResp := chat.BuildResponse(result)
		anthropicResp := anthropic.BuildResponse(result, "req", "model")

		chatChoices := chatResp["choices"].([]map[string]any)
		chatMsg := chatChoices[0]["message"].(map[string]any)
		if chatMsg["reasoning_content"] != "content text" {
			t.Errorf("[chat] reasoning_content = %#v, want \"content text\"", chatMsg["reasoning_content"])
		}

		anthropicContent := anthropicResp["content"].([]map[string]any)
		if anthropicContent[0]["type"] != "thinking" {
			t.Errorf("[anthropic] content[0].type = %s, want thinking", anthropicContent[0]["type"])
		}
		if anthropicContent[0]["thinking"] != "content text" {
			t.Errorf("[anthropic] content[0].thinking = %#v, want \"content text\"", anthropicContent[0]["thinking"])
		}
	})

	t.Run("thinking key (anthropic primary)", func(t *testing.T) {
		result := aggregate.Result{
			Text:      "Final.",
			Reasoning: map[string]any{"thinking": "anthropic thinking"},
		}
		anthropicResp := anthropic.BuildResponse(result, "req", "model")
		anthropicContent := anthropicResp["content"].([]map[string]any)
		if anthropicContent[0]["type"] != "thinking" {
			t.Errorf("[anthropic] content[0].type = %s, want thinking", anthropicContent[0]["type"])
		}
		if anthropicContent[0]["thinking"] != "anthropic thinking" {
			t.Errorf("[anthropic] content[0].thinking = %#v, want \"anthropic thinking\"", anthropicContent[0]["thinking"])
		}
	})

	t.Run("empty reasoning map produces no reasoning block", func(t *testing.T) {
		result := aggregate.Result{
			Text:      "Final.",
			Reasoning: map[string]any{},
		}
		chatResp := chat.BuildResponse(result)
		chatChoices := chatResp["choices"].([]map[string]any)
		chatMsg := chatChoices[0]["message"].(map[string]any)
		// chat only sets reasoning_content when content is non-empty; with empty
		// reasoning map the key is absent from the map.
		if _, exists := chatMsg["reasoning_content"]; exists {
			t.Errorf("[chat] reasoning_content key should be absent, got value %#v", chatMsg["reasoning_content"])
		}

		anthropicResp := anthropic.BuildResponse(result, "req", "model")
		anthropicContent := anthropicResp["content"].([]map[string]any)
		if len(anthropicContent) != 1 {
			t.Errorf("[anthropic] content length = %d, want 1 (just text)", len(anthropicContent))
		}
		if anthropicContent[0]["type"] != "text" {
			t.Errorf("[anthropic] content[0].type = %s, want text", anthropicContent[0]["type"])
		}
	})
}

// ---------------------------------------------------------------------------
// Test case: usage fields
// ---------------------------------------------------------------------------

// TestParity_UsageFields checks that usage is mapped correctly by each adapter,
// especially the difference between chat exposing top-level cached_tokens
// and anthropic using cache_read_input_tokens / cache_creation_input_tokens.
func TestParity_UsageFields(t *testing.T) {
	result := aggregate.Result{
		Text: "Done.",
		Usage: map[string]any{
			"input_tokens":  100,
			"output_tokens": 20,
			"total_tokens":  120,
			"input_tokens_details": map[string]any{
				"cached_tokens":         50,
				"cache_creation_tokens": 25,
			},
		},
	}

	chatResp := chat.BuildResponse(result)
	anthropicResp := anthropic.BuildResponse(result, "req_5", "gpt-4o")
	responsesResp := responses.BuildResponse(result)

	// Chat: usage.prompt_tokens = input_tokens
	chatUsage := chatResp["usage"].(map[string]any)
	if chatUsage["prompt_tokens"] != 100 {
		t.Errorf("[chat] prompt_tokens = %#v, want 100", chatUsage["prompt_tokens"])
	}
	if chatUsage["completion_tokens"] != 20 {
		t.Errorf("[chat] completion_tokens = %#v, want 20", chatUsage["completion_tokens"])
	}
	if chatUsage["total_tokens"] != 120 {
		t.Errorf("[chat] total_tokens = %#v, want 120", chatUsage["total_tokens"])
	}
	// Chat exposes cached_tokens at top level AND inside prompt_tokens_details
	if chatUsage["cached_tokens"] != 50 {
		t.Errorf("[chat] cached_tokens = %#v, want 50", chatUsage["cached_tokens"])
	}
	if chatUsage["cache_creation_tokens"] != 25 {
		t.Errorf("[chat] cache_creation_tokens = %#v, want 25", chatUsage["cache_creation_tokens"])
	}
	chatDetails := chatUsage["prompt_tokens_details"].(map[string]any)
	if chatDetails["cached_tokens"] != 50 {
		t.Errorf("[chat] prompt_tokens_details.cached_tokens = %#v, want 50", chatDetails["cached_tokens"])
	}

	// Anthropic: usage.input_tokens = input_tokens
	anthropicUsage := anthropicResp["usage"].(map[string]any)
	if anthropicUsage["input_tokens"] != 100 {
		t.Errorf("[anthropic] input_tokens = %#v, want 100", anthropicUsage["input_tokens"])
	}
	if anthropicUsage["output_tokens"] != 20 {
		t.Errorf("[anthropic] output_tokens = %#v, want 20", anthropicUsage["output_tokens"])
	}
	// Anthropic uses cache_read_input_tokens and cache_creation_input_tokens
	if anthropicUsage["cache_read_input_tokens"] != 50 {
		t.Errorf("[anthropic] cache_read_input_tokens = %#v, want 50", anthropicUsage["cache_read_input_tokens"])
	}
	if anthropicUsage["cache_creation_input_tokens"] != 25 {
		t.Errorf("[anthropic] cache_creation_input_tokens = %#v, want 25", anthropicUsage["cache_creation_input_tokens"])
	}

	// Responses: usage is passed through as-is (raw map)
	responsesUsage := responsesResp["usage"].(map[string]any)
	if responsesUsage["input_tokens"] != 100 {
		t.Errorf("[responses] input_tokens = %#v, want 100", responsesUsage["input_tokens"])
	}
}

// TestParity_EmptyUsage checks that empty usage is handled correctly.
// mapUsage/cloneMap return typed-nil maps; we check length to avoid the
// typed-nil interface comparison pitfall.
func TestParity_EmptyUsage(t *testing.T) {
	result := aggregate.Result{Text: "Hi."}

	chatResp := chat.BuildResponse(result)
	anthropicResp := anthropic.BuildResponse(result, "req", "model")
	responsesResp := responses.BuildResponse(result)

	if chatUsage := chatResp["usage"]; chatUsage != nil {
		if m, ok := chatUsage.(map[string]any); !ok || len(m) > 0 {
			t.Errorf("[chat] usage = %#v, want nil or empty", chatUsage)
		}
	}
	if anthropicUsage := anthropicResp["usage"]; anthropicUsage != nil {
		if m, ok := anthropicUsage.(map[string]any); !ok || len(m) > 0 {
			t.Errorf("[anthropic] usage = %#v, want nil or empty", anthropicUsage)
		}
	}
	if responsesUsage := responsesResp["usage"]; responsesUsage != nil {
		if m, ok := responsesUsage.(map[string]any); !ok || len(m) > 0 {
			t.Errorf("[responses] usage = %#v, want nil or empty", responsesUsage)
		}
	}
}

// ---------------------------------------------------------------------------
// Test case: finish reason mappings
// ---------------------------------------------------------------------------

// TestParity_FinishReason_Stop checks that the default "stop" finish reason
// maps to the correct string in each adapter.
func TestParity_FinishReason_Stop(t *testing.T) {
	result := aggregate.Result{Text: "Hello."}

	chatResp := chat.BuildResponse(result)
	anthropicResp := anthropic.BuildResponse(result, "req", "model")
	responsesResp := responses.BuildResponse(result)

	chatChoices := chatResp["choices"].([]map[string]any)
	if chatChoices[0]["finish_reason"] != "stop" {
		t.Errorf("[chat] finish_reason = %s, want stop", chatChoices[0]["finish_reason"])
	}

	if anthropicResp["stop_reason"] != "end_turn" {
		t.Errorf("[anthropic] stop_reason = %s, want end_turn", anthropicResp["stop_reason"])
	}

	if responsesResp["status"] != "completed" {
		t.Errorf("[responses] status = %s, want completed", responsesResp["status"])
	}
}

func TestParity_FinishReason_Length(t *testing.T) {
	result := aggregate.Result{Text: "Truncated.", FinishReason: "length"}

	chatResp := chat.BuildResponse(result)
	anthropicResp := anthropic.BuildResponse(result, "req", "model")

	chatChoices := chatResp["choices"].([]map[string]any)
	if chatChoices[0]["finish_reason"] != "length" {
		t.Errorf("[chat] finish_reason = %s, want length", chatChoices[0]["finish_reason"])
	}

	if anthropicResp["stop_reason"] != "max_tokens" {
		t.Errorf("[anthropic] stop_reason = %s, want max_tokens", anthropicResp["stop_reason"])
	}
}

func TestParity_FinishReason_MaxTokens(t *testing.T) {
	result := aggregate.Result{Text: "Truncated.", FinishReason: "max_tokens"}

	chatResp := chat.BuildResponse(result)
	anthropicResp := anthropic.BuildResponse(result, "req", "model")

	chatChoices := chatResp["choices"].([]map[string]any)
	if chatChoices[0]["finish_reason"] != "length" {
		t.Errorf("[chat] finish_reason = %s, want length (mapped from max_tokens)", chatChoices[0]["finish_reason"])
	}

	if anthropicResp["stop_reason"] != "max_tokens" {
		t.Errorf("[anthropic] stop_reason = %s, want max_tokens", anthropicResp["stop_reason"])
	}
}

func TestParity_FinishReason_ToolCalls(t *testing.T) {
	result := aggregate.Result{
		Text: "Done.",
		ToolCalls: []aggregate.ToolCall{{
			CallID:    "call_1",
			Name:      "foo",
			Arguments: `{}`,
		}},
		FinishReason: "tool_calls",
	}

	chatResp := chat.BuildResponse(result)
	anthropicResp := anthropic.BuildResponse(result, "req", "model")
	responsesResp := responses.BuildResponse(result)

	chatChoices := chatResp["choices"].([]map[string]any)
	if chatChoices[0]["finish_reason"] != "tool_calls" {
		t.Errorf("[chat] finish_reason = %s, want tool_calls", chatChoices[0]["finish_reason"])
	}

	if anthropicResp["stop_reason"] != "tool_use" {
		t.Errorf("[anthropic] stop_reason = %s, want tool_use", anthropicResp["stop_reason"])
	}

	// Responses: tool_calls finish reason with tool calls present should give status=completed
	if responsesResp["status"] != "completed" {
		t.Errorf("[responses] status = %s, want completed", responsesResp["status"])
	}
}

func TestParity_FinishReason_ToolUse(t *testing.T) {
	result := aggregate.Result{
		Text: "Done.",
		ToolCalls: []aggregate.ToolCall{{
			CallID:    "call_1",
			Name:      "foo",
			Arguments: `{}`,
		}},
		FinishReason: "tool_use",
	}

	chatResp := chat.BuildResponse(result)
	anthropicResp := anthropic.BuildResponse(result, "req", "model")

	chatChoices := chatResp["choices"].([]map[string]any)
	// chat maps tool_use -> tool_calls
	if chatChoices[0]["finish_reason"] != "tool_calls" {
		t.Errorf("[chat] finish_reason = %s, want tool_calls (mapped from tool_use)", chatChoices[0]["finish_reason"])
	}

	// anthropic maps tool_use -> tool_use (already tool_use)
	if anthropicResp["stop_reason"] != "tool_use" {
		t.Errorf("[anthropic] stop_reason = %s, want tool_use", anthropicResp["stop_reason"])
	}
}

// TestParity_FinishReason_ImplicitFromToolCalls checks that when no explicit
// FinishReason is set but ToolCalls are present, each adapter infers correctly.
func TestParity_FinishReason_ImplicitFromToolCalls(t *testing.T) {
	result := aggregate.Result{
		ToolCalls: []aggregate.ToolCall{{
			CallID:    "call_1",
			Name:      "foo",
			Arguments: `{}`,
		}},
		// No FinishReason set
	}

	chatResp := chat.BuildResponse(result)
	anthropicResp := anthropic.BuildResponse(result, "req", "model")

	chatChoices := chatResp["choices"].([]map[string]any)
	if chatChoices[0]["finish_reason"] != "tool_calls" {
		t.Errorf("[chat] finish_reason = %s, want tool_calls (implicit)", chatChoices[0]["finish_reason"])
	}

	if anthropicResp["stop_reason"] != "tool_use" {
		t.Errorf("[anthropic] stop_reason = %s, want tool_use (implicit)", anthropicResp["stop_reason"])
	}
}

// ---------------------------------------------------------------------------
// Test case: refusal
// ---------------------------------------------------------------------------

// TestParity_Refusal checks that refusal is surfaced correctly.
func TestParity_Refusal(t *testing.T) {
	result := aggregate.Result{
		Refusal: "I cannot comply with that request.",
	}

	chatResp := chat.BuildResponse(result)
	anthropicResp := anthropic.BuildResponse(result, "req", "model")

	// Chat: message.refusal should be set; if no text, content is nil
	chatChoices := chatResp["choices"].([]map[string]any)
	chatMsg := chatChoices[0]["message"].(map[string]any)
	if chatMsg["refusal"] != "I cannot comply with that request." {
		t.Errorf("[chat] refusal = %#v, want refusal text", chatMsg["refusal"])
	}
	if chatMsg["content"] != nil {
		t.Errorf("[chat] content = %#v, want nil when refusal is set without text", chatMsg["content"])
	}

	// Anthropic: refusal text becomes a text block
	anthropicContent := anthropicResp["content"].([]map[string]any)
	hasRefusalText := false
	for _, block := range anthropicContent {
		if block["type"] == "text" && block["text"] == "I cannot comply with that request." {
			hasRefusalText = true
		}
	}
	if !hasRefusalText {
		t.Errorf("[anthropic] content should include text block with refusal text")
	}
}

// ---------------------------------------------------------------------------
// Test case: empty content / edge cases
// ---------------------------------------------------------------------------

// TestParity_EmptyResult checks that a completely empty result is handled.
// This is a boundary case: all fields are zero values.
func TestParity_EmptyResult(t *testing.T) {
	result := aggregate.Result{}

	chatResp := chat.BuildResponse(result)
	anthropicResp := anthropic.BuildResponse(result, "req", "model")
	responsesResp := responses.BuildResponse(result)

	// Chat: content should be nil (no text, no tool calls → string("") → assigned)
	// With zero-value Text ("") and no ToolCalls, chat sets content = any("") = "".
	chatChoices := chatResp["choices"].([]map[string]any)
	chatMsg := chatChoices[0]["message"].(map[string]any)
	if chatMsg["content"] != "" {
		t.Errorf("[chat] content = %#v, want \"\" for empty result (string zero-value)", chatMsg["content"])
	}
	if chatChoices[0]["finish_reason"] != "stop" {
		t.Errorf("[chat] finish_reason = %s, want stop for empty result", chatChoices[0]["finish_reason"])
	}

	// Anthropic: should still produce at least one content block
	// (current code falls back to text block with empty string result.Text)
	anthropicContent := anthropicResp["content"].([]map[string]any)
	if len(anthropicContent) == 0 {
		t.Errorf("[anthropic] content is empty for empty result, want at least one text block")
	}

	// Responses: output should be non-empty due to fallback message construction
	responsesOutput := responsesResp["output"].([]map[string]any)
	if len(responsesOutput) == 0 {
		t.Errorf("[responses] output is empty for empty result, want fallback message")
	}
}

// TestParity_EmptyTextWithToolCallsAndRefusal verifies the interaction of
// empty text, tool calls, and refusal.
// PARITY GAP: chat currently includes tool_calls even when refusal is set
// (refusal does NOT suppress tool_calls), while anthropic includes refusal
// as a text block. This test documents current behavior (not ideal parity).
func TestParity_EmptyTextWithToolCallsAndRefusal(t *testing.T) {
	result := aggregate.Result{
		Refusal: "No.",
		ToolCalls: []aggregate.ToolCall{{
			CallID:    "call_1",
			Name:      "foo",
			Arguments: `{}`,
		}},
	}

	chatResp := chat.BuildResponse(result)
	chatChoices := chatResp["choices"].([]map[string]any)
	chatMsg := chatChoices[0]["message"].(map[string]any)

	// Refusal is set correctly
	if chatMsg["refusal"] != "No." {
		t.Errorf("[chat] refusal = %#v, want \"No.\"", chatMsg["refusal"])
	}
	// content is nil when refusal is set without text
	if chatMsg["content"] != nil {
		t.Errorf("[chat] content = %#v, want nil when refusal is set without text", chatMsg["content"])
	}
	// PARITY GAP: chat still includes tool_calls even with refusal (not suppressed)
	if chatMsg["tool_calls"] == nil {
		t.Errorf("[chat] tool_calls = nil, PARITY GAP: should be suppressed when refusal is set")
	}
}

// ---------------------------------------------------------------------------
// Test case: output_tokens_details
// ---------------------------------------------------------------------------

// TestParity_OutputTokensDetails checks that output_tokens_details are
// preserved in chat's completion_tokens_details but not in anthropic.
func TestParity_OutputTokensDetails(t *testing.T) {
	result := aggregate.Result{
		Text: "Done.",
		Usage: map[string]any{
			"input_tokens":  100,
			"output_tokens": 20,
			"output_tokens_details": map[string]any{
				"reasoning_tokens": 10,
			},
		},
	}

	chatResp := chat.BuildResponse(result)
	anthropicResp := anthropic.BuildResponse(result, "req", "model")

	chatUsage := chatResp["usage"].(map[string]any)
	chatDetails := chatUsage["completion_tokens_details"].(map[string]any)
	if chatDetails["reasoning_tokens"] != 10 {
		t.Errorf("[chat] completion_tokens_details.reasoning_tokens = %#v, want 10", chatDetails["reasoning_tokens"])
	}

	// Anthropic does not surface output_tokens_details
	anthropicUsage := anthropicResp["usage"].(map[string]any)
	if _, exists := anthropicUsage["output_tokens_details"]; exists {
		t.Errorf("[anthropic] output_tokens_details should not be surfaced")
	}
}

// ---------------------------------------------------------------------------
// Test case: responses-specific fields
// ---------------------------------------------------------------------------

// TestParity_ResponsesOutputItems checks that when ResponseOutputItems are
// already populated, responses adapter uses them directly while chat and
// anthropic ignore them (they only consume Text, ToolCalls, Reasoning, etc.).
func TestParity_ResponsesOutputItems(t *testing.T) {
	result := aggregate.Result{
		ResponseOutputItems: []map[string]any{
			{
				"id":      "msg_custom",
				"type":    "message",
				"status":  "completed",
				"role":    "assistant",
				"content": []map[string]any{{"type": "output_text", "text": "From output items"}},
			},
		},
		// These should be ignored when ResponseOutputItems is non-empty
		Text: "From result.Text",
	}

	responsesResp := responses.BuildResponse(result)
	responsesOutput := responsesResp["output"].([]map[string]any)

	// When ResponseOutputItems is populated, it should be used as-is
	if len(responsesOutput) == 0 {
		t.Errorf("[responses] output is empty, want ResponseOutputItems used")
	}
	if responsesOutput[0]["id"] != "msg_custom" {
		t.Errorf("[responses] output[0].id = %#v, want \"msg_custom\"", responsesOutput[0]["id"])
	}
}

// TestParity_ResponsesResponseID checks that custom ResponseID is preserved.
func TestParity_ResponsesResponseID(t *testing.T) {
	result := aggregate.Result{
		ResponseID: "resp_custom_123",
		Text:       "Hello.",
	}

	responsesResp := responses.BuildResponse(result)
	if responsesResp["id"] != "resp_custom_123" {
		t.Errorf("[responses] id = %s, want resp_custom_123", responsesResp["id"])
	}

	// Chat and anthropic don't have a response ID field to check
}

// ---------------------------------------------------------------------------
// Test case: parallel tool calls
// ---------------------------------------------------------------------------

// TestParity_MultipleToolCalls checks that multiple tool calls are all
// surfaced in each adapter's respective format.
func TestParity_MultipleToolCalls(t *testing.T) {
	result := aggregate.Result{
		ToolCalls: []aggregate.ToolCall{
			{CallID: "call_a", Name: "foo", Arguments: `{"a":1}`},
			{CallID: "call_b", Name: "bar", Arguments: `{"b":2}`},
		},
	}

	chatResp := chat.BuildResponse(result)
	anthropicResp := anthropic.BuildResponse(result, "req", "model")
	responsesResp := responses.BuildResponse(result)

	// Chat: should have 2 tool calls
	chatChoices := chatResp["choices"].([]map[string]any)
	chatMsg := chatChoices[0]["message"].(map[string]any)
	chatToolCalls := chatMsg["tool_calls"].([]map[string]any)
	if len(chatToolCalls) != 2 {
		t.Errorf("[chat] tool_calls length = %d, want 2", len(chatToolCalls))
	}

	// Anthropic: should have 2 tool_use blocks
	anthropicContent := anthropicResp["content"].([]map[string]any)
	toolUseCount := 0
	for _, block := range anthropicContent {
		if block["type"] == "tool_use" {
			toolUseCount++
		}
	}
	if toolUseCount != 2 {
		t.Errorf("[anthropic] tool_use blocks = %d, want 2", toolUseCount)
	}

	// Responses: should have 2 function_call blocks
	responsesOutput := responsesResp["output"].([]map[string]any)
	funcCallCount := 0
	for _, item := range responsesOutput {
		if item["type"] == "function_call" {
			funcCallCount++
		}
	}
	if funcCallCount != 2 {
		t.Errorf("[responses] function_call blocks = %d, want 2", funcCallCount)
	}
}

// ---------------------------------------------------------------------------
// Test case: unknown / non-standard finish reasons
// ---------------------------------------------------------------------------

// TestParity_UnknownFinishReason checks that unknown finish reasons pass
// through as-is in all adapters (with appropriate mapping in responses
// status determination).
func TestParity_UnknownFinishReason(t *testing.T) {
	result := aggregate.Result{
		Text:         "Hello.",
		FinishReason: "unknown_reason",
	}

	chatResp := chat.BuildResponse(result)
	anthropicResp := anthropic.BuildResponse(result, "req", "model")
	responsesResp := responses.BuildResponse(result)

	chatChoices := chatResp["choices"].([]map[string]any)
	if chatChoices[0]["finish_reason"] != "unknown_reason" {
		t.Errorf("[chat] finish_reason = %s, want unknown_reason", chatChoices[0]["finish_reason"])
	}

	if anthropicResp["stop_reason"] != "unknown_reason" {
		t.Errorf("[anthropic] stop_reason = %s, want unknown_reason", anthropicResp["stop_reason"])
	}

	// Responses: unknown finish reason should result in status=incomplete
	if responsesResp["status"] != "incomplete" {
		t.Errorf("[responses] status = %s, want incomplete for unknown finish_reason", responsesResp["status"])
	}
}

// TestParity_IncompleteDetails checks that responses includes incomplete_details
// when status is incomplete.
func TestParity_IncompleteDetails(t *testing.T) {
	result := aggregate.Result{
		Text:         "Incomplete.",
		FinishReason: "content_filter",
	}

	responsesResp := responses.BuildResponse(result)
	if responsesResp["status"] != "incomplete" {
		t.Errorf("[responses] status = %s, want incomplete", responsesResp["status"])
	}

	incompleteDetails := responsesResp["incomplete_details"].(map[string]any)
	if incompleteDetails["reason"] != "content_filter" {
		t.Errorf("[responses] incomplete_details.reason = %s, want content_filter", incompleteDetails["reason"])
	}
}
