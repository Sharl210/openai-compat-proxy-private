package anthropic

import (
	"encoding/json"
	"fmt"
	"io"

	"openai-compat-proxy/internal/model"
)

type request struct {
	Model     string    `json:"model"`
	Messages  []message `json:"messages"`
	System    string    `json:"system"`
	MaxTokens *int      `json:"max_tokens"`
	Stream    bool      `json:"stream"`
}

type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func DecodeRequest(r io.Reader) (model.CanonicalRequest, error) {
	var req request
	if err := json.NewDecoder(r).Decode(&req); err != nil {
		return model.CanonicalRequest{}, err
	}
	canon := model.CanonicalRequest{
		Model:           req.Model,
		Stream:          req.Stream,
		Instructions:    req.System,
		MaxOutputTokens: req.MaxTokens,
	}
	for _, msg := range req.Messages {
		parts, err := decodeContent(msg.Content)
		if err != nil {
			return model.CanonicalRequest{}, err
		}
		canon.Messages = append(canon.Messages, model.CanonicalMessage{Role: msg.Role, Parts: parts})
	}
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
		if part.Type != "text" {
			return nil, fmt.Errorf("unsupported anthropic content type: %s", part.Type)
		}
		out = append(out, model.CanonicalContentPart{Type: "text", Text: part.Text})
	}
	return out, nil
}
