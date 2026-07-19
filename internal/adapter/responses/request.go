package responses

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"openai-compat-proxy/internal/model"
)

type request struct {
	Model                     string          `json:"model"`
	Stream                    bool            `json:"stream"`
	Store                     *bool           `json:"store"`
	Include                   []string        `json:"include"`
	PreviousResponseID        string          `json:"previous_response_id"`
	Metadata                  json.RawMessage `json:"metadata"`
	ParallelToolCalls         *bool           `json:"parallel_tool_calls"`
	Truncation                json.RawMessage `json:"truncation"`
	Text                      json.RawMessage `json:"text"`
	Instructions              json.RawMessage `json:"instructions"`
	Input                     requestInput    `json:"input"`
	Tools                     []tool          `json:"tools"`
	ToolChoice                any             `json:"tool_choice"`
	Reasoning                 *reasoning      `json:"reasoning"`
	Temperature               json.RawMessage `json:"temperature"`
	TopP                      json.RawMessage `json:"top_p"`
	MaxOutputTokensRaw        json.RawMessage `json:"max_output_tokens"`
	Stop                      []string        `json:"stop"`
	PromptCacheKey            json.RawMessage `json:"prompt_cache_key"`
	PromptCacheOptions        json.RawMessage `json:"prompt_cache_options"`
	MultiAgent                json.RawMessage `json:"multi_agent"`
	inputWasString            bool            `json:"-"`
	passthroughTopLevelFields map[string]any  `json:"-"`
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
	Raw         map[string]any `json:"-"`
}

func (t *tool) UnmarshalJSON(data []byte) error {
	type alias tool
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*t = tool(decoded)
	t.Raw = raw
	return nil
}

type reasoning struct {
	Effort  string         `json:"effort"`
	Summary string         `json:"summary"`
	Raw     map[string]any `json:"-"`
}

func (r *request) UnmarshalJSON(data []byte) error {
	type alias request
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	passthrough, inputWasString, err := decodeRequestPreservedFields(data)
	if err != nil {
		return err
	}

	*r = request(decoded)
	r.inputWasString = inputWasString
	r.passthroughTopLevelFields = passthrough
	return nil
}

func decodeRequestPreservedFields(data []byte) (map[string]any, bool, error) {
	pos := skipJSONWhitespace(data, 0)
	if pos >= len(data) {
		return nil, false, fmt.Errorf("responses request must be a JSON object")
	}
	if isJSONNullAt(data, pos) {
		return nil, false, nil
	}
	if data[pos] != '{' {
		return nil, false, fmt.Errorf("responses request must be a JSON object")
	}
	pos++
	var passthrough map[string]any
	inputWasString := false
	for {
		pos = skipJSONWhitespace(data, pos)
		if pos >= len(data) {
			return nil, false, fmt.Errorf("unexpected end of responses request")
		}
		if data[pos] == '}' {
			return finishPreservedFields(data, pos+1, passthrough, inputWasString)
		}

		keyStart, keyEnd, next, err := scanJSONString(data, pos)
		if err != nil {
			return nil, false, err
		}
		var key string
		if err := json.Unmarshal(data[keyStart:keyEnd], &key); err != nil {
			return nil, false, err
		}
		pos = skipJSONWhitespace(data, next)
		if pos >= len(data) || data[pos] != ':' {
			return nil, false, fmt.Errorf("missing colon after responses request field %q", key)
		}
		pos = skipJSONWhitespace(data, pos+1)
		valueStart, valueEnd, next, err := scanJSONValue(data, pos)
		if err != nil {
			return nil, false, err
		}
		if key == "input" {
			inputWasString = isLegacyStringInputValue(data[valueStart:valueEnd])
		} else if !isKnownRequestField(key) {
			var value any
			if err := json.Unmarshal(data[valueStart:valueEnd], &value); err != nil {
				return nil, false, err
			}
			if passthrough == nil {
				passthrough = make(map[string]any)
			}
			passthrough[key] = value
		}
		pos = skipJSONWhitespace(data, next)
		if pos >= len(data) {
			return nil, false, fmt.Errorf("unexpected end after responses request field %q", key)
		}
		if data[pos] == '}' {
			return finishPreservedFields(data, pos+1, passthrough, inputWasString)
		}
		if data[pos] != ',' {
			return nil, false, fmt.Errorf("missing comma after responses request field %q", key)
		}
		pos++
	}
}

