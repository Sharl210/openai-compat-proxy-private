package responses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

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
	ImageURL   json.RawMessage `json:"image_url"`
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

	canon := model.CanonicalRequest{
		Model:                   req.Model,
		Stream:                  req.Stream,
		PreservedTopLevelFields: collectPassthroughTopLevelFields(raw, req),
		ResponseStore:           req.Store,
		ResponseInclude:         append([]string(nil), req.Include...),
		Instructions:            decodeOptionalString(req.Instructions),
		Temperature:             decodeOptionalFloat(req.Temperature),
		TopP:                    decodeOptionalFloat(req.TopP),
		MaxOutputTokens:         decodeOptionalInt(req.MaxOutputTokensRaw),
		Stop:                    req.Stop,
	}
	for _, inc := range req.Include {
		if inc == "usage" {
			canon.IncludeUsage = true
			break
		}
	}

	if preservedTopLevelFields := collectResponseEchoTopLevelFields(req); len(preservedTopLevelFields) > 0 {
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
			Raw: map[string]any{
				"type":        t.Type,
				"name":        t.Name,
				"description": t.Description,
				"parameters":  cloneMapAny(t.Parameters),
			},
		})
	}
	if req.ToolChoice != nil {
		canon.ToolChoice = model.CanonicalToolChoice{Raw: map[string]any{"value": req.ToolChoice}}
	}

	var inputInstructions []string
	for _, rawItem := range req.Input {
		if len(rawItem) == 0 {
			continue
		}
		preserved, msg, ok, syntheticReasoningReplay, err := decodeInputItem(rawItem)
		if err != nil {
			return model.CanonicalRequest{}, err
		}
		if syntheticReasoningReplay {
			canon.HasSyntheticReasoningReplay = true
		}
		if len(preserved) > 0 {
			if instructionText := extractInstructionTextFromInputItem(preserved); instructionText != "" {
				inputInstructions = append(inputInstructions, instructionText)
				continue
			}
			canon.ResponseInputItems = append(canon.ResponseInputItems, preserved)
		}
		if ok {
			if isInstructionRole(msg.Role) {
				continue
			}
			canon.Messages = append(canon.Messages, msg)
		}
	}
	if len(inputInstructions) > 0 {
		canon.Instructions = mergeResponsesInstructions(strings.Join(inputInstructions, "\n\n"), canon.Instructions)
	}
	canon.Messages = mergeAdjacentResponsesReasoningToolMessages(canon.Messages)

	return canon, nil
}

func mergeAdjacentResponsesReasoningToolMessages(messages []model.CanonicalMessage) []model.CanonicalMessage {
	if len(messages) < 2 {
		return messages
	}
	merged := make([]model.CanonicalMessage, 0, len(messages))
	for idx := 0; idx < len(messages); idx++ {
		msg := messages[idx]
		if isStandaloneResponsesReasoningMessage(msg) && idx+1 < len(messages) && canMergeResponsesReasoningIntoToolMessage(messages[idx+1]) {
			next := messages[idx+1]
			next.ReasoningBlocks = append(cloneMapSlice(msg.ReasoningBlocks), next.ReasoningBlocks...)
			if next.ReasoningContent == "" {
				next.ReasoningContent = msg.ReasoningContent
			}
			merged = append(merged, next)
			idx++
			continue
		}
		merged = append(merged, msg)
	}
	return merged
}

func isStandaloneResponsesReasoningMessage(msg model.CanonicalMessage) bool {
	return msg.Role == "assistant" && (len(msg.ReasoningBlocks) > 0 || msg.ReasoningContent != "") && len(msg.Parts) == 0 && len(msg.ToolCalls) == 0 && msg.ToolCallID == ""
}

func canMergeResponsesReasoningIntoToolMessage(msg model.CanonicalMessage) bool {
	return msg.Role == "assistant" && len(msg.ToolCalls) > 0 && msg.ToolCallID == "" && len(msg.Parts) == 0
}

