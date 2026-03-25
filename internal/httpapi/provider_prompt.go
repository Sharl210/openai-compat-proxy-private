package httpapi

import (
	"strings"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
)

func applyProviderSystemPrompt(req *model.CanonicalRequest, provider config.ProviderConfig) {
	promptText := provider.SystemPromptText
	if req == nil || promptText == "" {
		return
	}
	position := config.SystemPromptPositionPrepend
	if provider.SystemPromptPosition == config.SystemPromptPositionAppend {
		position = config.SystemPromptPositionAppend
	}
	if req.Instructions != "" {
		req.Instructions = mergeSystemPromptText(req.Instructions, promptText, position)
		return
	}
	if applySystemPromptToResponseInputItems(req.ResponseInputItems, promptText, position) {
		applySystemPromptToMessages(req.Messages, promptText, position)
		return
	}
	if applySystemPromptToMessages(req.Messages, promptText, position) {
		return
	}
	req.Instructions = promptText
}

func mergeSystemPromptText(existing string, injected string, position string) string {
	if existing == "" {
		return injected
	}
	if injected == "" {
		return existing
	}
	if position == config.SystemPromptPositionAppend {
		return existing + "\n\n" + injected
	}
	return injected + "\n\n" + existing
}

func applySystemPromptToMessages(messages []model.CanonicalMessage, injected string, position string) bool {
	for i := range messages {
		if !isInstructionRole(messages[i].Role) {
			continue
		}
		messages[i].Parts = mergePromptIntoCanonicalParts(messages[i].Parts, injected, position)
		return true
	}
	return false
}

func applySystemPromptToResponseInputItems(items []map[string]any, injected string, position string) bool {
	for i := range items {
		role, _ := items[i]["role"].(string)
		if !isInstructionRole(role) {
			continue
		}
		items[i]["content"] = mergePromptIntoRawContent(items[i]["content"], injected, position)
		return true
	}
	return false
}

func mergePromptIntoCanonicalParts(parts []model.CanonicalContentPart, injected string, position string) []model.CanonicalContentPart {
	if len(parts) == 0 {
		return []model.CanonicalContentPart{{Type: "text", Text: injected}}
	}
	merged := append([]model.CanonicalContentPart(nil), parts...)
	entry := model.CanonicalContentPart{Type: "text"}
	if position == config.SystemPromptPositionAppend {
		entry.Text = "\n\n" + injected
		return append(merged, entry)
	}
	entry.Text = injected + "\n\n"
	return append([]model.CanonicalContentPart{entry}, merged...)
}

func mergePromptIntoRawContent(content any, injected string, position string) []map[string]any {
	parts := coerceRawContentParts(content)
	entry := map[string]any{"type": "input_text", "text": injected}
	if len(parts) == 0 {
		return []map[string]any{entry}
	}
	if position == config.SystemPromptPositionAppend {
		entry["text"] = "\n\n" + injected
		return append(parts, entry)
	}
	entry["text"] = injected + "\n\n"
	return append([]map[string]any{entry}, parts...)
}

func coerceRawContentParts(content any) []map[string]any {
	switch typed := content.(type) {
	case nil:
		return nil
	case []map[string]any:
		return append([]map[string]any(nil), typed...)
	case []any:
		parts := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if mapped, ok := item.(map[string]any); ok {
				parts = append(parts, mapped)
			}
		}
		return parts
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []map[string]any{{"type": "input_text", "text": typed}}
	default:
		return nil
	}
}

func isInstructionRole(role string) bool {
	return role == "system" || role == "developer"
}
