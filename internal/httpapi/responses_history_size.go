package httpapi

import (
	"reflect"

	"openai-compat-proxy/internal/model"
)

func estimateCanonicalMessagesBytes(messages []model.CanonicalMessage) int64 {
	var total int64
	for _, message := range messages {
		total += int64(len(message.Role) + len(message.ToolCallID) + len(message.ReasoningContent))
		for _, part := range message.Parts {
			total += estimateCanonicalContentPartBytes(part)
		}
		for _, block := range message.OrderedContent {
			total += int64(len(block.Type) + len(block.ToolCallID))
			total += estimateCanonicalContentPartBytes(block.Part)
			total += estimateCanonicalToolCallBytes(block.ToolCall)
			for _, part := range block.ToolResultParts {
				total += estimateCanonicalContentPartBytes(part)
			}
			total += estimateDynamicValueBytes(block.Raw)
		}
		for _, call := range message.ToolCalls {
			total += estimateCanonicalToolCallBytes(call)
		}
		if message.RecoveredToolCall != nil {
			total += estimateCanonicalToolCallBytes(*message.RecoveredToolCall)
		}
		total += estimateDynamicValueBytes(message.ReasoningBlocks)
	}
	return total
}

func estimateCanonicalContentPartBytes(part model.CanonicalContentPart) int64 {
	return int64(len(part.Type)+len(part.Text)+len(part.ImageURL)+len(part.MimeType)) + estimateDynamicValueBytes(part.Raw)
}

func estimateCanonicalToolCallBytes(call model.CanonicalToolCall) int64 {
	return int64(len(call.ID) + len(call.ResponseItemID) + len(call.Type) + len(call.Name) + len(call.Arguments))
}

func estimateToolRecoveryBytes(messages []model.CanonicalMessage) int64 {
	var total int64
	for _, message := range messages {
		reasoningBytes := estimateDynamicValueBytes(message.ReasoningBlocks)
		if message.RecoveredToolCall != nil && message.RecoveredToolCall.ID != "" && message.RecoveredToolCall.Name != "" {
			total += estimateCanonicalToolCallBytes(*message.RecoveredToolCall) + reasoningBytes
		}
		for _, call := range message.ToolCalls {
			if call.ID == "" || call.Name == "" {
				continue
			}
			total += estimateCanonicalToolCallBytes(call) + reasoningBytes
		}
	}
	return total
}

func estimateResponsesHistoryToolCallEntryBytes(entry responsesHistoryToolCallEntry) int64 {
	callBytes := estimateCanonicalToolCallBytes(entry.Call)
	if entry.ArgumentsOriginalSize > len(entry.Call.Arguments) {
		callBytes += int64(entry.ArgumentsOriginalSize - len(entry.Call.Arguments))
	}
	return callBytes + int64(len(entry.ToolCallSequenceHash)) + estimateDynamicValueBytes(entry.ReasoningBlocks)
}

func estimateDynamicValueBytes(value any) int64 {
	if value == nil {
		return 0
	}
	switch typed := value.(type) {
	case string:
		return int64(len(typed))
	case []byte:
		return int64(len(typed))
	case map[string]any:
		var total int64
		for key, nested := range typed {
			total += int64(len(key)) + estimateDynamicValueBytes(nested)
		}
		return total
	case []any:
		var total int64
		for _, nested := range typed {
			total += estimateDynamicValueBytes(nested)
		}
		return total
	case []map[string]any:
		var total int64
		for _, nested := range typed {
			total += estimateDynamicValueBytes(nested)
		}
		return total
	}
	valueOf := reflect.ValueOf(value)
	switch valueOf.Kind() {
	case reflect.Array, reflect.Slice:
		var total int64
		for index := 0; index < valueOf.Len(); index++ {
			total += estimateDynamicValueBytes(valueOf.Index(index).Interface())
		}
		return total
	case reflect.Map:
		var total int64
		iterator := valueOf.MapRange()
		for iterator.Next() {
			total += estimateDynamicValueBytes(iterator.Key().Interface())
			total += estimateDynamicValueBytes(iterator.Value().Interface())
		}
		return total
	default:
		return int64(valueOf.Type().Size())
	}
}