func decodeInputItem(raw json.RawMessage) (map[string]any, model.CanonicalMessage, bool, bool, error) {
	var rawMap map[string]any
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		return nil, model.CanonicalMessage{}, false, false, err
	}
	itemType, _ := rawMap["type"].(string)
	if itemType == "reasoning" {
		if isSyntheticResponsesReasoningInputItem(rawMap) {
			return nil, model.CanonicalMessage{}, false, true, nil
		}
		reasoningContent := normalizeResponsesReasoningText(reasoningSummaryText(rawMap))
		preserved := cloneMapAny(rawMap)
		if summary := normalizeResponsesReasoningSummary(preserved["summary"]); len(summary) > 0 {
			preserved["summary"] = summary
		}
		message := model.CanonicalMessage{Role: "assistant", ReasoningContent: reasoningContent, ReasoningBlocks: []map[string]any{cloneMapAny(preserved)}}
		if stringMapValue(rawMap, "id") == "rs_proxy" {
			return nil, message, true, true, nil
		}
		return preserved, message, true, false, nil
	}
	if itemType == "function_call_output" {
		msg, ok, err := decodeFunctionCallOutput(rawMap)
		if err != nil {
			return nil, model.CanonicalMessage{}, false, false, err
		}
		return cloneMapAny(rawMap), msg, ok, false, nil
	}
	// Handle type: "function_call" - extract tool call from top-level fields
	if itemType == "function_call" {
		callID, _ := rawMap["call_id"].(string)
		if callID == "" {
			callID, _ = rawMap["id"].(string)
		}
		name, _ := rawMap["name"].(string)
		arguments, _ := rawMap["arguments"].(string)
		role, _ := rawMap["role"].(string)
		if role == "" {
			role = "assistant"
		}
		var parts []model.CanonicalContentPart
		if content, ok := rawMap["content"].(string); ok && content != "" {
			parts = []model.CanonicalContentPart{{Type: "text", Text: content}}
		}
		var toolCalls []model.CanonicalToolCall
		if callID != "" {
			toolCalls = []model.CanonicalToolCall{{ID: callID, Type: "function", Name: name, Arguments: arguments}}
		}
		preserved := cloneMapAny(rawMap)
		return preserved, model.CanonicalMessage{Role: role, Parts: parts, ToolCalls: toolCalls}, true, false, nil
	}
	if role, _ := rawMap["role"].(string); role != "" {
		var msg message
		if err := json.Unmarshal(raw, &msg); err != nil {
			return nil, model.CanonicalMessage{}, false, false, err
		}
		decodedContent, err := decodeMessageContent(msg.Content)
		if err != nil {
			return nil, model.CanonicalMessage{}, false, false, err
		}
		var rawContent []map[string]any
		_ = json.Unmarshal(msg.Content, &rawContent)
		parts := make([]model.CanonicalContentPart, 0, len(decodedContent))
		normalizedContent := make([]map[string]any, 0, len(decodedContent))
		reasoningBlocks := make([]map[string]any, 0, len(decodedContent))
		for idx, part := range decodedContent {
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
				imagePart, normalizedImage, err := decodeResponsesInputImage(part.ImageURL)
				if err != nil {
					return nil, model.CanonicalMessage{}, false, false, err
				}
				parts = append(parts, imagePart)
				normalizedContent = append(normalizedContent, map[string]any{"type": "input_image", "image_url": normalizedImage})
			case "input_file":
				var rawFile map[string]any
				if err := json.Unmarshal(part.InputFile, &rawFile); err != nil {
					return nil, model.CanonicalMessage{}, false, false, err
				}
				parts = append(parts, model.CanonicalContentPart{Type: "input_file", Raw: map[string]any{"input_file": rawFile}})
				normalizedContent = append(normalizedContent, map[string]any{"type": "input_file", "input_file": rawFile})
			case "input_audio":
				var rawAudio map[string]any
				if err := json.Unmarshal(part.InputAudio, &rawAudio); err != nil {
					return nil, model.CanonicalMessage{}, false, false, err
				}
				parts = append(parts, model.CanonicalContentPart{Type: "input_audio", Raw: map[string]any{"input_audio": rawAudio}})
				normalizedContent = append(normalizedContent, map[string]any{"type": "input_audio", "input_audio": rawAudio})
			case "reasoning":
				if idx < len(rawContent) {
					block := cloneMapAny(rawContent[idx])
					if isSyntheticResponsesReasoningInputItem(block) {
						continue
					}
					if len(block) > 0 {
						reasoningBlocks = append(reasoningBlocks, block)
						normalizedContent = append(normalizedContent, cloneMapAny(block))
					}
				}
			default:
				return nil, model.CanonicalMessage{}, false, false, fmt.Errorf("unsupported content type: %s", part.Type)
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

		return preserved, model.CanonicalMessage{Role: msg.Role, Parts: parts, ToolCalls: toolCalls, ToolCallID: msg.ToolCallID, ReasoningBlocks: reasoningBlocks}, true, false, nil
	}
	return cloneMapAny(rawMap), model.CanonicalMessage{}, false, false, nil
}

