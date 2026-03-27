package chat

import "openai-compat-proxy/internal/aggregate"

func BuildResponse(result aggregate.Result) map[string]any {
	content := any(result.Text)
	if len(result.ToolCalls) > 0 && result.Text == "" {
		content = nil
	}
	message := map[string]any{
		"role":    "assistant",
		"content": content,
	}
	if reasoningContent := reasoningContentValue(result.Reasoning); reasoningContent != "" {
		message["reasoning_content"] = reasoningContent
	}

	if len(result.ToolCalls) > 0 {
		var toolCalls []map[string]any
		for _, call := range result.ToolCalls {
			toolCalls = append(toolCalls, map[string]any{
				"id":   call.CallID,
				"type": "function",
				"function": map[string]any{
					"name":      call.Name,
					"arguments": call.Arguments,
				},
			})
		}
		message["tool_calls"] = toolCalls
	}
	finishReason := "stop"
	if len(result.ToolCalls) > 0 {
		finishReason = "tool_calls"
	}

	return map[string]any{
		"object": "chat.completion",
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
		"usage": chatUsage(result.Usage),
	}
}

func reasoningContentValue(reasoning map[string]any) string {
	if len(reasoning) == 0 {
		return ""
	}
	for _, key := range []string{"reasoning_content", "summary", "content", "delta"} {
		if text, _ := reasoning[key].(string); text != "" {
			return text
		}
	}
	return ""
}

func chatUsage(usage map[string]any) any {
	if len(usage) == 0 {
		return nil
	}
	result := map[string]any{}
	if promptTokens, ok := usage["input_tokens"]; ok {
		result["prompt_tokens"] = promptTokens
	}
	if completionTokens, ok := usage["output_tokens"]; ok {
		result["completion_tokens"] = completionTokens
	}
	if totalTokens, ok := usage["total_tokens"]; ok {
		result["total_tokens"] = totalTokens
	}
	if details, _ := usage["input_tokens_details"].(map[string]any); len(details) > 0 {
		result["prompt_tokens_details"] = cloneUsageMap(details)
	}
	if details, _ := usage["output_tokens_details"].(map[string]any); len(details) > 0 {
		result["completion_tokens_details"] = cloneUsageMap(details)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func cloneUsageMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(input))
	for k, v := range input {
		cloned[k] = v
	}
	return cloned
}
