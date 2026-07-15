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

func handleModels() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		refreshDefaultProviderOverlayCacheFromRequest(r)
		if snapshot, ok := runtimeSnapshotFromRequest(r); ok && snapshot != nil {
			if info, ok := routeInfoFromRequest(r); ok && info.Legacy && (len(snapshot.DefaultProviderIDs) > 1 || snapshot.Config.EnableDefaultProviderModelTags) {
				writeDefaultOverlayModels(w, r, snapshot)
				return
			}
		}
		providerCfg := providerConfigForRequest(r)
		provider, ok := providerForRequest(r)
		if !ok || !provider.SupportsModels {
			errorsx.WriteJSON(w, http.StatusBadRequest, "unsupported_provider_contract", "provider does not support models")
			return
		}
		client := upstream.NewClient(providerCfg.UpstreamBaseURL, providerCfg)
		authorization, err := authHeaderForUpstream(r, providerCfg)
		if err != nil {
			errorsx.WriteJSON(w, http.StatusUnauthorized, "missing_upstream_auth", err.Error())
			return
		}

		ctx := r.Context()
		var cancel context.CancelFunc
		if providerCfg.TotalTimeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, providerCfg.TotalTimeout)
			defer cancel()
		}

		status, body, contentType, err := client.Models(ctx, authorization)
		if err != nil {
			if isUpstreamTimeout(err, ctx) {
				errorsx.WriteJSON(w, http.StatusGatewayTimeout, "upstream_timeout", "upstream request timed out")
				return
			}
			errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
			return
		}
		if status == http.StatusNotFound {
			if fallbackBody, fallbackOK := configuredModelsFallbackBodyForRoute(provider, modelIDTemplateRootScopeFromRequest(r)); fallbackOK {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(fallbackBody)
				return
			}
		}

		if contentType == "" {
			contentType = "application/json"
		}
		if ok {
			body = rewriteModelsBodyForRoute(body, provider, modelIDTemplateRootScopeFromRequest(r))
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
}

func writeDefaultOverlayModels(w http.ResponseWriter, r *http.Request, snapshot *config.RuntimeSnapshot) {
	entries := buildDefaultOverlayModelEntries(r.Context(), r, snapshot)
	payload := map[string]any{
		"object": "list",
		"data":   entries,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		errorsx.WriteJSON(w, http.StatusInternalServerError, "internal_error", "failed to encode models response")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func buildDefaultOverlayModelEntries(ctx context.Context, r *http.Request, snapshot *config.RuntimeSnapshot) []map[string]any {
	if snapshot == nil {
		return nil
	}
	entries, ok := buildDefaultOverlayModelEntriesFromProviders(ctx, r, snapshot)
	if ok {
		return entries
	}
	entries = make([]map[string]any, 0, len(snapshot.DefaultVisibleModels))
	for _, modelID := range snapshot.DefaultVisibleModels {
		entry := map[string]any{"id": modelID, "object": "model"}
		if owner := snapshot.DefaultModelOwners[modelID]; owner != "" {
			entry["owned_by"] = owner
		}
		entries = append(entries, entry)
	}
	return entries
}

func buildDefaultOverlayModelEntriesFromProviders(ctx context.Context, r *http.Request, snapshot *config.RuntimeSnapshot) ([]map[string]any, bool) {
	entriesByID := map[string]map[string]any{}
	orderedIDs := make([]string, 0)
	anySource := false
	if snapshot == nil || snapshot.Config.EnableDefaultProviderModelTags {
		return nil, false
	}
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
		body, ok, _ := fetchProviderModelsBody(ctx, client, authorization, provider)
		if !ok {
			continue
		}
		anySource = true
		for _, entry := range decodeModelEntries(body) {
			id, _ := entry["id"].(string)
			if strings.TrimSpace(id) == "" {
				continue
			}
			if _, exists := entriesByID[id]; !exists {
				orderedIDs = append(orderedIDs, id)
			}
			if _, hasOwner := entry["owned_by"]; !hasOwner {
				entry["owned_by"] = providerID
			}
			entriesByID[id] = entry
		}
	}
	if !anySource {
		return nil, false
	}
	entries := make([]map[string]any, 0, len(orderedIDs))
	for _, id := range orderedIDs {
		entries = append(entries, entriesByID[id])
	}
	return entries, true
}

func authHeaderForOverlayProviderUpstream(r *http.Request, cfg config.Config, providerID string) (string, error) {
	return authHeaderForResolvedProviderUpstream(r, cfg, providerID)
}

func fetchProviderModelsBody(ctx context.Context, client *upstream.Client, authorization string, provider config.ProviderConfig) ([]byte, bool, error) {
	bodies, ok, err := fetchProviderModelsBodies(ctx, client, authorization, provider)
	return bodies.visible, ok, err
}

func decodeModelEntries(body []byte) []map[string]any {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	data, _ := payload["data"].([]any)
	entries := make([]map[string]any, 0, len(data))
	for _, item := range data {
		entry, _ := item.(map[string]any)
		if len(entry) == 0 {
			continue
		}
		entries = append(entries, cloneModelEntry(entry))
	}
	return entries
}

func rewriteModelsBody(body []byte, provider config.ProviderConfig) []byte {
	return rewriteModelsBodyForRoute(body, provider, true)
}

func modelIDTemplateRootScopeFromRequest(r *http.Request) bool {
	if info, ok := routeInfoFromRequest(r); ok {
		return info.Legacy
	}
	return true
}

func rewriteModelsBodyForRoute(body []byte, provider config.ProviderConfig, rootRoute bool) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	data, _ := payload["data"].([]any)
	manualLen := len(provider.ManualModels)
	baseIDs := make([]string, 0, len(data)+manualLen)
	upstreamBaseIDs := make([]string, 0, len(data))
	seenIDs := make(map[string]struct{}, len(data)+manualLen)
	entriesByID := map[string]map[string]any{}
	for _, item := range data {
		entry, _ := item.(map[string]any)
		id, _ := entry["id"].(string)
		if id != "" {
			if provider.HidesModel(id) || !modelSelectedByManualPatterns(provider, id) {
				continue
			}
			upstreamBaseIDs = append(upstreamBaseIDs, id)
			if _, exists := seenIDs[id]; !exists {
				baseIDs = append(baseIDs, id)
				seenIDs[id] = struct{}{}
			}
			entriesByID[id] = cloneModelEntry(entry)
		}
	}
	for _, manualModel := range provider.ManualModels {
		manualModel = strings.TrimSpace(manualModel)
		if manualModel == "" || strings.HasPrefix(manualModel, "#reason_suffix:") {
			continue
		}
		if !config.IsStaticModelPattern(manualModel) {
			continue
		}
		if _, exists := seenIDs[manualModel]; !exists {
			baseIDs = append(baseIDs, manualModel)
			seenIDs[manualModel] = struct{}{}
		}
	}
	for _, id := range provider.ManualReasonSuffixModelIDsFrom(upstreamBaseIDs) {
		if strings.TrimSpace(id) == "" || provider.HidesModel(id) {
			continue
		}
		if _, exists := seenIDs[id]; !exists {
			baseIDs = append(baseIDs, id)
			seenIDs[id] = struct{}{}
		}
	}
	expanded := baseIDs
	if provider.ExposeReasoningSuffixModels && provider.EnableReasoningEffortSuffix {
		expanded = reasoning.ExpandModelIDs(baseIDs, nil, true)
	}
	expanded = expandReasoningModeModelIDs(expanded, provider)
	filteredExpanded := make([]string, 0, len(expanded))
	for _, id := range expanded {
		if provider.HidesModel(id) {
			continue
		}
		filteredExpanded = append(filteredExpanded, id)
	}
	expanded = filteredExpanded
	entries := make([]map[string]any, 0, len(expanded))
	seenExternalIDs := make(map[string]struct{}, len(expanded))
	for _, id := range expanded {
		externalID := provider.ExternalModelID(id, rootRoute)
		if externalID == "" {
			continue
		}
		if _, exists := seenExternalIDs[externalID]; exists {
			continue
		}
		seenExternalIDs[externalID] = struct{}{}
		entry := cloneModelEntry(entriesByID[id])
		if len(entry) == 0 {
			entry = map[string]any{"id": externalID}
		} else {
			entry["id"] = externalID
		}
		entries = append(entries, entry)
	}
	payload["data"] = entries
	encoded, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return encoded
}

