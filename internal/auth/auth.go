package auth

import (
	"errors"
	"net/http"
	"strings"

	"openai-compat-proxy/internal/config"
)

var ErrUnauthorized = errors.New("unauthorized")
var ErrMissingUpstreamAuth = errors.New("missing upstream authorization")

func ValidateProxyAuth(r *http.Request, proxyKey string) error {
	return validateProxyAuthValue(r, proxyKey)
}

func ValidateProxyAuthForProvider(r *http.Request, rootKey string, provider config.ProviderConfig, allowRootFallback bool) error {
	if provider.ProxyAPIKeyDisabled() {
		return nil
	}
	if allowRootFallback && rootKey != "" {
		if err := validateProxyAuthValue(r, rootKey); err == nil {
			return nil
		}
	}
	return validateProxyAuthValue(r, provider.EffectiveProxyAPIKey(rootKey))
}

func validateProxyAuthValue(r *http.Request, proxyKey string) error {
	if proxyKey == "" {
		return nil
	}

	if r.Header.Get("Authorization") == "Bearer "+proxyKey {
		return nil
	}

	if strings.TrimSpace(r.Header.Get("X-API-Key")) == proxyKey {
		return nil
	}

	if strings.TrimSpace(r.Header.Get("Api-Key")) == proxyKey {
		return nil
	}

	if strings.TrimSpace(r.Header.Get("x-api-key")) == proxyKey {
		return nil
	}

	if strings.TrimSpace(r.Header.Get("api-key")) == proxyKey {
		return nil
	}

	if strings.TrimSpace(r.URL.Query().Get("key")) == proxyKey {
		return nil
	}

	if r.Header.Get("Authorization") != "Bearer "+proxyKey {
		return ErrUnauthorized
	}

	return nil
}

func ResolveUpstreamAuthorization(r *http.Request, cfg config.Config) (string, error) {
	if value := strings.TrimSpace(r.Header.Get("X-Upstream-Authorization")); value != "" {
		return value, nil
	}

	if cfg.ProxyAPIKey == "" {
		if value := strings.TrimSpace(r.Header.Get("Authorization")); value != "" {
			return value, nil
		}
	}

	if cfg.UpstreamAPIKey != "" {
		return "Bearer " + cfg.UpstreamAPIKey, nil
	}

	return "", ErrMissingUpstreamAuth
}
