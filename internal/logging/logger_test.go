package logging_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/logging"
)

func TestLoggerWritesJSONFileByRequestID(t *testing.T) {
	tmpDir := t.TempDir()
	stdout := &bytes.Buffer{}

	logger, closeFn, err := logging.New(config.Config{LogFilePath: tmpDir, LogIncludeBodies: false}, stdout)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	logger.Event("downstream_request_received", map[string]any{
		"request_id":    "req-test-123",
		"authorization": "Bearer secret",
		"api_key":       "proxy-secret",
		"x_api_key":     "proxy-secret-2",
		"body":          "top secret body",
		"body_hash":     "abc123",
		"cached_tokens": 0,
	})

	filePath := filepath.Join(tmpDir, "req-test-123.jsonl")
	content, err := os.ReadFile(filePath)
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
	stdout := &bytes.Buffer{}

	logger, closeFn, err := logging.New(config.Config{LogFilePath: tmpDir, LogIncludeBodies: true}, stdout)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	logger.Event("upstream_request_built", map[string]any{
		"request_id": "req-test-456",
		"body":       "visible body",
		"body_hash":  "def456",
	})

	filePath := filepath.Join(tmpDir, "req-test-456.jsonl")
	content, err := os.ReadFile(filePath)
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

func TestLoggerRespectsMaxHistory(t *testing.T) {
	tmpDir := t.TempDir()
	stdout := &bytes.Buffer{}

	logger, closeFn, err := logging.New(config.Config{
		LogFilePath:      tmpDir,
		LogMaxHistory:    3,
		LogIncludeBodies: false,
	}, stdout)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	for i := 0; i < 5; i++ {
		logger.Event("history_test", map[string]any{
			"request_id": "req-history-" + string(rune('a'+i)),
		})
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > 3 {
		t.Fatalf("expected at most 3 log files, got %d", len(entries))
	}
}

func TestLoggerRequiresRequestID(t *testing.T) {
	tmpDir := t.TempDir()
	stdout := &bytes.Buffer{}

	logger, closeFn, err := logging.New(config.Config{LogFilePath: tmpDir}, stdout)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	logger.Event("no_request_id", map[string]any{"body": "should not be logged"})

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no files when request_id missing, got %d", len(entries))
	}
}

func TestInitDisablesLoggingWhenLogEnableIsFalse(t *testing.T) {
	tmpDir := t.TempDir()
	stdout := &bytes.Buffer{}

	closeFn, err := logging.Init(config.Config{LogEnable: false, LogFilePath: tmpDir}, stdout)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	logging.Event("disabled_test", map[string]any{"request_id": "req-disabled", "body": "hidden"})

	if stdout.Len() != 0 {
		t.Fatalf("expected no stdout output when logging disabled, got %q", stdout.String())
	}
}

func TestLoggerSerializesErrorAttrsAsReadableStrings(t *testing.T) {
	tmpDir := t.TempDir()
	stdout := &bytes.Buffer{}

	logger, closeFn, err := logging.New(config.Config{LogFilePath: tmpDir}, stdout)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	logger.Event("upstream_request_failed", map[string]any{
		"request_id":   "req-error-test",
		"error":        errors.New("first byte timeout"),
		"nested_error": map[string]any{"cause": errors.New("connection reset by peer")},
	})

	filePath := filepath.Join(tmpDir, "req-error-test.jsonl")
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(content), &record); err != nil {
		t.Fatal(err)
	}
	if got := record["error"]; got != "first byte timeout" {
		t.Fatalf("expected top-level error string, got %#v", got)
	}
	nested, _ := record["nested_error"].(map[string]any)
	if got := nested["cause"]; got != "connection reset by peer" {
		t.Fatalf("expected nested error string, got %#v", record["nested_error"])
	}
}
