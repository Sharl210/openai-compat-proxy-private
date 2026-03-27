package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/logging"
)

var requestCounter uint64

const normalizationVersion = "v1"

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := fmt.Sprintf("req-%d-%d", time.Now().UnixNano(), atomic.AddUint64(&requestCounter, 1))
		w.Header().Set("X-Request-Id", id)
		started := time.Now()
		logging.Event("downstream_request_received", map[string]any{
			"request_id":                       id,
			"method":                           r.Method,
			"path":                             r.URL.Path,
			"normalization_version":            normalizationVersion,
			"content_length":                   r.ContentLength,
			"content_type":                     r.Header.Get("Content-Type"),
			"client_user_agent":                r.Header.Get("User-Agent"),
			"x_upstream_authorization_present": strings.TrimSpace(r.Header.Get("X-Upstream-Authorization")) != "",
		})
		cw := &captureWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(cw, r)
		logging.Event("downstream_response_sent", map[string]any{
			"request_id":            id,
			"path":                  r.URL.Path,
			"status":                cw.status,
			"elapsed_ms":            time.Since(started).Milliseconds(),
			"normalization_version": normalizationVersion,
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

func setRequestStatusHeaders(w http.ResponseWriter, r *http.Request, providerID string, requestID string, proxyKey string, healthFlag string) {
	if requestID == "" || providerID == "" {
		return
	}
	w.Header().Set("X-STATUS-CHECK-URL", buildStatusCheckURL(r, providerID, requestID, proxyKey))
	if healthFlag == "" {
		healthFlag = "health"
	}
	w.Header().Set("X-RESPONSE-PROCESS-HEALTH-FLAG", healthFlag)
}

func buildStatusCheckURL(r *http.Request, providerID string, requestID string, proxyKey string) string {
	scheme := "http"
	if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		scheme = proto
	} else if r.TLS != nil {
		scheme = "https"
	}
	urlText := fmt.Sprintf("%s://%s/%s/v1/requests/%s", scheme, r.Host, providerID, requestID)
	if proxyKey == "" {
		return urlText
	}
	return urlText + "?key=" + url.QueryEscape(proxyKey)
}

type captureWriter struct {
	http.ResponseWriter
	status int
}

func (w *captureWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
