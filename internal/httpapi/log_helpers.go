package httpapi

import (
	"crypto/sha256"
	"encoding"
	"encoding/json"
	"fmt"
	"hash"

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
	prefixHashes := hashCanonicalMessagePrefixes(req.Messages)
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
	}
	toolNames := make([]string, 0, len(req.Tools))
	for _, tool := range req.Tools {
		toolNames = append(toolNames, tool.Name)
	}
	reasoningMode := ""
	if req.Reasoning != nil {
		reasoningMode = string(req.Reasoning.Mode)
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
		"reasoning_mode":           reasoningMode,
		"reasoning_mode_origin":    string(req.ReasoningModeOrigin),
	}
}

type canonicalHashState interface {
	encoding.BinaryMarshaler
	encoding.BinaryUnmarshaler
}

func hashCanonicalMessagePrefixes(messages []model.CanonicalMessage) []string {
	return hashCanonicalMessagePrefixesWithHasher(messages, sha256.New())
}

func hashCanonicalMessagePrefixesWithHasher(messages []model.CanonicalMessage, prefixHasher hash.Hash) []string {
	state, ok := prefixHasher.(canonicalHashState)
	if !ok || !writeCanonicalHashBytes(prefixHasher, []byte{'['}) {
		return legacyCanonicalMessagePrefixHashes(messages)
	}
	prefixHashes := make([]string, 0, len(messages))
	for index := range messages {
		encodedMessage, err := json.Marshal(messages[index])
		if err != nil {
			return legacyCanonicalMessagePrefixHashes(messages)
		}
		if index > 0 && !writeCanonicalHashBytes(prefixHasher, []byte{','}) {
			return legacyCanonicalMessagePrefixHashes(messages)
		}
		if !writeCanonicalHashBytes(prefixHasher, encodedMessage) {
			return legacyCanonicalMessagePrefixHashes(messages)
		}
		checkpoint, err := state.MarshalBinary()
		if err != nil || !writeCanonicalHashBytes(prefixHasher, []byte{']'}) {
			return legacyCanonicalMessagePrefixHashes(messages)
		}
		sum := prefixHasher.Sum(nil)
		prefixHashes = append(prefixHashes, fmt.Sprintf("%x", sum[:8]))
		if err := state.UnmarshalBinary(checkpoint); err != nil {
			return legacyCanonicalMessagePrefixHashes(messages)
		}
	}
	return prefixHashes
}

func writeCanonicalHashBytes(hasher hash.Hash, value []byte) bool {
	_, err := hasher.Write(value)
	return err == nil
}

func legacyCanonicalMessagePrefixHashes(messages []model.CanonicalMessage) []string {
	prefixHashes := make([]string, 0, len(messages))
	for index := range messages {
		prefixHashes = append(prefixHashes, hashAny(messages[:index+1]))
	}
	return prefixHashes
}

func hashAny(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "marshal_error"
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:8])
}
