package httpapi

import (
	"fmt"
	"strings"

	"openai-compat-proxy/internal/model"
)

func stripAnthropicCacheControl(req *model.CanonicalRequest) {
	if req == nil {
		return
	}
	deleteNestedKey(req.PreservedTopLevelFields, "cache_control")
	if req.Reasoning != nil {
		deleteNestedKey(req.Reasoning.Raw, "cache_control")
	}
	for toolIndex := range req.Tools {
		deleteNestedKey(req.Tools[toolIndex].Raw, "cache_control")
		deleteNestedKey(req.Tools[toolIndex].Parameters, "cache_control")
	}
	for msgIndex := range req.Messages {
		deleteNestedKey(req.Messages[msgIndex].ReasoningBlocks, "cache_control")
		for partIndex := range req.Messages[msgIndex].Parts {
			deleteNestedKey(req.Messages[msgIndex].Parts[partIndex].Raw, "cache_control")
		}
	}
}

func deleteNestedKey(value any, key string) {
	switch typed := value.(type) {
	case map[string]any:
		delete(typed, key)
		for _, child := range typed {
			deleteNestedKey(child, key)
		}
	case []any:
		for _, child := range typed {
			deleteNestedKey(child, key)
		}
	case []map[string]any:
		for _, child := range typed {
			deleteNestedKey(child, key)
		}
	}
}

func prepareCanonicalMessages(messages []model.CanonicalMessage) []model.CanonicalMessage {
	if len(messages) == 0 {
		return messages
	}
	droppedToolCallIDs := map[string]struct{}{}
	immediateToolCallIDs := immediateToolResultIDs(messages)
	unmatchedToolResultIDs := unmatchedToolResultIDs(messages, immediateToolCallIDs)
	for _, msg := range messages {
		if shouldDropToolMessageFromHistory(msg) && msg.ToolCallID != "" {
			droppedToolCallIDs[msg.ToolCallID] = struct{}{}
		}
	}
	filtered := make([]model.CanonicalMessage, 0, len(messages))
	for _, msg := range messages {
		if isSyntheticReasoningSummary(msg.ReasoningContent) {
			msg.ReasoningContent = ""
		}
		if len(msg.ReasoningBlocks) > 0 {
			msg.ReasoningBlocks = filterSyntheticReasoningBlocks(msg.ReasoningBlocks)
		}
		if shouldDropToolMessageFromHistory(msg) {
			continue
		}
		if msg.Role == "tool" {
			if _, ok := unmatchedToolResultIDs[msg.ToolCallID]; ok {
				continue
			}
		}
		if shouldDropAssistantToolCallMessage(msg, droppedToolCallIDs) {
			continue
		}
		msg = pruneUnmatchedAssistantToolCalls(msg, immediateToolCallIDs)
		if shouldDropEmptyAssistantMessage(msg) {
			continue
		}
		filtered = append(filtered, msg)
	}
	if len(filtered) < 2 {
		return filtered
	}
	result := make([]model.CanonicalMessage, 0, len(filtered))
	for _, msg := range filtered {
		if len(result) > 0 && isDuplicateToolMessage(result[len(result)-1], msg) {
			continue
		}
		result = append(result, msg)
	}
	return result
}

func downgradeSyntheticOnlyAnthropicToolReplay(messages []model.CanonicalMessage) []model.CanonicalMessage {
	if !hasAnthropicReplayHistory(messages) || hasRealAnthropicThinkingHistory(messages) {
		return messages
	}
	downgraded := make([]model.CanonicalMessage, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			text := anthropicToolReplayText(msg)
			if text != "" {
				downgraded = append(downgraded, model.CanonicalMessage{Role: "assistant", Parts: []model.CanonicalContentPart{{Type: "text", Text: text}}})
			}
			continue
		}
		if msg.Role == "tool" {
			text := anthropicToolResultReplayText(msg)
			if text != "" {
				downgraded = append(downgraded, model.CanonicalMessage{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: text}}})
			}
			continue
		}
		downgraded = append(downgraded, msg)
	}
	return downgraded
}

func anthropicToolReplayText(msg model.CanonicalMessage) string {
	var lines []string
	for _, call := range msg.ToolCalls {
		name := strings.TrimSpace(call.Name)
		if name == "" {
			continue
		}
		callID := strings.TrimSpace(call.ID)
		if callID == "" {
			callID = name
		}
		arguments := strings.TrimSpace(call.Arguments)
		if arguments == "" {
			arguments = "{}"
		}
		lines = append(lines, fmt.Sprintf("工具调用 %s (%s): %s", name, callID, arguments))
	}
	return strings.Join(lines, "\n")
}