func finishPreservedFields(data []byte, pos int, passthrough map[string]any, inputWasString bool) (map[string]any, bool, error) {
	if skipJSONWhitespace(data, pos) != len(data) {
		return nil, false, fmt.Errorf("unexpected trailing data after responses request")
	}
	return passthrough, inputWasString, nil
}

func isJSONNullAt(data []byte, pos int) bool {
	return pos+4 <= len(data) && data[pos] == 'n' && data[pos+1] == 'u' && data[pos+2] == 'l' && data[pos+3] == 'l' && skipJSONWhitespace(data, pos+4) == len(data)
}

func isLegacyStringInputValue(value []byte) bool {
	if len(value) > 0 && value[0] == '"' {
		return true
	}
	return isJSONNullAt(value, 0)
}

func skipJSONWhitespace(data []byte, pos int) int {
	for pos < len(data) {
		switch data[pos] {
		case ' ', '\t', '\r', '\n':
			pos++
		default:
			return pos
		}
	}
	return pos
}

func scanJSONString(data []byte, pos int) (int, int, int, error) {
	if pos >= len(data) || data[pos] != '"' {
		return 0, 0, 0, fmt.Errorf("responses request field name must be a JSON string")
	}
	start := pos
	pos++
	for ; pos < len(data); pos++ {
		switch data[pos] {
		case '\\':
			pos++
		case '"':
			return start, pos + 1, pos + 1, nil
		}
	}
	return 0, 0, 0, fmt.Errorf("unterminated responses request string")
}

func scanJSONValue(data []byte, pos int) (int, int, int, error) {
	start := pos
	if pos >= len(data) {
		return 0, 0, 0, fmt.Errorf("missing JSON value in responses request")
	}
	if data[pos] == '"' {
		return scanJSONString(data, pos)
	}
	if data[pos] == '{' || data[pos] == '[' {
		open := data[pos]
		close := byte('}')
		if open == '[' {
			close = ']'
		}
		depth := 0
		inString := false
		for ; pos < len(data); pos++ {
			if inString {
				switch data[pos] {
				case '\\':
					pos++
				case '"':
					inString = false
				}
				continue
			}
			switch data[pos] {
			case '"':
				inString = true
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					if data[pos] != close {
						return 0, 0, 0, fmt.Errorf("mismatched JSON value delimiter")
					}
					return start, pos + 1, pos + 1, nil
				}
			}
		}
		return 0, 0, 0, fmt.Errorf("unterminated JSON value in responses request")
	}
	for pos < len(data) && data[pos] != ',' && data[pos] != '}' && data[pos] != ']' && data[pos] != ' ' && data[pos] != '\t' && data[pos] != '\r' && data[pos] != '\n' {
		pos++
	}
	if start == pos {
		return 0, 0, 0, fmt.Errorf("missing JSON value in responses request")
	}
	return start, pos, pos, nil
}

func isKnownRequestField(key string) bool {
	switch key {
	case "model", "stream", "store", "include", "previous_response_id", "metadata",
		"parallel_tool_calls", "truncation", "text", "instructions", "input", "tools",
		"tool_choice", "reasoning", "temperature", "top_p", "max_output_tokens", "stop",
		"prompt_cache_key", "prompt_cache_options", "multi_agent":
		return true
	default:
		return false
	}
}

const preservedResponsesTopLevelFieldsKey = "__openai_compat_responses_top_level"

