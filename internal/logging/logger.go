package logging

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"openai-compat-proxy/internal/config"
)

type Logger struct {
	stdout        io.Writer
	file          *os.File
	includeBodies bool
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
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, err
	}
	logger := &Logger{stdout: stdout, file: file, includeBodies: cfg.LogIncludeBodies}
	return logger, file.Close, nil
}

func Init(cfg config.Config, stdout io.Writer) (func() error, error) {
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
	_, _ = l.file.Write(append(line, '\n'))
	_, _ = fmt.Fprintf(l.stdout, "%s %s\n", name, summarize(record))
}

func redactAttrs(attrs map[string]any, includeBodies bool) map[string]any {
	clean := make(map[string]any, len(attrs))
	for k, v := range attrs {
		lower := strings.ToLower(k)
		switch {
		case strings.Contains(lower, "authorization"):
			clean[k] = "[REDACTED]"
		case strings.Contains(lower, "body") && lower == "body" && !includeBodies:
			clean[k] = "[REDACTED]"
		default:
			clean[k] = v
		}
	}
	return clean
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
