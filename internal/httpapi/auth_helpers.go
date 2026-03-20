package httpapi

import (
	"net/http"

	"openai-compat-proxy/internal/auth"
	"openai-compat-proxy/internal/config"
)

func authHeaderForUpstream(r *http.Request, cfg config.Config) (string, error) {
	if info, ok := routeInfoFromRequest(r); ok {
		if provider, err := cfg.ProviderByID(info.ProviderID); err == nil {
			providerCfg := cfg
			providerCfg.UpstreamAPIKey = provider.UpstreamAPIKey
			return auth.ResolveUpstreamAuthorization(r, providerCfg)
		}
	}
	return auth.ResolveUpstreamAuthorization(r, cfg)
}

func authModeForUpstream(r *http.Request, cfg config.Config) string {
	if info, ok := routeInfoFromRequest(r); ok {
		if provider, err := cfg.ProviderByID(info.ProviderID); err == nil {
			cfg.UpstreamAPIKey = provider.UpstreamAPIKey
		}
	}
	if r.Header.Get("X-Upstream-Authorization") != "" {
		return "x_upstream_authorization"
	}
	if cfg.ProxyAPIKey == "" && r.Header.Get("Authorization") != "" {
		return "authorization_passthrough"
	}
	if cfg.UpstreamAPIKey != "" {
		return "server_default_key"
	}
	return "missing"
}
