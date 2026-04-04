package httpapi

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"openai-compat-proxy/internal/model"
)

func nestedCachedTokens(usage map[string]any) any {
	if len(usage) == 0 {
		return nil
	}
	if details, _ := usage["input_tokens_details"].(map[string]any); len(details) > 0 {
		if cachedTokens, ok := details["cached_tokens"]; ok {
			return cachedTokens
		}
	}
	if details, _ := usage["prompt_tokens_details"].(map[string]any); len(details) > 0 {
		if cachedTokens, ok := details["cached_tokens"]; ok {
			return cachedTokens
		}
	}
	if cachedTokens, ok := usage["cache_read_input_tokens"]; ok {
		return cachedTokens
	}
	if cachedTokens, ok := usage["cached_tokens"]; ok {
		return cachedTokens
	}
	return nil
}

func canonicalLogAttrs(req model.CanonicalRequest) map[string]any {
	roles := make([]string, 0, len(req.Messages))
	messageHashes := make([]string, 0, len(req.Messages))
	prefixHashes := make([]string, 0, len(req.Messages))
	textBytes := make([]int, 0, len(req.Messages))
	toolCallCounts := make([]int, 0, len(req.Messages))
	hasReasoningContent := make([]bool, 0, len(req.Messages))
	totalTextBytes := 0
	for i := range req.Messages {
		msg := req.Messages[i]
		roles = append(roles, msg.Role)
		toolCallCounts = append(toolCallCounts, len(msg.ToolCalls))
		hasReasoningContent = append(hasReasoningContent, msg.ReasoningContent != "")
		msgTextBytes := 0
		for _, part := range msg.Parts {
			msgTextBytes += len(part.Text)
		}
		textBytes = append(textBytes, msgTextBytes)
		totalTextBytes += msgTextBytes
		messageHashes = append(messageHashes, hashAny(map[string]any{
			"role":              msg.Role,
			"parts":             msg.Parts,
			"tool_calls":        msg.ToolCalls,
			"tool_call_id":      msg.ToolCallID,
			"reasoning_content": msg.ReasoningContent,
		}))
		prefixHashes = append(prefixHashes, hashAny(req.Messages[:i+1]))
	}
	toolNames := make([]string, 0, len(req.Tools))
	for _, tool := range req.Tools {
		toolNames = append(toolNames, tool.Name)
	}
	return map[string]any{
		"message_roles":            roles,
		"message_hashes":           messageHashes,
		"prefix_hashes":            prefixHashes,
		"message_text_bytes":       textBytes,
		"message_tool_call_counts": toolCallCounts,
		"has_reasoning_content":    hasReasoningContent,
		"total_text_bytes":         totalTextBytes,
		"tool_names":               toolNames,
	}
}

func hashAny(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "marshal_error"
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:8])
}
