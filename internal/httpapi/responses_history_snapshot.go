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
	MessageIndex       int
	ItemIndex          int
	Kind               responsesHistoryCompressedFieldKind
	OriginalSize       int
	Data               []byte
	RestoreRawImageURL bool
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
			imageURL := part.ImageURL
			fieldCount := len(fields)
			fields = compressResponsesHistoryField(fields, &part.ImageURL, responsesHistoryCompressedField{MessageIndex: messageIndex, ItemIndex: partIndex, Kind: responsesHistoryCompressedPartImageURL})
			if len(fields) > fieldCount && compactResponsesHistoryRawImageURL(part, imageURL) {
				fields[len(fields)-1].RestoreRawImageURL = true
			}
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

func compactResponsesHistoryRawImageURL(part *model.CanonicalContentPart, imageURL string) bool {
	if part == nil || imageURL == "" || len(part.Raw) == 0 {
		return false
	}
	rawImage, _ := part.Raw["image_url"].(map[string]any)
	if len(rawImage) == 0 {
		return false
	}
	rawURL, _ := rawImage["url"].(string)
	if rawURL != imageURL {
		return false
	}
	compactRaw := make(map[string]any, len(part.Raw))
	for key, value := range part.Raw {
		compactRaw[key] = value
	}
	compactImage := make(map[string]any, len(rawImage))
	for key, value := range rawImage {
		if key != "url" {
			compactImage[key] = value
		}
	}
	compactRaw["image_url"] = compactImage
	part.Raw = compactRaw
	return true
}

func restoreResponsesHistoryRawImageURL(part *model.CanonicalContentPart, imageURL string) {
	if part == nil || imageURL == "" {
		return
	}
	rawImage, _ := part.Raw["image_url"].(map[string]any)
	restoredRaw := make(map[string]any, len(part.Raw)+1)
	for key, value := range part.Raw {
		restoredRaw[key] = value
	}
	restoredImage := make(map[string]any, len(rawImage)+1)
	for key, value := range rawImage {
		restoredImage[key] = value
	}
	restoredImage["url"] = imageURL
	restoredRaw["image_url"] = restoredImage
	part.Raw = restoredRaw
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

func compressResponsesHistoryToolCallEntryWithSnapshotField(entry responsesHistoryToolCallEntry, data []byte, originalSize int, ok bool) responsesHistoryToolCallEntry {
	if !ok || len(data) == 0 || originalSize <= 0 {
		return compressResponsesHistoryToolCallEntry(entry)
	}
	entry.ArgumentsOriginalSize = originalSize
	entry.ArgumentsCompressed = data
	entry.Call.Arguments = ""
	return entry
}

func responsesHistoryCompressedFieldData(snapshot *responsesConversationSnapshot, messageIndex, itemIndex int, kind responsesHistoryCompressedFieldKind) ([]byte, int, bool) {
	if snapshot == nil {
		return nil, 0, false
	}
	for _, field := range snapshot.CompressedFields {
		if field.MessageIndex == messageIndex && field.ItemIndex == itemIndex && field.Kind == kind {
			return field.Data, field.OriginalSize, true
		}
	}
	return nil, 0, false
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
		part := &message.Parts[field.ItemIndex]
		part.ImageURL = value
		if field.RestoreRawImageURL {
			// 加载副本单独恢复，避免把 URL 重新留回缓存快照。
			restoreResponsesHistoryRawImageURL(part, value)
		}
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
