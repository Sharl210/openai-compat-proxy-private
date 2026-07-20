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
	ProviderID             string
	RequestedModelID       string
	RawModelID             string
	VisibleModelIDs        map[string]struct{}
	SourceProxyModelIntent model.ProxyModelIntent
	ProxyModelIntent       model.ProxyModelIntent
	HasProxyModelIntent    bool
	ExactLiteral           bool
	InvalidProxyTail       bool
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
const upstreamTransportPoolKey routeContextKey = "upstream-transport-pool"
const inboundCallerIdentityKey routeContextKey = "inbound-caller-identity"

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

func withInboundCallerIdentity(ctx context.Context, identity string) context.Context {
	return context.WithValue(ctx, inboundCallerIdentityKey, identity)
}

func inboundCallerIdentityFromRequest(r *http.Request) string {
	if r == nil {
		return "anonymous"
	}
	identity, _ := r.Context().Value(inboundCallerIdentityKey).(string)
	if identity == "" {
		return "anonymous"
	}
	return identity
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

func withUpstreamTransportPool(ctx context.Context, pool *upstream.TransportPool) context.Context {
	if pool == nil {
		return ctx
	}
	return context.WithValue(ctx, upstreamTransportPoolKey, pool)
}

func upstreamClientForProvider(r *http.Request, providerID string, providerCfg config.Config) *upstream.Client {
	pool, _ := r.Context().Value(upstreamTransportPoolKey).(*upstream.TransportPool)
	if pool == nil {
		return upstream.NewClient(providerCfg.UpstreamBaseURL, providerCfg)
	}
	transports := pool.Get(providerID, providerCfg.UpstreamBaseURL, providerCfg)
	return upstream.NewClientWithOptions(upstream.ClientOptions{BaseURL: providerCfg.UpstreamBaseURL, Config: providerCfg, Transports: transports})
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
	info, ok := routeInfoFromRequest(r)
	if !ok {
		return config.ProviderConfig{}, config.Config{}, "", canonicalModel, false, nil
	}
	providerID := info.ProviderID
	resolvedModel := canonicalModel
	resolvedModelIsInternal := false
	selectedProxyIntent := model.ProxyModelIntent{}
	hasSelectedProxyIntent := false
	usedDefaultOverlayDiscovery := false
	usedDefaultProviderFallback := false
	if !info.Legacy && canonicalModel != "" {
		if discovery, discovered := defaultOverlayDiscoveryFromRequest(r); discovered && discovery.ProviderID == providerID && discovery.RequestedModelID == canonicalModel {
			if discovery.InvalidProxyTail {
				return config.ProviderConfig{}, config.Config{}, "", canonicalModel, false, nil
			}
			resolvedModel = strings.TrimSpace(discovery.RawModelID)
			if resolvedModel == "" {
				resolvedModel = config.ProxyModelIntentRoutingModel(discovery.SourceProxyModelIntent)
			}
			resolvedModelIsInternal = true
			if discovery.HasProxyModelIntent {
				selectedProxyIntent = discovery.SourceProxyModelIntent
				hasSelectedProxyIntent = true
				*r = *r.Clone(withProxyModelIntent(r.Context(), selectedProxyIntent))
			}
		}
	}
	if info.Legacy && canonicalModel != "" {
		if discovery, discovered := defaultOverlayDiscoveryFromRequest(r); discovered && discovery.ProviderID != "" && discovery.RequestedModelID == canonicalModel {
			if discovery.InvalidProxyTail {
				return config.ProviderConfig{}, config.Config{}, "", canonicalModel, false, nil
			}
			providerID = discovery.ProviderID
			resolvedModel = strings.TrimSpace(discovery.RawModelID)
			if resolvedModel == "" {
				resolvedModel = config.ProxyModelIntentRoutingModel(discovery.SourceProxyModelIntent)
			}
			resolvedModelIsInternal = true
			if discovery.HasProxyModelIntent {
				selectedProxyIntent = discovery.SourceProxyModelIntent
				hasSelectedProxyIntent = true
			}
			usedDefaultOverlayDiscovery = true
			ctx := context.WithValue(r.Context(), legacyRoutingModelKey, resolvedModel)
			if hasSelectedProxyIntent {
				ctx = withProxyModelIntent(ctx, selectedProxyIntent)
			}
			*r = *r.Clone(ctx)
		} else if rootIntent, mapped := snapshot.Config.ResolveV1ProxyModelIntentWithTargetCandidates(canonicalModel, defaultOverlayRoutingModelCandidates(snapshot)); mapped {
			resolvedModel = rootIntent.CanonicalModel()
			selectedProxyIntent = rootIntent
			hasSelectedProxyIntent = true
			resolvedModelIsInternal = true
			*r = *r.Clone(withProxyModelIntent(r.Context(), rootIntent))
		} else {
			resolvedModel = snapshot.Config.ResolveV1ModelForRequest(canonicalModel, providerSelectionEffortFromRouteContext(r))
		}
		if !usedDefaultOverlayDiscovery {
			*r = *r.Clone(context.WithValue(r.Context(), legacyRoutingModelKey, resolvedModel))
			if taggedProviderID, taggedModel, tagged, valid := realtimeOverlayRequestedModel(snapshot, resolvedModel); !valid && strings.HasPrefix(strings.TrimSpace(resolvedModel), "[") {
				return config.ProviderConfig{}, config.Config{}, "", resolvedModel, false, nil
			} else if discovery, discovered := defaultOverlayDiscoveryFromRequest(r); discovered && discovery.ProviderID != "" && discovery.RequestedModelID == resolvedModel {
				if discovery.InvalidProxyTail {
					return config.ProviderConfig{}, config.Config{}, "", resolvedModel, false, nil
				}
				providerID = discovery.ProviderID
				resolvedModel = strings.TrimSpace(discovery.RawModelID)
				if resolvedModel == "" {
					resolvedModel = config.ProxyModelIntentRoutingModel(discovery.SourceProxyModelIntent)
				}
				resolvedModelIsInternal = true
				if discovery.HasProxyModelIntent {
					selectedProxyIntent = discovery.SourceProxyModelIntent
					hasSelectedProxyIntent = true
					*r = *r.Clone(withProxyModelIntent(r.Context(), selectedProxyIntent))
				}
			} else if tagged {
				providerID = taggedProviderID
				resolvedModel = taggedModel
			} else if resolvedID, modelForProvider, intent, matched := configuredDefaultProviderSelection(snapshot, resolvedModel, providerSelectionEffortFromRouteContext(r)); matched {
				providerID = resolvedID
				resolvedModel = modelForProvider
				resolvedModelIsInternal = true
				if strings.TrimSpace(intent.BaseModel) != "" {
					selectedProxyIntent = intent
					hasSelectedProxyIntent = true
					*r = *r.Clone(withProxyModelIntent(r.Context(), intent))
				}
			} else if len(snapshot.DefaultProviderIDs) > 0 {
				providerID = snapshot.DefaultProviderIDs[len(snapshot.DefaultProviderIDs)-1]
				usedDefaultProviderFallback = true
			}
		}
	}
	provider, err := snapshot.Config.ProviderByID(providerID)
	if err != nil || !provider.Enabled {
		return config.ProviderConfig{}, config.Config{}, "", resolvedModel, false, nil
	}
	if strings.TrimSpace(resolvedModel) == "" {
		return provider, providerConfigForID(snapshot, providerID), providerID, "", true, nil
	}
	internalModel := resolvedModel
	if !resolvedModelIsInternal {
		var valid bool
		internalModel, valid = provider.InternalModelID(resolvedModel, info.Legacy)
		if !valid {
			return config.ProviderConfig{}, config.Config{}, "", resolvedModel, false, nil
		}
	}
	intent := selectedProxyIntent
	parsed := hasSelectedProxyIntent
	if !parsed {
		intent, parsed = parseProviderProxyModelIntentForRouting(provider, internalModel, snapshot.Config.EnableNoPromptModelSuffix, snapshot.Config.EffectiveEnableReasoningModeSuffix())
	}
	if parsed {
		if provider.HidesModel(intent.CanonicalModel()) {
			return config.ProviderConfig{}, config.Config{}, "", internalModel, false, nil
		}
		sourceIntent := intent
		if mappedIntent, mapped := provider.ResolveMappedProxyModelIntent(intent); mapped {
			intent = mappedIntent
		}
		if provider.HidesModel(intent.CanonicalModel()) {
			return config.ProviderConfig{}, config.Config{}, "", internalModel, false, nil
		}
		resolvedModel = config.ProxyModelIntentRoutingModel(sourceIntent)
		*r = *r.Clone(withProxyModelIntent(r.Context(), intent))
	} else if provider.HasProxyModelIntentCandidatePrefix(internalModel) ||
		provider.HidesModel(internalModel) ||
		(usedDefaultProviderFallback && !defaultFallbackAllowsUnconfiguredProxyTail(provider, internalModel, providerSelectionEffortFromRouteContext(r), snapshot.Config.EnableNoPromptModelSuffix)) {
		return config.ProviderConfig{}, config.Config{}, "", internalModel, false, nil
	} else {
		requestEffort := providerSelectionEffortFromRouteContext(r)
		mappedByProvider := providerModelMapMatches(provider, internalModel, requestEffort, snapshot.Config.EnableNoPromptModelSuffix)
		if mappedModel, mappedEffort := provider.ResolveModelAndEffortWithRequestEffort(internalModel, requestEffort, provider.EnableReasoningEffortSuffix); mappedModel != internalModel || mappedEffort != "" {
			if mappedByProvider {
				resolvedModel = internalModel
			} else {
				resolvedModel = mappedModel
			}
			if mappedEffort != "" && !mappedByProvider {
				intent = model.ProxyModelIntent{BaseModel: mappedModel, ReasoningEffort: mappedEffort}
				*r = *r.Clone(withProxyModelIntent(r.Context(), intent))
			}
		} else {
			resolvedModel = internalModel
		}
	}
	return provider, providerConfigForID(snapshot, providerID), providerID, resolvedModel, true, nil
}

func resolveDefaultOverlayDiscoveryBeforeProviderSelection(r *http.Request, canonicalModel string) {
	if r == nil || strings.TrimSpace(canonicalModel) == "" {
		return
	}
	snapshot, ok := runtimeSnapshotFromRequest(r)
	if !ok || snapshot == nil {
		return
	}
	info, ok := routeInfoFromRequest(r)
	if !ok || !info.Legacy {
		return
	}

	if _, mapped := snapshot.Config.ResolveV1ProxyModelIntent(canonicalModel); mapped || v1ModelMapMatchesForRouting(snapshot, r, canonicalModel) {
		return
	}
	_, _ = resolveDefaultOverlayDiscoveryForModel(r, snapshot, canonicalModel)
}

func resolveProviderModelDiscoveryBeforeProviderSelection(r *http.Request, canonicalModel string) {
	resolveDefaultOverlayDiscoveryBeforeProviderSelection(r, canonicalModel)
	if r == nil || strings.TrimSpace(canonicalModel) == "" {
		return
	}
	snapshot, ok := runtimeSnapshotFromRequest(r)
	if !ok || snapshot == nil {
		return
	}
	info, ok := routeInfoFromRequest(r)
	if !ok || info.Legacy {
		return
	}
	provider, err := snapshot.Config.ProviderByID(info.ProviderID)
	if err != nil || !provider.Enabled {
		return
	}
	internalModel, ok := provider.InternalModelID(canonicalModel, false)
	if !ok || providerModelIsHidden(provider, internalModel, providerSelectionEffortFromRouteContext(r)) {
		return
	}
	configured := explicitProviderModelHasConfiguredRoute(provider, internalModel, snapshot.Config.EnableNoPromptModelSuffix, snapshot.Config.EffectiveEnableReasoningModeSuffix(), providerSelectionEffortFromRouteContext(r))
	if configured && !explicitProviderModelMayContainProxyIntent(provider, canonicalModel, false) {
		return
	}
	discovery, _, resolved := resolveExplicitProviderSelectionFromRealtimeModels(r, snapshot, info.ProviderID, provider, canonicalModel)
	if resolved && (discovery.ExactLiteral || !configured) {
		*r = *r.Clone(withDefaultOverlayDiscovery(r.Context(), discovery))
	}
}

func explicitProviderModelHasConfiguredRoute(provider config.ProviderConfig, internalModel string, rootNoPrompt bool, rootReasoningMode bool, requestEffort string) bool {
	if providerRoutingModelIsExactLiteral(provider, internalModel) {
		return true
	}
	if intent, parsed := parseProviderProxyModelIntentForRouting(provider, internalModel, rootNoPrompt, rootReasoningMode); parsed {
		return !provider.HidesModel(intent.CanonicalModel())
	}
	return providerModelMapMatches(provider, internalModel, requestEffort, rootNoPrompt)
}

func resolveDefaultOverlayDiscoveryForModel(r *http.Request, snapshot *config.RuntimeSnapshot, modelName string) (bool, error) {
	taggedProviderID, externalModel, tagged, valid := realtimeOverlayRequestedModel(snapshot, modelName)
	if !valid {
		return false, nil
	}
	mayContainProxyIntent := defaultOverlayModelMayContainProxyIntent(snapshot, modelName)
	if snapshot.Config.EnableDefaultProviderModelTags {
		if !tagged {
			return false, nil
		}
		if !mayContainProxyIntent {
			taggedSnapshot := *snapshot
			taggedSnapshot.DefaultProviderIDs = []string{taggedProviderID}
			if _, _, _, configured := configuredDefaultProviderSelection(&taggedSnapshot, externalModel, providerSelectionEffortFromRouteContext(r)); configured {
				return false, nil
			}
		}
	} else if !mayContainProxyIntent {
		if _, _, _, configured := configuredDefaultProviderSelection(snapshot, modelName, providerSelectionEffortFromRouteContext(r)); configured {
			return false, nil
		}
	}
	discovery, err, resolved := resolveDefaultProviderSelectionFromRealtimeModels(r, snapshot, modelName)
	if err != nil {
		return false, err
	}
	if resolved {
		*r = *r.Clone(withDefaultOverlayDiscovery(r.Context(), discovery))
	}
	return resolved, nil
}

func v1ModelMapMatchesForRouting(snapshot *config.RuntimeSnapshot, r *http.Request, modelName string) bool {
	if snapshot == nil || len(snapshot.Config.V1ModelMap) == 0 {
		return false
	}
	provider := config.ProviderConfig{ModelMap: snapshot.Config.V1ModelMap}
	return providerModelMapMatches(provider, modelName, providerSelectionEffortFromRouteContext(r), snapshot.Config.EnableNoPromptModelSuffix)
}

func configuredDefaultProviderSelection(snapshot *config.RuntimeSnapshot, modelName string, requestEffort string) (string, string, model.ProxyModelIntent, bool) {
	if snapshot == nil {
		return "", modelName, model.ProxyModelIntent{}, false
	}
	for index := len(snapshot.DefaultProviderIDs) - 1; index >= 0; index-- {
		providerID := snapshot.DefaultProviderIDs[index]
		provider, err := snapshot.Config.ProviderByID(providerID)
		if err != nil || !provider.Enabled {
			continue
		}
		internalModel, ok := provider.InternalModelID(modelName, true)
		if !ok || provider.HidesModel(internalModel) {
			continue
		}
		if providerRoutingModelIsExactLiteral(provider, internalModel) {
			return providerID, internalModel, model.ProxyModelIntent{BaseModel: internalModel, IsExactLiteral: true}, true
		}
	}
	for _, providerID := range snapshot.DefaultProviderIDs {
		provider, err := snapshot.Config.ProviderByID(providerID)
		if err != nil || !provider.Enabled {
			continue
		}
		internalModel, ok := provider.InternalModelID(modelName, true)
		if !ok {
			continue
		}
		if provider.HidesModel(internalModel) {
			continue
		}
		if intent, parsed := parseProviderProxyModelIntentForRouting(provider, internalModel, snapshot.Config.EnableNoPromptModelSuffix, snapshot.Config.EffectiveEnableReasoningModeSuffix()); parsed {
			if provider.HidesModel(intent.CanonicalModel()) {
				continue
			}
			if provider.ProxyModelIntentAllowsAlias(intent) || providerRoutingModelContains(provider, intent.BaseModel) {
				return providerID, config.ProxyModelIntentRoutingModel(intent), intent, true
			}
		}
		if providerModelMapMatches(provider, internalModel, requestEffort, snapshot.Config.EnableNoPromptModelSuffix) {
			return providerID, internalModel, model.ProxyModelIntent{}, true
		}
	}
	return "", modelName, model.ProxyModelIntent{}, false
}

func providerRoutingModelIsExactLiteral(provider config.ProviderConfig, modelName string) bool {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return false
	}
	for _, candidate := range provider.RoutingModelCandidates() {
		if candidate == modelName {
			return true
		}
	}
	return false
}

