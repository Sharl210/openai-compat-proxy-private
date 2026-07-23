package logging

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"openai-compat-proxy/internal/config"
)

const loggerCleanupInterval = time.Minute

type Logger struct {
	stdout        io.Writer
	dir           string
	maxRequests   int
	maxBodySize   int
	lastCleanupAt time.Time
	requestFiles  map[string]*os.File
	mu            sync.Mutex
}

type DownstreamToolEventAttrs struct {
	RequestID          string
	DownstreamType     string
	Event              string
	ItemID             string
	CallID             string
	ToolName           string
	ArgumentsLen       int
	ArgumentsPreview   string
	IncludeCallDetails bool
}

type downstreamToolEventRecord struct {
	Timestamp        string `json:"ts"`
	Event            string `json:"event"`
	RequestID        string `json:"request_id"`
	DownstreamType   string `json:"downstream_type"`
	ItemID           string `json:"item_id"`
	ArgumentsLen     int    `json:"arguments_len"`
	ArgumentsPreview string `json:"arguments_preview"`
}

type downstreamToolItemEventRecord struct {
	Timestamp        string `json:"ts"`
	Event            string `json:"event"`
	RequestID        string `json:"request_id"`
	DownstreamType   string `json:"downstream_type"`
	ItemID           string `json:"item_id"`
	CallID           string `json:"call_id"`
	ToolName         string `json:"name"`
	ArgumentsLen     int    `json:"arguments_len"`
	ArgumentsPreview string `json:"arguments_preview"`
}

var (
	globalMu sync.RWMutex
	global   *Logger
)

