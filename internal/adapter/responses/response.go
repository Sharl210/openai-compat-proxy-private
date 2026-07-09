package responses

import (
	"strconv"

	"openai-compat-proxy/internal/aggregate"
	"openai-compat-proxy/internal/syntaxrepair"
)

func BuildResponse(result aggregate.Result) map[string]any {
	var output []map[string]any
	if len(result.ResponseOutputItems) > 0 {
		output = append(output, filterResponseOutputItems(result.ResponseOutputItems)...)
	}
	content := result.ResponseMessageContent
	if len(content) == 0 && result.Text != "" {
		content = []map[string]any{{
			"type": "output_text",
			"text": result.Text,
		}}
	} else if len(content) == 0 && len(output) == 0 && len(result.ToolCalls) == 0 {
		content = []map[string]any{{
			"type": "output_text",
			"text": result.Text,
		}}
	}
	if len(content) > 0 && !hasResponsesOutputItemType(output, "message") {
		output = append(output, map[string]any{
			"id":      buildOutputItemID(result),
			"type":    "message",
			"status":  "completed",
			"role":    "assistant",
			"content": content,
		})
	}
	if len(result.ReasoningBlocks) > 0 && !hasResponsesOutputItemType(output, "reasoning") {
		output = insertResponsesOutputItemsBeforeFirstFunctionCall(output, responsesReasoningOutputItems(result.ReasoningBlocks))
	}
	for _, call := range result.ToolCalls {
		if hasResponsesFunctionCall(output, call) {
			continue
		}
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

	response := map[string]any{
		"id":                 buildResponseID(result),
		"object":             "response",
		"status":             responsesStatus(result),
		"output":             output,
		"reasoning":          outwardReasoning(result.Reasoning),
		"usage":              cloneMap(result.Usage),
		"incomplete_details": responsesIncompleteDetails(result),
	}
	if result.ServiceTier != "" {
		response["service_tier"] = result.ServiceTier
	}
	return response
}

func responsesReasoningOutputItems(blocks []map[string]any) []map[string]any {
	if len(blocks) == 0 {
		return nil
	}
	items := make([]map[string]any, 0, len(blocks))
	for idx, block := range blocks {
		if len(block) == 0 {
			continue
		}
		item := cloneMap(block)
		if stringValue(item["type"]) == "thinking" {
			item["type"] = "reasoning"
		}
		if stringValue(item["type"]) == "reasoning" {
			if signature := stringValue(item["signature"]); signature != "" && stringValue(item["encrypted_content"]) == "" {
				item["encrypted_content"] = signature
			}
			delete(item, "signature")
		}
		if stringValue(item["id"]) == "" {
			item["id"] = buildReasoningOutputItemID(idx)
		}
		items = append(items, item)
	}
	return items
}

func insertResponsesOutputItemsBeforeFirstFunctionCall(output []map[string]any, items []map[string]any) []map[string]any {
	if len(items) == 0 {
		return output
	}
	insertAt := len(output)
	for idx, item := range output {
		if stringValue(item["type"]) == "function_call" {
			insertAt = idx
			break
		}
	}
	merged := make([]map[string]any, 0, len(output)+len(items))
	merged = append(merged, output[:insertAt]...)
	merged = append(merged, items...)
	merged = append(merged, output[insertAt:]...)
	return merged
}

func buildReasoningOutputItemID(index int) string {
	if index == 0 {
		return "rs_upstream"
	}
	return "rs_upstream_" + strconv.Itoa(index)
}

func outwardReasoning(reasoning map[string]any) map[string]any {
	if len(reasoning) == 0 {
		return nil
	}
	cloned := cloneMap(reasoning)
	delete(cloned, aggregate.InternalReasoningSourceKey)
	if stringValue(cloned["summary"]) == "" {
		for _, key := range []string{"reasoning_content", "content", "delta", "text"} {
			if text := stringValue(cloned[key]); text != "" {
				cloned["summary"] = text
				break
			}
		}
	}
	if len(cloned) == 0 {
		return nil
	}
	return cloned
}

func filterResponseOutputItems(items []map[string]any) []map[string]any {
	if len(items) == 0 {
		return nil
	}
	cloned := cloneOutputItems(items)
	return cloned
}

func hasResponsesOutputItemType(items []map[string]any, itemType string) bool {
	for _, item := range items {
		if stringValue(item["type"]) == itemType {
			return true
		}
	}
	return false
}

func hasResponsesFunctionCall(items []map[string]any, call aggregate.ToolCall) bool {
	for _, item := range items {
		if stringValue(item["type"]) != "function_call" {
			continue
		}
		if call.ID != "" && stringValue(item["id"]) == call.ID {
			return true
		}
		if call.CallID != "" && stringValue(item["call_id"]) == call.CallID {
			return true
		}
	}
	return false
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
