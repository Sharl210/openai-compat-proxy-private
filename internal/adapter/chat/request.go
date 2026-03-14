package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"openai-compat-proxy/internal/model"
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
	Stop            []string        `json:"stop"`
	N               *int            `json:"n"`
	Raw             json.RawMessage `json:"-"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type contentPart struct {
	Type     string         `json:"type"`
	Text     string         `json:"text"`
	ImageURL map[string]any `json:"image_url"`
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
	var req request
	if err := json.NewDecoder(r).Decode(&req); err != nil {
		return model.CanonicalRequest{}, err
	}

	if req.N != nil && *req.N > 1 {
		return model.CanonicalRequest{}, errors.New("n > 1 is not supported")
	}

	canon := model.CanonicalRequest{
		Model:           req.Model,
		Stream:          req.Stream,
		IncludeUsage:    req.StreamOptions != nil && req.StreamOptions.IncludeUsage,
		Temperature:     req.Temperature,
		TopP:            req.TopP,
		MaxOutputTokens: req.MaxTokens,
		Stop:            req.Stop,
	}

	if len(req.Reasoning) > 0 {
		canon.Reasoning = &model.CanonicalReasoning{
			Effort:  stringMapValue(req.Reasoning, "effort"),
			Summary: stringMapValue(req.Reasoning, "summary"),
			Raw:     req.Reasoning,
		}
	} else if req.ReasoningEffort != "" {
		canon.Reasoning = &model.CanonicalReasoning{Effort: req.ReasoningEffort, Raw: map[string]any{"effort": req.ReasoningEffort}}
	}

	for _, t := range req.Tools {
		canon.Tools = append(canon.Tools, model.CanonicalTool{
			Type:        t.Type,
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
		})
	}

	if req.ToolChoice != nil {
		canon.ToolChoice = model.CanonicalToolChoice{Raw: map[string]any{"value": req.ToolChoice}}
	}

	for _, msg := range req.Messages {
		parts, err := decodeContent(msg.Content)
		if err != nil {
			return model.CanonicalRequest{}, fmt.Errorf("decode message content: %w", err)
		}

		canon.Messages = append(canon.Messages, model.CanonicalMessage{
			Role:  msg.Role,
			Parts: parts,
		})
	}

	return canon, nil
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
		default:
			return nil, fmt.Errorf("unsupported content type: %s", part.Type)
		}
	}

	return result, nil
}

func stringMapValue(m map[string]any, key string) string {
	value, _ := m[key].(string)
	return value
}
