package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"openai-compat-proxy/internal/cacheinfo"
	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/reasoning"
	"openai-compat-proxy/internal/tokenestimator"
	"openai-compat-proxy/internal/upstream"
)

var defaultOverlayRefreshInterval = 24 * time.Hour

type routeInfo struct {
	ProviderID    string
	Legacy        bool
	CanonicalPath string
}

type defaultOverlayDiscovery struct {
	ProviderID      string
	RawModelID      string
	VisibleModelIDs map[string]struct{}
}

type routeContextKey string

const routeInfoKey routeContextKey = "route-info"
const runtimeSnapshotKey routeContextKey = "runtime-snapshot"
const cacheInfoManagerKey routeContextKey = "cache-info-manager"
const tokenEstimatorManagerKey routeContextKey = "token-estimator-manager"
const responsesHistoryContextKey routeContextKey = "responses-history"
const routeRequestEffortKey routeContextKey = "route-request-effort"
const routeProviderSelectionEffortKey routeContextKey = "route-provider-selection-effort"
const runtimeStoreKey routeContextKey = "runtime-store"
const legacyRoutingModelKey routeContextKey = "legacy-routing-model"
const proxyModelIntentKey routeContextKey = "proxy-model-intent"
const defaultOverlayDiscoveryKey routeContextKey = "default-overlay-discovery"

const (
	canonicalV1ModelsPath            = "/v1/models"
	canonicalV1ResponsesPath         = "/v1/responses"
	canonicalV1ResponsesCompactPath  = "/v1/responses/compact"
	canonicalV1ChatCompletionsPath   = "/v1/chat/completions"
	canonicalV1MessagesPath          = "/v1/messages"
	canonicalV1ImagesGenerationsPath = "/v1/images/generations"
	canonicalV1ImagesEditsPath       = "/v1/images/edits"
	canonicalV1ImagesVariationsPath  = "/v1/images/variations"
	canonicalV1EmbeddingsPath        = "/v1/embeddings"
	canonicalV1RerankPath            = "/v1/rerank"
)

var canonicalV1RoutePaths = []string{
	canonicalV1ModelsPath,
	canonicalV1ResponsesPath,
	canonicalV1ResponsesCompactPath,
	canonicalV1ChatCompletionsPath,
	canonicalV1MessagesPath,
	canonicalV1ImagesGenerationsPath,
	canonicalV1ImagesEditsPath,
	canonicalV1ImagesVariationsPath,
	canonicalV1EmbeddingsPath,
	canonicalV1RerankPath,
}

var publicRouteAliases = map[string]string{
	"/models":             canonicalV1ModelsPath,
	"/responses":          canonicalV1ResponsesPath,
	"/responses/compact":  canonicalV1ResponsesCompactPath,
	"/chat/completions":   canonicalV1ChatCompletionsPath,
	"/messages":           canonicalV1MessagesPath,
	"/images/generations": canonicalV1ImagesGenerationsPath,
	"/images/edits":       canonicalV1ImagesEditsPath,
	"/images/variations":  canonicalV1ImagesVariationsPath,
	"/embeddings":         canonicalV1EmbeddingsPath,
	"/rerank":             canonicalV1RerankPath,
}

func canonicalV1Paths() []string {
	return append([]string(nil), canonicalV1RoutePaths...)
}

func isCanonicalV1Path(path string) bool {
	for _, candidate := range canonicalV1RoutePaths {
		if path == candidate {
			return true
		}
	}
	return false
}

func canonicalPublicRoutePath(path string) (string, bool) {
	path = normalizePublicRoutePath(path)
	if isCanonicalV1Path(path) {
		return path, true
	}
	canonicalPath, ok := publicRouteAliases[path]
	return canonicalPath, ok
}

func normalizePublicRoutePath(path string) string {
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' })
	if len(parts) == 0 {
		return "/"
	}
	return "/" + strings.Join(parts, "/")
}

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

