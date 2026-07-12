package httpapi

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

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

	keys := make([]string, 0, len(s.toolCalls))
	for key, entry := range s.toolCalls {
		if key != "" && entry.Call.ID != "" && entry.Call.Name != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
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
		encodedEntry, err := json.Marshal(s.toolCalls[key])
		if err != nil {
			return err
		}
		if _, err := writer.Write(encodedEntry); err != nil {
			return err
		}
	}
	if _, err := writer.WriteString(`}}`); err != nil {
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
