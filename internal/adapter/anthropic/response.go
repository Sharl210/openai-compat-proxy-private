package anthropic

import (
	"encoding/json"

	"openai-compat-proxy/internal/aggregate"
)

func BuildResponse(result aggregate.Result, requestID string, modelName string) map[string]any {
	content := make([]map[string]any, 0, len(result.ToolCalls)+1)
	if thinking := reasoningContentValue(result.Reasoning); thinking != "" {
		content = append(content, map[string]any{
			"type":     "thinking",
			"thinking": thinking,
		})
	}
	if result.Text != "" {
		content = append(content, map[string]any{
			"type": "text",
			"text": result.Text,
		})
	} else if result.Refusal != "" {
		content = append(content, map[string]any{
			"type": "text",
			"text": result.Refusal,
		})
	}
	if len(result.ToolCalls) > 0 {
		for _, call := range result.ToolCalls {
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    call.CallID,
				"name":  call.Name,
				"input": parseArguments(call.Arguments),
			})
		}
	} else if len(content) == 0 {
		content = append(content, map[string]any{
			"type": "text",
			"text": result.Text,
		})
	}
	stopReason := anthropicStopReason(result)
	return map[string]any{
		"id":            requestID,
		"type":          "message",
		"role":          "assistant",
		"model":         modelName,
		"content":       content,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage":         mapUsage(result.Usage),
	}
}

func anthropicStopReason(result aggregate.Result) string {
	if result.FinishReason != "" {
		switch result.FinishReason {
		case "tool_calls":
			return "tool_use"
		case "length":
			return "max_tokens"
		default:
			return result.FinishReason
		}
	}
	if len(result.ToolCalls) > 0 {
		return "tool_use"
	}
	return "end_turn"
}

func reasoningContentValue(reasoning map[string]any) string {
	if len(reasoning) == 0 {
		return ""
	}
	for _, key := range []string{"thinking", "reasoning_content", "summary", "content", "delta"} {
		if text, _ := reasoning[key].(string); text != "" {
			return text
		}
	}
	return ""
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

func usageNumberAsFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
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
