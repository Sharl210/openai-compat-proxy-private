package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"openai-compat-proxy/internal/config"
)

type routeInfo struct {
	ProviderID    string
	Legacy        bool
	CanonicalPath string
}

type routeContextKey string

const routeInfoKey routeContextKey = "route-info"

func resolveRouteInfo(path string, cfg config.Config) (routeInfo, error) {
	if path == "/v1/models" || path == "/v1/responses" || path == "/v1/chat/completions" || path == "/v1/messages" {
		if len(cfg.Providers) == 0 {
			return routeInfo{Legacy: true, CanonicalPath: path}, nil
		}
		provider, err := cfg.DefaultProviderConfig()
		if err != nil {
			return routeInfo{}, err
		}
		return routeInfo{ProviderID: provider.ID, Legacy: true, CanonicalPath: path}, nil
	}

	trimmed := strings.Trim(path, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 3 {
		return routeInfo{}, errors.New("route not found")
	}
	providerID := parts[0]
	canonicalPath := "/" + strings.Join(parts[1:], "/")
	if canonicalPath != "/v1/models" && canonicalPath != "/v1/responses" && canonicalPath != "/v1/chat/completions" && canonicalPath != "/v1/messages" && canonicalPath != "/anthropic/v1/messages" {
		return routeInfo{}, errors.New("route not found")
	}
	provider, err := cfg.ProviderByID(providerID)
	if err != nil || !provider.Enabled {
		return routeInfo{}, errors.New("provider not found")
	}
	return routeInfo{ProviderID: providerID, CanonicalPath: canonicalPath}, nil
}

func withRouteInfo(ctx context.Context, info routeInfo) context.Context {
	return context.WithValue(ctx, routeInfoKey, info)
}

func routeInfoFromRequest(r *http.Request) (routeInfo, bool) {
	info, ok := r.Context().Value(routeInfoKey).(routeInfo)
	return info, ok
}

func providerConfigForRequest(r *http.Request, cfg config.Config) config.Config {
	providerCfg := cfg
	if info, ok := routeInfoFromRequest(r); ok {
		if provider, err := cfg.ProviderByID(info.ProviderID); err == nil {
			providerCfg.UpstreamBaseURL = provider.UpstreamBaseURL
			providerCfg.UpstreamAPIKey = provider.UpstreamAPIKey
		}
	}
	return providerCfg
}

func providerForRequest(r *http.Request, cfg config.Config) (config.ProviderConfig, bool) {
	if info, ok := routeInfoFromRequest(r); ok {
		if provider, err := cfg.ProviderByID(info.ProviderID); err == nil {
			return provider, true
		}
	}
	return config.ProviderConfig{}, false
}
