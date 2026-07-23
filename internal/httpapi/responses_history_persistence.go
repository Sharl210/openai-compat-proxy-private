package httpapi

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func cloneResponsesHistoryCompressedFields(fields []responsesHistoryCompressedField) []responsesHistoryCompressedField {
	if fields == nil {
		return nil
	}
	cloned := make([]responsesHistoryCompressedField, len(fields))
	for index, field := range fields {
		cloned[index] = field
		cloned[index].Data = append([]byte(nil), field.Data...)
		cloned[index].DynamicPath = append([]responsesHistoryDynamicPathStep(nil), field.DynamicPath...)
	}
	return cloned
}

func cloneResponsesHistoryReasoningSnapshot(snapshot responsesHistoryReasoningSnapshot) *responsesHistoryReasoningSnapshot {
	return &responsesHistoryReasoningSnapshot{
		Blocks:           cloneReasoningBlocksForHistory(snapshot.Blocks),
		CompressedFields: cloneResponsesHistoryCompressedFields(snapshot.CompressedFields),
	}
}

func responsesHistoryLegacyReasoningSnapshotKey(blocks []map[string]any) string {
	encoded, err := json.Marshal(blocks)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return "legacy-" + hex.EncodeToString(sum[:])
}

func estimateResponsesHistoryReasoningSnapshotBytes(snapshot *responsesHistoryReasoningSnapshot) int64 {
	if snapshot == nil {
		return 0
	}
	total := estimateDynamicValueBytes(snapshot.Blocks)
	for _, field := range snapshot.CompressedFields {
		total += int64(field.OriginalSize)
	}
	return total
}

func estimateResponsesHistoryToolCallMetadataBytes(entry responsesHistoryToolCallEntry) int64 {
	callBytes := estimateCanonicalToolCallBytes(entry.Call)
	if entry.ArgumentsOriginalSize > len(entry.Call.Arguments) {
		callBytes += int64(entry.ArgumentsOriginalSize - len(entry.Call.Arguments))
	}
	return callBytes + int64(len(entry.ToolCallSequenceHash))
}

