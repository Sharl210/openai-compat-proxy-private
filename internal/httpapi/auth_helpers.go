package httpapi

import (
	"net/http"

	"openai-compat-proxy/internal/auth"
	"openai-compat-proxy/internal/config"
)

func authHeaderForUpstream(r *http.Request, cfg config.Config) (string, error) {
	return auth.ResolveUpstreamAuthorization(r, effectiveUpstreamAuthConfig(r, cfg))
}

func authHeaderForResolvedProviderUpstream(r *http.Request, cfg config.Config, providerID string) (string, error) {
	return authHeaderForUpstream(requestWithProviderRouteInfo(r, providerID), cfg)
}

func authModeForUpstream(r *http.Request, cfg config.Config) string {
	cfg = effectiveUpstreamAuthConfig(r, cfg)
	if r.Header.Get("X-Upstream-Authorization") != "" {
		return "x_upstream_authorization"
	}
	if cfg.ProxyAPIKey == "" && r.Header.Get("Authorization") != "" {
		return "authorization_passthrough"
	}
	if cfg.ProxyAPIKey == "" {
		if r.Header.Get("X-API-Key") != "" || r.Header.Get("x-api-key") != "" {
			return "x_api_key_passthrough"
		}
	}
	if cfg.UpstreamAPIKey != "" {
		return "server_default_key"
	}
	return "missing"
}

func authModeForResolvedProviderUpstream(r *http.Request, cfg config.Config, providerID string) string {
	return authModeForUpstream(requestWithProviderRouteInfo(r, providerID), cfg)
}

func effectiveUpstreamAuthConfig(r *http.Request, cfg config.Config) config.Config {
	if info, ok := routeInfoFromRequest(r); ok {
		if provider, err := cfg.ProviderByID(info.ProviderID); err == nil {
			providerCfg := cfg
			providerCfg.UpstreamAPIKey = provider.UpstreamAPIKey
			if allowRootProxyKeyForRequest(r, cfg, provider) {
				providerCfg.ProxyAPIKey = cfg.ProxyAPIKey
			} else {
				providerCfg.ProxyAPIKey = provider.EffectiveProxyAPIKey(cfg.ProxyAPIKey)
			}
			return providerCfg
		}
	}
	return cfg
}

func allowRootProxyKeyForRequest(r *http.Request, cfg config.Config, provider config.ProviderConfig) bool {
	if provider.ID == "" || provider.ID != cfg.DefaultProvider {
		return false
	}
	if info, ok := routeInfoFromRequest(r); ok {
		return info.Legacy
	}
	return false
}

func statusCheckProxyKeyForRequest(r *http.Request, cfg config.Config, provider config.ProviderConfig) string {
	_ = r
	_ = cfg
	_ = provider
	return ""
}

func requestWithProviderRouteInfo(r *http.Request, providerID string) *http.Request {
	if r == nil || providerID == "" {
		return r
	}
	if info, ok := routeInfoFromRequest(r); ok {
		info.ProviderID = providerID
		return r.Clone(withRouteInfo(r.Context(), info))
	}
	return r
}
