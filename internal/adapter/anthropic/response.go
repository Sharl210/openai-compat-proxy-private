package anthropic

import "openai-compat-proxy/internal/aggregate"

func BuildResponse(result aggregate.Result, modelName string) map[string]any {
	content := []map[string]any{{
		"type": "text",
		"text": result.Text,
	}}
	return map[string]any{
		"type":          "message",
		"role":          "assistant",
		"model":         modelName,
		"content":       content,
		"stop_reason":   "end_turn",
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
	if len(out) == 0 {
		return nil
	}
	return out
}
