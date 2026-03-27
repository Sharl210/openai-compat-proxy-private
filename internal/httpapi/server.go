package httpapi

import (
	"net/http"

	"openai-compat-proxy/internal/auth"
	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/errorsx"
)

func NewServer(cfg config.Config) http.Handler {
	return NewServerWithStore(config.NewStaticRuntimeStore(cfg))
}

func NewServerWithStore(store *config.RuntimeStore) http.Handler {
	statusStore := newRequestStatusStore()
	statusAuthStore := newRequestStatusAuthStore()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz(store))
	mux.HandleFunc("/v1/models", handleModels())
	mux.HandleFunc("/v1/responses", handleResponses())
	mux.HandleFunc("/v1/chat/completions", handleChat())
	mux.HandleFunc("/v1/messages", handleAnthropicMessages())

	return withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snapshot := store.Active()
		if snapshot == nil {
			errorsx.WriteJSON(w, http.StatusServiceUnavailable, "config_unavailable", "runtime config unavailable")
			return
		}
		if r.URL.Path == "/healthz" {
			mux.ServeHTTP(w, r)
			return
		}
		if statusPath, ok := parseRequestStatusPath(r.URL.Path, snapshot.Config); ok {
			provider, err := snapshot.Config.ProviderByID(statusPath.ProviderID)
			if err != nil {
				errorsx.WriteJSON(w, http.StatusNotFound, "not_found", "request not found")
				return
			}
			r = r.Clone(withRequestStatusID(withRequestStatusAuthStore(withRequestStatusStore(r.Context(), statusStore), statusAuthStore), statusPath.RequestID))
			if err := validateStatusCheckAuth(r, snapshot.Config.ProxyAPIKey, provider, statusPath.RequestID); err != nil {
				errorsx.WriteJSON(w, http.StatusUnauthorized, "unauthorized", "invalid proxy api key")
				return
			}
			setConfigVersionHeaders(w, snapshot, statusPath.ProviderID)
			setRequestStatusHeaders(w, r, statusPath.ProviderID, statusPath.RequestID, statusCheckProxyKeyForRequest(r, snapshot.Config, provider), "health")
			handleRequestStatus(statusStore).ServeHTTP(w, r)
			return
		}

		if info, err := resolveRouteInfo(r.URL.Path, snapshot.Config); err == nil {
			provider, err := snapshot.Config.ProviderByID(info.ProviderID)
			if err != nil {
				errorsx.WriteJSON(w, http.StatusNotFound, "not_found", "route not found")
				return
			}
			setConfigVersionHeaders(w, snapshot, info.ProviderID)
			requestID := w.Header().Get("X-Request-Id")
			r = r.Clone(withRequestStatusID(withRequestStatusAuthStore(withRequestStatusStore(withRuntimeSnapshot(withRouteInfo(r.Context(), info), snapshot), statusStore), statusAuthStore), requestID))
			r.URL.Path = info.CanonicalPath
			if err := auth.ValidateProxyAuthForProvider(r, snapshot.Config.ProxyAPIKey, provider, info.Legacy); err != nil {
				errorsx.WriteJSON(w, http.StatusUnauthorized, "unauthorized", "invalid proxy api key")
				return
			}
			statusStore.start(requestID, info.ProviderID, info.CanonicalPath)
			setRequestStatusHeaders(w, r, info.ProviderID, requestID, statusCheckProxyKeyForRequest(r, snapshot.Config, provider), "health")

			mux.ServeHTTP(w, r)
			return
		}

		errorsx.WriteJSON(w, http.StatusNotFound, "not_found", "route not found")
	}))
}
