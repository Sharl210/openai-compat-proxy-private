package httpapi

import (
	"encoding/json"
	"unicode/utf8"

	modelpkg "openai-compat-proxy/internal/model"
)

type estimatorSnapshot struct {
	TextChars           int64
	InputItemCount      int64
	ReasoningItemCount  int64
	ToolCallCount       int64
	ToolResultCount     int64
	MultimodalItemCount int64
	BaseEstimate        int64
}

func buildEstimatorSnapshot(canon modelpkg.CanonicalRequest) estimatorSnapshot {
	var snap estimatorSnapshot

	snap.TextChars += int64(utf8.RuneCountInString(canon.Model))
	snap.TextChars += int64(utf8.RuneCountInString(canon.Instructions))
	snap.InputItemCount = int64(len(canon.ResponseInputItems))

	for _, item := range canon.ResponseInputItems {
		typeName, _ := item["type"].(string)
		switch typeName {
		case "reasoning":
			snap.ReasoningItemCount++
		case "function_call_output":
			snap.ToolResultCount++
		case "function_call":
			snap.ToolCallCount++
		}
	}

	for _, part := range canon.InstructionParts {
		snap.TextChars += int64(estimateContentPartChars(part))
		if isMultimodalPart(part) {
			snap.MultimodalItemCount++
		}
	}

	for _, msg := range canon.Messages {
		snap.TextChars += int64(utf8.RuneCountInString(msg.Role))
		snap.TextChars += int64(utf8.RuneCountInString(msg.ToolCallID))
		snap.TextChars += int64(utf8.RuneCountInString(msg.ReasoningContent))
		snap.ReasoningItemCount += int64(len(msg.ReasoningBlocks))

		if len(msg.OrderedContent) > 0 {
			for _, block := range msg.OrderedContent {
				snap.TextChars += int64(estimateContentPartChars(block.Part))
				if isMultimodalPart(block.Part) {
					snap.MultimodalItemCount++
				}
				switch block.Type {
				case "tool_use":
					snap.ToolCallCount++
				case "tool_result":
					snap.ToolResultCount++
				}
				snap.TextChars += int64(utf8.RuneCountInString(block.ToolCall.Name))
				snap.TextChars += int64(utf8.RuneCountInString(block.ToolCall.Arguments))
				snap.TextChars += int64(utf8.RuneCountInString(block.ToolCallID))
				for _, part := range block.ToolResultParts {
					snap.TextChars += int64(estimateContentPartChars(part))
					if isMultimodalPart(part) {
						snap.MultimodalItemCount++
					}
				}
			}
			continue
		}

		for _, part := range msg.Parts {
			snap.TextChars += int64(estimateContentPartChars(part))
			if isMultimodalPart(part) {
				snap.MultimodalItemCount++
			}
		}

		snap.ToolCallCount += int64(len(msg.ToolCalls))
		for _, toolCall := range msg.ToolCalls {
			snap.TextChars += int64(utf8.RuneCountInString(toolCall.Name))
			snap.TextChars += int64(utf8.RuneCountInString(toolCall.Arguments))
		}

		if msg.Role == "tool" && len(msg.Parts) > 0 {
			snap.ToolResultCount++
		}
	}

	if len(canon.Messages) == 0 {
		for _, item := range canon.ResponseInputItems {
			if encoded, err := json.Marshal(item); err == nil {
				snap.TextChars += int64(utf8.RuneCount(encoded))
			}
		}
	}

	for _, tool := range canon.Tools {
		snap.ToolCallCount++
		snap.TextChars += int64(utf8.RuneCountInString(tool.Name))
		snap.TextChars += int64(utf8.RuneCountInString(tool.Description))
		if encoded, err := json.Marshal(tool.Parameters); err == nil {
			snap.TextChars += int64(utf8.RuneCount(encoded))
		}
	}

	if snap.TextChars > 0 {
		snap.BaseEstimate = (snap.TextChars + 3) / 4
	}

	return snap
}

func isMultimodalPart(part modelpkg.CanonicalContentPart) bool {
	return part.ImageURL != "" || part.MimeType != "" || part.Type == "input_file" || part.Type == "input_audio"
}
