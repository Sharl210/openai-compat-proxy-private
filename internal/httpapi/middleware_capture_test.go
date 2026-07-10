package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestWithRequestIDBoundsArchivedRequestBodyWhenPayloadExceedsLogLimit(t *testing.T) {
	// Given
	archiveDir := t.TempDir()
	store := config.NewStaticRuntimeStore(config.Config{
		LogEnable:               true,
		LogMaxBodySizeMB:        0.00002,
		DebugArchiveRootDir:     archiveDir,
		DebugArchiveMaxRequests: 2,
	})
	payload := strings.Repeat("x", 1024)
	var received string
	h := withRequestID(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read handler request body: %v", err)
		}
		received = string(body)
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(payload))
	rec := httptest.NewRecorder()

	// When
	h.ServeHTTP(rec, req)

	// Then
	if received != payload {
		t.Fatalf("expected handler to receive full payload, got %d bytes", len(received))
	}
	requestID := rec.Header().Get("X-Request-Id")
	data, err := os.ReadFile(filepath.Join(archiveDir, requestID, "request.ndjson"))
	if err != nil {
		t.Fatalf("read archived request: %v", err)
	}
	var archived map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(data), &archived); err != nil {
		t.Fatalf("decode archived request: %v", err)
	}
	body, _ := archived["request_body"].(string)
	if len(body) >= len(payload) {
		t.Fatalf("expected bounded archived body, got %d bytes for %d-byte payload", len(body), len(payload))
	}
	if !strings.Contains(body, "[TRUNCATED]") {
		t.Fatalf("expected truncated marker in archived body, got %q", body)
	}
}

func TestWithRequestIDWritesRequestArchiveBeforeHandlerRuns(t *testing.T) {
	// Given
	archiveDir := t.TempDir()
	store := config.NewStaticRuntimeStore(config.Config{
		LogEnable:               true,
		LogMaxBodySizeMB:        1,
		DebugArchiveRootDir:     archiveDir,
		DebugArchiveMaxRequests: 2,
	})
	archiveVisible := false
	h := withRequestID(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := w.Header().Get("X-Request-Id")
		data, err := os.ReadFile(filepath.Join(archiveDir, requestID, "request.ndjson"))
		archiveVisible = err == nil && len(bytes.TrimSpace(data)) > 0
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5"}`))
	rec := httptest.NewRecorder()

	// When
	h.ServeHTTP(rec, req)

	// Then
	if !archiveVisible {
		t.Fatal("expected request archive to exist before handler execution")
	}
}

func TestWithRequestIDRedactsImageDataURLFromLogAndArchiveWithoutChangingHandlerBody(t *testing.T) {
	// Given
	archiveDir := t.TempDir()
	logDir := initMiddlewareTestLogger(t)
	store := config.NewStaticRuntimeStore(config.Config{
		LogEnable:               true,
		LogMaxBodySizeMB:        1,
		DebugArchiveRootDir:     archiveDir,
		DebugArchiveMaxRequests: 2,
	})
	const imageDataSentinel = "SW5ib3VuZEltYWdlRGF0YVVybEJhc2U2NFNlbnRpbmVs"
	payload := `{"model":"gpt-5","input":[{"role":"user","content":[{"type":"input_text","text":"keep-this-text"},{"type":"input_image","image_url":"data:image/png;base64,` + imageDataSentinel + `"}]}]}`
	var received string
	h := withRequestID(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read handler request body: %v", err)
		}
		received = string(body)
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// When
	h.ServeHTTP(rec, req)

	// Then
	if received != payload {
		t.Fatalf("expected handler to receive original image payload, got %s", received)
	}
	requestID := rec.Header().Get("X-Request-Id")
	archiveData, err := os.ReadFile(filepath.Join(archiveDir, requestID, "request.ndjson"))
	if err != nil {
		t.Fatalf("read archived request: %v", err)
	}
	if strings.Contains(string(archiveData), imageDataSentinel) {
		t.Fatalf("expected archive request to redact image data, got %s", archiveData)
	}
	if !strings.Contains(string(archiveData), "image") || !strings.Contains(string(archiveData), "keep-this-text") {
		t.Fatalf("expected archive to retain image placeholder and non-image content, got %s", archiveData)
	}
	files := middlewareLogFiles(t, logDir)
	if len(files) != 1 {
		t.Fatalf("expected exactly one log file, got %v", files)
	}
	logData, err := os.ReadFile(filepath.Join(logDir, files[0]))
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	if strings.Contains(string(logData), imageDataSentinel) {
		t.Fatalf("expected clientToProxyRequest log to redact image data, got %s", logData)
	}
	if !strings.Contains(string(logData), "image") || !strings.Contains(string(logData), "keep-this-text") {
		t.Fatalf("expected log to retain image placeholder and non-image content, got %s", logData)
	}
}

func TestResponseCaptureWriterDoesNotBufferEventStream(t *testing.T) {
	// Given
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "text/event-stream")
	writer := &responseCaptureWriter{
		ResponseWriter: recorder,
		status:         http.StatusOK,
		captureBody:    true,
		captureLimit:   -1,
	}
	payload := []byte(strings.Repeat("data: chunk\n\n", 1024))

	// When
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("write event stream: %v", err)
	}

	// Then
	if writer.body.Len() != 0 {
		t.Fatalf("expected event stream not to be buffered, got %d bytes", writer.body.Len())
	}
}
