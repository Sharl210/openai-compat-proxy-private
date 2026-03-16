package logging_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
