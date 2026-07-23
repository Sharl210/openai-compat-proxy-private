package logging_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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

func TestLoggerReusesOpenRequestFileHandle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("renaming an open file is not portable on Windows")
	}

	tmpDir := t.TempDir()
	logger, closeFn, err := logging.New(config.Config{LogFilePath: tmpDir, LogMaxRequests: 50, LogMaxBodySizeMB: 1}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}
	defer closeFn()

	attrs := map[string]any{"request_id": "req-reused-handle"}
	logger.Event("first_event", attrs)
	logPath := filepath.Join(tmpDir, "req-reused-handle.txt")
	renamedPath := filepath.Join(tmpDir, "renamed-request-log.txt")
	if err := os.Rename(logPath, renamedPath); err != nil {
		t.Fatalf("rename active request log: %v", err)
	}

	logger.Event("second_event", attrs)

	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("expected no reopened path while the request handle remains active, stat err=%v", err)
	}
	content, err := os.ReadFile(renamedPath)
	if err != nil {
		t.Fatalf("read renamed active request log: %v", err)
	}
	if !strings.Contains(string(content), `"event":"first_event"`) || !strings.Contains(string(content), `"event":"second_event"`) {
		t.Fatalf("expected both events through the same open handle, got %s", content)
	}
}

func TestLoggerReopensRequestFileAfterCloseRequest(t *testing.T) {
	tmpDir := t.TempDir()
	logger, closeFn, err := logging.New(config.Config{LogFilePath: tmpDir, LogMaxRequests: 50, LogMaxBodySizeMB: 1}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}
	defer closeFn()

	const requestID = "req-reopen-after-close"
	attrs := map[string]any{"request_id": requestID}
	logger.Event("before_close", attrs)
	logger.CloseRequest(requestID)

	logPath := filepath.Join(tmpDir, requestID+".txt")
	closedPath := filepath.Join(tmpDir, "closed-request-log.txt")
	if err := os.Rename(logPath, closedPath); err != nil {
		t.Fatalf("rename closed request log: %v", err)
	}

	logger.Event("after_close", attrs)

	newContent, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read reopened request log: %v", err)
	}
	if !strings.Contains(string(newContent), `"event":"after_close"`) || strings.Contains(string(newContent), `"event":"before_close"`) {
		t.Fatalf("expected only the reopened event in the new request log, got %s", newContent)
	}
	closedContent, err := os.ReadFile(closedPath)
	if err != nil {
		t.Fatalf("read closed request log: %v", err)
	}
	if !strings.Contains(string(closedContent), `"event":"before_close"`) || strings.Contains(string(closedContent), `"event":"after_close"`) {
		t.Fatalf("expected closed request log to remain unchanged, got %s", closedContent)
	}
}

func TestLoggerCloseClosesRemainingRequestHandles(t *testing.T) {
	tmpDir := t.TempDir()
	logger, closeFn, err := logging.New(config.Config{LogFilePath: tmpDir, LogMaxRequests: 50, LogMaxBodySizeMB: 1}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}
	t.Cleanup(func() { _ = closeFn() })

	const requestID = "req-shutdown-close"
	attrs := map[string]any{"request_id": requestID}
	logger.Event("before_shutdown", attrs)
	if err := closeFn(); err != nil {
		t.Fatalf("close logger: %v", err)
	}

	logPath := filepath.Join(tmpDir, requestID+".txt")
	closedPath := filepath.Join(tmpDir, "shutdown-request-log.txt")
	if err := os.Rename(logPath, closedPath); err != nil {
		t.Fatalf("rename shutdown request log: %v", err)
	}
	logger.Event("after_shutdown", attrs)

	newContent, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read post-shutdown request log: %v", err)
	}
	if !strings.Contains(string(newContent), `"event":"after_shutdown"`) || strings.Contains(string(newContent), `"event":"before_shutdown"`) {
		t.Fatalf("expected a reopened log after logger shutdown, got %s", newContent)
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

func TestDownstreamToolEventMatchesGenericLoggerOutput(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		attrs logging.DownstreamToolEventAttrs
	}{
		{
			name: "arguments delta",
			attrs: logging.DownstreamToolEventAttrs{
				RequestID:        "req-tool-delta",
				DownstreamType:   "responses",
				Event:            "response.function_call_arguments.delta",
				ItemID:           "fc_1",
				ArgumentsLen:     42,
				ArgumentsPreview: `{"query":"data:image/png;base64,VG9vbEV2ZW50SW1hZ2U="}`,
			},
		},
		{
			name: "tool item with empty call details",
			attrs: logging.DownstreamToolEventAttrs{
				RequestID:          "req-tool-item",
				DownstreamType:     "chat",
				Event:              "response.output_item.done",
				ItemID:             "fc_2",
				ArgumentsLen:       0,
				ArgumentsPreview:   "",
				IncludeCallDetails: true,
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			genericDir := t.TempDir()
			typedDir := t.TempDir()
			genericStdout := &bytes.Buffer{}
			typedStdout := &bytes.Buffer{}
			genericLogger, genericClose, err := logging.New(config.Config{LogFilePath: genericDir, LogMaxRequests: 50, LogMaxBodySizeMB: 1}, genericStdout)
			if err != nil {
				t.Fatalf("new generic logger: %v", err)
			}
			defer genericClose()
			typedLogger, typedClose, err := logging.New(config.Config{LogFilePath: typedDir, LogMaxRequests: 50, LogMaxBodySizeMB: 1}, typedStdout)
			if err != nil {
				t.Fatalf("new typed logger: %v", err)
			}
			defer typedClose()

			genericAttrs := map[string]any{
				"request_id":        testCase.attrs.RequestID,
				"downstream_type":   testCase.attrs.DownstreamType,
				"event":             testCase.attrs.Event,
				"item_id":           testCase.attrs.ItemID,
				"arguments_len":     testCase.attrs.ArgumentsLen,
				"arguments_preview": testCase.attrs.ArgumentsPreview,
			}
			if testCase.attrs.IncludeCallDetails {
				genericAttrs["call_id"] = testCase.attrs.CallID
				genericAttrs["name"] = testCase.attrs.ToolName
			}
			genericLogger.Event("downstreamToolEvent", genericAttrs)
			typedLogger.DownstreamToolEvent(testCase.attrs)

			genericRecord := readLogRecord(t, genericDir, testCase.attrs.RequestID)
			typedRecord := readLogRecord(t, typedDir, testCase.attrs.RequestID)
			delete(genericRecord, "ts")
			delete(typedRecord, "ts")
			if !reflect.DeepEqual(genericRecord, typedRecord) {
				t.Fatalf("typed record differs from generic record\ngeneric: %#v\ntyped: %#v", genericRecord, typedRecord)
			}
			if genericStdout.String() != typedStdout.String() {
				t.Fatalf("typed stdout differs from generic stdout\ngeneric: %q\ntyped: %q", genericStdout.String(), typedStdout.String())
			}
			if strings.Contains(typedStdout.String(), "VG9vbEV2ZW50SW1hZ2U=") {
				t.Fatalf("typed stdout leaked image data: %q", typedStdout.String())
			}
		})
	}
}

func readLogRecord(t *testing.T, dir, requestID string) map[string]any {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dir, requestID+".txt"))
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(content), &record); err != nil {
		t.Fatalf("unmarshal request log: %v", err)
	}
	return record
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
