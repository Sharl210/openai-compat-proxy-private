package logging

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"openai-compat-proxy/internal/config"
)

type Logger struct {
	stdout        io.Writer
	dir           string
	includeBodies bool
	maxHistory    int
	mu            sync.Mutex
}

var (
	globalMu sync.RWMutex
	global   *Logger
)

func New(cfg config.Config, stdout io.Writer) (*Logger, func() error, error) {
	if stdout == nil {
		stdout = io.Discard
	}
	dir := strings.TrimSpace(cfg.LogFilePath)
	if dir == "" {
		dir = ".proxy_requests"
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	logger := &Logger{stdout: stdout, dir: dir, includeBodies: cfg.LogIncludeBodies, maxHistory: cfg.LogMaxHistory}
	return logger, func() error { return nil }, nil
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

func (l *Logger) Event(name string, attrs map[string]any) {
	if l == nil {
		return
	}
	record := make(map[string]any, len(attrs)+2)
	record["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	record["event"] = name
	for k, v := range redactAttrs(attrs, l.includeBodies) {
		record[k] = v
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	requestID, ok := attrs["request_id"].(string)
	if !ok || requestID == "" {
		return
	}

	filePath := filepath.Join(l.dir, requestID+".jsonl")
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()

	line, err := json.Marshal(record)
	if err != nil {
		return
	}
	_, _ = file.Write(append(line, '\n'))
	_, _ = fmt.Fprintf(l.stdout, "%s %s\n", name, summarize(record))

	l.cleanupOldFiles()
}

func (l *Logger) cleanupOldFiles() error {
	if l.maxHistory <= 0 {
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
	if len(files) > l.maxHistory {
		for _, info := range files[:len(files)-l.maxHistory] {
			os.Remove(filepath.Join(l.dir, info.Name()))
		}
	}
	return nil
}

func redactAttrs(attrs map[string]any, includeBodies bool) map[string]any {
	clean := make(map[string]any, len(attrs))
	for k, v := range attrs {
		lower := strings.ToLower(k)
		switch {
		case strings.Contains(lower, "authorization"):
			clean[k] = "[REDACTED]"
		case strings.Contains(lower, "api_key") || strings.Contains(lower, "apikey"):
			clean[k] = "[REDACTED]"
		case lower == "body" && !includeBodies:
			clean[k] = "[REDACTED]"
		default:
			clean[k] = normalizeAttrValue(v)
		}
	}
	return clean
}

func normalizeAttrValue(v any) any {
	if v == nil {
		return nil
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
