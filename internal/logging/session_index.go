package logging

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	sessionRequestIndexDirectory  = "session-index"
	sessionRequestIndexFile       = "index.ndjson"
	sessionRequestStateFile       = "state.json"
	sessionIndexCacheLimit        = 512
	sessionIndexReservationLimit  = 256
	defaultSessionIndexMaxRecords = 200
)

// SessionRequestRecord 只保存请求标识和生命周期元数据，不携带正文、凭据或媒体。
type SessionRequestRecord struct {
	Event                  string `json:"event"`
	SessionID              string `json:"session_id"`
	ConversationRequestSeq uint64 `json:"conversation_request_seq"`
	ConversationRequestID  string `json:"conversation_request_id"`
	RequestUID             string `json:"request_uid"`
	XRequestID             string `json:"x_request_id"`
	Timestamp              string `json:"timestamp"`
	Status                 int    `json:"status,omitempty"`
	Route                  string `json:"route,omitempty"`
	SessionConflict        bool   `json:"session_conflict,omitempty"`
	SessionConflictWith    string `json:"session_conflict_with,omitempty"`
	LookupStatus           string `json:"lookup_status,omitempty"`
	LookupError            string `json:"lookup_error,omitempty"`
}

type sessionRequestState struct {
	SessionID string `json:"session_id"`
	NextSeq   uint64 `json:"next_sequence"`
}

type sessionIndexCacheEntry struct {
	next         uint64
	reservations map[string]uint64
}

// SessionRequestIndex 按会话持久化直接索引，不在内存中保留请求内容。
type SessionRequestIndex struct {
	root       string
	mu         sync.Mutex
	cache      map[string]*sessionIndexCacheEntry
	maxRecords int
}

func NewSessionRequestIndex(root string, maxRecords ...int) *SessionRequestIndex {
	limit := defaultSessionIndexMaxRecords
	if len(maxRecords) > 0 && maxRecords[0] > 0 {
		limit = maxRecords[0]
	}
	return &SessionRequestIndex{
		root:       strings.TrimSpace(root),
		cache:      make(map[string]*sessionIndexCacheEntry),
		maxRecords: limit,
	}
}

// SessionRequestIndexPath 返回会话对应的确定性目录；原始 session ID 不会作为路径组件。
func SessionRequestIndexPath(root, sessionID string) string {
	root = strings.TrimSpace(root)
	sessionID = strings.TrimSpace(sessionID)
	if root == "" || sessionID == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(sessionID))
	return filepath.Join(root, sessionRequestIndexDirectory, hex.EncodeToString(hash[:]))
}