func DecodeRequest(r io.Reader) (model.CanonicalRequest, error) {
	var req request
	if err := json.NewDecoder(r).Decode(&req); err != nil {
		return model.CanonicalRequest{}, err
	}

	canon := model.CanonicalRequest{
		Model:                         req.Model,
		Stream:                        req.Stream,
		PreservedTopLevelFields:       collectPassthroughTopLevelFields(req),
		ResponseStore:                 req.Store,
		ResponseInclude:               append([]string(nil), req.Include...),
		ResponsePromptCacheKey:        cloneRawMessage(req.PromptCacheKey),
		ResponsePromptCacheOptions:    cloneRawMessage(req.PromptCacheOptions),
		ResponseMultiAgent:            cloneRawMessage(req.MultiAgent),
		Instructions:                  decodeOptionalString(req.Instructions),
		Temperature:                   decodeOptionalFloat(req.Temperature),
		TopP:                          decodeOptionalFloat(req.TopP),
		MaxOutputTokens:               decodeOptionalInt(req.MaxOutputTokensRaw),
		Stop:                          req.Stop,
		ParallelToolCalls:             req.ParallelToolCalls,
		ReasoningModeOrigin:           model.ReasoningModeOriginNone,
		ResponseInputItemsAreOriginal: !req.inputWasString,
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
			Mode:    model.ReasoningMode(stringMapValue(reasoningRaw, "mode")),
			Raw:     reasoningRaw,
		}
		if canon.Reasoning.Mode != "" {
			canon.ReasoningModeOrigin = model.ReasoningModeOriginBody
		}
	}

	for _, t := range req.Tools {
		rawTool := cloneMapAny(t.Raw)
		if len(rawTool) == 0 {
			rawTool = map[string]any{
				"type":        t.Type,
				"name":        t.Name,
				"description": t.Description,
				"parameters":  cloneMapAny(t.Parameters),
			}
		}
		canon.Tools = append(canon.Tools, model.CanonicalTool{
			Type:        t.Type,
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
			Raw:         rawTool,
		})
	}
	if req.ToolChoice != nil {
		canon.ToolChoice = model.DecodeOpenAIToolChoice(req.ToolChoice)
	}

	var inputInstructions []string
	functionCallIDs := make(map[string]struct{})
	for idx, rawItem := range req.Input {
		req.Input[idx] = nil
		if len(rawItem) == 0 {
			continue
		}
		var rawItemMap map[string]any
		if err := json.Unmarshal(rawItem, &rawItemMap); err != nil {
			return model.CanonicalRequest{}, err
		}
		if err := validateResponsesInputItemGraphItem(rawItemMap, functionCallIDs); err != nil {
			return model.CanonicalRequest{}, err
		}
		if isResponsesLegacyToolCallMessage(rawItemMap) {
			canon.ResponseInputItemsAreOriginal = false
		}
		preserved, msg, ok, syntheticReasoningReplay, err := decodeInputItem(rawItem, rawItemMap)
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

func isResponsesLegacyToolCallMessage(rawMap map[string]any) bool {
	role, _ := rawMap["role"].(string)
	switch role {
	case "assistant":
		_, ok := rawMap["tool_calls"]
		return ok
	case "tool":
		return stringMapValue(rawMap, "tool_call_id") != ""
	default:
		return false
	}
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
	return mergeResponsesParallelToolCallRounds(merged)
}

func mergeResponsesParallelToolCallRounds(messages []model.CanonicalMessage) []model.CanonicalMessage {
	if len(messages) < 3 {
		return messages
	}
	merged := make([]model.CanonicalMessage, 0, len(messages))
	for idx := 0; idx < len(messages); idx++ {
		msg := messages[idx]
		if !canStartResponsesParallelToolCallRound(msg) {
			merged = append(merged, msg)
			continue
		}

		callIDs := toolCallIDSet(msg.ToolCalls)
		toolResults := make([]model.CanonicalMessage, 0)
		nextIdx := idx + 1
		changed := false
		for nextIdx < len(messages) {
			for nextIdx < len(messages) && isResponsesToolResultForCallIDs(messages[nextIdx], callIDs) {
				toolResults = append(toolResults, messages[nextIdx])
				nextIdx++
			}
			if nextIdx >= len(messages) || !canMergeResponsesToolCallContinuation(messages[nextIdx]) {
				break
			}
			for _, call := range messages[nextIdx].ToolCalls {
				msg.ToolCalls = append(msg.ToolCalls, call)
				if call.ID != "" {
					callIDs[call.ID] = struct{}{}
				}
			}
			changed = true
			nextIdx++
		}

		if !changed {
			merged = append(merged, messages[idx])
			continue
		}
		merged = append(merged, msg)
		merged = append(merged, toolResults...)
		idx = nextIdx - 1
	}
	return merged
}

func isStandaloneResponsesReasoningMessage(msg model.CanonicalMessage) bool {
	return msg.Role == "assistant" && (len(msg.ReasoningBlocks) > 0 || msg.ReasoningContent != "") && len(msg.Parts) == 0 && len(msg.ToolCalls) == 0 && msg.ToolCallID == ""
}

func canMergeResponsesReasoningIntoToolMessage(msg model.CanonicalMessage) bool {
	return msg.Role == "assistant" && len(msg.ToolCalls) > 0 && msg.ToolCallID == "" && len(msg.Parts) == 0
}

func canStartResponsesParallelToolCallRound(msg model.CanonicalMessage) bool {
	return msg.Role == "assistant" && len(msg.ToolCalls) > 0 && msg.ToolCallID == "" && len(msg.Parts) == 0 && len(msg.OrderedContent) == 0 && (msg.ReasoningContent != "" || len(msg.ReasoningBlocks) > 0)
}

func canMergeResponsesToolCallContinuation(msg model.CanonicalMessage) bool {
	return msg.Role == "assistant" && len(msg.ToolCalls) > 0 && msg.ToolCallID == "" && len(msg.Parts) == 0 && len(msg.OrderedContent) == 0 && msg.ReasoningContent == "" && len(msg.ReasoningBlocks) == 0
}

func toolCallIDSet(calls []model.CanonicalToolCall) map[string]struct{} {
	ids := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		if call.ID != "" {
			ids[call.ID] = struct{}{}
		}
	}
	return ids
}