func New(cfg config.Config, stdout io.Writer) (*Logger, func() error, error) {
	if stdout == nil {
		stdout = io.Discard
	}
	dir := cfg.LogFilePath
	if dir == "" {
		dir = "logs"
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	logger := &Logger{
		stdout:       stdout,
		dir:          dir,
		maxRequests:  cfg.LogMaxRequests,
		maxBodySize:  int(cfg.LogMaxBodySizeMB * 1024 * 1024),
		requestFiles: make(map[string]*os.File),
	}
	return logger, logger.Close, nil
}

func Init(cfg config.Config, stdout io.Writer) (func() error, error) {
	if !cfg.LogEnable {
		globalMu.Lock()
		global = nil
		globalMu.Unlock()
		return func() error { return nil }, nil
	}
	logger, closeFn, err := New(cfg, stdout)
	if err != nil {
		return nil, err
	}
	globalMu.Lock()
	global = logger
	globalMu.Unlock()
	return closeFn, nil
}

func Default() *Logger {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return global
}

func Event(name string, attrs map[string]any) {
	if logger := Default(); logger != nil {
		logger.Event(name, attrs)
	}
}

func DownstreamToolEvent(attrs DownstreamToolEventAttrs) {
	if logger := Default(); logger != nil {
		logger.DownstreamToolEvent(attrs)
	}
}

func CloseRequest(requestID string) {
	if logger := Default(); logger != nil {
		logger.CloseRequest(requestID)
	}
}

func (l *Logger) Event(name string, attrs map[string]any) {
	if l == nil {
		return
	}
	record := make(map[string]any, len(attrs)+2)
	record["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	record["event"] = name
	for k, v := range redactAttrs(attrs, l.maxBodySize) {
		record[k] = v
	}

	requestID, ok := attrs["request_id"].(string)
	if !ok || requestID == "" {
		return
	}

	line, err := json.Marshal(record)
	if err != nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	file, created := l.getFileLocked(requestID)
	if file == nil {
		return
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		_ = l.closeRequestLocked(requestID)
	}
	_, _ = fmt.Fprintf(l.stdout, "%s %s\n", name, summarize(record))

	l.maybeCleanup(created, requestID)
}

func (l *Logger) DownstreamToolEvent(attrs DownstreamToolEventAttrs) {
	if l == nil || attrs.RequestID == "" {
		return
	}
	attrs = redactDownstreamToolEventAttrs(attrs)
	line, err := marshalDownstreamToolEvent(attrs)
	if err != nil {
		return
	}
	summary := summarizeDownstreamToolEvent(attrs)

	l.mu.Lock()
	defer l.mu.Unlock()

	file, created := l.getFileLocked(attrs.RequestID)
	if file == nil {
		return
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		_ = l.closeRequestLocked(attrs.RequestID)
	}
	_, _ = io.WriteString(l.stdout, "downstreamToolEvent "+summary+"\n")

	l.maybeCleanup(created, attrs.RequestID)
}

func (l *Logger) CloseRequest(requestID string) {
	if l == nil || requestID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.closeRequestLocked(requestID)
}

func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	var closeErr error
	for requestID := range l.requestFiles {
		if err := l.closeRequestLocked(requestID); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

func (l *Logger) getFileLocked(requestID string) (*os.File, bool) {
	if file := l.requestFiles[requestID]; file != nil {
		return file, false
	}
	filePath := filepath.Join(l.dir, requestID+".txt")
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_EXCL|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		if l.requestFiles == nil {
			l.requestFiles = make(map[string]*os.File)
		}
		l.requestFiles[requestID] = file
		return file, true
	}
	if !os.IsExist(err) {
		return nil, false
	}
	file, err = os.OpenFile(filePath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, false
	}
	if l.requestFiles == nil {
		l.requestFiles = make(map[string]*os.File)
	}
	l.requestFiles[requestID] = file
	return file, false
}

func (l *Logger) closeRequestLocked(requestID string) error {
	file, ok := l.requestFiles[requestID]
	if !ok {
		return nil
	}
	delete(l.requestFiles, requestID)
	return file.Close()
}

func redactDownstreamToolEventAttrs(attrs DownstreamToolEventAttrs) DownstreamToolEventAttrs {
	attrs.RequestID = redactImageDataURLsInString(attrs.RequestID)
	attrs.DownstreamType = redactImageDataURLsInString(attrs.DownstreamType)
	attrs.Event = redactImageDataURLsInString(attrs.Event)
	attrs.ItemID = redactImageDataURLsInString(attrs.ItemID)
	attrs.CallID = redactImageDataURLsInString(attrs.CallID)
	attrs.ToolName = redactImageDataURLsInString(attrs.ToolName)
	attrs.ArgumentsPreview = redactImageDataURLsInString(attrs.ArgumentsPreview)
	return attrs
}

func marshalDownstreamToolEvent(attrs DownstreamToolEventAttrs) ([]byte, error) {
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	if attrs.IncludeCallDetails {
		return json.Marshal(downstreamToolItemEventRecord{
			Timestamp:        timestamp,
			Event:            attrs.Event,
			RequestID:        attrs.RequestID,
			DownstreamType:   attrs.DownstreamType,
			ItemID:           attrs.ItemID,
			CallID:           attrs.CallID,
			ToolName:         attrs.ToolName,
			ArgumentsLen:     attrs.ArgumentsLen,
			ArgumentsPreview: attrs.ArgumentsPreview,
		})
	}
	return json.Marshal(downstreamToolEventRecord{
		Timestamp:        timestamp,
		Event:            attrs.Event,
		RequestID:        attrs.RequestID,
		DownstreamType:   attrs.DownstreamType,
		ItemID:           attrs.ItemID,
		ArgumentsLen:     attrs.ArgumentsLen,
		ArgumentsPreview: attrs.ArgumentsPreview,
	})
}

func summarizeDownstreamToolEvent(attrs DownstreamToolEventAttrs) string {
	var summary strings.Builder
	summary.Grow(len(attrs.ArgumentsPreview) + len(attrs.CallID) + len(attrs.DownstreamType) + len(attrs.ItemID) + len(attrs.ToolName) + len(attrs.RequestID) + 96)
	summary.WriteString("arguments_len=")
	summary.WriteString(strconv.Itoa(attrs.ArgumentsLen))
	summary.WriteString(" arguments_preview=")
	summary.WriteString(attrs.ArgumentsPreview)
	if attrs.IncludeCallDetails {
		summary.WriteString(" call_id=")
		summary.WriteString(attrs.CallID)
	}
	summary.WriteString(" downstream_type=")
	summary.WriteString(attrs.DownstreamType)
	summary.WriteString(" item_id=")
	summary.WriteString(attrs.ItemID)
	if attrs.IncludeCallDetails {
		summary.WriteString(" name=")
		summary.WriteString(attrs.ToolName)
	}
	summary.WriteString(" request_id=")
	summary.WriteString(attrs.RequestID)
	return summary.String()
}

func (l *Logger) maybeCleanup(created bool, currentRequestID string) {
	if l.maxRequests <= 0 {
		return
	}
	now := time.Now()
	if !created && !l.lastCleanupAt.IsZero() && now.Sub(l.lastCleanupAt) < loggerCleanupInterval {
		return
	}
	if err := l.cleanupOldFiles(currentRequestID); err == nil {
		l.lastCleanupAt = now
	}
}

func (l *Logger) cleanupOldFiles(currentRequestID string) error {
	if l.maxRequests <= 0 {
		return nil
	}
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return err
	}
	var files []os.FileInfo
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if info, err := entry.Info(); err == nil {
			files = append(files, info)
		}
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].ModTime().Before(files[j].ModTime())
	})
	if len(files) > l.maxRequests {
		remaining := len(files) - l.maxRequests
		currentPath := filepath.Clean(filepath.Join(l.dir, currentRequestID+".txt"))
		for _, info := range files {
			if remaining == 0 {
				break
			}
			filePath := filepath.Join(l.dir, info.Name())
			if filepath.Clean(filePath) == currentPath {
				continue
			}
			if err := l.closeOpenFileForPathLocked(filePath, currentRequestID); err != nil {
				return err
			}
			_ = os.Remove(filePath)
			remaining--
		}
	}
	return nil
}

