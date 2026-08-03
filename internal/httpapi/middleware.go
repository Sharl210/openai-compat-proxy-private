package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/debugarchive"
	"openai-compat-proxy/internal/logging"
)

var requestCounter uint64

const normalizationVersion = "v1"

type clientToProxyRequestLogContextKey struct{}

type clientToProxyRequestLogState struct {
	once        sync.Once
	requestID   string
	method      string
	path        string
	contentType string
	requestBody string
}

func withClientToProxyRequestLogState(ctx context.Context, state *clientToProxyRequestLogState) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if state == nil {
		return ctx
	}
	return context.WithValue(ctx, clientToProxyRequestLogContextKey{}, state)
}

func emitClientToProxyRequestLog(r *http.Request) {
	if r == nil {
		return
	}
	state, _ := r.Context().Value(clientToProxyRequestLogContextKey{}).(*clientToProxyRequestLogState)
	if state == nil {
		return
	}
	state.emit(r)
}

func (s *clientToProxyRequestLogState) emit(r *http.Request) {
	if s == nil || r == nil {
		return
	}
	s.once.Do(func() {
		attrs := map[string]any{
			"request_id":   s.requestID,
			"session_id":   requestSessionIDFromRequest(r),
			"method":       s.method,
			"path":         s.path,
			"content_type": s.contentType,
			"request_body": truncateBody([]byte(s.requestBody), 512),
		}
		if meta, ok := requestLineageFromRequest(r); ok {
			appendRequestLineageLogFields(attrs, meta)
		}
		logging.Event("clientToProxyRequest", attrs)
	})
}

func withRequestID(store *config.RuntimeStore, next http.Handler) http.Handler {
	return withRequestIDAndLineage(store, nil, next)
}

func withRequestIDAndLineage(store *config.RuntimeStore, lineageStore *requestLineageStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := fmt.Sprintf("req-%d-%d", time.Now().UnixNano(), atomic.AddUint64(&requestCounter, 1))
		defer logging.CloseRequest(id)
		w.Header().Set("X-Request-Id", id)
		carrier := newRequestLineageCarrier(id, explicitProxySessionIDFromRequest(r))
		r = r.Clone(withRequestLineageCarrier(r.Context(), carrier))
		r, _ = ensureProxySessionID(r, w)
		markSuccessfulDownstreamOutput := func() {
			if lineageStore == nil || carrier == nil {
				return
			}
			if meta, ok := carrier.lineageSnapshot(); ok {
				lineageStore.markReusable(meta)
			}
		}
		started := time.Now()
		archiveWriter := archiveWriterForRequest(store, r)
		shouldLog := shouldLogAPITraffic(r.URL.Path)
		capturedRequestBody := ""
		if r.Body != nil && (archiveWriter != nil || shouldLog) {
			capturedRequestBody, r.Body = captureRequestBody(r.Body, requestCaptureLimit(store, archiveWriter != nil))
		}
		recordedRequestBody := redactCapturedImageDataURLs(capturedRequestBody)
		if recordedRequestBody == capturedRequestBody {
			recordedRequestBody = logging.RedactImageDataForLog([]byte(capturedRequestBody))
		}
		if shouldLog {
			r = r.WithContext(withClientToProxyRequestLogState(r.Context(), &clientToProxyRequestLogState{
				requestID:   id,
				method:      r.Method,
				path:        r.URL.Path,
				contentType: r.Header.Get("Content-Type"),
				requestBody: recordedRequestBody,
			}))
		}
		if archiveWriter != nil {
			defer func() { _ = archiveWriter.Close() }()
			r = r.WithContext(debugarchive.WithArchiveWriter(r.Context(), archiveWriter))
		}
		if archiveWriter != nil {
			_ = archiveWriter.WriteRequest(requestArchivePayload(id, r, recordedRequestBody))
		}
		cw := &responseCaptureWriter{
			ResponseWriter:               w,
			status:                       http.StatusOK,
			captureBody:                  archiveWriter != nil,
			captureLimit:                 archiveCaptureLimit(store),
			onSuccessfulDownstreamOutput: markSuccessfulDownstreamOutput,
		}
		next.ServeHTTP(cw, r)
		meta, lineageOK := requestLineageFromRequest(r)
		if !lineageOK && lineageStore != nil && carrier != nil {
			meta, lineageOK = carrier.ensureResolved(lineageStore, requestSessionIDFromRequest(r), "")
		}
		emitClientToProxyRequestLog(r)
		if archiveWriter != nil {
			_ = archiveWriter.ReplaceRequest(requestArchivePayload(id, r, recordedRequestBody))
		}
		if lineageOK && lineageStore != nil {
			lineageStore.recordSessionRequest(meta, cw.status, r.URL.Path)
		}
		if archiveWriter != nil {
			snapshot := debugarchive.FinalSnapshot{StatusCode: cw.status, SessionID: requestSessionIDFromRequest(r)}
			if lineageOK {
				snapshot.RequestLineage = meta
			}
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
			attrs := map[string]any{
				"request_id": id,
				"session_id": requestSessionIDFromRequest(r),
				"status":     cw.status,
				"elapsed_ms": time.Since(started).Milliseconds(),
			}
			if lineageOK {
				appendRequestLineageLogFields(attrs, meta)
			}
			logging.Event("proxyToClientResponse", attrs)
		}
		if lineageOK && lineageStore != nil {
			reusable, finalOutcomeKnown := cw.finalDownstreamReuseDecision()
			if !finalOutcomeKnown {
				if cw.isEventStream() {
					reusable = false
				} else {
					reusable = cw.hasSuccessfulDownstreamOutput()
				}
			}
			lineageStore.markFinished(meta, reusable)
		}
	})
}

func requestArchivePayload(id string, r *http.Request, recordedRequestBody string) map[string]any {
	payload := map[string]any{
		"request_id":   id,
		"session_id":   requestSessionIDFromRequest(r),
		"method":       r.Method,
		"path":         r.URL.Path,
		"content_type": r.Header.Get("Content-Type"),
		"request_body": recordedRequestBody,
	}
	if meta, ok := requestLineageFromRequest(r); ok {
		appendRequestLineageLogFields(payload, meta)
	}
	return payload
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

func archiveWriterForRequest(store *config.RuntimeStore, r *http.Request) *debugarchive.ArchiveWriter {
	if r == nil {
		return nil
	}
	carrier := requestLineageCarrierFromContext(r.Context())
	requestID := ""
	if carrier != nil {
		requestID = carrier.requestUIDValue()
	}
	path := r.URL.Path
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