func isResponsesToolResultForCallIDs(msg model.CanonicalMessage, callIDs map[string]struct{}) bool {
	if msg.Role != "tool" || msg.ToolCallID == "" {
		return false
	}
	_, ok := callIDs[msg.ToolCallID]
	return ok
}

func validateResponsesInputItemGraphItem(rawMap map[string]any, functionCallIDs map[string]struct{}) error {
	switch itemType, _ := rawMap["type"].(string); itemType {
	case "item_reference":
		if id, _ := rawMap["id"].(string); id == "" {
			return fmt.Errorf("item_reference id is required")
		}
	case "function_call":
		callID, _ := rawMap["call_id"].(string)
		if callID == "" {
			return nil
		}
		if _, exists := functionCallIDs[callID]; exists {
			return fmt.Errorf("duplicate function_call call_id: %s", callID)
		}
		functionCallIDs[callID] = struct{}{}
	case "function_call_output":
		callID, _ := rawMap["call_id"].(string)
		if callID == "" || utf8.RuneCountInString(callID) > 64 {
			return fmt.Errorf("function_call_output call_id must contain 1 to 64 characters")
		}
		if _, exists := rawMap["output"]; !exists {
			return fmt.Errorf("function_call_output output is required")
		}
	}

	return nil
}

func decodeInputItem(raw json.RawMessage, rawMap map[string]any) (map[string]any, model.CanonicalMessage, bool, bool, error) {
	itemType, _ := rawMap["type"].(string)
	if itemType == "reasoning" {
		if model.IsSyntheticResponsesReasoningPlaceholder(rawMap) {
			return nil, model.CanonicalMessage{}, false, true, nil
		}
		reasoningContent := normalizeResponsesReasoningText(reasoningSummaryText(rawMap))
		preserved := cloneMapAny(rawMap)
		canonicalReasoningBlock := cloneMapAny(rawMap)
		if summary := normalizeResponsesReasoningSummary(canonicalReasoningBlock["summary"]); len(summary) > 0 {
			canonicalReasoningBlock["summary"] = summary
		}
		message := model.CanonicalMessage{Role: "assistant", ReasoningContent: reasoningContent, ReasoningBlocks: []map[string]any{canonicalReasoningBlock}}
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
		if msg, ok := decodeTextOnlyMessageInputItem(rawMap); ok {
			return preserveResponsesInputItem(rawMap), msg, true, false, nil
		}
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
					if model.IsSyntheticResponsesReasoningPlaceholder(block) {
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

		preserved := preserveResponsesInputItem(rawMap)

		return preserved, model.CanonicalMessage{Role: msg.Role, Parts: parts, ToolCalls: toolCalls, ToolCallID: msg.ToolCallID, ReasoningBlocks: reasoningBlocks}, true, false, nil
	}
	return cloneMapAny(rawMap), model.CanonicalMessage{}, false, false, nil
}

func decodeTextOnlyMessageInputItem(rawMap map[string]any) (model.CanonicalMessage, bool) {
	if _, hasToolCalls := rawMap["tool_calls"]; hasToolCalls {
		return model.CanonicalMessage{}, false
	}
	if _, hasToolCallID := rawMap["tool_call_id"]; hasToolCallID {
		return model.CanonicalMessage{}, false
	}

	role, _ := rawMap["role"].(string)
	content, hasContent := rawMap["content"]
	if !hasContent || content == nil {
		return model.CanonicalMessage{Role: role, Parts: make([]model.CanonicalContentPart, 0)}, true
	}
	if text, ok := content.(string); ok {
		if isUndefinedString(text) {
			return model.CanonicalMessage{Role: role, Parts: make([]model.CanonicalContentPart, 0)}, true
		}
		return model.CanonicalMessage{
			Role:  role,
			Parts: []model.CanonicalContentPart{{Type: "text", Text: text}},
		}, true
	}

	rawParts, ok := content.([]any)
	if !ok {
		return model.CanonicalMessage{}, false
	}
	parts := make([]model.CanonicalContentPart, 0, len(rawParts))
	for _, rawPart := range rawParts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			return model.CanonicalMessage{}, false
		}
		partType, ok := part["type"].(string)
		if !ok {
			return model.CanonicalMessage{}, false
		}
		switch partType {
		case "input_text", "output_text", "text":
			text, ok := textOnlyContentPartText(part)
			if !ok {
				return model.CanonicalMessage{}, false
			}
			parts = append(parts, model.CanonicalContentPart{Type: "text", Text: text})
		default:
			return model.CanonicalMessage{}, false
		}
	}
	return model.CanonicalMessage{Role: role, Parts: parts}, true
}