func (l *Logger) closeOpenFileForPathLocked(filePath, currentRequestID string) error {
	targetPath := filepath.Clean(filePath)
	for requestID, file := range l.requestFiles {
		if requestID == currentRequestID || filepath.Clean(file.Name()) != targetPath {
			continue
		}
		delete(l.requestFiles, requestID)
		return file.Close()
	}
	return nil
}

func redactAttrs(attrs map[string]any, maxBodySize int) map[string]any {
	clean := make(map[string]any, len(attrs))
	for k, v := range attrs {
		lower := strings.ToLower(k)
		switch {
		case strings.Contains(lower, "authorization"):
			clean[k] = "[REDACTED]"
		case strings.Contains(lower, "api_key") || strings.Contains(lower, "apikey"):
			clean[k] = "[REDACTED]"
		case lower == "body":
			clean[k] = truncateBody(normalizeAttrValue(v), maxBodySize)
		default:
			clean[k] = normalizeAttrValue(v)
		}
	}
	return clean
}

func truncateBody(v any, maxSize int) any {
	if maxSize <= 0 {
		return v
	}
	str, ok := v.(string)
	if !ok {
		return v
	}
	runes := []rune(str)
	if len(runes) > maxSize {
		return string(runes[:maxSize]) + "...[TRUNCATED]"
	}
	return str
}

func normalizeAttrValue(v any) any {
	if v == nil {
		return nil
	}
	if text, ok := v.(string); ok {
		return redactImageDataURLsInString(text)
	}
	if err, ok := v.(error); ok {
		return err.Error()
	}
	switch typed := v.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, value := range typed {
			normalized[key] = normalizeAttrValue(value)
		}
		return normalized
	case []any:
		normalized := make([]any, 0, len(typed))
		for _, value := range typed {
			normalized = append(normalized, normalizeAttrValue(value))
		}
		return normalized
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		normalized := make([]any, 0, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			normalized = append(normalized, normalizeAttrValue(rv.Index(i).Interface()))
		}
		return normalized
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return v
		}
		normalized := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			normalized[iter.Key().String()] = normalizeAttrValue(iter.Value().Interface())
		}
		return normalized
	default:
		return v
	}
}

func summarize(record map[string]any) string {
	keys := make([]string, 0, len(record))
	for k := range record {
		if k == "ts" || k == "event" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", key, record[key]))
	}
	return strings.Join(parts, " ")
}
