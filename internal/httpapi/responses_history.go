package httpapi

import (
	"encoding/json"
	"strings"
	"sync"

	"openai-compat-proxy/internal/aggregate"
	"openai-compat-proxy/internal/model"
)

type responsesHistoryStore struct {
	mu      sync.RWMutex
	entries map[string]responsesConversationSnapshot
	order   []string
	maxSize int
}

type responsesConversationSnapshot struct {
	Messages []model.CanonicalMessage
}

const defaultResponsesHistoryMaxSize = 512

var globalResponsesHistory = &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, maxSize: defaultResponsesHistoryMaxSize}

func responsesHistoryKey(providerID, responseID string) string {
	return providerID + "::" + responseID
}

func cloneCanonicalMessages(messages []model.CanonicalMessage) []model.CanonicalMessage {
	if len(messages) == 0 {
		return nil
	}
	cloned := make([]model.CanonicalMessage, 0, len(messages))
	for _, msg := range messages {
		clone := model.CanonicalMessage{Role: msg.Role, ToolCallID: msg.ToolCallID, ReasoningContent: msg.ReasoningContent}
		if len(msg.Parts) > 0 {
			clone.Parts = append([]model.CanonicalContentPart(nil), msg.Parts...)
		}
		if len(msg.ToolCalls) > 0 {
			clone.ToolCalls = append([]model.CanonicalToolCall(nil), msg.ToolCalls...)
		}
		cloned = append(cloned, clone)
	}
	return cloned
}

func (s *responsesHistoryStore) Save(providerID, responseID string, messages []model.CanonicalMessage) {
	if s == nil || providerID == "" || responseID == "" || len(messages) == 0 {
		return
	}
	if s.maxSize <= 0 {
		s.maxSize = defaultResponsesHistoryMaxSize
	}
	key := responsesHistoryKey(providerID, responseID)
	s.mu.Lock()
	if _, exists := s.entries[key]; exists {
		s.removeKeyLocked(key)
	}
	s.entries[key] = responsesConversationSnapshot{Messages: cloneCanonicalMessages(messages)}
	s.order = append(s.order, key)
	for len(s.order) > s.maxSize {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.entries, oldest)
	}
	s.mu.Unlock()
}

func (s *responsesHistoryStore) Load(providerID, responseID string) []model.CanonicalMessage {
	if s == nil || providerID == "" || responseID == "" {
		return nil
	}
	s.mu.RLock()
	stored, ok := s.entries[responsesHistoryKey(providerID, responseID)]
	s.mu.RUnlock()
	if !ok || len(stored.Messages) == 0 {
		return nil
	}
	return cloneCanonicalMessages(stored.Messages)
}

func (s *responsesHistoryStore) removeKeyLocked(target string) {
	for idx, key := range s.order {
		if key != target {
			continue
		}
		s.order = append(s.order[:idx], s.order[idx+1:]...)
		return
	}
}

func previousResponseIDFromItems(items []map[string]any) string {
	for _, item := range items {
		preserved, _ := item["__openai_compat_responses_top_level"].(map[string]any)
		if preserved == nil {
			continue
		}
		if responseID, _ := preserved["previous_response_id"].(string); responseID != "" {
			return responseID
		}
	}
	return ""
}

func shouldRestorePreviousConversation(messages []model.CanonicalMessage) bool {
	if len(messages) == 0 {
		return true
	}
	for _, msg := range messages {
		if msg.Role != "tool" {
			return false
		}
	}
	return true
}

func assistantHistoryMessagesFromResult(result aggregate.Result) []model.CanonicalMessage {
	parts := make([]model.CanonicalContentPart, 0, len(result.ResponseMessageContent))
	for _, part := range result.ResponseMessageContent {
		if partType, _ := part["type"].(string); partType == "output_text" {
			if text, _ := part["text"].(string); text != "" {
				parts = append(parts, model.CanonicalContentPart{Type: "text", Text: text})
			}
		}
	}
	toolCalls := make([]model.CanonicalToolCall, 0, len(result.ToolCalls))
	for _, call := range result.ToolCalls {
		toolCalls = append(toolCalls, model.CanonicalToolCall{ID: call.ID, Type: "function", Name: call.Name, Arguments: call.Arguments})
	}
	if len(parts) == 0 && len(toolCalls) == 0 {
		return nil
	}
	msg := model.CanonicalMessage{Role: "assistant", Parts: parts, ToolCalls: toolCalls}
	if summary, _ := result.Reasoning["summary"].(string); summary != "" {
		msg.ReasoningContent = summary
	}
	return []model.CanonicalMessage{msg}
}

func buildResponsesHistorySnapshot(base []model.CanonicalMessage, assistant []model.CanonicalMessage) []model.CanonicalMessage {
	snapshot := make([]model.CanonicalMessage, 0, len(base)+len(assistant))
	for _, msg := range base {
		switch msg.Role {
		case "user", "tool":
			if msg.Role == "tool" && shouldDropToolMessageFromHistory(msg) {
				continue
			}
			snapshot = append(snapshot, cloneCanonicalMessages([]model.CanonicalMessage{msg})...)
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				snapshot = append(snapshot, cloneCanonicalMessages([]model.CanonicalMessage{msg})...)
			}
		}
	}
	snapshot = append(snapshot, cloneCanonicalMessages(assistant)...)
	return snapshot
}

func mergeConversationHistory(base []model.CanonicalMessage, assistant []model.CanonicalMessage) []model.CanonicalMessage {
	merged := make([]model.CanonicalMessage, 0, len(base)+len(assistant))
	merged = append(merged, cloneCanonicalMessages(base)...)
	merged = append(merged, cloneCanonicalMessages(assistant)...)
	return merged
}

func shouldDropToolMessageFromHistory(msg model.CanonicalMessage) bool {
	if msg.Role != "tool" {
		return false
	}
	text := strings.TrimSpace(toolMessageText(msg))
	if text == "" {
		return false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return false
	}
	_, hasError := payload["error"]
	return hasError
}

func toolMessageText(msg model.CanonicalMessage) string {
	var builder strings.Builder
	for _, part := range msg.Parts {
		if part.Type == "text" {
			builder.WriteString(part.Text)
		}
	}
	return builder.String()
}