func anthropicToolResultReplayText(msg model.CanonicalMessage) string {
	var parts []string
	for _, part := range msg.Parts {
		text := strings.TrimSpace(part.Text)
		if text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	toolCallID := strings.TrimSpace(msg.ToolCallID)
	if toolCallID == "" {
		toolCallID = "unknown"
	}
	return fmt.Sprintf("工具结果 %s: %s", toolCallID, strings.Join(parts, "\n"))
}

func immediateToolResultIDs(messages []model.CanonicalMessage) map[string]struct{} {
	matched := map[string]struct{}{}
	for i, msg := range messages {
		if msg.Role != "assistant" || len(msg.ToolCalls) == 0 || i+1 >= len(messages) {
			continue
		}
		toolCallIDs := map[string]struct{}{}
		for _, call := range msg.ToolCalls {
			if call.ID != "" {
				toolCallIDs[call.ID] = struct{}{}
			}
		}
		for j := i + 1; j < len(messages); j++ {
			next := messages[j]
			for _, block := range next.OrderedContent {
				if block.Type != "tool_result" || block.ToolCallID == "" {
					continue
				}
				if _, ok := toolCallIDs[block.ToolCallID]; ok {
					matched[block.ToolCallID] = struct{}{}
				}
			}
			if next.Role != "tool" {
				break
			}
			if shouldDropToolMessageFromHistory(next) {
				continue
			}
			if _, ok := toolCallIDs[next.ToolCallID]; ok {
				matched[next.ToolCallID] = struct{}{}
			}
		}
	}
	return matched
}

func unmatchedToolResultIDs(messages []model.CanonicalMessage, matched map[string]struct{}) map[string]struct{} {
	assistantToolCallIDs := map[string]struct{}{}
	for _, msg := range messages {
		if msg.Role != "assistant" {
			continue
		}
		for _, call := range msg.ToolCalls {
			if call.ID != "" {
				assistantToolCallIDs[call.ID] = struct{}{}
			}
		}
	}
	unmatched := map[string]struct{}{}
	for _, msg := range messages {
		if msg.Role != "tool" || msg.ToolCallID == "" || shouldDropToolMessageFromHistory(msg) {
			continue
		}
		if _, hasAssistantCall := assistantToolCallIDs[msg.ToolCallID]; !hasAssistantCall {
			continue
		}
		if _, ok := matched[msg.ToolCallID]; !ok {
			unmatched[msg.ToolCallID] = struct{}{}
		}
	}
	return unmatched
}

func pruneUnmatchedAssistantToolCalls(msg model.CanonicalMessage, matched map[string]struct{}) model.CanonicalMessage {
	if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
		return msg
	}
	kept := make([]model.CanonicalToolCall, 0, len(msg.ToolCalls))
	for _, call := range msg.ToolCalls {
		if call.ID == "" {
			kept = append(kept, call)
			continue
		}
		if _, ok := matched[call.ID]; ok {
			kept = append(kept, call)
		}
	}
	msg.ToolCalls = kept
	return msg
}

func shouldDropEmptyAssistantMessage(msg model.CanonicalMessage) bool {
	return msg.Role == "assistant" && len(msg.Parts) == 0 && len(msg.ToolCalls) == 0 && msg.ReasoningContent == "" && len(msg.ReasoningBlocks) == 0
}

func shouldDropAssistantToolCallMessage(msg model.CanonicalMessage, dropped map[string]struct{}) bool {
	if msg.Role != "assistant" || len(msg.ToolCalls) == 0 || len(dropped) == 0 || len(msg.Parts) > 0 || msg.ReasoningContent != "" || len(msg.ReasoningBlocks) > 0 {
		return false
	}
	for _, call := range msg.ToolCalls {
		if call.ID == "" {
			return false
		}
		if _, ok := dropped[call.ID]; !ok {
			return false
		}
	}
	return true
}

func isDuplicateToolMessage(prev, curr model.CanonicalMessage) bool {
	if prev.Role != "tool" || curr.Role != "tool" {
		return false
	}
	if prev.ToolCallID == "" || prev.ToolCallID != curr.ToolCallID {
		return false
	}
	if len(prev.ToolCalls) != 0 || len(curr.ToolCalls) != 0 {
		return false
	}
	return canonicalPartsEqual(prev.Parts, curr.Parts)
}

func canonicalPartsEqual(a, b []model.CanonicalContentPart) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type || a[i].Text != b[i].Text || a[i].ImageURL != b[i].ImageURL || a[i].MimeType != b[i].MimeType {
			return false
		}
	}
	return true
}
