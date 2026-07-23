package chat

import (
	"strings"

	"openai-compat-proxy/internal/aggregate"
	reasoningtext "openai-compat-proxy/internal/reasoning"
	"openai-compat-proxy/internal/texttail"
)

func BuildResponse(result aggregate.Result) map[string]any {
	content := any(texttail.TrimTrailingCRLF(result.Text))
	if len(result.ToolCalls) > 0 && result.Text == "" {
		content = nil
	}
	message := map[string]any{
		"role":    "assistant",
		"content": content,
	}
	if result.Refusal != "" {
		message["refusal"] = result.Refusal
		if result.Text == "" {
			message["content"] = nil
		}
	}
	reasoningContent := reasoningContentValue(result.Reasoning)
	if reasoningContent == "" {
		reasoningContent = reasoningBlocksContentValue(result.ReasoningBlocks)
	}
	if reasoningContent != "" {
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
	finishReason := chatFinishReason(result)

	response := map[string]any{
		"object": "chat.completion",
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
		"usage": chatUsage(result.Usage),
	}
	if result.ServiceTier != "" {
		response["service_tier"] = result.ServiceTier
	}
	return response
}

func chatFinishReason(result aggregate.Result) string {
	if result.FinishReason != "" {
		switch result.FinishReason {
		case "tool_use":
			return "tool_calls"
		case "max_tokens":
			return "length"
		default:
			return result.FinishReason
		}
	}
	if len(result.ToolCalls) > 0 {
		return "tool_calls"
	}
	return "stop"
}

func reasoningContentValue(reasoning map[string]any) string {
	if len(reasoning) == 0 {
		return ""
	}
	for _, key := range []string{"reasoning_content", "summary", "content", "delta"} {
		if text, _ := reasoning[key].(string); text != "" {
			return reasoningtext.FormatText(text)
		}
	}
	return ""
}

func reasoningBlocksContentValue(blocks []map[string]any) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if thinking, _ := block["thinking"].(string); thinking != "" {
			parts = append(parts, reasoningtext.FormatText(thinking))
			continue
		}
		if text := reasoningContentValue(block); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
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
		if cachedTokens, ok := details["cached_tokens"]; ok {
			result["cached_tokens"] = cachedTokens
		}
		if cacheCreationTokens, ok := details["cache_creation_tokens"]; ok {
			result["cache_creation_tokens"] = cacheCreationTokens
		}
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
