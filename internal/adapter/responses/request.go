package responses

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

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
	Effort  string         `json:"effort"`
	Summary string         `json:"summary"`
	Raw     map[string]any `json:"-"`
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
		reasoningRaw := cloneReasoningMap(req.Reasoning.Raw)
		if len(reasoningRaw) == 0 {
			reasoningRaw = map[string]any{}
			if req.Reasoning.Effort != "" {
				reasoningRaw["effort"] = req.Reasoning.Effort
			}
			if req.Reasoning.Summary != "" {
				reasoningRaw["summary"] = req.Reasoning.Summary
			}
		}
		if _, ok := reasoningRaw["summary"]; !ok {
			reasoningRaw["summary"] = "auto"
		}
		canon.Reasoning = &model.CanonicalReasoning{
			Effort:  stringMapValue(reasoningRaw, "effort"),
			Summary: stringMapValue(reasoningRaw, "summary"),
			Raw:     reasoningRaw,
		}
	}

	for _, t := range req.Tools {
		canon.Tools = append(canon.Tools, model.CanonicalTool{
			Type:        t.Type,
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
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

func (r *reasoning) UnmarshalJSON(data []byte) error {
	type alias reasoning
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.Raw = raw
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	r.Effort = decoded.Effort
	r.Summary = decoded.Summary
	return nil
}

func cloneReasoningMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(input))
	for k, v := range input {
		cloned[k] = v
	}
	return cloned
}

func stringMapValue(m map[string]any, key string) string {
	value, _ := m[key].(string)
	return value
}