func (s *responsesHistoryStore) saveToolCallRecoveryIndexLocked() error {
	if s == nil || s.toolCallRecoveryIndexPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.toolCallRecoveryIndexPath), 0o755); err != nil {
		return err
	}
	if len(s.toolCalls) == 0 {
		if err := os.Remove(s.toolCallRecoveryIndexPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return atomicWriteResponsesHistoryFile(s.toolCallRecoveryIndexPath, s.writeToolCallRecoveryIndex)
}

func (s *responsesHistoryStore) writeToolCallRecoveryIndex(output io.Writer) error {
	keys := make([]string, 0, len(s.toolCalls))
	for key, entry := range s.toolCalls {
		if key != "" && entry.Call.ID != "" && entry.Call.Name != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	persistedSnapshots := make(map[string]responsesHistoryReasoningSnapshot)
	entrySnapshotKeys := make(map[string]string)
	pointerKeys := make(map[*responsesHistoryReasoningSnapshot]string)
	normalSnapshotPointers := make(map[string]*responsesHistoryReasoningSnapshot)
	nextSnapshotKey := 0
	registerSnapshot := func(snapshot *responsesHistoryReasoningSnapshot, preferredKey string) string {
		if snapshot == nil {
			return ""
		}
		if key, exists := pointerKeys[snapshot]; exists {
			return key
		}
		key := strings.TrimSpace(preferredKey)
		if _, exists := persistedSnapshots[key]; key == "" || exists {
			for {
				nextSnapshotKey++
				key = "reasoning-" + strconv.Itoa(nextSnapshotKey)
				if _, exists := persistedSnapshots[key]; !exists {
					break
				}
			}
		}
		pointerKeys[snapshot] = key
		persistedSnapshots[key] = *snapshot
		return key
	}

	for _, key := range keys {
		entry := s.toolCalls[key]
		var snapshot *responsesHistoryReasoningSnapshot
		preferredKey := entry.ReasoningSnapshotKey
		if entry.SharedReasoningSnapshot != nil {
			snapshot = entry.SharedReasoningSnapshot
		} else if entry.ReasoningBlocksFromSnapshot {
			logicalKey := entry.SnapshotKey + "\x00" + strconv.Itoa(entry.SnapshotReasoningMessageIndex)
			snapshot = normalSnapshotPointers[logicalKey]
			if snapshot == nil {
				storedSnapshot, found := s.entries[entry.SnapshotKey]
				if !found {
					return errors.New("invalid responses tool-call reasoning snapshot reference")
				}
				snapshot, found = newResponsesHistoryReasoningSnapshotFromConversationSnapshot(storedSnapshot, entry.SnapshotReasoningMessageIndex)
				if !found {
					return errors.New("invalid responses tool-call reasoning snapshot reference")
				}
				normalSnapshotPointers[logicalKey] = snapshot
			}
		} else if len(entry.ReasoningBlocks) > 0 {
			snapshot = newResponsesHistoryReasoningSnapshot(entry.ReasoningBlocks)
		}
		if snapshot != nil {
			entrySnapshotKeys[key] = registerSnapshot(snapshot, preferredKey)
		}
	}

	writer := bufio.NewWriter(output)
	if _, err := writer.WriteString(`{"version":` + strconv.Itoa(responsesHistoryToolCallRecoveryIndexVersion) + `,"order":`); err != nil {
		return err
	}
	order, err := json.Marshal(s.order)
	if err != nil {
		return err
	}
	if _, err := writer.Write(order); err != nil {
		return err
	}
	if _, err := writer.WriteString(`,"tool_calls":{`); err != nil {
		return err
	}
	for index, key := range keys {
		if index > 0 {
			if err := writer.WriteByte(','); err != nil {
				return err
			}
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return err
		}
		if _, err := writer.Write(encodedKey); err != nil {
			return err
		}
		if err := writer.WriteByte(':'); err != nil {
			return err
		}
		entry := s.toolCalls[key]
		if len(entry.ArgumentsCompressed) > 0 && entry.Call.Arguments == "" {
			arguments, ok := loadResponsesHistoryToolCallArguments(entry)
			if !ok {
				return errors.New("invalid compressed responses tool-call arguments")
			}
			entry.Call.Arguments = arguments
		}
		if snapshotKey, hasSnapshot := entrySnapshotKeys[key]; hasSnapshot {
			entry.ReasoningSnapshotKey = snapshotKey
			entry.ReasoningBlocks = nil
			entry.SharedReasoningSnapshot = nil
			entry.ReasoningBlocksFromSnapshot = false
			entry.SnapshotReasoningMessageIndex = 0
		}
		encodedEntry, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		if _, err := writer.Write(encodedEntry); err != nil {
			return err
		}
	}
	if err := writer.WriteByte('}'); err != nil {
		return err
	}
	if len(persistedSnapshots) > 0 {
		if _, err := writer.WriteString(`,"reasoning_snapshots":{`); err != nil {
			return err
		}
		snapshotKeys := make([]string, 0, len(persistedSnapshots))
		for key := range persistedSnapshots {
			snapshotKeys = append(snapshotKeys, key)
		}
		sort.Strings(snapshotKeys)
		for index, key := range snapshotKeys {
			if index > 0 {
				if err := writer.WriteByte(','); err != nil {
					return err
				}
			}
			encodedKey, err := json.Marshal(key)
			if err != nil {
				return err
			}
			if _, err := writer.Write(encodedKey); err != nil {
				return err
			}
			if err := writer.WriteByte(':'); err != nil {
				return err
			}
			encodedSnapshot, err := json.Marshal(persistedSnapshots[key])
			if err != nil {
				return err
			}
			if _, err := writer.Write(encodedSnapshot); err != nil {
				return err
			}
		}
		if err := writer.WriteByte('}'); err != nil {
			return err
		}
	}
	if err := writer.WriteByte('}'); err != nil {
		return err
	}
	return writer.Flush()
}

func atomicWriteResponsesHistoryFile(path string, write func(io.Writer) error) error {
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := write(file); err != nil {
		return cleanupResponsesHistoryTempFile(file, tmp, err)
	}
	if err := file.Sync(); err != nil {
		return cleanupResponsesHistoryTempFile(file, tmp, err)
	}
	if err := file.Close(); err != nil {
		if removeErr := os.Remove(tmp); removeErr != nil && !os.IsNotExist(removeErr) {
			return errors.Join(err, removeErr)
		}
		return err
	}
	return os.Rename(tmp, path)
}

func cleanupResponsesHistoryTempFile(file *os.File, tmp string, cause error) error {
	if err := file.Close(); err != nil {
		cause = errors.Join(cause, err)
	}
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		cause = errors.Join(cause, err)
	}
	return cause
}
