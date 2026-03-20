package responses

import (
	"fmt"

	"openai-compat-proxy/internal/aggregate"
)

func BuildResponse(result aggregate.Result) map[string]any {
	content := result.ResponseMessageContent
	if len(content) == 0 {
		content = []map[string]any{{
			"type": "output_text",
			"text": result.Text,
		}}
	}

	outputItem := map[string]any{
		"id":      buildOutputItemID(result),
		"type":    "message",
		"status":  "completed",
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
		"id":        buildResponseID(result),
		"object":    "response",
		"status":    "completed",
		"output":    []map[string]any{outputItem},
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
