package httpapi

import (
	"bytes"
	"compress/flate"
	"io"
	"strings"
	"sync"

	"openai-compat-proxy/internal/model"
)

const responsesHistoryCompressionMinSnapshotBytes int64 = 64 << 10
const responsesHistoryCompressionMinFieldBytes = 16 << 10

type responsesHistoryCompressedFieldKind uint8

const (
	responsesHistoryCompressedReasoning responsesHistoryCompressedFieldKind = iota + 1
	responsesHistoryCompressedPartText
	responsesHistoryCompressedPartImageURL
	responsesHistoryCompressedToolArguments
	responsesHistoryCompressedRecoveredToolArguments
)

type responsesHistoryCompressedField struct {
	MessageIndex int
	ItemIndex    int
	Kind         responsesHistoryCompressedFieldKind
	OriginalSize int
	Data         []byte
}

var responsesHistoryFlateWriterPool sync.Pool

func newResponsesConversationSnapshot(messages []model.CanonicalMessage, logicalBytes int64) (responsesConversationSnapshot, []model.CanonicalMessage) {
	cloned := cloneCanonicalMessages(messages)
	snapshot := responsesConversationSnapshot{Messages: cloned, Bytes: logicalBytes}
	if logicalBytes >= responsesHistoryCompressionMinSnapshotBytes {
		snapshot.CompressedFields = compressResponsesHistoryFields(cloned)
	}
	return snapshot, messages
}

func compressResponsesHistoryFields(messages []model.CanonicalMessage) []responsesHistoryCompressedField {
	var fields []responsesHistoryCompressedField
	for messageIndex := range messages {
		message := &messages[messageIndex]
		fields = compressResponsesHistoryField(fields, &message.ReasoningContent, responsesHistoryCompressedField{MessageIndex: messageIndex, Kind: responsesHistoryCompressedReasoning})
		for partIndex := range message.Parts {
			part := &message.Parts[partIndex]
			fields = compressResponsesHistoryField(fields, &part.Text, responsesHistoryCompressedField{MessageIndex: messageIndex, ItemIndex: partIndex, Kind: responsesHistoryCompressedPartText})
			fields = compressResponsesHistoryField(fields, &part.ImageURL, responsesHistoryCompressedField{MessageIndex: messageIndex, ItemIndex: partIndex, Kind: responsesHistoryCompressedPartImageURL})
		}
		for toolIndex := range message.ToolCalls {
			toolCall := &message.ToolCalls[toolIndex]
			fields = compressResponsesHistoryField(fields, &toolCall.Arguments, responsesHistoryCompressedField{MessageIndex: messageIndex, ItemIndex: toolIndex, Kind: responsesHistoryCompressedToolArguments})
		}
		if message.RecoveredToolCall != nil {
			fields = compressResponsesHistoryField(fields, &message.RecoveredToolCall.Arguments, responsesHistoryCompressedField{MessageIndex: messageIndex, Kind: responsesHistoryCompressedRecoveredToolArguments})
		}
	}
	return fields
}

func compressResponsesHistoryField(fields []responsesHistoryCompressedField, value *string, field responsesHistoryCompressedField) []responsesHistoryCompressedField {
	if len(*value) < responsesHistoryCompressionMinFieldBytes {
		return fields
	}
	compressed, ok := compressResponsesHistoryString(*value)
	if !ok {
		return fields
	}
	field.OriginalSize = len(*value)
	field.Data = compressed
	*value = ""
	return append(fields, field)
}

func compressResponsesHistoryString(value string) ([]byte, bool) {
	writer, err := responsesHistoryFlateWriter()
	if err != nil {
		return nil, false
	}
	var compressed bytes.Buffer
	writer.Reset(&compressed)
	if _, err := io.Copy(writer, strings.NewReader(value)); err != nil {
		if closeErr := writer.Close(); closeErr != nil {
			return nil, false
		}
		return nil, false
	}
	if err := writer.Close(); err != nil {
		return nil, false
	}
	writer.Reset(io.Discard)
	responsesHistoryFlateWriterPool.Put(writer)
	if compressed.Len()*4 >= len(value)*3 {
		return nil, false
	}
	return append([]byte(nil), compressed.Bytes()...), true
}

func compressResponsesHistoryToolCallEntry(entry responsesHistoryToolCallEntry) responsesHistoryToolCallEntry {
	if entry.Call.Arguments == "" || len(entry.Call.Arguments) < responsesHistoryCompressionMinFieldBytes {
		return entry
	}
	compressed, ok := compressResponsesHistoryString(entry.Call.Arguments)
	if !ok {
		return entry
	}
	entry.ArgumentsOriginalSize = len(entry.Call.Arguments)
	entry.ArgumentsCompressed = compressed
	entry.Call.Arguments = ""
	return entry
}

