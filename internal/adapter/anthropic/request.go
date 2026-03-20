package anthropic

import (
	"encoding/json"
	"fmt"
	"io"

	"openai-compat-proxy/internal/model"
)

type request struct {
	Model         string          `json:"model"`
	Messages      []message       `json:"messages"`
	System        json.RawMessage `json:"system"`
	MaxTokens     *int            `json:"max_tokens"`
	StreamRaw     json.RawMessage `json:"stream"`
	Tools         []tool          `json:"tools"`
	ToolChoiceRaw json.RawMessage `json:"tool_choice"`
}

type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type contentPart struct {
	Type         string          `json:"type"`
	Text         string          `json:"text"`
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Input        json.RawMessage `json:"input"`
	ToolUseID    string          `json:"tool_use_id"`
	Content      json.RawMessage `json:"content"`
	CacheControl json.RawMessage `json:"cache_control"`
}

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

func DecodeRequest(r io.Reader) (model.CanonicalRequest, error) {
	var req request
	if err := json.NewDecoder(r).Decode(&req); err != nil {
		return model.CanonicalRequest{}, err
	}
	canon := model.CanonicalRequest{
		Model:           req.Model,
		Stream:          decodeAnthropicOptionalBool(req.StreamRaw),
		Instructions:    decodeAnthropicSystem(req.System),
		MaxOutputTokens: req.MaxTokens,
	}
	for _, msg := range req.Messages {
		parts, err := decodeContent(msg.Content)
		if err != nil {
			return model.CanonicalRequest{}, err
		}
		toolCalls, toolResults, err := decodeToolTransitions(msg.Role, msg.Content)
		if err != nil {
			return model.CanonicalRequest{}, err
		}
		if len(parts) > 0 {
			canon.Messages = append(canon.Messages, model.CanonicalMessage{Role: msg.Role, Parts: parts, ToolCalls: toolCalls})
			toolCalls = nil
		}
		if len(toolResults) > 0 {
			canon.Messages = append(canon.Messages, toolResults...)
			continue
		}
		if len(toolCalls) > 0 {
			canon.Messages = append(canon.Messages, model.CanonicalMessage{Role: msg.Role, ToolCalls: toolCalls})
		}
	}
	for _, tool := range req.Tools {
		canon.Tools = append(canon.Tools, model.CanonicalTool{
			Type:        "function",
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.InputSchema,
		})
	}
	canon.ToolChoice = decodeAnthropicToolChoice(req.ToolChoiceRaw)
	return canon, nil
}

func decodeContent(raw json.RawMessage) ([]model.CanonicalContentPart, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []model.CanonicalContentPart{{Type: "text", Text: text}}, nil
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, err
	}
	out := make([]model.CanonicalContentPart, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "text":
			out = append(out, model.CanonicalContentPart{Type: "text", Text: part.Text})
		case "tool_use", "tool_result":
			continue
		case "":
			if part.Text != "" {
				out = append(out, model.CanonicalContentPart{Type: "text", Text: part.Text})
				continue
			}
		default:
			return nil, fmt.Errorf("unsupported anthropic content type: %s", part.Type)
		}
	}
	return out, nil
}

func decodeToolTransitions(role string, raw json.RawMessage) ([]model.CanonicalToolCall, []model.CanonicalMessage, error) {
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, nil, nil
	}
	var toolCalls []model.CanonicalToolCall
	var toolResults []model.CanonicalMessage
	for _, part := range parts {
		switch part.Type {
		case "tool_use":
			arguments := "{}"
			if len(part.Input) > 0 {
				arguments = string(part.Input)
			}
			toolCalls = append(toolCalls, model.CanonicalToolCall{ID: part.ID, Type: "function", Name: part.Name, Arguments: arguments})
		case "tool_result":
			toolText, err := decodeToolResultContent(part.Content)
			if err != nil {
				return nil, nil, err
			}
			toolResults = append(toolResults, model.CanonicalMessage{Role: "tool", ToolCallID: part.ToolUseID, Parts: []model.CanonicalContentPart{{Type: "text", Text: toolText}}})
		}
	}
	return toolCalls, toolResults, nil
}

func decodeToolResultContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		var out string
		for _, part := range parts {
			if part.Type == "text" {
				out += part.Text
			}
		}
		return out, nil
	}
	return string(raw), nil
}

func decodeAnthropicSystem(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if isUndefinedString(text) {
			return ""
		}
		return text
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		var out string
		for _, part := range parts {
			if part.Type == "text" || part.Type == "" {
				out += part.Text
			}
		}
		return out
	}
	return ""
}

func decodeAnthropicOptionalBool(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil && isUndefinedString(text) {
		return false
	}
	return false
}

func decodeAnthropicToolChoice(raw json.RawMessage) model.CanonicalToolChoice {
	if len(raw) == 0 {
		return model.CanonicalToolChoice{}
	}
	var mode string
	if err := json.Unmarshal(raw, &mode); err == nil {
		if isUndefinedString(mode) {
			return model.CanonicalToolChoice{}
		}
		return model.CanonicalToolChoice{Mode: mode, Raw: map[string]any{"value": mode}}
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		if mode, _ := obj["type"].(string); mode != "" && !isUndefinedString(mode) {
			return model.CanonicalToolChoice{Mode: mode, Raw: obj}
		}
	}
	return model.CanonicalToolChoice{}
}

func isUndefinedString(value string) bool {
	return value == "[undefined]" || value == "undefined" || value == ""
}