func providerRoutingModelContains(provider config.ProviderConfig, modelName string) bool {
	modelName = strings.TrimSpace(modelName)
	for _, candidate := range provider.RoutingModelCandidates() {
		if candidate == modelName {
			return true
		}
	}
	return false
}

func providerModelMapMatches(provider config.ProviderConfig, modelName string, requestEffort string, enableNoPrompt bool) bool {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return false
	}
	candidates := []string{modelName}
	if enableNoPrompt {
		if strippedModel, stripped := stripNoPromptModelSuffix(modelName); stripped {
			candidates = append(candidates, strippedModel)
		}
	}
	parsed := parseProxyModelSuffixes(modelName)
	if requestEffort = strings.TrimSpace(requestEffort); requestEffort != "" && parsed.baseModel != "" {
		candidates = append(candidates, parsed.baseModel+"-"+requestEffort)
	}
	if parsed.baseModel != "" && parsed.baseModel != modelName {
		candidates = append(candidates, parsed.baseModel)
	}
	for index := len(provider.ModelMap) - 1; index >= 0; index-- {
		for _, candidate := range candidates {
			if strings.TrimSpace(provider.ModelMap[index].Resolve(candidate)) != "" {
				return true
			}
		}
	}
	return false
}

func defaultFallbackAllowsUnconfiguredProxyTail(provider config.ProviderConfig, modelName string, requestEffort string, enableNoPrompt bool) bool {
	if !model.HasProxyModelIntentTail(modelName) {
		return true
	}
	parsed := parseProxyModelSuffixes(modelName)
	if !parsed.hasNoPrompt || parsed.reasoningEffort != "" || strings.TrimSpace(requestEffort) != "" {
		return true
	}
	for _, effort := range model.ReasoningEfforts() {
		if providerModelMapMatches(provider, parsed.baseModel+"-"+effort, "", enableNoPrompt) {
			return false
		}
	}
	return true
}