func normalizeResponsesHistoryToolCallEntry(entry responsesHistoryToolCallEntry) (responsesHistoryToolCallEntry, bool) {
	if len(entry.ArgumentsCompressed) > 0 || entry.ArgumentsOriginalSize != 0 {
		if !validResponsesHistoryCompressedStream(entry.ArgumentsCompressed, entry.ArgumentsOriginalSize) {
			return responsesHistoryToolCallEntry{}, false
		}
		entry.Call.Arguments = ""
		return entry, true
	}
	return compressResponsesHistoryToolCallEntry(entry), true
}

func loadResponsesHistoryToolCallArguments(entry responsesHistoryToolCallEntry) (string, bool) {
	if len(entry.ArgumentsCompressed) == 0 && entry.ArgumentsOriginalSize == 0 {
		return entry.Call.Arguments, true
	}
	if entry.Call.Arguments != "" || !validResponsesHistoryCompressedData(entry.ArgumentsCompressed, entry.ArgumentsOriginalSize) {
		return "", false
	}
	return decompressResponsesHistoryString(entry.ArgumentsCompressed, entry.ArgumentsOriginalSize)
}

func loadResponsesConversationSnapshot(snapshot responsesConversationSnapshot) []model.CanonicalMessage {
	messages := cloneCanonicalMessages(snapshot.Messages)
	for _, field := range snapshot.CompressedFields {
		value, ok := decompressResponsesHistoryString(field.Data, field.OriginalSize)
		if !ok || !restoreResponsesHistoryField(messages, field, value) {
			return nil
		}
	}
	return messages
}

func decompressResponsesHistoryString(data []byte, originalSize int) (string, bool) {
	if !validResponsesHistoryCompressedData(data, originalSize) {
		return "", false
	}
	reader := flate.NewReader(bytes.NewReader(data))
	var restored strings.Builder
	restored.Grow(originalSize)
	_, copyErr := io.Copy(&restored, io.LimitReader(reader, int64(originalSize)+1))
	closeErr := reader.Close()
	if copyErr != nil || closeErr != nil || restored.Len() != originalSize {
		return "", false
	}
	return restored.String(), true
}

func validResponsesHistoryCompressedData(data []byte, originalSize int) bool {
	if len(data) == 0 || originalSize <= 0 || int64(originalSize) > defaultResponsesHistoryMaxBytes || int64(len(data)) > defaultResponsesHistoryMaxBytes {
		return false
	}
	return int64(len(data))*4 < int64(originalSize)*3
}

func validResponsesHistoryCompressedStream(data []byte, originalSize int) bool {
	if !validResponsesHistoryCompressedData(data, originalSize) {
		return false
	}
	reader := flate.NewReader(bytes.NewReader(data))
	restored, copyErr := io.Copy(io.Discard, io.LimitReader(reader, int64(originalSize)+1))
	closeErr := reader.Close()
	return copyErr == nil && closeErr == nil && restored == int64(originalSize)
}

func restoreResponsesHistoryField(messages []model.CanonicalMessage, field responsesHistoryCompressedField, value string) bool {
	if field.MessageIndex < 0 || field.MessageIndex >= len(messages) {
		return false
	}
	message := &messages[field.MessageIndex]
	switch field.Kind {
	case responsesHistoryCompressedReasoning:
		message.ReasoningContent = value
	case responsesHistoryCompressedPartText:
		if field.ItemIndex < 0 || field.ItemIndex >= len(message.Parts) {
			return false
		}
		message.Parts[field.ItemIndex].Text = value
	case responsesHistoryCompressedPartImageURL:
		if field.ItemIndex < 0 || field.ItemIndex >= len(message.Parts) {
			return false
		}
		message.Parts[field.ItemIndex].ImageURL = value
	case responsesHistoryCompressedToolArguments:
		if field.ItemIndex < 0 || field.ItemIndex >= len(message.ToolCalls) {
			return false
		}
		message.ToolCalls[field.ItemIndex].Arguments = value
	case responsesHistoryCompressedRecoveredToolArguments:
		if message.RecoveredToolCall == nil {
			return false
		}
		message.RecoveredToolCall.Arguments = value
	default:
		return false
	}
	return true
}

func responsesHistoryFlateWriter() (*flate.Writer, error) {
	if pooled := responsesHistoryFlateWriterPool.Get(); pooled != nil {
		return pooled.(*flate.Writer), nil
	}
	return flate.NewWriter(io.Discard, flate.BestSpeed)
}
