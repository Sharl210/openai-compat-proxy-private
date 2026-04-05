package responses

import (
	"openai-compat-proxy/internal/aggregate"
	"openai-compat-proxy/internal/syntaxrepair"
)

func BuildResponse(result aggregate.Result) map[string]any {
	var output []map[string]any
	if len(result.ResponseOutputItems) > 0 {
		output = append(output, cloneOutputItems(result.ResponseOutputItems)...)
	}
	if len(output) == 0 {
		content := result.ResponseMessageContent
		if len(content) == 0 && (result.Text != "" || len(result.ToolCalls) == 0) {
			content = []map[string]any{{
				"type": "output_text",
				"text": result.Text,
			}}
		}
		if len(content) > 0 {
			output = append(output, map[string]any{
				"id":      buildOutputItemID(result),
				"type":    "message",
				"status":  "completed",
				"role":    "assistant",
				"content": content,
			})
		}
		for _, call := range result.ToolCalls {
			item := map[string]any{
				"id":        call.ID,
				"type":      "function_call",
				"status":    "completed",
				"call_id":   call.CallID,
				"name":      call.Name,
				"arguments": call.Arguments,
			}
			if parsed := parseToolParameters(call.Arguments); len(parsed) > 0 {
				item["parameters"] = parsed
			}
			output = append(output, item)
		}
	}

	return map[string]any{
		"id":                 buildResponseID(result),
		"object":             "response",
		"status":             responsesStatus(result),
		"output":             output,
		"reasoning":          result.Reasoning,
		"usage":              cloneMap(result.Usage),
		"incomplete_details": responsesIncompleteDetails(result),
	}
}

func responsesStatus(result aggregate.Result) string {
	if reason := result.FinishReason; reason != "" && reason != "stop" && reason != "tool_calls" && reason != "tool_use" && reason != "end_turn" {
		return "incomplete"
	}
	return "completed"
}

func responsesIncompleteDetails(result aggregate.Result) any {
	if responsesStatus(result) != "incomplete" {
		return nil
	}
	return map[string]any{"reason": result.FinishReason}
}

func cloneOutputItems(input []map[string]any) []map[string]any {
	if len(input) == 0 {
		return nil
	}
	cloned := make([]map[string]any, 0, len(input))
	for _, item := range input {
		copy := make(map[string]any, len(item))
		for k, v := range item {
			copy[k] = v
		}
		if itemType, _ := copy["type"].(string); itemType == "function_call" {
			if _, exists := copy["parameters"]; !exists {
				if parsed := parseToolParameters(stringValue(copy["arguments"])); len(parsed) > 0 {
					copy["parameters"] = parsed
				}
			}
		}
		cloned = append(cloned, copy)
	}
	return cloned
}

func buildResponseID(result aggregate.Result) string {
	if result.ResponseID != "" {
		return result.ResponseID
	}
	return "resp_proxy"
}

func buildOutputItemID(result aggregate.Result) string {
	if len(result.ToolCalls) > 0 && result.ToolCalls[0].ID != "" {
		return "msg_" + result.ToolCalls[0].ID
	}
	if result.Text != "" {
		return "msg_proxy"
	}
	return "msg_output"
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

func parseToolParameters(arguments string) map[string]any {
	if arguments == "" {
		return nil
	}
	parsed, _, ok := syntaxrepair.ParseJSONObject(arguments)
	if !ok || len(parsed) == 0 {
		return nil
	}
	return parsed
}

func stringValue(v any) string {
	if s, _ := v.(string); s != "" {
		return s
	}
	return ""
}
