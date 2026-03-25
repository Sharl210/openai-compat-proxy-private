package httpapi

import (
	"reflect"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
)

func TestApplyProviderSystemPromptPrependsToInstructions(t *testing.T) {
	req := model.CanonicalRequest{Instructions: "user system"}
	provider := config.ProviderConfig{
		SystemPromptText:     "provider system",
		SystemPromptPosition: config.SystemPromptPositionPrepend,
	}

	applyProviderSystemPrompt(&req, provider)

	if req.Instructions != "provider system\n\nuser system" {
		t.Fatalf("expected prepended instructions, got %q", req.Instructions)
	}
}

func TestApplyProviderSystemPromptAppendsToInstructions(t *testing.T) {
	req := model.CanonicalRequest{Instructions: "user system"}
	provider := config.ProviderConfig{
		SystemPromptText:     "provider system",
		SystemPromptPosition: config.SystemPromptPositionAppend,
	}

	applyProviderSystemPrompt(&req, provider)

	if req.Instructions != "user system\n\nprovider system" {
		t.Fatalf("expected appended instructions, got %q", req.Instructions)
	}
}

func TestApplyProviderSystemPromptPrependsToFirstSystemMessage(t *testing.T) {
	req := model.CanonicalRequest{Messages: []model.CanonicalMessage{{
		Role:  "system",
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "user system"}},
	}}}
	provider := config.ProviderConfig{SystemPromptText: "provider system"}

	applyProviderSystemPrompt(&req, provider)

	parts := req.Messages[0].Parts
	if len(parts) != 2 {
		t.Fatalf("expected prepended text part, got %#v", parts)
	}
	if parts[0].Text != "provider system\n\n" || parts[1].Text != "user system" {
		t.Fatalf("unexpected system parts after prepend: %#v", parts)
	}
}

func TestApplyProviderSystemPromptAppendsToResponsesInputSystemMessage(t *testing.T) {
	req := model.CanonicalRequest{ResponseInputItems: []map[string]any{{
		"role":    "developer",
		"content": []map[string]any{{"type": "input_text", "text": "user system"}},
	}}}
	provider := config.ProviderConfig{
		SystemPromptText:     "provider system",
		SystemPromptPosition: config.SystemPromptPositionAppend,
	}

	applyProviderSystemPrompt(&req, provider)

	content, _ := req.ResponseInputItems[0]["content"].([]map[string]any)
	if len(content) != 2 {
		t.Fatalf("expected appended developer content, got %#v", req.ResponseInputItems[0]["content"])
	}
	if content[0]["text"] != "user system" || content[1]["text"] != "\n\nprovider system" {
		t.Fatalf("unexpected developer content after append: %#v", content)
	}
}

func TestApplyProviderSystemPromptCreatesInstructionsWhenRequestHasNoSystemPrompt(t *testing.T) {
	req := model.CanonicalRequest{}
	provider := config.ProviderConfig{SystemPromptText: "provider system"}

	applyProviderSystemPrompt(&req, provider)

	if req.Instructions != "provider system" {
		t.Fatalf("expected provider prompt to become instructions, got %q", req.Instructions)
	}
}

func TestApplyProviderSystemPromptIsNoOpWhenProviderPromptBlank(t *testing.T) {
	req := model.CanonicalRequest{
		Instructions: "user system",
		Messages: []model.CanonicalMessage{{
			Role:  "system",
			Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}},
		}},
	}
	before := req
	before.Messages = append([]model.CanonicalMessage(nil), req.Messages...)
	before.Messages[0].Parts = append([]model.CanonicalContentPart(nil), req.Messages[0].Parts...)

	applyProviderSystemPrompt(&req, config.ProviderConfig{})

	if !reflect.DeepEqual(req, before) {
		t.Fatalf("expected blank provider prompt to be no-op, got %#v", req)
	}
}
