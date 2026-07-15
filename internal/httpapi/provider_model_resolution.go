package httpapi

import (
	"net/http"
	"strings"

	"openai-compat-proxy/internal/config"
	modelpkg "openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/upstream"
)

type realtimeOverlayProviderModels struct {
	providerID string
	provider   config.ProviderConfig
	config     config.Config
	rawIDs     []string
	visible    map[string]struct{}
}

func resolveExplicitProviderSelectionFromRealtimeModels(r *http.Request, snapshot *config.RuntimeSnapshot, providerID string, provider config.ProviderConfig, externalModel string) (defaultOverlayDiscovery, error, bool) {
	if snapshot == nil || !provider.SupportsModels || strings.TrimSpace(externalModel) == "" {
		return defaultOverlayDiscovery{}, nil, false
	}
	providerCfg := providerConfigForID(snapshot, providerID)
	authorization, err := authHeaderForOverlayProviderUpstream(r, providerCfg, providerID)
	if err != nil {
		return defaultOverlayDiscovery{}, nil, false
	}
	client := upstream.NewClient(providerCfg.UpstreamBaseURL, providerCfg)
	bodies, ok, err := fetchProviderModelsBodies(r.Context(), client, authorization, provider)
	if err != nil || !ok {
		return defaultOverlayDiscovery{}, err, false
	}
	entries := decodeModelEntries(bodies.visible)
	visible := rawModelIDSet(entries)
	rawIDs := visibleRawModelIDs(provider, decodeModelEntries(bodies.raw), visible)
	if len(rawIDs) == 0 {
		return defaultOverlayDiscovery{}, nil, false
	}
	resolution, resolved := provider.ResolveExternalProxyModelIntentWithCandidates(
		externalModel,
		providerCfg.EnableNoPromptModelSuffix,
		providerCfg.EffectiveEnableReasoningModeSuffix(),
		rawIDs,
	)
	if !resolved {
		return defaultOverlayDiscovery{}, nil, false
	}
	if rawModelID, exact := exactVisibleRawModelID(provider, rawIDs, externalModel); exact {
		resolution.SourceIntent.BaseModel = rawModelID
		resolution.SourceIntent.IsExactLiteral = true
		resolution.ResolvedIntent.IsExactLiteral = true
	}
	return defaultOverlayDiscovery{
		ProviderID:             providerID,
		RequestedModelID:       externalModel,
		RawModelID:             config.ProxyModelIntentRoutingModel(resolution.SourceIntent),
		VisibleModelIDs:        visible,
		SourceProxyModelIntent: resolution.SourceIntent,
		ProxyModelIntent:       resolution.ResolvedIntent,
		HasProxyModelIntent:    true,
		ExactLiteral:           resolution.SourceIntent.IsExactLiteral,
	}, nil, true
}

