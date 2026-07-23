package httpapi

import (
	"bytes"
	"compress/flate"
	"encoding/json"
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
	responsesHistoryCompressedPartRawString
	responsesHistoryCompressedReasoningBlockString
)

type responsesHistoryDynamicPathStepKind uint8

const (
	responsesHistoryDynamicPathMapKey responsesHistoryDynamicPathStepKind = iota + 1
	responsesHistoryDynamicPathSliceIndex
)

type responsesHistoryDynamicPathStep struct {
	Kind  responsesHistoryDynamicPathStepKind `json:"kind"`
	Key   string                              `json:"key,omitempty"`
	Index int                                 `json:"index,omitempty"`
}

type responsesHistoryCompressedField struct {
	MessageIndex       int                                 `json:"message_index,omitempty"`
	ItemIndex          int                                 `json:"item_index"`
	Kind               responsesHistoryCompressedFieldKind `json:"kind"`
	OriginalSize       int                                 `json:"original_size"`
	Data               []byte                              `json:"data"`
	RestoreRawImageURL bool                                `json:"restore_raw_image_url,omitempty"`
	DynamicPath        []responsesHistoryDynamicPathStep   `json:"dynamic_path,omitempty"`
}

type responsesHistoryReasoningSnapshot struct {
	Blocks           []map[string]any                  `json:"blocks,omitempty"`
	CompressedFields []responsesHistoryCompressedField `json:"compressed_fields,omitempty"`
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
		for blockIndex := range message.ReasoningBlocks {
			fields = compressResponsesHistoryDynamicStrings(fields, message.ReasoningBlocks[blockIndex], responsesHistoryCompressedField{MessageIndex: messageIndex, ItemIndex: blockIndex, Kind: responsesHistoryCompressedReasoningBlockString})
		}
		for partIndex := range message.Parts {
			part := &message.Parts[partIndex]
			fields = compressResponsesHistoryField(fields, &part.Text, responsesHistoryCompressedField{MessageIndex: messageIndex, ItemIndex: partIndex, Kind: responsesHistoryCompressedPartText})
			imageURL := part.ImageURL
			fieldCount := len(fields)
			fields = compressResponsesHistoryField(fields, &part.ImageURL, responsesHistoryCompressedField{MessageIndex: messageIndex, ItemIndex: partIndex, Kind: responsesHistoryCompressedPartImageURL})
			if len(fields) > fieldCount && compactResponsesHistoryRawImageURL(part, imageURL) {
				fields[len(fields)-1].RestoreRawImageURL = true
			}
			fields = compressResponsesHistoryDynamicStrings(fields, part.Raw, responsesHistoryCompressedField{MessageIndex: messageIndex, ItemIndex: partIndex, Kind: responsesHistoryCompressedPartRawString})
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

func newResponsesHistoryReasoningSnapshot(blocks []map[string]any) *responsesHistoryReasoningSnapshot {
	cloned := cloneReasoningBlocksForHistory(blocks)
	if len(cloned) == 0 {
		return nil
	}
	snapshot := &responsesHistoryReasoningSnapshot{Blocks: cloned}
	for blockIndex := range snapshot.Blocks {
		snapshot.CompressedFields = compressResponsesHistoryDynamicStrings(snapshot.CompressedFields, snapshot.Blocks[blockIndex], responsesHistoryCompressedField{ItemIndex: blockIndex, Kind: responsesHistoryCompressedReasoningBlockString})
	}
	return snapshot
}

func newResponsesHistoryReasoningSnapshotFromConversationSnapshot(snapshot responsesConversationSnapshot, messageIndex int) (*responsesHistoryReasoningSnapshot, bool) {
	if messageIndex < 0 || messageIndex >= len(snapshot.Messages) {
		return nil, false
	}
	blocks := cloneReasoningBlocksForHistory(snapshot.Messages[messageIndex].ReasoningBlocks)
	if len(blocks) == 0 {
		return nil, true
	}
	compressedFields := make([]responsesHistoryCompressedField, 0)
	for _, field := range snapshot.CompressedFields {
		if field.Kind != responsesHistoryCompressedReasoningBlockString || field.MessageIndex != messageIndex {
			continue
		}
		copied := field
		copied.MessageIndex = 0
		copied.Data = append([]byte(nil), field.Data...)
		copied.DynamicPath = append([]responsesHistoryDynamicPathStep(nil), field.DynamicPath...)
		compressedFields = append(compressedFields, copied)
	}
	return &responsesHistoryReasoningSnapshot{Blocks: blocks, CompressedFields: compressedFields}, true
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

func compressResponsesHistoryDynamicStrings(fields []responsesHistoryCompressedField, value any, field responsesHistoryCompressedField) []responsesHistoryCompressedField {
	return compressResponsesHistoryDynamicStringsAtPath(fields, value, field, nil)
}

func compressResponsesHistoryDynamicStringsAtPath(fields []responsesHistoryCompressedField, value any, field responsesHistoryCompressedField, path []responsesHistoryDynamicPathStep) []responsesHistoryCompressedField {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			nextPath := appendResponsesHistoryDynamicPathStep(path, responsesHistoryDynamicPathStep{Kind: responsesHistoryDynamicPathMapKey, Key: key})
			if text, ok := nested.(string); ok {
				if len(text) < responsesHistoryCompressionMinFieldBytes {
					continue
				}
				compressed, compressedOK := compressResponsesHistoryString(text)
				if !compressedOK {
					continue
				}
				compressedField := field
				compressedField.OriginalSize = len(text)
				compressedField.Data = compressed
				compressedField.DynamicPath = nextPath
				typed[key] = ""
				fields = append(fields, compressedField)
				continue
			}
			fields = compressResponsesHistoryDynamicStringsAtPath(fields, nested, field, nextPath)
		}
	case []any:
		for index, nested := range typed {
			nextPath := appendResponsesHistoryDynamicPathStep(path, responsesHistoryDynamicPathStep{Kind: responsesHistoryDynamicPathSliceIndex, Index: index})
			if text, ok := nested.(string); ok {
				if len(text) < responsesHistoryCompressionMinFieldBytes {
					continue
				}
				compressed, compressedOK := compressResponsesHistoryString(text)
				if !compressedOK {
					continue
				}
				compressedField := field
				compressedField.OriginalSize = len(text)
				compressedField.Data = compressed
				compressedField.DynamicPath = nextPath
				typed[index] = ""
				fields = append(fields, compressedField)
				continue
			}
			fields = compressResponsesHistoryDynamicStringsAtPath(fields, nested, field, nextPath)
		}
	case []map[string]any:
		for index := range typed {
			nextPath := appendResponsesHistoryDynamicPathStep(path, responsesHistoryDynamicPathStep{Kind: responsesHistoryDynamicPathSliceIndex, Index: index})
			fields = compressResponsesHistoryDynamicStringsAtPath(fields, typed[index], field, nextPath)
		}
	}
	return fields
}

func appendResponsesHistoryDynamicPathStep(path []responsesHistoryDynamicPathStep, step responsesHistoryDynamicPathStep) []responsesHistoryDynamicPathStep {
	next := make([]responsesHistoryDynamicPathStep, len(path)+1)
	copy(next, path)
	next[len(path)] = step
	return next
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

func loadResponsesHistoryReasoningBlocksFromSnapshot(snapshot responsesConversationSnapshot, messageIndex int) ([]map[string]any, bool) {
	if messageIndex < 0 || messageIndex >= len(snapshot.Messages) {
		return nil, false
	}
	blocks := cloneReasoningBlocksForHistory(snapshot.Messages[messageIndex].ReasoningBlocks)
	for _, field := range snapshot.CompressedFields {
		if field.Kind != responsesHistoryCompressedReasoningBlockString || field.MessageIndex != messageIndex {
			continue
		}
		value, ok := decompressResponsesHistoryString(field.Data, field.OriginalSize)
		if !ok || !restoreResponsesHistoryReasoningBlockString(blocks, field, value) {
			return nil, false
		}
	}
	return blocks, true
}

func loadResponsesHistoryReasoningSnapshot(snapshot *responsesHistoryReasoningSnapshot) ([]map[string]any, bool) {
	if snapshot == nil {
		return nil, true
	}
	blocks := cloneReasoningBlocksForHistory(snapshot.Blocks)
	for _, field := range snapshot.CompressedFields {
		value, ok := decompressResponsesHistoryString(field.Data, field.OriginalSize)
		if !ok || !restoreResponsesHistoryReasoningBlockString(blocks, field, value) {
			return nil, false
		}
	}
	return blocks, true
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
	case responsesHistoryCompressedPartRawString:
		if field.ItemIndex < 0 || field.ItemIndex >= len(message.Parts) {
			return false
		}
		if !restoreResponsesHistoryDynamicString(message.Parts[field.ItemIndex].Raw, field.DynamicPath, value) {
			return false
		}
	case responsesHistoryCompressedReasoningBlockString:
		if !restoreResponsesHistoryReasoningBlockString(message.ReasoningBlocks, field, value) {
			return false
		}
	default:
		return false
	}
	return true
}

func restoreResponsesHistoryReasoningBlockString(blocks []map[string]any, field responsesHistoryCompressedField, value string) bool {
	if field.ItemIndex < 0 || field.ItemIndex >= len(blocks) {
		return false
	}
	return restoreResponsesHistoryDynamicString(blocks[field.ItemIndex], field.DynamicPath, value)
}

func restoreResponsesHistoryDynamicString(root any, path []responsesHistoryDynamicPathStep, value string) bool {
	if len(path) == 0 {
		return false
	}
	current := root
	for index, step := range path {
		isLast := index == len(path)-1
		switch step.Kind {
		case responsesHistoryDynamicPathMapKey:
			object, ok := current.(map[string]any)
			if !ok {
				return false
			}
			if isLast {
				object[step.Key] = value
				return true
			}
			next, exists := object[step.Key]
			if !exists {
				return false
			}
			current = next
		case responsesHistoryDynamicPathSliceIndex:
			switch items := current.(type) {
			case []any:
				if step.Index < 0 || step.Index >= len(items) {
					return false
				}
				if isLast {
					items[step.Index] = value
					return true
				}
				current = items[step.Index]
			case []map[string]any:
				if step.Index < 0 || step.Index >= len(items) || isLast {
					return false
				}
				current = items[step.Index]
			default:
				return false
			}
		default:
			return false
		}
	}
	return false
}

func cloneResponsesHistoryDynamicMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = cloneResponsesHistoryDynamicValue(value)
	}
	return cloned
}

func cloneResponsesHistoryDynamicValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneResponsesHistoryDynamicMap(typed)
	case []any:
		if typed == nil {
			return nil
		}
		cloned := make([]any, len(typed))
		for index, nested := range typed {
			cloned[index] = cloneResponsesHistoryDynamicValue(nested)
		}
		return cloned
	case []map[string]any:
		if typed == nil {
			return nil
		}
		cloned := make([]map[string]any, len(typed))
		for index, nested := range typed {
			cloned[index] = cloneResponsesHistoryDynamicMap(nested)
		}
		return cloned
	case map[string]string:
		if typed == nil {
			return nil
		}
		cloned := make(map[string]string, len(typed))
		for key, nested := range typed {
			cloned[key] = nested
		}
		return cloned
	case []string:
		if typed == nil {
			return nil
		}
		cloned := make([]string, len(typed))
		copy(cloned, typed)
		return cloned
	case json.RawMessage:
		if typed == nil {
			return nil
		}
		cloned := make(json.RawMessage, len(typed))
		copy(cloned, typed)
		return cloned
	case []byte:
		if typed == nil {
			return nil
		}
		cloned := make([]byte, len(typed))
		copy(cloned, typed)
		return cloned
	default:
		return value
	}
}

func responsesHistoryFlateWriter() (*flate.Writer, error) {
	if pooled := responsesHistoryFlateWriterPool.Get(); pooled != nil {
		return pooled.(*flate.Writer), nil
	}
	return flate.NewWriter(io.Discard, flate.BestSpeed)
}
