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
	mux.HandleFunc("/v1/responses", handleResponses(cfg))
	mux.HandleFunc("/v1/chat/completions", handleChat(cfg))

	return withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			mux.ServeHTTP(w, r)
			return
		}

		if r.URL.Path == "/v1/responses" || r.URL.Path == "/v1/chat/completions" {
			if err := auth.ValidateProxyAuth(r, cfg.ProxyAPIKey); err != nil {
				errorsx.WriteJSON(w, http.StatusUnauthorized, "unauthorized", "invalid proxy api key")
				return
			}
			if _, err := auth.ResolveUpstreamAuthorization(r, cfg); err != nil {
				errorsx.WriteJSON(w, http.StatusUnauthorized, "missing_upstream_auth", err.Error())
				return
			}

			mux.ServeHTTP(w, r)
			return
		}

		errorsx.WriteJSON(w, http.StatusNotFound, "not_found", "route not found")
	}))
}