func (i *SessionRequestIndex) Reserve(sessionID, requestUID, xRequestID string) (uint64, error) {
	if i == nil {
		return 0, errors.New("session request index is nil")
	}
	sessionID = strings.TrimSpace(sessionID)
	requestUID = strings.TrimSpace(requestUID)
	if sessionID == "" {
		return 0, errors.New("session ID is empty")
	}
	if requestUID == "" {
		return 0, errors.New("request UID is empty")
	}
	if strings.TrimSpace(xRequestID) == "" {
		xRequestID = requestUID
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	if i.cache == nil {
		i.cache = make(map[string]*sessionIndexCacheEntry)
	}

	directory := SessionRequestIndexPath(i.root, sessionID)
	if directory == "" {
		return 0, errors.New("session request index root is empty")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return 0, fmt.Errorf("create session request index directory: %w", err)
	}
	entry, err := i.cacheEntryLocked(directory, sessionID)
	if err != nil {
		return 0, err
	}
	if existing, ok := entry.reservations[requestUID]; ok {
		return existing, nil
	}
	entry.next++
	sequence := entry.next
	if len(entry.reservations) >= sessionIndexReservationLimit {
		for existingUID := range entry.reservations {
			delete(entry.reservations, existingUID)
			break
		}
	}
	entry.reservations[requestUID] = sequence
	if err := writeSessionRequestStateLocked(directory, sessionID, sequence); err != nil {
		return 0, err
	}
	if err := appendSessionRequestRecordLocked(directory, i.maxRecords, SessionRequestRecord{
		Event:                  "allocated",
		SessionID:              sessionID,
		ConversationRequestSeq: sequence,
		ConversationRequestID:  fmt.Sprintf("r%06d", sequence),
		RequestUID:             requestUID,
		XRequestID:             xRequestID,
		Timestamp:              time.Now().UTC().Format(time.RFC3339Nano),
		LookupStatus:           "persisted",
	}); err != nil {
		return 0, err
	}
	return sequence, nil
}

func (i *SessionRequestIndex) Append(record SessionRequestRecord) error {
	if i == nil {
		return errors.New("session request index is nil")
	}
	record.SessionID = strings.TrimSpace(record.SessionID)
	record.RequestUID = strings.TrimSpace(record.RequestUID)
	if record.SessionID == "" {
		return errors.New("session ID is empty")
	}
	if record.RequestUID == "" {
		return errors.New("request UID is empty")
	}
	if record.ConversationRequestSeq == 0 {
		return errors.New("conversation request sequence is zero")
	}
	if strings.TrimSpace(record.ConversationRequestID) == "" {
		record.ConversationRequestID = fmt.Sprintf("r%06d", record.ConversationRequestSeq)
	}
	if strings.TrimSpace(record.XRequestID) == "" {
		record.XRequestID = record.RequestUID
	}
	if strings.TrimSpace(record.Event) == "" {
		record.Event = "update"
	}
	if strings.TrimSpace(record.Timestamp) == "" {
		record.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	directory := SessionRequestIndexPath(i.root, record.SessionID)
	if directory == "" {
		return errors.New("session request index root is empty")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create session request index directory: %w", err)
	}
	return appendSessionRequestRecordLocked(directory, i.maxRecords, record)
}

// Lookup 只读取目标会话对应的确定性索引，不扫描请求归档。
func (i *SessionRequestIndex) Lookup(sessionID string) ([]SessionRequestRecord, error) {
	if i == nil {
		return nil, errors.New("session request index is nil")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session ID is empty")
	}
	directory := SessionRequestIndexPath(i.root, sessionID)
	if directory == "" {
		return nil, errors.New("session request index root is empty")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	file, err := os.Open(filepath.Join(directory, sessionRequestIndexFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	records := make([]SessionRequestRecord, 0)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	for scanner.Scan() {
		var record SessionRequestRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, fmt.Errorf("decode session request index record: %w", err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sortSessionRequestRecords(records)
	return records, nil
}

func (i *SessionRequestIndex) loadNextSequenceLocked(directory, sessionID string) (uint64, error) {
	statePath := filepath.Join(directory, sessionRequestStateFile)
	var next uint64
	stateValid := false
	stateData, stateErr := os.ReadFile(statePath)
	if stateErr == nil {
		var state sessionRequestState
		if err := json.Unmarshal(stateData, &state); err == nil && state.NextSeq > 0 {
			if state.SessionID != "" && state.SessionID != sessionID {
				return 0, fmt.Errorf("session request index state belongs to %q", state.SessionID)
			}
			next = state.NextSeq
			stateValid = true
		}
	} else if !os.IsNotExist(stateErr) {
		return 0, fmt.Errorf("read session request index state: %w", stateErr)
	}
	if stateValid {
		return next, nil
	}

	indexed, err := maxIndexedSequence(filepath.Join(directory, sessionRequestIndexFile))
	if err != nil {
		return 0, err
	}
	if indexed > next {
		next = indexed
	}
	return next, nil
}

func (i *SessionRequestIndex) cacheEntryLocked(directory, sessionID string) (*sessionIndexCacheEntry, error) {
	if entry := i.cache[sessionID]; entry != nil {
		return entry, nil
	}
	if len(i.cache) >= sessionIndexCacheLimit {
		for existingSessionID := range i.cache {
			delete(i.cache, existingSessionID)
			break
		}
	}
	next, err := i.loadNextSequenceLocked(directory, sessionID)
	if err != nil {
		return nil, err
	}
	entry := &sessionIndexCacheEntry{
		next:         next,
		reservations: make(map[string]uint64),
	}
	i.cache[sessionID] = entry
	return entry, nil
}

func maxIndexedSequence(path string) (uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read session request index: %w", err)
	}
	defer file.Close()

	var max uint64
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	for scanner.Scan() {
		var record SessionRequestRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return 0, fmt.Errorf("decode session request index: %w", err)
		}
		if record.ConversationRequestSeq > max {
			max = record.ConversationRequestSeq
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return max, nil
}

func writeSessionRequestStateLocked(directory, sessionID string, next uint64) error {
	temporary, err := os.CreateTemp(directory, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create session request index state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set session request index state permissions: %w", err)
	}
	data, err := json.Marshal(sessionRequestState{SessionID: sessionID, NextSeq: next})
	if err != nil {
		_ = temporary.Close()
		return fmt.Errorf("encode session request index state: %w", err)
	}
	if _, err := writeBytes(temporary, append(data, '\n')); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write session request index state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync session request index state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close session request index state: %w", err)
	}
	if err := os.Rename(temporaryPath, filepath.Join(directory, sessionRequestStateFile)); err != nil {
		return fmt.Errorf("replace session request index state: %w", err)
	}
	return nil
}

func appendSessionRequestRecordLocked(directory string, maxRecords int, record SessionRequestRecord) error {
	path := filepath.Join(directory, sessionRequestIndexFile)
	records, err := readSessionRequestRecords(path)
	if err != nil {
		return err
	}
	records = append(records, record)
	sortSessionRequestRecords(records)
	if maxRecords <= 0 {
		maxRecords = defaultSessionIndexMaxRecords
	}
	if len(records) > maxRecords {
		records = records[:maxRecords]
	}
	data := make([]byte, 0, len(records)*256)
	for _, existing := range records {
		line, marshalErr := json.Marshal(existing)
		if marshalErr != nil {
			return fmt.Errorf("encode session request index record: %w", marshalErr)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	temporary, err := os.CreateTemp(directory, ".index-*.tmp")
	if err != nil {
		return fmt.Errorf("create session request index: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set session request index permissions: %w", err)
	}
	if _, err := writeBytes(temporary, data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write session request index: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync session request index: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close session request index: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace session request index: %w", err)
	}
	return nil
}

func readSessionRequestRecords(path string) ([]SessionRequestRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read session request index: %w", err)
	}
	records := make([]SessionRequestRecord, 0)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var record SessionRequestRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, fmt.Errorf("decode session request index record: %w", err)
		}
		records = append(records, record)
	}
	return records, nil
}

func sortSessionRequestRecords(records []SessionRequestRecord) {
	sort.SliceStable(records, func(left, right int) bool {
		leftTime, leftErr := time.Parse(time.RFC3339Nano, records[left].Timestamp)
		rightTime, rightErr := time.Parse(time.RFC3339Nano, records[right].Timestamp)
		if leftErr == nil && rightErr == nil && !leftTime.Equal(rightTime) {
			return leftTime.After(rightTime)
		}
		return records[left].ConversationRequestSeq > records[right].ConversationRequestSeq
	})
}

func writeBytes(writer io.Writer, data []byte) (int, error) {
	total := 0
	for total < len(data) {
		written, err := writer.Write(data[total:])
		total += written
		if err != nil {
			return total, err
		}
		if written == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}
