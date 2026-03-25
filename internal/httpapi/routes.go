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
const runtimeSnapshotKey routeContextKey = "runtime-snapshot"

func resolveRouteInfo(path string, cfg config.Config) (routeInfo, error) {
	if path == "/v1/models" || path == "/v1/responses" || path == "/v1/chat/completions" || path == "/v1/messages" {
		if !cfg.EnableLegacyV1Routes {
			return routeInfo{}, errors.New("route not found")
		}
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
	if canonicalPath != "/v1/models" && canonicalPath != "/v1/responses" && canonicalPath != "/v1/chat/completions" && canonicalPath != "/v1/messages" {
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

func withRuntimeSnapshot(ctx context.Context, snapshot *config.RuntimeSnapshot) context.Context {
	return context.WithValue(ctx, runtimeSnapshotKey, snapshot)
}

func routeInfoFromRequest(r *http.Request) (routeInfo, bool) {
	info, ok := r.Context().Value(routeInfoKey).(routeInfo)
	return info, ok
}

func runtimeSnapshotFromRequest(r *http.Request) (*config.RuntimeSnapshot, bool) {
	snapshot, ok := r.Context().Value(runtimeSnapshotKey).(*config.RuntimeSnapshot)
	return snapshot, ok
}

func providerConfigForRequest(r *http.Request) config.Config {
	snapshot, ok := runtimeSnapshotFromRequest(r)
	if !ok || snapshot == nil {
		return config.Config{}
	}
	providerCfg := snapshot.Config
	if info, ok := routeInfoFromRequest(r); ok {
		if provider, err := snapshot.Config.ProviderByID(info.ProviderID); err == nil {
			providerCfg.UpstreamBaseURL = provider.UpstreamBaseURL
			providerCfg.UpstreamAPIKey = provider.UpstreamAPIKey
			providerCfg.UpstreamRetryCount = provider.UpstreamRetryCount
			providerCfg.UpstreamRetryDelay = provider.UpstreamRetryDelay
		}
	}
	return providerCfg
}

func providerForRequest(r *http.Request) (config.ProviderConfig, bool) {
	snapshot, ok := runtimeSnapshotFromRequest(r)
	if !ok || snapshot == nil {
		return config.ProviderConfig{}, false
	}
	if info, ok := routeInfoFromRequest(r); ok {
		if provider, err := snapshot.Config.ProviderByID(info.ProviderID); err == nil {
			return provider, true
		}
	}
	return config.ProviderConfig{}, false
}
