package responses

import (
	"fmt"

	"openai-compat-proxy/internal/aggregate"
)

func BuildResponse(result aggregate.Result) map[string]any {
	var output []map[string]any
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
		output = append(output, map[string]any{
			"id":        call.ID,
			"type":      "function_call",
			"status":    "completed",
			"call_id":   call.CallID,
			"name":      call.Name,
			"arguments": call.Arguments,
		})
	}

	return map[string]any{
		"id":        buildResponseID(result),
		"object":    "response",
		"status":    "completed",
		"output":    output,
		"reasoning": result.Reasoning,
		"usage":     cloneMap(result.Usage),
	}
}

func buildResponseID(result aggregate.Result) string {
	if len(result.ToolCalls) > 0 && result.ToolCalls[0].CallID != "" {
		return fmt.Sprintf("resp_%s", result.ToolCalls[0].CallID)
	}
	return "resp_proxy"
}

func buildOutputItemID(result aggregate.Result) string {
	if len(result.ToolCalls) > 0 && result.ToolCalls[0].ID != "" {
		return fmt.Sprintf("msg_%s", result.ToolCalls[0].ID)
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
