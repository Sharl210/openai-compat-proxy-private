package httpapi

import (
	"net/http"
	"strings"

	"openai-compat-proxy/internal/auth"
	"openai-compat-proxy/internal/config"
)

func authHeaderForUpstream(r *http.Request, cfg config.Config) (string, error) {
	return auth.ResolveUpstreamAuthorization(r, effectiveUpstreamAuthConfig(r, cfg))
}

func authModeForUpstream(r *http.Request, cfg config.Config) string {
	cfg = effectiveUpstreamAuthConfig(r, cfg)
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
	if provider.StatusCheckProxyAPIKey(cfg.ProxyAPIKey, false) == "" {
		return ""
	}
	requestID, ok := requestStatusIDFromRequest(r)
	if !ok || strings.TrimSpace(requestID) == "" {
		return ""
	}
	store, ok := requestStatusAuthStoreFromRequest(r)
	if !ok || store == nil {
		return ""
	}
	return store.issueToken(provider.ID, requestID)
}

func validateStatusCheckAuth(r *http.Request, rootKey string, provider config.ProviderConfig, requestID string) error {
	if store, ok := requestStatusAuthStoreFromRequest(r); ok && store != nil {
		if token := strings.TrimSpace(r.URL.Query().Get("token")); token != "" && store.consumeToken(token, provider.ID, requestID) {
			return nil
		}
	}
	statusCheckKey := provider.StatusCheckProxyAPIKey(rootKey, false)
	return auth.ValidateProxyAuth(r, statusCheckKey)
}
