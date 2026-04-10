package chat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/syntaxrepair"
)

type request struct {
	Model           string          `json:"model"`
	Stream          bool            `json:"stream"`
	StreamOptions   *streamOptions  `json:"stream_options"`
	Messages        []message       `json:"messages"`
	Tools           []tool          `json:"tools"`
	ToolChoice      any             `json:"tool_choice"`
	ReasoningEffort string          `json:"reasoning_effort"`
	Reasoning       map[string]any  `json:"reasoning"`
	Temperature     *float64        `json:"temperature"`
	TopP            *float64        `json:"top_p"`
	MaxTokens       *int            `json:"max_tokens"`
	StopRaw         json.RawMessage `json:"stop"`
	N               *int            `json:"n"`
	Raw             json.RawMessage `json:"-"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type message struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content"`
	ToolCalls        []toolCall      `json:"tool_calls"`
	ToolCallID       string          `json:"tool_call_id"`
	ReasoningContent string          `json:"reasoning_content"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type contentPart struct {
	Type       string         `json:"type"`
	Text       string         `json:"text"`
	ImageURL   map[string]any `json:"image_url"`
	InputAudio map[string]any `json:"input_audio"`
	File       map[string]any `json:"file"`
}

type tool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

func DecodeRequest(r io.Reader) (model.CanonicalRequest, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return model.CanonicalRequest{}, err
	}
	var req request
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&req); err != nil {
		return model.CanonicalRequest{}, err
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return model.CanonicalRequest{}, err
	}

	if req.N != nil && *req.N > 1 {
		return model.CanonicalRequest{}, errors.New("n > 1 is not supported")
	}

	canon := model.CanonicalRequest{
		Model:                   req.Model,
		Stream:                  req.Stream,
		PreservedTopLevelFields: collectUnhandledTopLevelFields(raw),
		IncludeUsage:            req.StreamOptions != nil && req.StreamOptions.IncludeUsage,
		Temperature:             req.Temperature,
		TopP:                    req.TopP,
		MaxOutputTokens:         req.MaxTokens,
	}
	stop, err := decodeStop(req.StopRaw)
	if err != nil {
		return model.CanonicalRequest{}, fmt.Errorf("decode stop: %w", err)
	}
	canon.Stop = stop

	if len(req.Reasoning) > 0 {
		reasoningRaw := cloneMap(req.Reasoning)
		if _, ok := reasoningRaw["summary"]; !ok {
			reasoningRaw["summary"] = "auto"
		}
		canon.Reasoning = &model.CanonicalReasoning{
			Effort:  stringMapValue(reasoningRaw, "effort"),
			Summary: stringMapValue(reasoningRaw, "summary"),
			Raw:     reasoningRaw,
		}
	} else if req.ReasoningEffort != "" {
		canon.Reasoning = &model.CanonicalReasoning{Effort: req.ReasoningEffort, Summary: "auto", Raw: map[string]any{"effort": req.ReasoningEffort, "summary": "auto"}}
	}

	for _, t := range req.Tools {
		canon.Tools = append(canon.Tools, model.CanonicalTool{
			Type:        t.Type,
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
		})
	}
	sort.SliceStable(canon.Tools, func(i, j int) bool {
		if canon.Tools[i].Name == canon.Tools[j].Name {
			return canon.Tools[i].Type < canon.Tools[j].Type
		}
		return canon.Tools[i].Name < canon.Tools[j].Name
	})

	if req.ToolChoice != nil {
		canon.ToolChoice = model.CanonicalToolChoice{Raw: map[string]any{"value": req.ToolChoice}}
	}

	for _, msg := range req.Messages {
		parts, err := decodeContent(msg.Content)
		if err != nil {
			return model.CanonicalRequest{}, fmt.Errorf("decode message content: %w", err)
		}

		var toolCalls []model.CanonicalToolCall
		for _, tc := range msg.ToolCalls {
			toolCalls = append(toolCalls, model.CanonicalToolCall{
				ID:        tc.ID,
				Type:      tc.Type,
				Name:      tc.Function.Name,
				Arguments: sanitizeToolArguments(tc.Function.Arguments),
			})
		}

		canon.Messages = append(canon.Messages, model.CanonicalMessage{
			Role:             msg.Role,
			Parts:            parts,
			ToolCalls:        toolCalls,
			ToolCallID:       msg.ToolCallID,
			ReasoningContent: msg.ReasoningContent,
		})
	}

	return canon, nil
}

func sanitizeToolArguments(arguments string) string {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return arguments
	}
	if normalized, ok := normalizeToolArgumentsJSON(trimmed); ok {
		return normalized
	}
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] != '{' && trimmed[i] != '[' {
			continue
		}
		if normalized, ok := normalizeToolArgumentsJSON(trimmed[i:]); ok {
			return normalized
		}
	}
	if json.Valid([]byte(trimmed)) {
		return arguments
	}
	return arguments
}

func normalizeToolArgumentsJSON(raw string) (string, bool) {
	if normalized, ok := syntaxrepair.RepairJSON(raw); ok {
		return normalized, true
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", false
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return "", false
	}
	return string(normalized), true
}

func collectUnhandledTopLevelFields(raw map[string]any) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	known := map[string]struct{}{
		"model": {}, "stream": {}, "stream_options": {}, "messages": {}, "tools": {}, "tool_choice": {},
		"reasoning_effort": {}, "reasoning": {}, "temperature": {}, "top_p": {}, "max_tokens": {}, "stop": {}, "n": {},
	}
	fields := map[string]any{}
	for key, value := range raw {
		if _, ok := known[key]; ok {
			continue
		}
		fields[key] = value
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func decodeContent(raw json.RawMessage) ([]model.CanonicalContentPart, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []model.CanonicalContentPart{{Type: "text", Text: single}}, nil
	}

	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, err
	}

	result := make([]model.CanonicalContentPart, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "text":
			result = append(result, model.CanonicalContentPart{Type: "text", Text: part.Text})
		case "image_url":
			url, _ := part.ImageURL["url"].(string)
			if url == "" {
				return nil, errors.New("image_url.url is required")
			}
			result = append(result, model.CanonicalContentPart{Type: "image_url", ImageURL: url, Raw: map[string]any{"image_url": part.ImageURL}})
		case "input_audio":
			if len(part.InputAudio) == 0 {
				return nil, errors.New("input_audio is required")
			}
			result = append(result, model.CanonicalContentPart{Type: "input_audio", Raw: map[string]any{"input_audio": cloneMap(part.InputAudio)}})
		case "file":
			if len(part.File) == 0 {
				return nil, errors.New("file is required")
			}
			result = append(result, model.CanonicalContentPart{Type: "input_file", Raw: map[string]any{"input_file": cloneMap(part.File)}})
		default:
			return nil, fmt.Errorf("unsupported content type: %s", part.Type)
		}
	}

	return result, nil
}

func decodeStop(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if single == "" {
			return nil, nil
		}
		return []string{single}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, err
	}
	return many, nil
}

func stringMapValue(m map[string]any, key string) string {
	value, _ := m[key].(string)
	return value
}

func cloneMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(input))
	for k, v := range input {
		cloned[k] = v
	}
	return cloned
}
