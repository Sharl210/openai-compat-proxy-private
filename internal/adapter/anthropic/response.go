package anthropic

import (
	"encoding/json"

	"openai-compat-proxy/internal/aggregate"
)

func BuildResponse(result aggregate.Result, modelName string) map[string]any {
	content := make([]map[string]any, 0, len(result.ToolCalls)+1)
	if len(result.ToolCalls) > 0 {
		for _, call := range result.ToolCalls {
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    call.CallID,
				"name":  call.Name,
				"input": parseArguments(call.Arguments),
			})
		}
	} else {
		content = append(content, map[string]any{
			"type": "text",
			"text": result.Text,
		})
	}
	stopReason := "end_turn"
	if len(result.ToolCalls) > 0 {
		stopReason = "tool_use"
	}
	return map[string]any{
		"type":          "message",
		"role":          "assistant",
		"model":         modelName,
		"content":       content,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage":         mapUsage(result.Usage),
	}
}

func mapUsage(usage map[string]any) map[string]any {
	out := map[string]any{}
	if input, ok := usage["input_tokens"]; ok {
		out["input_tokens"] = input
	}
	if output, ok := usage["output_tokens"]; ok {
		out["output_tokens"] = output
	}
	if details, _ := usage["input_tokens_details"].(map[string]any); len(details) > 0 {
		if cached, ok := details["cached_tokens"]; ok {
			out["cache_read_input_tokens"] = cached
		}
		if created, ok := details["cache_creation_tokens"]; ok {
			out["cache_creation_input_tokens"] = created
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseArguments(arguments string) any {
	if arguments == "" {
		return map[string]any{}
	}
	var decoded any
	if err := json.Unmarshal([]byte(arguments), &decoded); err != nil {
		return map[string]any{"raw": arguments}
	}
	return decoded
}
