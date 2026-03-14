package responses

import (
	"encoding/json"
	"fmt"
	"io"

	"openai-compat-proxy/internal/model"
)

type request struct {
	Model           string     `json:"model"`
	Stream          bool       `json:"stream"`
	Instructions    string     `json:"instructions"`
	Input           []message  `json:"input"`
	Tools           []tool     `json:"tools"`
	ToolChoice      any        `json:"tool_choice"`
	Reasoning       *reasoning `json:"reasoning"`
	Temperature     *float64   `json:"temperature"`
	TopP            *float64   `json:"top_p"`
	MaxOutputTokens *int       `json:"max_output_tokens"`
	Stop            []string   `json:"stop"`
}

type message struct {
	Role    string        `json:"role"`
	Content []contentPart `json:"content"`
}

type contentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL string `json:"image_url"`
}

type tool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type reasoning struct {
	Effort string `json:"effort"`
	Raw    any    `json:"-"`
}

func DecodeRequest(r io.Reader) (model.CanonicalRequest, error) {
	var req request
	if err := json.NewDecoder(r).Decode(&req); err != nil {
		return model.CanonicalRequest{}, err
	}

	canon := model.CanonicalRequest{
		Model:           req.Model,
		Stream:          req.Stream,
		Instructions:    req.Instructions,
		Temperature:     req.Temperature,
		TopP:            req.TopP,
		MaxOutputTokens: req.MaxOutputTokens,
		Stop:            req.Stop,
	}

	if req.Reasoning != nil {
		canon.Reasoning = &model.CanonicalReasoning{Effort: req.Reasoning.Effort}
	}

	for _, t := range req.Tools {
		canon.Tools = append(canon.Tools, model.CanonicalTool{
			Type:        t.Type,
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
		})
	}

	if req.ToolChoice != nil {
		canon.ToolChoice = model.CanonicalToolChoice{Raw: map[string]any{"value": req.ToolChoice}}
	}

	for _, msg := range req.Input {
		parts := make([]model.CanonicalContentPart, 0, len(msg.Content))
		for _, part := range msg.Content {
			switch part.Type {
			case "input_text":
				parts = append(parts, model.CanonicalContentPart{Type: "text", Text: part.Text})
			case "input_image":
				parts = append(parts, model.CanonicalContentPart{Type: "input_image", ImageURL: part.ImageURL})
			default:
				return model.CanonicalRequest{}, fmt.Errorf("unsupported content type: %s", part.Type)
			}
		}

		canon.Messages = append(canon.Messages, model.CanonicalMessage{Role: msg.Role, Parts: parts})
	}

	return canon, nil
}
