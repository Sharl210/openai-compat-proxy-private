package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/debugarchive"
	"openai-compat-proxy/internal/logging"
)

var requestCounter uint64

const normalizationVersion = "v1"

func withRequestID(store *config.RuntimeStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := fmt.Sprintf("req-%d-%d", time.Now().UnixNano(), atomic.AddUint64(&requestCounter, 1))
		defer logging.CloseRequest(id)
		w.Header().Set("X-Request-Id", id)
		started := time.Now()
		archiveWriter := archiveWriterForRequest(store, id, r.URL.Path)
		shouldLog := shouldLogAPITraffic(r.URL.Path)
		capturedRequestBody := ""
		if r.Body != nil && (archiveWriter != nil || shouldLog) {
			capturedRequestBody, r.Body = captureRequestBody(r.Body, requestCaptureLimit(store, archiveWriter != nil))
		}
		recordedRequestBody := redactCapturedImageDataURLs(capturedRequestBody)
		if recordedRequestBody == capturedRequestBody {
			recordedRequestBody = logging.RedactImageDataForLog([]byte(capturedRequestBody))
		}
		if archiveWriter != nil {
			defer archiveWriter.Close()
			r = r.WithContext(debugarchive.WithArchiveWriter(r.Context(), archiveWriter))
			_ = archiveWriter.WriteRequest(map[string]any{
				"request_id":   id,
				"method":       r.Method,
				"path":         r.URL.Path,
				"content_type": r.Header.Get("Content-Type"),
				"request_body": recordedRequestBody,
			})
		}
		if shouldLog {
			logging.Event("clientToProxyRequest", map[string]any{
				"request_id":   id,
				"method":       r.Method,
				"path":         r.URL.Path,
				"content_type": r.Header.Get("Content-Type"),
				"request_body": truncateBody([]byte(recordedRequestBody), 512),
			})
		}
		cw := &responseCaptureWriter{
			ResponseWriter: w,
			status:         http.StatusOK,
			captureBody:    archiveWriter != nil,
			captureLimit:   archiveCaptureLimit(store),
		}
		next.ServeHTTP(cw, r)
		if archiveWriter != nil {
			snapshot := debugarchive.FinalSnapshot{StatusCode: cw.status}
			if body := bytes.TrimSpace(cw.body.Bytes()); len(body) > 0 && !cw.truncated {
				var payload map[string]any
				if err := json.Unmarshal(body, &payload); err == nil {
					if cw.status >= http.StatusBadRequest {
						snapshot.Error = payload
					} else {
						snapshot.Response = payload
					}
				}
			}
			_ = archiveWriter.WriteFinalSnapshot(snapshot)
		}
		if shouldLog {
			logging.Event("proxyToClientResponse", map[string]any{
				"request_id": id,
				"status":     cw.status,
				"elapsed_ms": time.Since(started).Milliseconds(),
			})
		}
	})
}

func shouldLogAPITraffic(path string) bool {
	clean := strings.TrimSpace(path)
	if clean == "" {
		return false
	}
	if clean == "/" || clean == "/favicon.ico" || clean == "/robots.txt" {
		return false
	}
	if clean == "/_admin" || strings.HasPrefix(clean, "/_admin/") {
		return false
	}
	if clean == canonicalV1ImagesGenerationsPath || clean == canonicalV1ImagesEditsPath || clean == canonicalV1ImagesVariationsPath {
		return false
	}
	if clean == canonicalV1EmbeddingsPath {
		return false
	}
	if clean == canonicalV1RerankPath {
		return false
	}
	if clean == "/images/generations" || clean == "/images/edits" || clean == "/images/variations" {
		return false
	}
	if clean == "/embeddings" {
		return false
	}
	if clean == "/rerank" {
		return false
	}
	if strings.HasPrefix(clean, "/") && strings.Contains(clean, "/images/") {
		return false
	}
	if strings.HasPrefix(clean, "/") && strings.HasSuffix(clean, "/embeddings") {
		return false
	}
	if strings.HasPrefix(clean, "/") && strings.HasSuffix(clean, "/rerank") {
		return false
	}
	return true
}

func shouldArchiveAPITraffic(path string) bool {
	return shouldLogAPITraffic(path)
}

func archiveWriterForRequest(store *config.RuntimeStore, requestID string, path string) *debugarchive.ArchiveWriter {
	if requestID == "" {
		return nil
	}
	if !shouldArchiveAPITraffic(path) {
		return nil
	}
	if store != nil {
		if snapshot := store.Active(); snapshot != nil {
			if !snapshot.Config.LogEnable {
				return nil
			}
			if root := snapshot.Config.DebugArchiveRootDir; root != "" {
				if !filepath.IsAbs(root) {
					if snapshot.RootEnvPath != "" {
						root = filepath.Join(filepath.Dir(snapshot.RootEnvPath), root)
					} else if root == debugarchive.EnvRootDir {
						return nil
					}
				}
				return debugarchive.NewArchiveWriterWithRetention(root, requestID, snapshot.Config.DebugArchiveMaxRequests)
			}
		}
	}
	return nil
}

func setNormalizationVersionHeader(w http.ResponseWriter) {
	w.Header().Set("X-Proxy-Normalization-Version", normalizationVersion)
}

func setConfigVersionHeaders(w http.ResponseWriter, snapshot *config.RuntimeSnapshot, providerID string) {
	if snapshot == nil {
		return
	}
	if snapshot.RootEnvVersion != "" {
		w.Header().Set("X-Root-Env-Version", snapshot.RootEnvVersion)
	}
	if timezone := snapshot.Config.CacheInfoTimezone; timezone != "" {
		w.Header().Set(headerCacheInfoTimezone, timezone)
	}
	if providerID == "" {
		return
	}
	w.Header().Set("X-Provider-Name", providerID)
	if version := snapshot.ProviderVersionByID[providerID]; version != "" {
		w.Header().Set("X-Provider-Version", version)
	}
}
