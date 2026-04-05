package debugarchive

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"openai-compat-proxy/internal/model"
)

func TestArchiveWriter_CreatesPerRequestDirectory(t *testing.T) {
	tmp := t.TempDir()
	writer := NewArchiveWriter(tmp, "req-123")

	if writer == nil {
		t.Fatal("expected non-nil ArchiveWriter")
	}

	expectedDir := filepath.Join(tmp, "req-123")
	if _, err := os.Stat(expectedDir); os.IsNotExist(err) {
		t.Errorf("expected archive directory %s to exist after NewArchiveWriter", expectedDir)
	}
}

func TestArchiveWriter_RequestDirectoryStructure(t *testing.T) {
	tmp := t.TempDir()
	requestID := "req-test-dir-abc"
	writer := NewArchiveWriter(tmp, requestID)
	_ = writer

	expectedBase := filepath.Join(tmp, requestID)

	writer.Close()

	for _, filename := range []string{"request.ndjson", "raw.ndjson", "canonical.ndjson", "final.ndjson"} {
		path := filepath.Join(expectedBase, filename)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("expected file %s to exist after Close, got error: %v", path, err)
			continue
		}
		if info.IsDir() {
			t.Errorf("expected %s to be a file, got directory", path)
		}
	}
}

func TestArchiveWriter_WriteRequest_SingleLineNDJSON(t *testing.T) {
	tmp := t.TempDir()
	writer := NewArchiveWriter(tmp, "req-ndjson-req")
	_ = writer

	payload := map[string]any{
		"model": "gpt-4o",
		"input": []any{"hello"},
	}
	if err := writer.WriteRequest(payload); err != nil {
		t.Fatalf("WriteRequest failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmp, "req-ndjson-req", "request.ndjson"))
	if err != nil {
		t.Fatalf("failed to read request.ndjson: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Errorf("request.ndjson is not valid NDJSON: %v\ncontent: %s", err, string(data))
	}
	if decoded["model"] != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got %v", decoded["model"])
	}
}

func TestArchiveWriter_WriteRequest_AppendMultipleRequests(t *testing.T) {
	tmp := t.TempDir()
	writer := NewArchiveWriter(tmp, "req-multi-req")
	_ = writer

	payload1 := map[string]any{"model": "gpt-4o"}
	payload2 := map[string]any{"model": "gpt-4o-mini"}

	if err := writer.WriteRequest(payload1); err != nil {
		t.Fatalf("WriteRequest(1) failed: %v", err)
	}
	if err := writer.WriteRequest(payload2); err != nil {
		t.Fatalf("WriteRequest(2) failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmp, "req-multi-req", "request.ndjson"))
	if err != nil {
		t.Fatalf("failed to read request.ndjson: %v", err)
	}

	lines := splitLines(string(data))
	if len(lines) != 2 {
		t.Errorf("expected 2 NDJSON lines, got %d", len(lines))
	}
}

func TestArchiveWriter_WriteRawEvent_SingleLineNDJSON(t *testing.T) {
	tmp := t.TempDir()
	writer := NewArchiveWriter(tmp, "req-raw-event")
	_ = writer

	event := RawEventEnvelope{
		EventName: "response.output_text.delta",
		Raw:       json.RawMessage(`{"delta":"hello"}`),
	}
	if err := writer.WriteRawEvent(event); err != nil {
		t.Fatalf("WriteRawEvent failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmp, "req-raw-event", "raw.ndjson"))
	if err != nil {
		t.Fatalf("failed to read raw.ndjson: %v", err)
	}

	var decoded RawEventEnvelope
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Errorf("raw.ndjson is not valid NDJSON: %v\ncontent: %s", err, string(data))
	}
	if decoded.EventName != "response.output_text.delta" {
		t.Errorf("expected EventName 'response.output_text.delta', got %q", decoded.EventName)
	}
}

func TestArchiveWriter_WriteRawEvent_MultipleLinesNDJSON(t *testing.T) {
	tmp := t.TempDir()
	writer := NewArchiveWriter(tmp, "req-raw-multi")
	_ = writer

	events := []RawEventEnvelope{
		{EventName: "response.created", Raw: json.RawMessage(`{}`)},
		{EventName: "response.output_text.delta", Raw: json.RawMessage(`{"delta":"a"}`)},
		{EventName: "response.output_text.delta", Raw: json.RawMessage(`{"delta":"b"}`)},
	}
	for _, e := range events {
		if err := writer.WriteRawEvent(e); err != nil {
			t.Fatalf("WriteRawEvent failed: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmp, "req-raw-multi", "raw.ndjson"))
	if err != nil {
		t.Fatalf("failed to read raw.ndjson: %v", err)
	}

	lines := splitLines(string(data))
	if len(lines) != 3 {
		t.Errorf("expected 3 NDJSON lines, got %d", len(lines))
	}
}

func TestArchiveWriter_WriteCanonicalEvent_SingleLineNDJSON(t *testing.T) {
	tmp := t.TempDir()
	writer := NewArchiveWriter(tmp, "req-canon")
	_ = writer

	evt := model.CanonicalEvent{
		Seq:       1,
		Type:      "message.delta",
		ItemID:    "msg-1",
		TextDelta: "hello",
	}
	if err := writer.WriteCanonicalEvent(evt); err != nil {
		t.Fatalf("WriteCanonicalEvent failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmp, "req-canon", "canonical.ndjson"))
	if err != nil {
		t.Fatalf("failed to read canonical.ndjson: %v", err)
	}

	var decoded model.CanonicalEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Errorf("canonical.ndjson is not valid NDJSON: %v\ncontent: %s", err, string(data))
	}
	if decoded.Type != "message.delta" {
		t.Errorf("expected Type 'message.delta', got %q", decoded.Type)
	}
}

func TestArchiveWriter_WriteCanonicalEvent_MultipleLinesNDJSON(t *testing.T) {
	tmp := t.TempDir()
	writer := NewArchiveWriter(tmp, "req-canon-multi")
	_ = writer

	events := []model.CanonicalEvent{
		{Seq: 1, Type: "response.start"},
		{Seq: 2, Type: "message.start", ItemID: "msg-1"},
		{Seq: 3, Type: "message.delta", ItemID: "msg-1", TextDelta: "hi"},
		{Seq: 4, Type: "message.done", ItemID: "msg-1"},
	}
	for _, e := range events {
		if err := writer.WriteCanonicalEvent(e); err != nil {
			t.Fatalf("WriteCanonicalEvent failed: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmp, "req-canon-multi", "canonical.ndjson"))
	if err != nil {
		t.Fatalf("failed to read canonical.ndjson: %v", err)
	}

	lines := splitLines(string(data))
	if len(lines) != 4 {
		t.Errorf("expected 4 NDJSON lines, got %d", len(lines))
	}
}

func TestArchiveWriter_WriteFinalSnapshot_SingleLineNDJSON(t *testing.T) {
	tmp := t.TempDir()
	writer := NewArchiveWriter(tmp, "req-final")
	_ = writer

	snapshot := FinalSnapshot{
		StatusCode: 200,
		Response:   map[string]any{"model": "gpt-4o", "output": "ok"},
		Error:      nil,
	}
	if err := writer.WriteFinalSnapshot(snapshot); err != nil {
		t.Fatalf("WriteFinalSnapshot failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmp, "req-final", "final.ndjson"))
	if err != nil {
		t.Fatalf("failed to read final.ndjson: %v", err)
	}

	var decoded FinalSnapshot
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Errorf("final.ndjson is not valid NDJSON: %v\ncontent: %s", err, string(data))
	}
	if decoded.StatusCode != 200 {
		t.Errorf("expected StatusCode 200, got %d", decoded.StatusCode)
	}
}

func TestArchiveWriter_Close_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	writer := NewArchiveWriter(tmp, "req-close")
	_ = writer

	if err := writer.Close(); err != nil {
		t.Fatalf("first Close failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("second Close should not fail: %v", err)
	}
}

func TestArchiveWriter_DirectoryExists_NoPanic(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "req-exists")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("failed to pre-create directory: %v", err)
	}

	writer := NewArchiveWriter(tmp, "req-exists")
	if writer == nil {
		t.Error("expected non-nil writer when directory already exists")
	}
	writer.Close()
}

func TestArchiveWriterWithRetentionPrunesOldRequestDirectoriesOnClose(t *testing.T) {
	tmp := t.TempDir()
	writer1 := NewArchiveWriterWithRetention(tmp, "req-1", 2)
	if writer1 == nil {
		t.Fatal("expected first writer")
	}
	if err := writer1.WriteRequest(map[string]any{"request_id": "req-1"}); err != nil {
		t.Fatalf("WriteRequest(1) failed: %v", err)
	}
	if err := writer1.Close(); err != nil {
		t.Fatalf("Close(1) failed: %v", err)
	}

	writer2 := NewArchiveWriterWithRetention(tmp, "req-2", 2)
	if writer2 == nil {
		t.Fatal("expected second writer")
	}
	if err := writer2.WriteRequest(map[string]any{"request_id": "req-2"}); err != nil {
		t.Fatalf("WriteRequest(2) failed: %v", err)
	}
	if err := writer2.Close(); err != nil {
		t.Fatalf("Close(2) failed: %v", err)
	}

	writer3 := NewArchiveWriterWithRetention(tmp, "req-3", 2)
	if writer3 == nil {
		t.Fatal("expected third writer")
	}
	if err := writer3.WriteRequest(map[string]any{"request_id": "req-3"}); err != nil {
		t.Fatalf("WriteRequest(3) failed: %v", err)
	}
	if err := writer3.Close(); err != nil {
		t.Fatalf("Close(3) failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(tmp, "req-1")); !os.IsNotExist(err) {
		t.Fatalf("expected oldest archive directory to be pruned, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "req-2")); err != nil {
		t.Fatalf("expected second archive directory to remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "req-3")); err != nil {
		t.Fatalf("expected newest archive directory to remain: %v", err)
	}
}

func splitLines(s string) []string {
	var lines []string
	i := 0
	for i < len(s) {
		j := i
		for j < len(s) && s[j] != '\n' {
			j++
		}
		if j > i {
			lines = append(lines, s[i:j])
		}
		i = j + 1
	}
	return lines
}