func resolveDefaultProviderSelectionFromRealtimeModels(r *http.Request, snapshot *config.RuntimeSnapshot, modelName string) (defaultOverlayDiscovery, error, bool) {
	if snapshot == nil || strings.TrimSpace(modelName) == "" {
		return defaultOverlayDiscovery{}, nil, false
	}
	targetProviderID, externalModel, tagged, valid := realtimeOverlayRequestedModel(snapshot, modelName)
	if !valid {
		return defaultOverlayDiscovery{}, nil, false
	}
	providers := make([]realtimeOverlayProviderModels, 0, len(snapshot.DefaultProviderIDs))
	exact := defaultOverlayDiscovery{}
	exactMatches := 0
	var upstreamErr error
	for _, providerID := range snapshot.DefaultProviderIDs {
		if targetProviderID != "" && providerID != targetProviderID {
			continue
		}
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
		bodies, ok, err := fetchProviderModelsBodies(r.Context(), client, authorization, provider)
		if err != nil {
			upstreamErr = err
		}
		if !ok {
			continue
		}
		entries := decodeModelEntries(bodies.visible)
		visible := rawModelIDSet(entries)
		rawIDs := visibleRawModelIDs(provider, decodeModelEntries(bodies.raw), visible)
		if len(rawIDs) == 0 {
			continue
		}
		providers = append(providers, realtimeOverlayProviderModels{
			providerID: providerID,
			provider:   provider,
			config:     providerCfg,
			rawIDs:     rawIDs,
			visible:    visible,
		})
		if rawModelID, found := exactVisibleRawModelID(provider, rawIDs, externalModel); found {
			resolution, resolved := provider.ResolveExternalProxyModelIntentWithCandidates(
				externalModel,
				providerCfg.EnableNoPromptModelSuffix,
				providerCfg.EffectiveEnableReasoningModeSuffix(),
				rawIDs,
			)
			if !resolved {
				continue
			}
			resolution.SourceIntent.BaseModel = rawModelID
			resolution.SourceIntent.IsExactLiteral = true
			resolution.ResolvedIntent.IsExactLiteral = true
			exactMatches++
			exact = defaultOverlayDiscovery{
				ProviderID:             providerID,
				RequestedModelID:       modelName,
				RawModelID:             rawModelID,
				VisibleModelIDs:        visible,
				SourceProxyModelIntent: resolution.SourceIntent,
				ProxyModelIntent:       resolution.ResolvedIntent,
				HasProxyModelIntent:    true,
				ExactLiteral:           true,
			}
		}
	}
	if exact.ProviderID != "" && (!snapshot.Config.EnableDefaultProviderModelTags || tagged || exactMatches == 1) {
		return exact, nil, true
	}
	discovery := defaultOverlayDiscovery{}
	derivedMatches := 0
	for _, candidate := range providers {
		resolution, resolved := candidate.provider.ResolveExternalProxyModelIntentWithCandidates(
			externalModel,
			candidate.config.EnableNoPromptModelSuffix,
			candidate.config.EffectiveEnableReasoningModeSuffix(),
			candidate.rawIDs,
		)
		if !resolved {
			continue
		}
		derivedMatches++
		discovery = defaultOverlayDiscovery{
			ProviderID:             candidate.providerID,
			RequestedModelID:       modelName,
			RawModelID:             config.ProxyModelIntentRoutingModel(resolution.SourceIntent),
			VisibleModelIDs:        candidate.visible,
			SourceProxyModelIntent: resolution.SourceIntent,
			ProxyModelIntent:       resolution.ResolvedIntent,
			HasProxyModelIntent:    true,
		}
	}
	if snapshot.Config.EnableDefaultProviderModelTags && !tagged && derivedMatches != 1 {
		return defaultOverlayDiscovery{}, upstreamErr, false
	}
	if discovery.ProviderID == "" {
		return defaultOverlayDiscovery{}, upstreamErr, false
	}
	return discovery, nil, true
}

func realtimeOverlayRequestedModel(snapshot *config.RuntimeSnapshot, modelName string) (string, string, bool, bool) {
	modelName = strings.TrimSpace(modelName)
	if snapshot == nil || modelName == "" {
		return "", modelName, false, false
	}
	if !snapshot.Config.EnableDefaultProviderModelTags {
		return "", modelName, false, true
	}
	for _, providerID := range snapshot.DefaultProviderIDs {
		prefix := "[" + providerID + "]"
		if strings.HasPrefix(modelName, prefix) && len(modelName) > len(prefix) {
			return providerID, strings.TrimPrefix(modelName, prefix), true, true
		}
	}
	if strings.HasPrefix(modelName, "[") || snapshot.Config.EnableAllDefaultProviderModelTags {
		return "", modelName, false, false
	}
	return "", modelName, false, true
}

func visibleRawModelIDs(provider config.ProviderConfig, rawEntries []map[string]any, visible map[string]struct{}) []string {
	modelIDs := make([]string, 0, len(rawEntries))
	for _, entry := range rawEntries {
		rawModelID, _ := entry["id"].(string)
		rawModelID = strings.TrimSpace(rawModelID)
		if rawModelID == "" {
			continue
		}
		if _, allowed := visible[provider.ExternalModelID(rawModelID, true)]; allowed {
			modelIDs = append(modelIDs, rawModelID)
		}
	}
	return modelIDs
}

func exactVisibleRawModelID(provider config.ProviderConfig, rawModelIDs []string, externalModel string) (string, bool) {
	for _, rawModelID := range rawModelIDs {
		if provider.ExternalModelID(rawModelID, true) == externalModel {
			return rawModelID, true
		}
	}
	return "", false
}

func defaultOverlayModelMayContainProxyIntent(snapshot *config.RuntimeSnapshot, modelName string) bool {
	targetProviderID, externalModel, _, valid := realtimeOverlayRequestedModel(snapshot, modelName)
	if !valid {
		return false
	}
	for _, providerID := range snapshot.DefaultProviderIDs {
		if targetProviderID != "" && providerID != targetProviderID {
			continue
		}
		provider, err := snapshot.Config.ProviderByID(providerID)
		if err == nil && explicitProviderModelMayContainProxyIntent(provider, externalModel, true) {
			return true
		}
	}
	return false
}

func explicitProviderModelMayContainProxyIntent(provider config.ProviderConfig, externalModel string, legacy bool) bool {
	internalModel, ok := provider.InternalModelID(externalModel, legacy)
	return ok && modelpkg.HasProxyModelIntentTail(internalModel)
}