func textOnlyContentPartText(part map[string]any) (string, bool) {
	text, exists := part["text"]
	if !exists || text == nil {
		return "", true
	}
	value, ok := text.(string)
	return value, ok
}

func preserveResponsesInputItem(rawMap map[string]any) map[string]any {
	preserved := cloneMapAny(rawMap)
	content, ok := rawMap["content"].([]any)
	if !ok {
		return preserved
	}
	filtered := make([]any, 0, len(content))
	removedSyntheticReasoning := false
	for _, part := range content {
		block, _ := part.(map[string]any)
		if model.IsSyntheticResponsesReasoningPlaceholder(block) {
			removedSyntheticReasoning = true
			continue
		}
		filtered = append(filtered, part)
	}
	if removedSyntheticReasoning {
		preserved["content"] = filtered
	}
	return preserved
}

func extractInstructionTextFromInputItem(item map[string]any) string {
	role, _ := item["role"].(string)
	if !isInstructionRole(role) {
		return ""
	}
	return strings.TrimSpace(extractTextFromResponsesContent(item["content"]))
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
		*ri = requestInput{}
		return nil
	}

	var multi []json.RawMessage
	if err := json.Unmarshal(data, &multi); err == nil {
		*ri = requestInput(multi)
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
		*ri = requestInput{wrapped}
		return nil
	}

	var rawItem json.RawMessage
	if err := json.Unmarshal(data, &rawItem); err != nil {
		return err
	}
	*ri = requestInput{rawItem}
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

func collectPassthroughTopLevelFields(req request) map[string]any {
	fields := req.passthroughTopLevelFields
	if preserved := collectResponseEchoTopLevelFields(req); len(preserved) > 0 {
		if fields == nil {
			fields = make(map[string]any, len(preserved))
		}
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

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}
