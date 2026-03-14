package chat

import "openai-compat-proxy/internal/aggregate"

func BuildResponse(result aggregate.Result) map[string]any {
	message := map[string]any{
		"role":    "assistant",
		"content": result.Text,
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

	return map[string]any{
		"object": "chat.completion",
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": "stop",
		}},
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