func extractInstructionTextFromInputItem(item map[string]any) string {
	role, _ := item["role"].(string)
	if !isInstructionRole(role) {
		return ""
	}
	return strings.TrimSpace(extractTextFromResponsesContent(item["content"]))
}

func isSyntheticResponsesReasoningInputItem(item map[string]any) bool {
	if stringMapValue(item, "id") != "rs_proxy" {
		return false
	}
	summary := responsesReasoningSummaryItems(item["summary"])
	if len(summary) == 0 {
		return true
	}
	for _, raw := range summary {
		entry, _ := raw.(map[string]any)
		if !isSyntheticResponsesReasoningSummaryText(stringMapValue(entry, "text")) {
			return false
		}
	}
	return true
}

func isSyntheticResponsesReasoningSummaryText(text string) bool {
	text = normalizeResponsesReasoningText(text)
	return text == "" || strings.Contains(strings.TrimSpace(text), "代理层占位")
}

func responsesReasoningSummaryItems(raw any) []any {
	switch typed := raw.(type) {
	case []any:
		return typed
	case []map[string]any:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
		return items
	default:
		return nil
	}
}

func normalizeResponsesReasoningText(text string) string {
	if text == "" {
		return ""
	}
	text = strings.TrimLeft(text, "\u200b\ufeff")
	if isInvisibleResponsesReasoningResidue(text) {
		return ""
	}
	return text
}

func isInvisibleResponsesReasoningResidue(text string) bool {
	if text == "" {
		return false
	}
	text = strings.ReplaceAll(text, "\u200b", "")
	text = strings.ReplaceAll(text, "\ufeff", "")
	return strings.TrimSpace(text) == ""
}

func normalizeResponsesReasoningSummary(raw any) []map[string]any {
	var summary []map[string]any
	switch typed := raw.(type) {
	case []map[string]any:
		summary = cloneMapSlice(typed)
	case []any:
		for _, item := range typed {
			entry, _ := item.(map[string]any)
			if len(entry) == 0 {
				continue
			}
			summary = append(summary, cloneMapAny(entry))
		}
	default:
		return nil
	}
	for _, entry := range summary {
		if text := stringMapValue(entry, "text"); text != "" {
			entry["text"] = normalizeResponsesReasoningText(text)
		}
		if nested, ok := entry["summary_text"].(map[string]any); ok && len(nested) > 0 {
			nested = cloneMap(nested)
			if text := stringValue(nested["text"]); text != "" {
				nested["text"] = normalizeResponsesReasoningText(text)
			}
			entry["summary_text"] = nested
		}
	}
	return summary
}

func extractTextFromResponsesContent(content any) string {
	switch typed := content.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []map[string]any:
		return joinResponsesTextParts(typed)
	case []any:
		parts := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			mapped, ok := item.(map[string]any)
			if !ok {
				continue
			}
			parts = append(parts, mapped)
		}
		return joinResponsesTextParts(parts)
	default:
		return ""
	}
}

func joinResponsesTextParts(parts []map[string]any) string {
	var builder strings.Builder
	for _, part := range parts {
		partType, _ := part["type"].(string)
		switch partType {
		case "input_text", "output_text", "text", "":
			text, _ := part["text"].(string)
			builder.WriteString(text)
		}
	}
	return builder.String()
}

func isInstructionRole(role string) bool {
	return role == "system" || role == "developer"
}

func mergeResponsesInstructions(prepend string, existing string) string {
	prepend = strings.TrimSpace(prepend)
	existing = strings.TrimSpace(existing)
	if prepend == "" {
		return existing
	}
	if existing == "" {
		return prepend
	}
	return prepend + "\n\n" + existing
}