func withTokenEstimatorManager(ctx context.Context, manager *tokenestimator.Manager) context.Context {
	if manager == nil {
		return ctx
	}
	return context.WithValue(ctx, tokenEstimatorManagerKey, manager)
}

func tokenEstimatorManagerFromRequest(r *http.Request) *tokenestimator.Manager {
	manager, _ := r.Context().Value(tokenEstimatorManagerKey).(*tokenestimator.Manager)
	return manager
}

func withResponsesHistory(ctx context.Context, history *responsesHistoryStore) context.Context {
	if history == nil {
		return ctx
	}
	return context.WithValue(ctx, responsesHistoryContextKey, history)
}

func responsesHistoryFromRequest(r *http.Request) *responsesHistoryStore {
	history, _ := r.Context().Value(responsesHistoryContextKey).(*responsesHistoryStore)
	return history
}

func resolveRouteInfo(path string, cfg config.Config) (routeInfo, error) {
	path = normalizePublicRoutePath(path)
	if canonicalPath, ok := canonicalPublicRoutePath(path); ok {
		if !cfg.EnableLegacyV1Routes {
			return routeInfo{}, errors.New("route not found")
		}
		if len(cfg.Providers) == 0 {
			return routeInfo{Legacy: true, CanonicalPath: canonicalPath}, nil
		}
		provider, err := cfg.DefaultProviderConfig()
		if err != nil {
			return routeInfo{}, errors.New("route not found")
		}
		if !provider.Enabled {
			return routeInfo{}, errors.New("route not found")
		}
		return routeInfo{ProviderID: provider.ID, Legacy: true, CanonicalPath: canonicalPath}, nil
	}

	trimmed := strings.Trim(path, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 2 {
		return routeInfo{}, errors.New("route not found")
	}
	providerID := parts[0]
	canonicalPath, ok := canonicalPublicRoutePath("/" + strings.Join(parts[1:], "/"))
	if !ok {
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

func withRuntimeStore(ctx context.Context, store *config.RuntimeStore) context.Context {
	if store == nil {
		return ctx
	}
	return context.WithValue(ctx, runtimeStoreKey, store)
}

func routeInfoFromRequest(r *http.Request) (routeInfo, bool) {
	info, ok := r.Context().Value(routeInfoKey).(routeInfo)
	return info, ok
}

func requestEffortFromRouteContext(r *http.Request) string {
	if r == nil {
		return ""
	}
	if value, ok := r.Context().Value(routeRequestEffortKey).(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func providerSelectionEffortFromRouteContext(r *http.Request) string {
	if r == nil {
		return ""
	}
	if value, ok := r.Context().Value(routeProviderSelectionEffortKey).(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func runtimeSnapshotFromRequest(r *http.Request) (*config.RuntimeSnapshot, bool) {
	snapshot, ok := r.Context().Value(runtimeSnapshotKey).(*config.RuntimeSnapshot)
	return snapshot, ok
}

func runtimeStoreFromRequest(r *http.Request) *config.RuntimeStore {
	store, _ := r.Context().Value(runtimeStoreKey).(*config.RuntimeStore)
	return store
}

func legacyRoutingModelFromRequest(r *http.Request) (string, bool) {
	model, ok := r.Context().Value(legacyRoutingModelKey).(string)
	return model, ok
}

func withProxyModelIntent(ctx context.Context, intent model.ProxyModelIntent) context.Context {
	return context.WithValue(ctx, proxyModelIntentKey, intent)
}

func proxyModelIntentFromRequest(r *http.Request) (model.ProxyModelIntent, bool) {
	if r == nil {
		return model.ProxyModelIntent{}, false
	}
	intent, ok := r.Context().Value(proxyModelIntentKey).(model.ProxyModelIntent)
	return intent, ok
}

func withDefaultOverlayDiscovery(ctx context.Context, discovery defaultOverlayDiscovery) context.Context {
	return context.WithValue(ctx, defaultOverlayDiscoveryKey, discovery)
}

func defaultOverlayDiscoveryFromRequest(r *http.Request) (defaultOverlayDiscovery, bool) {
	if r == nil {
		return defaultOverlayDiscovery{}, false
	}
	discovery, ok := r.Context().Value(defaultOverlayDiscoveryKey).(defaultOverlayDiscovery)
	return discovery, ok
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
	providerCfg.AnthropicMaxThinkingBudget = provider.AnthropicMaxThinkingBudget
	providerCfg.UpstreamMaxOutputTokens = provider.UpstreamMaxOutputTokens
	providerCfg.UpstreamMaxOutputTokenRules = provider.UpstreamMaxOutputTokenRules
	providerCfg.ForceUpstreamMaxOutputTokens = provider.ForceUpstreamMaxOutputTokens
	providerCfg.ResponsesToolCompatMode = provider.ResponsesToolCompatMode
	providerCfg.AnthropicVersion = provider.AnthropicVersion
	providerCfg.DownstreamNonStreamStrategy = provider.EffectiveDownstreamNonStreamStrategy(snapshot.Config.DownstreamNonStreamStrategy)
	providerCfg.EnableNoPromptModelSuffix = provider.EffectiveNoPromptModelSuffix(snapshot.Config.EnableNoPromptModelSuffix)
	if provider.UpstreamFirstByteTimeout > 0 {
		providerCfg.FirstByteTimeout = provider.UpstreamFirstByteTimeout
	}
	if provider.UpstreamStreamOpenTimeout > 0 {
		providerCfg.StreamOpenTimeout = provider.UpstreamStreamOpenTimeout
	}
	providerCfg.UpstreamRetryCount = provider.UpstreamRetryCount
	providerCfg.UpstreamRetryDelay = provider.UpstreamRetryDelay
	providerCfg.UpstreamCacheControl = provider.UpstreamCacheControl
	if provider.UpstreamUserAgent != "" {
		providerCfg.UpstreamUserAgent = provider.UpstreamUserAgent
	}
	if provider.MasqueradeClientVersion != "" {
		providerCfg.UpstreamMasqueradeClientVersion = provider.MasqueradeClientVersion
	}
	if provider.MasqueradeTarget != "" {
		providerCfg.MasqueradeTarget = provider.MasqueradeTarget
	}
	if provider.InjectClaudeCodeMetadataUserIDSet {
		providerCfg.InjectClaudeCodeMetadataUserID = provider.InjectClaudeCodeMetadataUserID
	}
	if provider.ClaudeCodeMetadataDeviceIDSet || provider.ClaudeCodeMetadataDeviceID != "" {
		providerCfg.ClaudeCodeMetadataDeviceID = provider.ClaudeCodeMetadataDeviceID
	}
	if provider.ClaudeCodeMetadataAccountUUIDSet || provider.ClaudeCodeMetadataAccountUUID != "" {
		providerCfg.ClaudeCodeMetadataAccountUUID = provider.ClaudeCodeMetadataAccountUUID
	}
	if strings.TrimSpace(providerCfg.ClaudeCodeMetadataDeviceID) == "" {
		providerCfg.ClaudeCodeMetadataDeviceID = config.DefaultClaudeCodeMetadataDeviceID(providerID)
	}
	if strings.TrimSpace(providerCfg.ClaudeCodeMetadataAccountUUID) == "" {
		providerCfg.ClaudeCodeMetadataAccountUUID = config.DefaultClaudeCodeMetadataAccountUUID(providerID)
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
	provider, providerCfg, providerID, _, ok, _ := providerSelectionForModelRequest(r, canonicalModel)
	return provider, providerCfg, providerID, ok
}

func providerSelectionForModelRequest(r *http.Request, canonicalModel string) (config.ProviderConfig, config.Config, string, string, bool, error) {
	snapshot, ok := runtimeSnapshotFromRequest(r)
	if !ok || snapshot == nil {
		return config.ProviderConfig{}, config.Config{}, "", canonicalModel, false, nil
	}
	if info, ok := routeInfoFromRequest(r); ok {
		providerID := info.ProviderID
		originalModel := canonicalModel
		resolvedModel := canonicalModel
		resolvedModelIsInternal := false
		rootModelMapped := false
		if info.Legacy && canonicalModel != "" {
			if rootIntent, mapped := resolveV1ProxyModelIntentForLegacyRequest(r, canonicalModel); mapped {
				canonicalModel = rootIntent.CanonicalModel()
				rootModelMapped = true
			} else {
				snapshot, _ = runtimeSnapshotFromRequest(r)
				canonicalModel = snapshot.Config.ResolveV1ModelForRequest(canonicalModel, requestEffortFromRouteContext(r))
			}
			resolvedModel = canonicalModel
			*r = *r.Clone(context.WithValue(r.Context(), legacyRoutingModelKey, canonicalModel))
			snapshot, _ = runtimeSnapshotFromRequest(r)
			if resolvedID, modelForProvider, intent, ok := snapshot.ResolveDefaultProviderSelectionForProxyModel(canonicalModel); ok {
				providerID = resolvedID
				resolvedModel = modelForProvider
				resolvedModelIsInternal = true
				if rootModelMapped {
					intent.HasModelMapAlias = true
				}
				*r = *r.Clone(withProxyModelIntent(r.Context(), intent))
			} else if resolvedID, modelForProvider, ok := snapshot.ResolveDefaultProviderSelectionForRequestEffort(canonicalModel, providerSelectionEffortFromRouteContext(r)); ok {
				providerID = resolvedID
				resolvedModel = modelForProvider
				resolvedModelIsInternal = true
			} else if legacyModelsListEnforced(snapshot) && hasProxyModelTail(canonicalModel) {
				return config.ProviderConfig{}, config.Config{}, "", canonicalModel, false, nil
			} else if discovery, realtimeErr, ok := resolveDefaultProviderSelectionFromRealtimeModels(r, snapshot, canonicalModel); ok {
				providerID = discovery.ProviderID
				resolvedModel = discovery.RawModelID
				resolvedModelIsInternal = true
				*r = *r.Clone(withDefaultOverlayDiscovery(r.Context(), discovery))
			} else if realtimeErr != nil {
				return config.ProviderConfig{}, config.Config{}, "", canonicalModel, false, realtimeErr
			} else if legacyModelsListEnforced(snapshot) && snapshot.Config.EnableDefaultProviderModelTags {
				return config.ProviderConfig{}, config.Config{}, "", canonicalModel, false, nil
			} else if snapshot.Config.EnableNoPromptModelSuffix {
				strippedModel, stripped := stripNoPromptModelSuffix(canonicalModel)
				if !stripped {
					if shouldBypassUsageRecorderForRequest(r) {
						if len(snapshot.DefaultProviderIDs) > 0 {
							providerID = snapshot.DefaultProviderIDs[len(snapshot.DefaultProviderIDs)-1]
						}
					} else if legacyModelsListEnforced(snapshot) {
						return config.ProviderConfig{}, config.Config{}, "", canonicalModel, false, nil
					}
				} else if resolvedID, modelForProvider, strippedOK := snapshot.ResolveDefaultProviderSelection(strippedModel); strippedOK {
					if provider, err := snapshot.Config.ProviderByID(resolvedID); err == nil && provider.EffectiveNoPromptModelSuffix(snapshot.Config.EnableNoPromptModelSuffix) && !provider.HidesModel(canonicalModel) {
						providerID = resolvedID
						resolvedModel = modelForProvider
						resolvedModelIsInternal = true
					} else if legacyModelsListEnforced(snapshot) {
						return config.ProviderConfig{}, config.Config{}, "", canonicalModel, false, nil
					}
				} else if legacyModelsListEnforced(snapshot) {
					return config.ProviderConfig{}, config.Config{}, "", canonicalModel, false, nil
				}
			} else if shouldBypassUsageRecorderForRequest(r) {
				if len(snapshot.DefaultProviderIDs) > 0 {
					providerID = snapshot.DefaultProviderIDs[len(snapshot.DefaultProviderIDs)-1]
				}
			} else if legacyModelsListEnforced(snapshot) {
				return config.ProviderConfig{}, config.Config{}, "", canonicalModel, false, nil
			}
		}
		if provider, err := snapshot.Config.ProviderByID(providerID); err == nil {
			if info.Legacy && hasNoPromptModelSuffix(originalModel) {
				discovery, exactRawModel := defaultOverlayDiscoveryFromRequest(r)
				if (!exactRawModel || discovery.ProviderID != providerID || discovery.RawModelID != originalModel) && (!provider.EffectiveNoPromptModelSuffix(snapshot.Config.EnableNoPromptModelSuffix) || provider.HidesModel(originalModel)) {
					return config.ProviderConfig{}, config.Config{}, "", originalModel, false, nil
				}
			}
			if canonicalModel != "" && !resolvedModelIsInternal {
				if internalModel, ok := provider.InternalModelID(resolvedModel, info.Legacy); ok {
					if intent, parsed := provider.ParseProxyModelIntentWithReasoningMode(internalModel, snapshot.Config.EnableNoPromptModelSuffix, snapshot.Config.EffectiveEnableReasoningModeSuffix()); parsed {
						matchedAlias := provider.ProxyModelIntentAllowsAlias(intent)
						if mappedIntent, mapped := provider.ResolveMappedProxyModelIntent(intent); mapped {
							intent = mappedIntent
						}
						if matchedAlias || provider.AllowsInternalProxyModelIntent(intent, snapshot.Config.EnableNoPromptModelSuffix) {
							resolvedModel = config.ProxyModelIntentRoutingModel(intent)
							*r = *r.Clone(withProxyModelIntent(r.Context(), intent))
						} else {
							return config.ProviderConfig{}, config.Config{}, "", resolvedModel, false, nil
						}
					} else if provider.HasProxyModelIntentCandidatePrefix(internalModel) {
						return config.ProviderConfig{}, config.Config{}, "", resolvedModel, false, nil
					} else {
						resolvedModel = internalModel
					}
				} else {
					return config.ProviderConfig{}, config.Config{}, "", resolvedModel, false, nil
				}
			}
			return provider, providerConfigForID(snapshot, providerID), providerID, resolvedModel, true, nil
		}
	}
	return config.ProviderConfig{}, config.Config{}, "", canonicalModel, false, nil
}

func resolveV1ProxyModelIntentForLegacyRequest(r *http.Request, modelName string) (model.ProxyModelIntent, bool) {
	refreshDefaultProviderOverlayCacheFromRequest(r)
	snapshot, ok := runtimeSnapshotFromRequest(r)
	if !ok || snapshot == nil {
		return model.ProxyModelIntent{}, false
	}
	return snapshot.Config.ResolveV1ProxyModelIntentWithTargetCandidates(modelName, snapshot.DefaultVisibleModels)
}

func refreshDefaultProviderOverlayCacheFromRequest(r *http.Request) {
	refreshDefaultProviderOverlayCacheFromRequestWithRefresh(r, refreshDefaultProviderOverlayCache)
}

func refreshDefaultProviderOverlayCacheFromRequestWithRefresh(r *http.Request, refresh func(*config.RuntimeStore, time.Time) error) {
	if r == nil {
		return
	}
	snapshot, ok := runtimeSnapshotFromRequest(r)
	if !ok || snapshot == nil {
		return
	}
	store := runtimeStoreFromRequest(r)
	if store == nil {
		return
	}
	active := store.Active()
	if err := refresh(store, time.Now()); err != nil {
		if active != nil {
			*r = *r.Clone(withRuntimeSnapshot(r.Context(), active))
		}
		return
	}
	if latest := store.Active(); latest != nil {
		*r = *r.Clone(withRuntimeSnapshot(r.Context(), latest))
	}
}

func refreshDefaultProviderOverlayCache(store *config.RuntimeStore, now time.Time) error {
	if store == nil {
		return nil
	}
	snapshot := store.Active()
	if snapshot == nil {
		return nil
	}
	if !shouldRefreshDefaultOverlayCache(snapshot, now) {
		return nil
	}
	entries, owners, taggedOwners, taggedVisible, rawIDs, taggedRawIDs, err := fetchLatestDefaultOverlay(snapshot)
	if err != nil {
		return err
	}
	store.UpdateDefaultOverlayIndex(owners, entries, taggedOwners, taggedVisible, rawIDs, taggedRawIDs)
	return nil
}

func shouldRefreshDefaultOverlayCache(snapshot *config.RuntimeSnapshot, now time.Time) bool {
	if snapshot == nil {
		return false
	}
	if len(snapshot.DefaultProviderIDs) <= 1 && !snapshot.Config.EnableDefaultProviderModelTags {
		return false
	}
	if now.Sub(snapshot.RootEnvMTime) >= defaultOverlayRefreshInterval {
		return true
	}
	return false
}

func fetchLatestDefaultOverlay(snapshot *config.RuntimeSnapshot) ([]string, map[string]string, map[string]string, []string, map[string]string, map[string]string, error) {
	if snapshot == nil {
		return nil, nil, nil, nil, nil, nil, nil
	}
	owners := make(map[string]string)
	rawIDs := make(map[string]string)
	taggedOwners := make(map[string]string)
	taggedRawIDs := make(map[string]string)
	visibleByProvider := make(map[string][]string, len(snapshot.DefaultProviderIDs))
	externalByProvider := make(map[string]map[string]string, len(snapshot.DefaultProviderIDs))
	modelCount := make(map[string]int)
	for _, id := range snapshot.DefaultProviderIDs {
		provider, err := snapshot.Config.ProviderByID(id)
		if err != nil || !provider.Enabled {
			continue
		}
		visible := provider.VisibleModelIDs()
		visibleByProvider[id] = visible
		externalByProvider[id] = make(map[string]string, len(visible))
		for _, modelID := range visible {
			externalID := provider.ExternalModelID(modelID, true)
			externalByProvider[id][modelID] = externalID
			modelCount[externalID]++
			taggedID := taggedModelID(id, externalID)
			taggedOwners[taggedID] = id
			taggedRawIDs[taggedID] = modelID
		}
	}
	visible := make([]string, 0, len(modelCount))
	seen := make(map[string]struct{}, len(modelCount))
	if snapshot.Config.EnableDefaultProviderModelTags {
		for i := len(snapshot.DefaultProviderIDs) - 1; i >= 0; i-- {
			id := snapshot.DefaultProviderIDs[i]
			for _, modelID := range visibleByProvider[id] {
				externalID := externalByProvider[id][modelID]
				visibleID := externalID
				if snapshot.Config.EnableAllDefaultProviderModelTags || modelCount[externalID] > 1 {
					visibleID = taggedModelID(id, externalID)
				}
				if _, ok := seen[visibleID]; ok {
					continue
				}
				seen[visibleID] = struct{}{}
				owners[visibleID] = id
				rawIDs[visibleID] = modelID
				visible = append(visible, visibleID)
			}
		}
	} else {
		for _, id := range snapshot.DefaultProviderIDs {
			for _, modelID := range visibleByProvider[id] {
				externalID := externalByProvider[id][modelID]
				owners[externalID] = id
				rawIDs[externalID] = modelID
			}
		}
		for i := len(snapshot.DefaultProviderIDs) - 1; i >= 0; i-- {
			id := snapshot.DefaultProviderIDs[i]
			for _, modelID := range visibleByProvider[id] {
				externalID := externalByProvider[id][modelID]
				if owners[externalID] != id {
					continue
				}
				if _, ok := seen[externalID]; ok {
					continue
				}
				seen[externalID] = struct{}{}
				visible = append(visible, externalID)
			}
		}
	}
	taggedVisible := make([]string, 0, len(taggedOwners))
	for i := len(snapshot.DefaultProviderIDs) - 1; i >= 0; i-- {
		id := snapshot.DefaultProviderIDs[i]
		for _, modelID := range visibleByProvider[id] {
			taggedVisible = append(taggedVisible, taggedModelID(id, externalByProvider[id][modelID]))
		}
	}
	return visible, owners, taggedOwners, taggedVisible, rawIDs, taggedRawIDs, nil
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

func resolveDefaultProviderSelectionFromRealtimeModels(r *http.Request, snapshot *config.RuntimeSnapshot, modelName string) (defaultOverlayDiscovery, error, bool) {
	if snapshot == nil || snapshot.Config.EnableDefaultProviderModelTags || strings.TrimSpace(modelName) == "" {
		return defaultOverlayDiscovery{}, nil, false
	}
	discovery := defaultOverlayDiscovery{}
	exactRawFound := false
	var upstreamErr error
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
		body, ok, err := fetchProviderModelsBody(r.Context(), client, authorization, provider)
		if err != nil {
			upstreamErr = err
		}
		if !ok {
			continue
		}
		entries := decodeModelEntries(body)
		if modelEntriesContain(entries, modelName) {
			discovery = defaultOverlayDiscovery{
				ProviderID:      providerID,
				RawModelID:      modelName,
				VisibleModelIDs: rawModelIDSet(entries),
			}
			exactRawFound = true
			continue
		}
		if exactRawFound || hasProxyModelTail(modelName) {
			continue
		}
		if modelEntriesAllowModel(entries, modelName, provider, providerCfg.EnableNoPromptModelSuffix) {
			discovery = defaultOverlayDiscovery{
				ProviderID:      providerID,
				RawModelID:      modelName,
				VisibleModelIDs: rawModelIDSet(entries),
			}
		}
	}
	if discovery.ProviderID == "" {
		return defaultOverlayDiscovery{}, upstreamErr, false
	}
	return discovery, nil, true
}

func rawModelIDSet(entries []map[string]any) map[string]struct{} {
	modelIDs := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		modelID, _ := entry["id"].(string)
		modelID = strings.TrimSpace(modelID)
		if modelID != "" {
			modelIDs[modelID] = struct{}{}
		}
	}
	return modelIDs
}

func hasProxyModelTail(modelName string) bool {
	original := strings.TrimSpace(modelName)
	current := original
	for current != "" {
		if baseModel, _, ok := model.SplitReasoningEffortSuffix(current); ok {
			current = baseModel
			continue
		}
		if strings.HasSuffix(current, "-pro") {
			current = strings.TrimSuffix(current, "-pro")
			continue
		}
		if strings.HasSuffix(current, "-noprompt") {
			current = strings.TrimSuffix(current, "-noprompt")
			continue
		}
		return current != original
	}
	return false
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

func modelEntriesAllowModel(entries []map[string]any, model string, provider config.ProviderConfig, enableNoPrompt bool) bool {
	if modelEntriesContain(entries, model) {
		return true
	}
	if strippedModel, stripped := stripNoPromptModelSuffix(model); stripped {
		return enableNoPrompt && !provider.HidesModel(model) && modelEntriesAllowModel(entries, strippedModel, provider, enableNoPrompt)
	}
	baseModel, _, ok := reasoning.SplitSuffix(model)
	if !ok || (!provider.EnableReasoningEffortSuffix && !provider.HasManualReasonSuffixForModel(model)) || provider.HidesModel(model) {
		return false
	}
	return modelEntriesContain(entries, baseModel)
}

func taggedModelID(providerID string, modelID string) string {
	return "[" + providerID + "]" + modelID
}
