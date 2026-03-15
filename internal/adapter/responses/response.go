package responses

import "openai-compat-proxy/internal/aggregate"

func BuildResponse(result aggregate.Result) map[string]any {
	content := result.ResponseMessageContent
	if len(content) == 0 {
		content = []map[string]any{{
			"type": "output_text",
			"text": result.Text,
		}}
	}

	outputItem := map[string]any{
		"type":    "message",
		"role":    "assistant",
		"content": content,
	}

	if len(result.ToolCalls) > 0 {
		var toolCalls []map[string]any
		for _, call := range result.ToolCalls {
			toolCalls = append(toolCalls, map[string]any{
				"id":        call.CallID,
				"name":      call.Name,
				"arguments": call.Arguments,
			})
		}
		outputItem["tool_calls"] = toolCalls
	}

	return map[string]any{
		"object":    "response",
		"status":    "completed",
		"output":    []map[string]any{outputItem},
		"reasoning": result.Reasoning,
		"usage":     cloneMap(result.Usage),
	}
}

func cloneMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(input))
	for k, v := range input {
		cloned[k] = v
	}
	return cloned
}
