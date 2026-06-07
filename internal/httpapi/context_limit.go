package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/errorsx"
	modelpkg "openai-compat-proxy/internal/model"
)

const contextOverflowMessage = "prompt is too long: context_length_exceeded by proxy model limit"

func setProxyModelLimitContextHeader(w http.ResponseWriter, provider config.ProviderConfig, clientModel string) int {
	limit := provider.ResolveModelLimitContextTokens(strings.TrimSpace(clientModel))
	w.Header().Set(headerProxyModelLimitContextTokens, strconv.Itoa(limit))
	return limit
}

func writeContextLimitExceededIfNeeded(w http.ResponseWriter, provider config.ProviderConfig, clientModel string, canon modelpkg.CanonicalRequest, protocol string) bool {
	limit := setProxyModelLimitContextHeader(w, provider, clientModel)
	if limit < 0 {
		return false
	}
	if estimateCanonicalInputTokens(canon) <= limit {
		return false
	}
	switch protocol {
	case clientReasoningProtocolMessages:
		writeAnthropicContextLimitExceeded(w)
	default:
		errorsx.WriteJSON(w, http.StatusBadRequest, "context_length_exceeded", contextOverflowMessage)
	}
	return true
}

func writeAnthropicContextLimitExceeded(w http.ResponseWriter) {
	payload := map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    "invalid_request_error",
			"message": contextOverflowMessage,
			"code":    "context_length_exceeded",
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		errorsx.WriteJSON(w, http.StatusBadRequest, "context_length_exceeded", contextOverflowMessage)
		return
	}
	errorsx.WriteRawJSON(w, http.StatusBadRequest, encoded)
}

func estimateCanonicalInputTokens(canon modelpkg.CanonicalRequest) int {
	chars := utf8.RuneCountInString(canon.Model)
	chars += utf8.RuneCountInString(canon.Instructions)
	for _, part := range canon.InstructionParts {
		chars += estimateContentPartChars(part)
	}
	for _, msg := range canon.Messages {
		chars += utf8.RuneCountInString(msg.Role)
		chars += utf8.RuneCountInString(msg.ToolCallID)
		chars += utf8.RuneCountInString(msg.ReasoningContent)
		for _, part := range msg.Parts {
			chars += estimateContentPartChars(part)
		}
		for _, block := range msg.OrderedContent {
			chars += estimateContentPartChars(block.Part)
			chars += utf8.RuneCountInString(block.ToolCall.Name)
			chars += utf8.RuneCountInString(block.ToolCall.Arguments)
			chars += utf8.RuneCountInString(block.ToolCallID)
			for _, part := range block.ToolResultParts {
				chars += estimateContentPartChars(part)
			}
		}
		for _, toolCall := range msg.ToolCalls {
			chars += utf8.RuneCountInString(toolCall.Name)
			chars += utf8.RuneCountInString(toolCall.Arguments)
		}
	}
	for _, item := range canon.ResponseInputItems {
		if encoded, err := json.Marshal(item); err == nil {
			chars += utf8.RuneCount(encoded)
		}
	}
	for _, tool := range canon.Tools {
		chars += utf8.RuneCountInString(tool.Name)
		chars += utf8.RuneCountInString(tool.Description)
		if encoded, err := json.Marshal(tool.Parameters); err == nil {
			chars += utf8.RuneCount(encoded)
		}
	}
	if chars <= 0 {
		return 0
	}
	return (chars + 3) / 4
}

func estimateContentPartChars(part modelpkg.CanonicalContentPart) int {
	chars := utf8.RuneCountInString(part.Type)
	chars += utf8.RuneCountInString(part.Text)
	chars += utf8.RuneCountInString(part.ImageURL)
	chars += utf8.RuneCountInString(part.MimeType)
	if encoded, err := json.Marshal(part.Raw); err == nil {
		chars += utf8.RuneCount(encoded)
	}
	return chars
}
