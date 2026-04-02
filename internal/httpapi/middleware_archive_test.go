package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"openai-compat-proxy/internal/debugarchive"
)

func TestWithRequestID_WritesArchiveRequestWhenEnabled(t *testing.T) {
	t.Setenv(debugarchive.EnvRootDir, t.TempDir())
	h := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"gpt-5"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	requestID := rec.Header().Get("X-Request-Id")
	if requestID == "" {
		t.Fatal("expected request id header")
	}
	path := filepath.Join(os.Getenv(debugarchive.EnvRootDir), requestID, "request.ndjson")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected archive request file at %s: %v", path, err)
	}
}