func expandReasoningModeModelIDs(modelIDs []string, provider config.ProviderConfig) []string {
	if !provider.EnableReasoningModeSuffix || !provider.ExposeReasoningModeSuffixModels {
		return modelIDs
	}
	seen := make(map[string]struct{}, len(modelIDs)*2)
	expanded := make([]string, 0, len(modelIDs)*2)
	for _, modelID := range modelIDs {
		if _, exists := seen[modelID]; !exists {
			seen[modelID] = struct{}{}
			expanded = append(expanded, modelID)
		}
		if strings.Contains(modelID, "-noprompt") || strings.HasSuffix(modelID, "-pro") {
			continue
		}
		finalUpstreamModel, _ := provider.ResolveModelAndEffort(modelID, provider.EnableReasoningEffortSuffix)
		if provider.ResolveReasoningModeProCapability(finalUpstreamModel) == config.ReasoningModeProCapabilityUnsupported {
			continue
		}
		variant := modelID + "-pro"
		if _, exists := seen[variant]; exists {
			continue
		}
		seen[variant] = struct{}{}
		expanded = append(expanded, variant)
	}
	return expanded
}

func configuredModelsFallbackBody(provider config.ProviderConfig) ([]byte, bool) {
	return configuredModelsFallbackBodyForRoute(provider, true)
}

func configuredModelsFallbackBodyForRoute(provider config.ProviderConfig, rootRoute bool) ([]byte, bool) {
	ids := provider.VisibleModelIDs()
	ids = expandReasoningModeModelIDs(ids, provider)
	if len(ids) == 0 {
		return nil, false
	}
	entries := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		if provider.HidesModel(id) {
			continue
		}
		entries = append(entries, map[string]any{
			"id":     provider.ExternalModelID(id, rootRoute),
			"object": "model",
		})
	}
	if len(entries) == 0 {
		return nil, false
	}
	payload := map[string]any{
		"object": "list",
		"data":   entries,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

func cloneModelEntry(entry map[string]any) map[string]any {
	if len(entry) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(entry))
	for k, v := range entry {
		cloned[k] = v
	}
	return cloned
}

func modelSelectedByManualPatterns(provider config.ProviderConfig, modelID string) bool {
	if !hasRegexManualModelPattern(provider) {
		return true
	}
	for _, pattern := range provider.ManualModels {
		if config.ManualReasonSuffixBasePatternMatches(pattern, modelID) {
			return true
		}
		if config.ModelPatternMatches(pattern, modelID) {
			return true
		}
	}
	return false
}

func hasRegexManualModelPattern(provider config.ProviderConfig) bool {
	for _, pattern := range provider.ManualModels {
		pattern = strings.TrimSpace(pattern)
		if pattern != "" && (!config.IsStaticModelPattern(pattern) || config.IsManualReasonSuffixRegexPattern(pattern)) {
			return true
		}
	}
	return false
}
