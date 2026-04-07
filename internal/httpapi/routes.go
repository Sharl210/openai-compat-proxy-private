package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"openai-compat-proxy/internal/cacheinfo"
	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/upstream"
)

type routeInfo struct {
	ProviderID    string
	Legacy        bool
	CanonicalPath string
}

type routeContextKey string

const routeInfoKey routeContextKey = "route-info"
const runtimeSnapshotKey routeContextKey = "runtime-snapshot"
const cacheInfoManagerKey routeContextKey = "cache-info-manager"

func withCacheInfoManager(ctx context.Context, manager *cacheinfo.Manager) context.Context {
	if manager == nil {
		return ctx
	}
	return context.WithValue(ctx, cacheInfoManagerKey, manager)
}

func cacheInfoManagerFromRequest(r *http.Request) *cacheinfo.Manager {
	manager, _ := r.Context().Value(cacheInfoManagerKey).(*cacheinfo.Manager)
	return manager
}

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
			return routeInfo{}, errors.New("route not found")
		}
		if !provider.Enabled {
			return routeInfo{}, errors.New("route not found")
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
	_, providerCfg, _, ok := providerSelectionForRequest(r, "")
	if !ok {
		return config.Config{}
	}
	return providerCfg
}

func providerConfigForID(snapshot *config.RuntimeSnapshot, providerID string) config.Config {
	if snapshot == nil {
		return config.Config{}
	}
	providerCfg := snapshot.Config
	provider, err := snapshot.Config.ProviderByID(providerID)
	if err != nil {
		return providerCfg
	}
	providerCfg.UpstreamBaseURL = provider.UpstreamBaseURL
	providerCfg.UpstreamAPIKey = provider.UpstreamAPIKey
	providerCfg.UpstreamEndpointType = provider.UpstreamEndpointType
	providerCfg.AnthropicVersion = provider.AnthropicVersion
	providerCfg.DownstreamNonStreamStrategy = provider.EffectiveDownstreamNonStreamStrategy(snapshot.Config.DownstreamNonStreamStrategy)
	if provider.UpstreamFirstByteTimeout > 0 {
		providerCfg.FirstByteTimeout = provider.UpstreamFirstByteTimeout
	}
	providerCfg.UpstreamRetryCount = provider.UpstreamRetryCount
	providerCfg.UpstreamRetryDelay = provider.UpstreamRetryDelay
	if provider.UpstreamUserAgent != "" {
		providerCfg.UpstreamUserAgent = provider.UpstreamUserAgent
	}
	if provider.MasqueradeTarget != "" {
		providerCfg.MasqueradeTarget = provider.MasqueradeTarget
	}
	if provider.InjectClaudeCodeMetadataUserIDSet {
		providerCfg.InjectClaudeCodeMetadataUserID = provider.InjectClaudeCodeMetadataUserID
	}
	if provider.InjectClaudeCodeSystemPromptSet {
		providerCfg.InjectClaudeCodeSystemPrompt = provider.InjectClaudeCodeSystemPrompt
	}
	if provider.UpstreamThinkingTagStyle != "" {
		providerCfg.UpstreamThinkingTagStyle = provider.UpstreamThinkingTagStyle
	}
	return providerCfg
}

func providerSelectionForRequest(r *http.Request, canonicalModel string) (config.ProviderConfig, config.Config, string, bool) {
	provider, providerCfg, providerID, _, ok := providerSelectionForModelRequest(r, canonicalModel)
	return provider, providerCfg, providerID, ok
}

func providerSelectionForModelRequest(r *http.Request, canonicalModel string) (config.ProviderConfig, config.Config, string, string, bool) {
	snapshot, ok := runtimeSnapshotFromRequest(r)
	if !ok || snapshot == nil {
		return config.ProviderConfig{}, config.Config{}, "", canonicalModel, false
	}
	if info, ok := routeInfoFromRequest(r); ok {
		providerID := info.ProviderID
		resolvedModel := canonicalModel
		if info.Legacy && canonicalModel != "" {
			if resolvedID, modelForProvider, ok := snapshot.ResolveDefaultProviderSelection(canonicalModel); ok {
				providerID = resolvedID
				resolvedModel = modelForProvider
			} else if resolvedID, ok := resolveDefaultProviderSelectionFromRealtimeModels(r, snapshot, canonicalModel); ok {
				providerID = resolvedID
			} else if legacyModelsListEnforced(snapshot) {
				return config.ProviderConfig{}, config.Config{}, "", canonicalModel, false
			}
		}
		if provider, err := snapshot.Config.ProviderByID(providerID); err == nil {
			return provider, providerConfigForID(snapshot, providerID), providerID, resolvedModel, true
		}
	}
	return config.ProviderConfig{}, config.Config{}, "", canonicalModel, false
}

func legacyModelsListEnforced(snapshot *config.RuntimeSnapshot) bool {
	if snapshot == nil {
		return false
	}
	if snapshot.Config.EnableDefaultProviderModelTags {
		return len(snapshot.DefaultVisibleModels) > 0 || len(snapshot.DefaultTaggedVisibleModels) > 0
	}
	return len(snapshot.DefaultVisibleModels) > 0
}

func providerForRequest(r *http.Request) (config.ProviderConfig, bool) {
	provider, _, _, ok := providerSelectionForRequest(r, "")
	return provider, ok
}

func resolveDefaultProviderSelectionFromRealtimeModels(r *http.Request, snapshot *config.RuntimeSnapshot, model string) (string, bool) {
	if snapshot == nil || snapshot.Config.EnableDefaultProviderModelTags || strings.TrimSpace(model) == "" {
		return "", false
	}
	owner := ""
	for _, providerID := range snapshot.DefaultProviderIDs {
		provider, err := snapshot.Config.ProviderByID(providerID)
		if err != nil || !provider.Enabled || !provider.SupportsModels {
			continue
		}
		providerCfg := providerConfigForID(snapshot, providerID)
		authorization, err := authHeaderForOverlayProviderUpstream(r, providerCfg, providerID)
		if err != nil {
			continue
		}
		client := upstream.NewClient(providerCfg.UpstreamBaseURL, providerCfg)
		body, ok := fetchProviderModelsBody(r.Context(), client, authorization, provider)
		if !ok {
			continue
		}
		if modelEntriesContain(decodeModelEntries(body), model) {
			owner = providerID
		}
	}
	if owner == "" {
		return "", false
	}
	return owner, true
}

func modelEntriesContain(entries []map[string]any, model string) bool {
	needle := strings.TrimSpace(model)
	if needle == "" {
		return false
	}
	for _, entry := range entries {
		id, _ := entry["id"].(string)
		if strings.TrimSpace(id) == needle {
			return true
		}
	}
	return false
}
