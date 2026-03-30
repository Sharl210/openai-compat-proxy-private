package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/errorsx"
	"openai-compat-proxy/internal/reasoning"
	"openai-compat-proxy/internal/upstream"
)

func handleModels() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
			if fallbackBody, fallbackOK := configuredModelsFallbackBody(provider); fallbackOK {
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
			body = rewriteModelsBody(body, provider)
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
}

func rewriteModelsBody(body []byte, provider config.ProviderConfig) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	data, _ := payload["data"].([]any)
	mapLen := len(provider.ModelMap)
	manualLen := len(provider.ManualModels)
	baseIDs := make([]string, 0, len(data)+mapLen+manualLen)
	seenIDs := make(map[string]struct{}, len(data)+mapLen+manualLen)
	entriesByID := map[string]map[string]any{}
	for _, item := range data {
		entry, _ := item.(map[string]any)
		id, _ := entry["id"].(string)
		if id != "" {
			if _, exists := seenIDs[id]; !exists {
				baseIDs = append(baseIDs, id)
				seenIDs[id] = struct{}{}
			}
			entriesByID[id] = cloneModelEntry(entry)
		}
	}
	publicAliases := sortedPublicModelAliases(provider.ModelMap)
	for _, publicModel := range publicAliases {
		if _, exists := entriesByID[publicModel]; exists {
			continue
		}
		if source := cloneSourceModelEntry(provider, publicModel, entriesByID); len(source) > 0 {
			if _, exists := seenIDs[publicModel]; !exists {
				baseIDs = append(baseIDs, publicModel)
				seenIDs[publicModel] = struct{}{}
			}
			source["id"] = publicModel
			entriesByID[publicModel] = source
		}
	}
	for _, manualModel := range provider.ManualModels {
		if _, exists := seenIDs[manualModel]; !exists {
			baseIDs = append(baseIDs, manualModel)
			seenIDs[manualModel] = struct{}{}
		}
	}
	expanded := baseIDs
	if provider.ExposeReasoningSuffixModels && provider.EnableReasoningEffortSuffix {
		modelMapKeys := modelMapKeysFromEntries(provider.ModelMap)
		expanded = reasoning.ExpandModelIDs(baseIDs, modelMapKeys, true)
	}
	entries := make([]map[string]any, 0, len(expanded))
	for _, id := range expanded {
		entry := cloneModelEntry(entriesByID[id])
		if len(entry) == 0 {
			entry = map[string]any{"id": id}
		} else {
			entry["id"] = id
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

func configuredModelsFallbackBody(provider config.ProviderConfig) ([]byte, bool) {
	ids := sortedPublicModelAliases(provider.ModelMap)
	if len(ids) == 0 {
		return nil, false
	}
	if provider.ExposeReasoningSuffixModels && provider.EnableReasoningEffortSuffix {
		ids = reasoning.ExpandModelIDs(ids, ids, true)
	}
	entries := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, map[string]any{
			"id":     id,
			"object": "model",
		})
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

func sortedPublicModelAliases(entries []config.ModelMapEntry) []string {
	aliases := make([]string, 0, len(entries))
	for _, entry := range entries {
		if shouldHideModelAlias(entry.Key) {
			continue
		}
		aliases = append(aliases, entry.Key)
	}
	sort.Strings(aliases)
	return aliases
}

func shouldHideModelAlias(id string) bool {
	id = strings.TrimSpace(id)
	return id == "" || strings.Contains(id, "*")
}

func cloneSourceModelEntry(provider config.ProviderConfig, publicModel string, entriesByID map[string]map[string]any) map[string]any {
	mapped := resolveModelMapTarget(provider.ModelMap, publicModel)
	if mapped == "" {
		return nil
	}
	if base, _, ok := reasoning.SplitSuffix(mapped); ok {
		mapped = base
	}
	if entry := cloneModelEntry(entriesByID[mapped]); len(entry) > 0 {
		return entry
	}
	if mapped == publicModel {
		return cloneModelEntry(entriesByID[publicModel])
	}
	return nil
}

func resolveModelMapTarget(entries []config.ModelMapEntry, model string) string {
	for _, entry := range entries {
		if !entry.HasWildcard && entry.Key == model {
			return entry.Target
		}
	}
	return ""
}

func modelMapKeysFromEntries(entries []config.ModelMapEntry) []string {
	keys := make([]string, 0, len(entries))
	for _, e := range entries {
		keys = append(keys, e.Key)
	}
	return keys
}
