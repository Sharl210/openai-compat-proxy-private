package logging_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/logging"
)

func TestLoggerWritesJSONFileAndRedactsBodiesByDefault(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "proxy.jsonl")
	stdout := &bytes.Buffer{}

	logger, closeFn, err := logging.New(config.Config{LogFilePath: logPath, LogIncludeBodies: false}, stdout)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	logger.Event("downstream_request_received", map[string]any{
		"authorization": "Bearer secret",
		"api_key":       "proxy-secret",
		"x_api_key":     "proxy-secret-2",
		"body":          "top secret body",
		"body_hash":     "abc123",
		"cached_tokens": 0,
	})

	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(content), &record); err != nil {
		t.Fatal(err)
	}
	if record["event"] != "downstream_request_received" {
		t.Fatalf("unexpected event: %#v", record)
	}
	if record["body"] != "[REDACTED]" {
		t.Fatalf("expected body redaction, got %#v", record["body"])
	}
	if record["authorization"] != "[REDACTED]" {
		t.Fatalf("expected auth redaction, got %#v", record["authorization"])
	}
	if record["api_key"] != "[REDACTED]" {
		t.Fatalf("expected api_key redaction, got %#v", record["api_key"])
	}
	if record["x_api_key"] != "[REDACTED]" {
		t.Fatalf("expected x_api_key redaction, got %#v", record["x_api_key"])
	}
	if record["body_hash"] != "abc123" {
		t.Fatalf("expected body hash to remain, got %#v", record["body_hash"])
	}
	if !bytes.Contains(stdout.Bytes(), []byte("downstream_request_received")) {
		t.Fatalf("expected stdout summary to mention event, got %s", stdout.String())
	}
}

func TestLoggerCanIncludeBodiesWhenEnabled(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "proxy.jsonl")
	stdout := &bytes.Buffer{}

	logger, closeFn, err := logging.New(config.Config{LogFilePath: logPath, LogIncludeBodies: true}, stdout)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	logger.Event("upstream_request_built", map[string]any{
		"body":      "visible body",
		"body_hash": "def456",
	})

	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(content), &record); err != nil {
		t.Fatal(err)
	}
	if record["body"] != "visible body" {
		t.Fatalf("expected body to be preserved, got %#v", record["body"])
	}
	if record["body_hash"] != "def456" {
		t.Fatalf("expected body hash to remain, got %#v", record["body_hash"])
	}
}

func TestLoggerRotatesAndKeepsRecentBackups(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "proxy.jsonl")
	stdout := &bytes.Buffer{}

	logger, closeFn, err := logging.New(config.Config{
		LogFilePath:      logPath,
		LogMaxSizeMB:     1,
		LogMaxBackups:    2,
		LogIncludeBodies: false,
	}, stdout)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	largeBody := strings.Repeat("x", 600*1024)
	for i := 0; i < 4; i++ {
		logger.Event("rotation_test", map[string]any{
			"body":      largeBody,
			"body_hash": i,
		})
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	var rotated int
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "proxy-") && strings.HasSuffix(name, ".jsonl") {
			rotated++
		}
	}
	if rotated > 2 {
		t.Fatalf("expected at most 2 rotated backups, got %d", rotated)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("expected active log file to exist: %v", err)
	}
}

func TestInitDisablesLoggingWhenLogEnableIsFalse(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "proxy.jsonl")
	stdout := &bytes.Buffer{}

	closeFn, err := logging.Init(config.Config{LogEnable: false, LogFilePath: logPath}, stdout)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	logging.Event("disabled_test", map[string]any{"body": "hidden"})

	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("expected no log file when logging disabled, got err=%v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected no stdout output when logging disabled, got %q", stdout.String())
	}
}
