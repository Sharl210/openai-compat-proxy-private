package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/errorsx"
	"openai-compat-proxy/internal/reasoning"
	"openai-compat-proxy/internal/upstream"
)

type modelAllowanceError struct {
	status  int
	code    string
	message string
}

func (e *modelAllowanceError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func ensureProviderModelAllowed(ctx context.Context, r *http.Request, provider config.ProviderConfig, providerCfg config.Config, requestedModel string, authorization string) error {
	requestedModel = strings.TrimSpace(requestedModel)
	if info, ok := routeInfoFromRequest(r); ok && info.Legacy {
		if mappedModel, mapped := legacyRoutingModelFromRequest(r); mapped {
			requestedModel = strings.TrimSpace(mappedModel)
		}
	}
	if requestedModel == "" {
		return nil
	}
	if provider.AllowsLiteralModelMapTarget(requestedModel) {
		return nil
	}
	if intent, ok := proxyModelIntentFromRequest(r); ok {
		if intent.HasModelMapAlias || provider.ProxyModelIntentAllowsAlias(intent) || provider.AllowsInternalProxyModelIntent(intent, providerCfg.EnableNoPromptModelSuffix) {
			return nil
		}
	}
	if discovery, ok := defaultOverlayDiscoveryFromRequest(r); ok && discovery.ProviderID == provider.ID {
		if discovery.RequestedModelID == requestedModel || discovery.RawModelID == requestedModel {
			return nil
		}
		if _, allowed := discovery.VisibleModelIDs[requestedModel]; allowed {
			return nil
		}
	}
	if shouldBypassUsageRecorderForRequest(r) {
		return nil
	}
	if bypassProviderModelAllowanceForRequest(r) {
		return nil
	}
	info, ok := routeInfoFromRequest(r)
	if !ok {
		return nil
	}
	if !providerModelsListEnforcedForRequest(r, provider, info) {
		return nil
	}
	allowed, discoveredModelIDs, enforced, err := explicitProviderVisibleModelSet(ctx, r, provider, providerCfg, authorization)
	if err != nil {
		return err
	}
	if !enforced {
		return nil
	}
	if _, ok := allowed[requestedModel]; ok {
		if intent, parsed := provider.ParseProxyModelIntentWithReasoningModeCandidates(requestedModel, providerCfg.EnableNoPromptModelSuffix, providerCfg.EffectiveEnableReasoningModeSuffix(), discoveredModelIDs); parsed && (intent.ReasoningEffort != "" || intent.ReasoningMode != "" || intent.HasNoPrompt || intent.HasUltra) {
			*r = *r.Clone(withProxyModelIntent(r.Context(), intent))
		}
		if provider.HidesModel(requestedModel) {
			return &modelAllowanceError{status: http.StatusBadRequest, code: "invalid_model", message: "requested model is not in models list"}
		}
		return nil
	}
	if providerAllowsModelMapAliasForVisibleSet(provider, requestedModel, requestEffortFromRouteContext(r), allowed) {
		return nil
	}
	if baseWithoutNoPrompt, ok := stripNoPromptModelSuffix(requestedModel); ok && providerCfg.EnableNoPromptModelSuffix {
		if _, exists := allowed[baseWithoutNoPrompt]; exists && !provider.HidesModel(requestedModel) {
			return nil
		}
		if baseModel, _, suffixOK := reasoning.SplitSuffix(baseWithoutNoPrompt); suffixOK {
			if _, exists := allowed[baseModel]; exists && (provider.EnableReasoningEffortSuffix || provider.HasManualReasonSuffixForModel(baseWithoutNoPrompt)) && !provider.HidesModel(requestedModel) {
				return nil
			}
		}
	}
	if baseModel, _, ok := reasoning.SplitSuffix(requestedModel); ok {
		if _, exists := allowed[baseModel]; exists && (provider.EnableReasoningEffortSuffix || provider.HasManualReasonSuffixForModel(requestedModel)) && !provider.HidesModel(requestedModel) {
			return nil
		}
	}
	return &modelAllowanceError{status: http.StatusBadRequest, code: "invalid_model", message: "requested model is not in models list"}
}

func providerAllowsModelMapAliasForVisibleSet(provider config.ProviderConfig, model string, requestEffort string, _ map[string]struct{}) bool {
	model = strings.TrimSpace(model)
	if model == "" || provider.HidesModel(model) {
		return false
	}
	for _, candidate := range providerStaticModelMapAliasCandidates(model, requestEffort, provider.EnableReasoningEffortSuffix || provider.HasManualReasonSuffixForModel(model)) {
		for i := len(provider.ModelMap) - 1; i >= 0; i-- {
			entry := provider.ModelMap[i]
			mapped := strings.TrimSpace(entry.Resolve(candidate))
			if mapped == "" {
				continue
			}
			return !provider.HidesModel(mapped)
		}
	}
	return false
}

func providerStaticModelMapAliasCandidates(model string, requestEffort string, enableClientSuffix bool) []string {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	requestEffort = strings.TrimSpace(requestEffort)
	if baseModel, stripped := stripNoPromptModelSuffix(model); stripped {
		model = baseModel
	}
	candidates := []string{}
	seen := map[string]struct{}{}
	add := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return
		}
		if _, ok := seen[candidate]; ok {
			return
		}
		seen[candidate] = struct{}{}
		candidates = append(candidates, candidate)
	}
	if requestEffort != "" {
		if baseModel, _, ok := reasoning.SplitSuffix(model); ok && enableClientSuffix {
			add(baseModel + "-" + requestEffort)
		} else {
			add(model + "-" + requestEffort)
		}
	}
	add(model)
	if enableClientSuffix {
		if baseModel, _, ok := reasoning.SplitSuffix(model); ok {
			add(baseModel)
		}
	}
	return candidates
}

func bypassProviderModelAllowanceForRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	path := strings.TrimSpace(r.URL.Path)
	switch path {
	case canonicalV1ImagesGenerationsPath,
		canonicalV1ImagesEditsPath,
		canonicalV1ImagesVariationsPath,
		canonicalV1EmbeddingsPath,
		canonicalV1RerankPath:
		return true
	default:
		return false
	}
}

func explicitModelsListEnforced(provider config.ProviderConfig) bool {
	return provider.SupportsModels || len(provider.VisibleModelIDs()) > 0
}

func providerModelsListEnforcedForRequest(r *http.Request, provider config.ProviderConfig, info routeInfo) bool {
	if !explicitModelsListEnforced(provider) {
		return false
	}
	if !info.Legacy {
		return true
	}
	snapshot, ok := runtimeSnapshotFromRequest(r)
	if !ok || snapshot == nil {
		return false
	}
	return len(snapshot.DefaultProviderIDs) == 1
}

func writeModelAllowanceError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	if writeUpstreamError(w, err) {
		return
	}
	if typed, ok := err.(*modelAllowanceError); ok {
		errorsx.WriteJSON(w, typed.status, typed.code, typed.message)
		return
	}
	errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
}

func explicitProviderVisibleModelSet(ctx context.Context, r *http.Request, provider config.ProviderConfig, providerCfg config.Config, authorization string) (map[string]struct{}, []string, bool, error) {
	if provider.SupportsModels {
		client := upstreamClientForProvider(r, provider.ID, providerCfg)
		status, body, contentType, err := client.Models(ctx, authorization)
		if err != nil {
			return nil, nil, false, err
		}
		if status == http.StatusNotFound {
			if ids := provider.VisibleModelIDs(); len(ids) > 0 {
				return rawModelIDSetFromIDs(ids), nil, true, nil
			}
			return nil, nil, false, nil
		}
		if status >= 200 && status < 300 {
			set, discoveredModelIDs, err := rawModelIDSetFromModelsBody(body, provider)
			return set, discoveredModelIDs, true, err
		}
		return nil, nil, false, &upstream.HTTPStatusError{
			StatusCode:  status,
			ContentType: contentType,
			BodyBytes:   append([]byte(nil), body...),
			Body:        strings.TrimSpace(string(body)),
		}
	}
	ids := provider.VisibleModelIDs()
	if len(ids) == 0 {
		return nil, nil, false, nil
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set, nil, true, nil
}

func rawModelIDSetFromIDs(ids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		set[id] = struct{}{}
	}
	return set
}

func rawModelIDSetFromModelsBody(body []byte, provider config.ProviderConfig) (map[string]struct{}, []string, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, nil, err
	}
	data, _ := payload["data"].([]any)
	baseIDs := make([]string, 0, len(data)+len(provider.ManualModels))
	upstreamBaseIDs := make([]string, 0, len(data))
	seenIDs := make(map[string]struct{}, len(data)+len(provider.ManualModels))
	for _, item := range data {
		entry, _ := item.(map[string]any)
		id, _ := entry["id"].(string)
		id = strings.TrimSpace(id)
		if id == "" || provider.HidesModel(id) || !modelSelectedByManualPatterns(provider, id) {
			continue
		}
		upstreamBaseIDs = append(upstreamBaseIDs, id)
		if _, exists := seenIDs[id]; !exists {
			baseIDs = append(baseIDs, id)
			seenIDs[id] = struct{}{}
		}
	}
	for _, manualModel := range provider.ManualModels {
		manualModel = strings.TrimSpace(manualModel)
		if manualModel == "" || strings.HasPrefix(manualModel, "#reason_suffix:") || !config.IsStaticModelPattern(manualModel) {
			continue
		}
		if _, exists := seenIDs[manualModel]; !exists {
			baseIDs = append(baseIDs, manualModel)
			seenIDs[manualModel] = struct{}{}
		}
	}
	for _, id := range provider.ManualReasonSuffixModelIDsFrom(upstreamBaseIDs) {
		id = strings.TrimSpace(id)
		if id == "" || provider.HidesModel(id) {
			continue
		}
		if _, exists := seenIDs[id]; !exists {
			baseIDs = append(baseIDs, id)
			seenIDs[id] = struct{}{}
		}
	}
	visible := baseIDs
	if provider.ExposeReasoningSuffixModels && provider.EnableReasoningEffortSuffix {
		visible = reasoning.ExpandModelIDs(baseIDs, nil, true)
	}
	visible = expandReasoningModeModelIDs(visible, provider)
	set := make(map[string]struct{}, len(visible))
	for _, id := range visible {
		id = strings.TrimSpace(id)
		if id == "" || provider.HidesModel(id) {
			continue
		}
		set[id] = struct{}{}
	}
	return set, upstreamBaseIDs, nil
}