func decodeFunctionCallOutput(rawMap map[string]any) (model.CanonicalMessage, bool, error) {
	callID, _ := rawMap["call_id"].(string)
	if callID == "" {
		return model.CanonicalMessage{}, false, nil
	}
	parts, err := decodeFunctionCallOutputParts(rawMap["output"])
	if err != nil {
		return model.CanonicalMessage{}, false, err
	}
	return model.CanonicalMessage{Role: "tool", ToolCallID: callID, Parts: parts}, true, nil
}

func reasoningSummaryText(rawMap map[string]any) string {
	if rawMap == nil {
		return ""
	}
	if summary, ok := rawMap["summary"]; ok {
		switch typed := summary.(type) {
		case string:
			return typed
		case []any:
			for _, item := range typed {
				if summaryText := reasoningSummaryTextFromItem(item); summaryText != "" {
					return summaryText
				}
			}
		case []map[string]any:
			for _, item := range typed {
				if summaryText := reasoningSummaryTextFromItem(item); summaryText != "" {
					return summaryText
				}
			}
		case map[string]any:
			return reasoningSummaryTextFromItem(typed)
		}
	}
	if text, ok := rawMap["reasoning_content"].(string); ok {
		return text
	}
	if text, ok := rawMap["thinking"].(string); ok {
		return text
	}
	if text, ok := rawMap["content"].(string); ok {
		return text
	}
	if text, ok := rawMap["text"].(string); ok {
		return text
	}
	return ""
}

func reasoningSummaryTextFromItem(item any) string {
	block, ok := item.(map[string]any)
	if !ok {
		return ""
	}
	if text, ok := block["text"].(string); ok && text != "" {
		return text
	}
	if text, ok := block["summary_text"].(string); ok && text != "" {
		return text
	}
	if nested, ok := block["summary_text"].(map[string]any); ok {
		if text, ok := nested["text"].(string); ok && text != "" {
			return text
		}
	}
	if text, ok := block["thinking"].(string); ok && text != "" {
		return text
	}
	return ""
}

func decodeFunctionCallOutputParts(raw any) ([]model.CanonicalContentPart, error) {
	switch typed := raw.(type) {
	case nil:
		return nil, nil
	case string:
		return []model.CanonicalContentPart{{Type: "text", Text: typed}}, nil
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return nil, err
		}
		return []model.CanonicalContentPart{{Type: "text", Text: string(encoded), Raw: map[string]any{"tool_output_structured": typed}}}, nil
	}
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

func decodeResponsesInputImage(raw json.RawMessage) (model.CanonicalContentPart, any, error) {
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return model.CanonicalContentPart{Type: "input_image", ImageURL: asString}, asString, nil
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		return model.CanonicalContentPart{}, nil, err
	}
	url, _ := asMap["url"].(string)
	return model.CanonicalContentPart{Type: "input_image", ImageURL: url, Raw: map[string]any{"image_url": cloneMapAny(asMap)}}, cloneMapAny(asMap), nil
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

func cloneMapSlice(input []map[string]any) []map[string]any {
	if len(input) == 0 {
		return nil
	}
	cloned := make([]map[string]any, 0, len(input))
	for _, item := range input {
		cloned = append(cloned, cloneMapAny(item))
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

func collectPassthroughTopLevelFields(raw map[string]any, req request) map[string]any {
	fields := map[string]any{}
	known := map[string]struct{}{
		"model": {}, "stream": {}, "store": {}, "include": {}, "previous_response_id": {}, "metadata": {},
		"parallel_tool_calls": {}, "truncation": {}, "text": {}, "instructions": {}, "input": {}, "tools": {},
		"tool_choice": {}, "reasoning": {}, "temperature": {}, "top_p": {}, "max_output_tokens": {}, "stop": {},
	}
	for key, value := range raw {
		if _, ok := known[key]; ok {
			continue
		}
		fields[key] = value
	}
	if preserved := collectResponseEchoTopLevelFields(req); len(preserved) > 0 {
		for key, value := range preserved {
			fields[key] = value
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func collectResponseEchoTopLevelFields(req request) map[string]any {
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
