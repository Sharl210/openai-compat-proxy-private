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
	file          *os.File
	path          string
	includeBodies bool
	maxSizeBytes  int64
	maxBackups    int
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
	path := strings.TrimSpace(cfg.LogFilePath)
	if path == "" {
		path = ".proxy.requests.jsonl"
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	logger := &Logger{stdout: stdout, file: file, path: path, includeBodies: cfg.LogIncludeBodies, maxSizeBytes: int64(cfg.LogMaxSizeMB) * 1024 * 1024, maxBackups: cfg.LogMaxBackups}
	return logger, file.Close, nil
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
	return func() error {
		globalMu.Lock()
		if global == logger {
			global = nil
		}
		globalMu.Unlock()
		return closeFn()
	}, nil
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
	line, err := json.Marshal(record)
	if err != nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.rotateIfNeeded(int64(len(line) + 1))
	_, _ = l.file.Write(append(line, '\n'))
	_, _ = fmt.Fprintf(l.stdout, "%s %s\n", name, summarize(record))
}

func (l *Logger) rotateIfNeeded(nextWrite int64) error {
	if l == nil || l.file == nil || l.maxSizeBytes <= 0 {
		return nil
	}
	info, err := l.file.Stat()
	if err != nil {
		return err
	}
	if info.Size()+nextWrite <= l.maxSizeBytes {
		return nil
	}
	current := l.file
	rotatedPath := rotatedName(l.path)
	if err := os.Rename(l.path, rotatedPath); err != nil {
		return err
	}
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		l.file = current
		return err
	}
	l.file = file
	if err := current.Close(); err != nil {
		return err
	}
	return l.cleanupOldBackups()
}

func rotatedName(path string) string {
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	return fmt.Sprintf("%s-%s%s", base, time.Now().UTC().Format("20060102-150405.000000000"), ext)
}

func (l *Logger) cleanupOldBackups() error {
	if l.maxBackups < 0 {
		return nil
	}
	dir := filepath.Dir(l.path)
	base := filepath.Base(l.path)
	ext := filepath.Ext(base)
	prefix := strings.TrimSuffix(base, ext) + "-"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var backups []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ext) {
			backups = append(backups, filepath.Join(dir, name))
		}
	}
	sort.Strings(backups)
	for len(backups) > l.maxBackups {
		if err := os.Remove(backups[0]); err != nil && !os.IsNotExist(err) {
			return err
		}
		backups = backups[1:]
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
