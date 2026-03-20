package httpapi

import (
	"net/http"

	"openai-compat-proxy/internal/auth"
	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/errorsx"
)

func NewServer(cfg config.Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/v1/models", handleModels(cfg))
	mux.HandleFunc("/v1/responses", handleResponses(cfg))
	mux.HandleFunc("/v1/chat/completions", handleChat(cfg))
	mux.HandleFunc("/v1/messages", handleAnthropicMessages(cfg))

	return withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			mux.ServeHTTP(w, r)
			return
		}

		if info, err := resolveRouteInfo(r.URL.Path, cfg); err == nil {
			r = r.Clone(withRouteInfo(r.Context(), info))
			r.URL.Path = info.CanonicalPath
			if err := auth.ValidateProxyAuth(r, cfg.ProxyAPIKey); err != nil {
				errorsx.WriteJSON(w, http.StatusUnauthorized, "unauthorized", "invalid proxy api key")
				return
			}

			mux.ServeHTTP(w, r)
			return
		}

		errorsx.WriteJSON(w, http.StatusNotFound, "not_found", "route not found")
	}))
}
