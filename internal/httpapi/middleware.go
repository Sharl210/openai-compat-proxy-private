package httpapi

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/debugarchive"
	"openai-compat-proxy/internal/logging"
)

var requestCounter uint64

const normalizationVersion = "v1"

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := fmt.Sprintf("req-%d-%d", time.Now().UnixNano(), atomic.AddUint64(&requestCounter, 1))
		w.Header().Set("X-Request-Id", id)
		started := time.Now()

		var requestBody []byte
		if r.Body != nil {
			requestBody, _ = io.ReadAll(r.Body)
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewBuffer(requestBody))
		}

		archiveWriter := debugarchive.NewWriterFromEnv(id)
		if archiveWriter != nil {
			_ = archiveWriter.WriteRequest(map[string]any{
				"request_id":   id,
				"method":       r.Method,
				"path":         r.URL.Path,
				"content_type": r.Header.Get("Content-Type"),
				"request_body": string(requestBody),
			})
			defer archiveWriter.Close()
			r = r.WithContext(debugarchive.WithArchiveWriter(r.Context(), archiveWriter))
		}

		logging.Event("clientToProxyRequest", map[string]any{
			"request_id":   id,
			"method":       r.Method,
			"path":         r.URL.Path,
			"content_type": r.Header.Get("Content-Type"),
			"request_body": truncateBody(requestBody, 512),
		})
		cw := &responseCaptureWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(cw, r)
		logging.Event("proxyToClientResponse", map[string]any{
			"request_id": id,
			"status":     cw.status,
			"elapsed_ms": time.Since(started).Milliseconds(),
		})
	})
}

func setNormalizationVersionHeader(w http.ResponseWriter) {
	w.Header().Set("X-Proxy-Normalization-Version", normalizationVersion)
}

func setConfigVersionHeaders(w http.ResponseWriter, snapshot *config.RuntimeSnapshot, providerID string) {
	if snapshot == nil {
		return
	}
	if snapshot.RootEnvVersion != "" {
		w.Header().Set("X-Env-Version", snapshot.RootEnvVersion)
	}
	if providerID == "" {
		return
	}
	w.Header().Set("X-Provider-Name", providerID)
	if version := snapshot.ProviderVersionByID[providerID]; version != "" {
		w.Header().Set("X-Provider-Version", version)
	}
	provider, err := snapshot.Config.ProviderByID(providerID)
	if err != nil {
		return
	}
	if provider.SystemPromptText != "" && provider.SystemPromptFilesRaw != "" {
		w.Header().Set("X-SYSTEM-PROMPT-ATTACH", provider.SystemPromptPosition+":"+provider.SystemPromptFilesRaw)
	}
}

type responseCaptureWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (w *responseCaptureWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseCaptureWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *responseCaptureWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func truncateBody(body []byte, maxLen int) string {
	if len(body) <= maxLen {
		return string(body)
	}
	return string(body[:maxLen]) + "...[TRUNCATED]"
}
