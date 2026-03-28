package responses

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"openai-compat-proxy/internal/model"
)

type request struct {
	Model              string          `json:"model"`
	Stream             bool            `json:"stream"`
	Store              *bool           `json:"store"`
	Include            []string        `json:"include"`
	PreviousResponseID string          `json:"previous_response_id"`
	Metadata           json.RawMessage `json:"metadata"`
	ParallelToolCalls  *bool           `json:"parallel_tool_calls"`
	Truncation         json.RawMessage `json:"truncation"`
	Text               json.RawMessage `json:"text"`
	Instructions       json.RawMessage `json:"instructions"`
	Input              requestInput    `json:"input"`
	Tools              []tool          `json:"tools"`
	ToolChoice         any             `json:"tool_choice"`
	Reasoning          *reasoning      `json:"reasoning"`
	Temperature        json.RawMessage `json:"temperature"`
	TopP               json.RawMessage `json:"top_p"`
	MaxOutputTokensRaw json.RawMessage `json:"max_output_tokens"`
	Stop               []string        `json:"stop"`
}

type requestInput []json.RawMessage

type message struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []toolCall      `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
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
	Type       string          `json:"type"`
	Text       string          `json:"text"`
	ImageURL   string          `json:"image_url"`
	InputAudio json.RawMessage `json:"input_audio"`
	InputFile  json.RawMessage `json:"input_file"`
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

const preservedResponsesTopLevelFieldsKey = "__openai_compat_responses_top_level"

func DecodeRequest(r io.Reader) (model.CanonicalRequest, error) {
	var req request
	if err := json.NewDecoder(r).Decode(&req); err != nil {
		return model.CanonicalRequest{}, err
	}

	canon := model.CanonicalRequest{
		Model:           req.Model,
		Stream:          req.Stream,
		ResponseStore:   req.Store,
		ResponseInclude: append([]string(nil), req.Include...),
		Instructions:    decodeOptionalString(req.Instructions),
		Temperature:     decodeOptionalFloat(req.Temperature),
		TopP:            decodeOptionalFloat(req.TopP),
		MaxOutputTokens: decodeOptionalInt(req.MaxOutputTokensRaw),
		Stop:            req.Stop,
	}

	if preservedTopLevelFields := collectPreservedTopLevelFields(req); len(preservedTopLevelFields) > 0 {
		canon.ResponseInputItems = append(canon.ResponseInputItems, map[string]any{
			preservedResponsesTopLevelFieldsKey: preservedTopLevelFields,
		})
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

	for _, rawItem := range req.Input {
		if len(rawItem) == 0 {
			continue
		}
		preserved, msg, ok, err := decodeInputItem(rawItem)
		if err != nil {
			return model.CanonicalRequest{}, err
		}
		if len(preserved) > 0 {
			canon.ResponseInputItems = append(canon.ResponseInputItems, preserved)
		}
		if ok {
			canon.Messages = append(canon.Messages, msg)
		}
	}

	return canon, nil
}

func decodeInputItem(raw json.RawMessage) (map[string]any, model.CanonicalMessage, bool, error) {
	var rawMap map[string]any
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		return nil, model.CanonicalMessage{}, false, err
	}
	if role, _ := rawMap["role"].(string); role != "" {
		var msg message
		if err := json.Unmarshal(raw, &msg); err != nil {
			return nil, model.CanonicalMessage{}, false, err
		}
		decodedContent, err := decodeMessageContent(msg.Content)
		if err != nil {
			return nil, model.CanonicalMessage{}, false, err
		}
		parts := make([]model.CanonicalContentPart, 0, len(decodedContent))
		normalizedContent := make([]map[string]any, 0, len(decodedContent))
		for _, part := range decodedContent {
			switch part.Type {
			case "input_text", "output_text", "text":
				parts = append(parts, model.CanonicalContentPart{Type: "text", Text: part.Text})
				normalizedType := part.Type
				if role == "assistant" && normalizedType == "input_text" {
					normalizedType = "output_text"
				}
				if normalizedType == "text" || normalizedType == "" {
					if role == "assistant" {
						normalizedType = "output_text"
					} else {
						normalizedType = "input_text"
					}
				}
				normalizedContent = append(normalizedContent, map[string]any{"type": normalizedType, "text": part.Text})
			case "input_image", "image_url":
				parts = append(parts, model.CanonicalContentPart{Type: "input_image", ImageURL: part.ImageURL})
				normalizedContent = append(normalizedContent, map[string]any{"type": "input_image", "image_url": part.ImageURL})
			case "input_file":
				var rawFile map[string]any
				if err := json.Unmarshal(part.InputFile, &rawFile); err != nil {
					return nil, model.CanonicalMessage{}, false, err
				}
				parts = append(parts, model.CanonicalContentPart{Type: "input_file", Raw: map[string]any{"input_file": rawFile}})
				normalizedContent = append(normalizedContent, map[string]any{"type": "input_file", "input_file": rawFile})
			case "input_audio":
				var rawAudio map[string]any
				if err := json.Unmarshal(part.InputAudio, &rawAudio); err != nil {
					return nil, model.CanonicalMessage{}, false, err
				}
				parts = append(parts, model.CanonicalContentPart{Type: "input_audio", Raw: map[string]any{"input_audio": rawAudio}})
				normalizedContent = append(normalizedContent, map[string]any{"type": "input_audio", "input_audio": rawAudio})
			default:
				return nil, model.CanonicalMessage{}, false, fmt.Errorf("unsupported content type: %s", part.Type)
			}
		}

		var toolCalls []model.CanonicalToolCall
		for _, tc := range msg.ToolCalls {
			toolCalls = append(toolCalls, model.CanonicalToolCall{
				ID:        tc.ID,
				Type:      tc.Type,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}

		preserved := map[string]any{"role": role}
		if len(normalizedContent) > 0 {
			preserved["content"] = normalizedContent
		}
		if msg.ToolCallID != "" {
			preserved["tool_call_id"] = msg.ToolCallID
		}
		if len(msg.ToolCalls) > 0 {
			preserved["tool_calls"] = rawMap["tool_calls"]
		}

		return preserved, model.CanonicalMessage{Role: msg.Role, Parts: parts, ToolCalls: toolCalls, ToolCallID: msg.ToolCallID}, true, nil
	}
	return cloneMapAny(rawMap), model.CanonicalMessage{}, false, nil
}

func decodeMessageContent(raw json.RawMessage) ([]contentPart, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if isUndefinedString(single) {
			return nil, nil
		}
		return []contentPart{{Type: "input_text", Text: single}}, nil
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, err
	}
	return parts, nil
}

func (ri *requestInput) UnmarshalJSON(data []byte) error {
	trimmed := json.RawMessage(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*ri = nil
		return nil
	}

	var multi []json.RawMessage
	if err := json.Unmarshal(data, &multi); err == nil {
		*ri = multi
		return nil
	}

	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		wrapped, err := json.Marshal(map[string]any{
			"role":    "user",
			"content": single,
		})
		if err != nil {
			return err
		}
		*ri = []json.RawMessage{wrapped}
		return nil
	}

	var rawItem json.RawMessage
	if err := json.Unmarshal(data, &rawItem); err != nil {
		return err
	}
	*ri = []json.RawMessage{rawItem}
	return nil
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

func cloneMapAny(input map[string]any) map[string]any {
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

func decodeOptionalString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || isUndefinedString(value) {
		return ""
	}
	return value
}

func decodeOptionalFloat(raw json.RawMessage) *float64 {
	if len(raw) == 0 {
		return nil
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err == nil {
		return &value
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil && isUndefinedString(text) {
		return nil
	}
	return nil
}

func decodeOptionalInt(raw json.RawMessage) *int {
	if len(raw) == 0 {
		return nil
	}
	var value int
	if err := json.Unmarshal(raw, &value); err == nil {
		return &value
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil && isUndefinedString(text) {
		return nil
	}
	return nil
}

func isUndefinedString(value string) bool {
	return value == "[undefined]" || value == "undefined" || value == ""
}

func collectPreservedTopLevelFields(req request) map[string]any {
	fields := map[string]any{}
	if req.PreviousResponseID != "" {
		fields["previous_response_id"] = req.PreviousResponseID
	}
	if value := decodeOptionalAny(req.Metadata); value != nil {
		fields["metadata"] = value
	}
	if req.ParallelToolCalls != nil {
		fields["parallel_tool_calls"] = *req.ParallelToolCalls
	}
	if value := decodeOptionalAny(req.Truncation); value != nil {
		fields["truncation"] = value
	}
	if value := decodeOptionalAny(req.Text); value != nil {
		fields["text"] = value
	}
	return fields
}

func decodeOptionalAny(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return value
}
