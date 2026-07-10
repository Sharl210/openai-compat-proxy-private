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

func TestWithRequestIDRedactsGenericImageDataURLFromLogAndArchiveWithoutChangingHandlerBody(t *testing.T) {
	// Given
	archiveDir := t.TempDir()
	logDir := initMiddlewareTestLogger(t)
	store := config.NewStaticRuntimeStore(config.Config{
		LogEnable:               true,
		LogMaxBodySizeMB:        1,
		DebugArchiveRootDir:     archiveDir,
		DebugArchiveMaxRequests: 2,
	})
	const imageDataSentinel = "R2VuZXJpY0ltYWdlRGF0YVVybEJhc2U2NFNlbnRpbmVs"
	payload := "{\n  \"model\": \"gpt-5\",\n  \"attachment\": \"data:image/png;base64," + imageDataSentinel + "\",\n  \"text\": \"keep-this-text\"\n}"
	wantRecordedBody := "{\n  \"model\": \"gpt-5\",\n  \"attachment\": \"image\",\n  \"text\": \"keep-this-text\"\n}"
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
	var archived map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(archiveData), &archived); err != nil {
		t.Fatalf("decode archived request: %v", err)
	}
	if got, _ := archived["request_body"].(string); got != wantRecordedBody {
		t.Fatalf("expected archive to preserve non-image request formatting, got %q", got)
	}
	files := middlewareLogFiles(t, logDir)
	if len(files) != 1 {
		t.Fatalf("expected exactly one log file, got %v", files)
	}
	logData, err := os.ReadFile(filepath.Join(logDir, files[0]))
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	for _, copy := range []struct {
		name string
		data []byte
	}{
		{name: "archive", data: archiveData},
		{name: "clientToProxyRequest log", data: logData},
	} {
		text := string(copy.data)
		if strings.Contains(text, imageDataSentinel) || strings.Contains(text, "data:image/") {
			t.Fatalf("expected %s to redact image data, got %s", copy.name, text)
		}
		if !strings.Contains(text, "image") {
			t.Fatalf("expected %s to retain image placeholder, got %s", copy.name, text)
		}
	}
}

func TestWithRequestIDRedactsTruncatedImageDataURLWithoutLeakingBase64Prefix(t *testing.T) {
	// Given
	archiveDir := t.TempDir()
	logDir := initMiddlewareTestLogger(t)
	store := config.NewStaticRuntimeStore(config.Config{
		LogEnable:               true,
		LogMaxBodySizeMB:        0.00006,
		DebugArchiveRootDir:     archiveDir,
		DebugArchiveMaxRequests: 2,
	})
	const imageDataSentinel = "VHJ1bmNhdGVkR2VuZXJpY0ltYWdlRGF0YVVybEJhc2U2NFNlbnRpbmVs"
	payload := `{"attachment":"data:image/png;base64,` + imageDataSentinel + strings.Repeat("A", 256) + `","text":"keep-this-text"}`
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
	archiveText := string(archiveData)
	if strings.Contains(archiveText, imageDataSentinel) || strings.Contains(archiveText, "data:image/") {
		t.Fatalf("expected archive to redact truncated image data, got %s", archiveText)
	}
	if !strings.Contains(archiveText, "image") {
		t.Fatalf("expected archive to retain image placeholder, got %s", archiveText)
	}
	files := middlewareLogFiles(t, logDir)
	if len(files) != 1 {
		t.Fatalf("expected exactly one log file, got %v", files)
	}
	logData, err := os.ReadFile(filepath.Join(logDir, files[0]))
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	logText := string(logData)
	if strings.Contains(logText, imageDataSentinel) || strings.Contains(logText, "data:image/") {
		t.Fatalf("expected clientToProxyRequest log to redact truncated image data, got %s", logText)
	}
	if !strings.Contains(logText, "image") {
		t.Fatalf("expected clientToProxyRequest log to retain image placeholder, got %s", logText)
	}
}
