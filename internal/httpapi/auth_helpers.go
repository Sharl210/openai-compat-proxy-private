package httpapi

import (
	"net/http"

	"openai-compat-proxy/internal/auth"
	"openai-compat-proxy/internal/config"
)

func authHeaderForUpstream(r *http.Request, cfg config.Config) (string, error) {
	return auth.ResolveUpstreamAuthorization(r, cfg)
}