func parseProviderProxyModelIntentForRouting(provider config.ProviderConfig, modelName string, rootNoPrompt bool, rootReasoningMode bool) (model.ProxyModelIntent, bool) {
	if intent, parsed := provider.ParseProxyModelIntentWithReasoningModeCandidates(modelName, rootNoPrompt, rootReasoningMode, provider.VisibleModelIDs()); parsed && (intent.ReasoningMode != "" || intent.HasNoPrompt || intent.HasUltra) {
		return intent, true
	}
	return provider.ParseProxyModelIntentWithReasoningMode(modelName, rootNoPrompt, rootReasoningMode)
}

func resolveV1ProxyModelIntentForLegacyRequest(r *http.Request, modelName string) (model.ProxyModelIntent, bool) {
	refreshDefaultProviderOverlayCacheFromRequest(r)
	snapshot, ok := runtimeSnapshotFromRequest(r)
	if !ok || snapshot == nil {
		return model.ProxyModelIntent{}, false
	}
	return snapshot.Config.ResolveV1ProxyModelIntentWithTargetCandidates(modelName, defaultOverlayRoutingModelCandidates(snapshot))
}

func defaultOverlayRoutingModelCandidates(snapshot *config.RuntimeSnapshot) []string {
	if snapshot == nil {
		return nil
	}
	candidates := make([]string, 0)
	seen := make(map[string]struct{})
	add := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return
		}
		if _, exists := seen[candidate]; exists {
			return
		}
		seen[candidate] = struct{}{}
		candidates = append(candidates, candidate)
	}
	for _, providerID := range snapshot.DefaultProviderIDs {
		provider, err := snapshot.Config.ProviderByID(providerID)
		if err != nil || !provider.Enabled {
			continue
		}
		for _, candidate := range provider.RoutingModelCandidates() {
			add(candidate)
		}
	}
	for _, rawModelID := range snapshot.DefaultModelRawIDs {
		add(rawModelID)
	}
	return candidates
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

func providerForRequest(r *http.Request) (config.ProviderConfig, bool) {
	provider, _, _, ok := providerSelectionForRequest(r, "")
	return provider, ok
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

func taggedModelID(providerID string, modelID string) string {
	return "[" + providerID + "]" + modelID
}
