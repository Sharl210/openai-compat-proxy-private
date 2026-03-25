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
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz)
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

		if info, err := resolveRouteInfo(r.URL.Path, snapshot.Config); err == nil {
			setConfigVersionHeaders(w, snapshot, info.ProviderID)
			r = r.Clone(withRuntimeSnapshot(withRouteInfo(r.Context(), info), snapshot))
			r.URL.Path = info.CanonicalPath
			if err := auth.ValidateProxyAuth(r, snapshot.Config.ProxyAPIKey); err != nil {
				errorsx.WriteJSON(w, http.StatusUnauthorized, "unauthorized", "invalid proxy api key")
				return
			}

			mux.ServeHTTP(w, r)
			return
		}

		errorsx.WriteJSON(w, http.StatusNotFound, "not_found", "route not found")
	}))
}
