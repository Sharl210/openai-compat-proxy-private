package logging_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/logging"
)

func TestLoggerWritesTxtFileByRequestID(t *testing.T) {
	tmpDir := t.TempDir()
	stdout := &bytes.Buffer{}

	logger, closeFn, err := logging.New(config.Config{LogFilePath: tmpDir, LogMaxRequests: 50, LogMaxBodySizeMB: 1}, stdout)
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

	filePath := filepath.Join(tmpDir, "req-test-123.txt")
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
	if record["authorization"] != "[REDACTED]" {
		t.Fatalf("expected auth redaction, got %#v", record["authorization"])
	}
	if record["api_key"] != "[REDACTED]" {
		t.Fatalf("expected api_key redaction, got %#v", record["api_key"])
	}
	if record["body"] != "top secret body" {
		t.Fatalf("expected body to be preserved, got %#v", record["body"])
	}
	if !bytes.Contains(stdout.Bytes(), []byte("downstream_request_received")) {
		t.Fatalf("expected stdout summary to mention event, got %s", stdout.String())
	}
}

func TestLoggerTruncatesBody(t *testing.T) {
	tmpDir := t.TempDir()
	stdout := &bytes.Buffer{}

	logger, closeFn, err := logging.New(config.Config{
		LogFilePath:      tmpDir,
		LogMaxRequests:   50,
		LogMaxBodySizeMB: 0.00001,
	}, stdout)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	logger.Event("test", map[string]any{
		"request_id": "req-truncate-test",
		"body":       "this is a very long body that should be truncated",
	})

	filePath := filepath.Join(tmpDir, "req-truncate-test.txt")
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(content), &record); err != nil {
		t.Fatal(err)
	}
	body := record["body"].(string)
	if !strings.HasSuffix(body, "...[TRUNCATED]") {
		t.Fatalf("expected body to be truncated, got %#v", body)
	}
}

func TestLoggerRedactsImageDataURLsFromNestedAttributes(t *testing.T) {
	// Given
	const imageDataSentinel = "TmVzdGVkTG9nSW1hZ2VEYXRhU2VudGluZWw="
	tmpDir := t.TempDir()
	logger, closeFn, err := logging.New(config.Config{LogFilePath: tmpDir, LogMaxRequests: 50, LogMaxBodySizeMB: 1}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}
	defer closeFn()

	// When
	logger.Event("canonical_request", map[string]any{
		"request_id": "req-nested-image",
		"canonical": map[string]any{
			"image_url": "data:image/png;base64," + imageDataSentinel,
		},
	})

	// Then
	data, err := os.ReadFile(filepath.Join(tmpDir, "req-nested-image.txt"))
	if err != nil {
		t.Fatalf("read log record: %v", err)
	}
	if strings.Contains(string(data), imageDataSentinel) || strings.Contains(string(data), "data:image/") {
		t.Fatalf("expected nested image data to be redacted, got %s", data)
	}
	if !strings.Contains(string(data), "image") {
		t.Fatalf("expected nested image placeholder, got %s", data)
	}
}

func TestLoggerRedactsImageDataURLFromBodyField(t *testing.T) {
	// Given
	const imageDataSentinel = "Qm9keUxvZ0ltYWdlRGF0YVNlbnRpbmVs"
	tmpDir := t.TempDir()
	logger, closeFn, err := logging.New(config.Config{LogFilePath: tmpDir, LogMaxRequests: 50, LogMaxBodySizeMB: 1}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}
	defer closeFn()

	// When
	logger.Event("upstream_request", map[string]any{
		"request_id": "req-body-image",
		"body":       `{"image_url":"data:image/png;base64,` + imageDataSentinel + `"}`,
	})

	// Then
	data, err := os.ReadFile(filepath.Join(tmpDir, "req-body-image.txt"))
	if err != nil {
		t.Fatalf("read log record: %v", err)
	}
	if strings.Contains(string(data), imageDataSentinel) || strings.Contains(string(data), "data:image/") {
		t.Fatalf("expected body image data to be redacted, got %s", data)
	}
	if !strings.Contains(string(data), "image") {
		t.Fatalf("expected body image placeholder, got %s", data)
	}
}

func TestLoggerRespectsMaxRequests(t *testing.T) {
	tmpDir := t.TempDir()
	stdout := &bytes.Buffer{}

	logger, closeFn, err := logging.New(config.Config{
		LogFilePath:      tmpDir,
		LogMaxRequests:   3,
		LogMaxBodySizeMB: 1,
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

func TestLoggerRecreatesPrunedRequestLogWhenRequestIDReturns(t *testing.T) {
	// Given
	tmpDir := t.TempDir()
	logger, closeFn, err := logging.New(config.Config{
		LogFilePath:      tmpDir,
		LogMaxRequests:   1,
		LogMaxBodySizeMB: 1,
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}
	defer closeFn()

	logger.Event("first_event", map[string]any{"request_id": "req-recreated"})
	firstLogPath := filepath.Join(tmpDir, "req-recreated.txt")
	olderThanNewerLog := time.Unix(1, 0)
	if err := os.Chtimes(firstLogPath, olderThanNewerLog, olderThanNewerLog); err != nil {
		t.Fatalf("age first request log: %v", err)
	}
	logger.Event("newer_event", map[string]any{"request_id": "req-newer"})

	// When
	logger.Event("recreated_event", map[string]any{"request_id": "req-recreated"})

	// Then
	content, err := os.ReadFile(filepath.Join(tmpDir, "req-recreated.txt"))
	if err != nil {
		t.Fatalf("read recreated request log: %v", err)
	}
	if !strings.Contains(string(content), "recreated_event") {
		t.Fatalf("expected recreated event in log, got %s", content)
	}
}

func TestLoggerRequiresRequestID(t *testing.T) {
	tmpDir := t.TempDir()
	stdout := &bytes.Buffer{}

	logger, closeFn, err := logging.New(config.Config{
		LogFilePath:      tmpDir,
		LogMaxRequests:   50,
		LogMaxBodySizeMB: 1,
	}, stdout)
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

	logger, closeFn, err := logging.New(config.Config{
		LogFilePath:      tmpDir,
		LogMaxRequests:   50,
		LogMaxBodySizeMB: 1,
	}, stdout)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	logger.Event("upstream_request_failed", map[string]any{
		"request_id":   "req-error-test",
		"error":        errors.New("first byte timeout"),
		"nested_error": map[string]any{"cause": errors.New("connection reset by peer")},
	})

	filePath := filepath.Join(tmpDir, "req-error-test.txt")
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(content), &record); err != nil {
		t.Fatal(err)
	}
	if got := record["error"]; got != "first byte timeout" {
		t.Fatalf("expected top-level error String, got %#v", got)
	}
	nested, _ := record["nested_error"].(map[string]any)
	if got := nested["cause"]; got != "connection reset by peer" {
		t.Fatalf("expected nested error String, got %#v", record["nested_error"])
	}
}
