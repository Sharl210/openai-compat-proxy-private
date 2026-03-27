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
		requestID := w.Header().Get("X-Request-Id")
		statusStore, _ := requestStatusStoreFromRequest(r)
		statusCheckKey := statusCheckProxyKeyForRequest(r, providerCfg, provider)
		if !ok || !provider.SupportsModels {
			if statusStore != nil {
				statusStore.markFailed(requestID, "proxy_internal_error", "unsupported_provider_contract", "provider does not support models")
			}
			setRequestStatusHeaders(w, r, provider.ID, requestID, statusCheckKey, "proxy_internal_error")
			errorsx.WriteJSON(w, http.StatusBadRequest, "unsupported_provider_contract", "provider does not support models")
			return
		}
		client := upstream.NewClient(providerCfg.UpstreamBaseURL, providerCfg)
		authorization, err := authHeaderForUpstream(r, providerCfg)
		if err != nil {
			if statusStore != nil {
				statusStore.markFailed(requestID, "proxy_internal_error", "missing_upstream_auth", err.Error())
			}
			setRequestStatusHeaders(w, r, provider.ID, requestID, statusCheckKey, "proxy_internal_error")
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
			if statusStore != nil {
				statusStore.markFailed(requestID, "upstream_timeout", "upstream_timeout", "upstream request timed out")
			}
			if isUpstreamTimeout(err, ctx) {
				setRequestStatusHeaders(w, r, provider.ID, requestID, statusCheckKey, "upstream_timeout")
				errorsx.WriteJSON(w, http.StatusGatewayTimeout, "upstream_timeout", "upstream request timed out")
				return
			}
			if statusStore != nil {
				statusStore.markFailed(requestID, "upstream_error", "upstream_error", err.Error())
			}
			setRequestStatusHeaders(w, r, provider.ID, requestID, statusCheckKey, "upstream_error")
			errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
			return
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
		if statusStore != nil {
			statusStore.markCompleted(requestID)
		}
	}
}

func rewriteModelsBody(body []byte, provider config.ProviderConfig) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	data, _ := payload["data"].([]any)
	baseIDs := make([]string, 0, len(data)+len(provider.ModelMap))
	entriesByID := map[string]map[string]any{}
	for _, item := range data {
		entry, _ := item.(map[string]any)
		id, _ := entry["id"].(string)
		if id != "" {
			baseIDs = append(baseIDs, id)
			entriesByID[id] = cloneModelEntry(entry)
		}
	}
	publicAliases := sortedPublicModelAliases(provider.ModelMap)
	for _, publicModel := range publicAliases {
		baseIDs = append(baseIDs, publicModel)
		if _, exists := entriesByID[publicModel]; exists {
			continue
		}
		if source := cloneSourceModelEntry(provider, publicModel, entriesByID); len(source) > 0 {
			source["id"] = publicModel
			entriesByID[publicModel] = source
			continue
		}
		entriesByID[publicModel] = map[string]any{"id": publicModel}
	}
	expanded := baseIDs
	if provider.ExposeReasoningSuffixModels && provider.EnableReasoningEffortSuffix {
		modelMapKeys := make([]string, 0, len(provider.ModelMap))
		for k := range provider.ModelMap {
			modelMapKeys = append(modelMapKeys, k)
		}
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

func sortedPublicModelAliases(modelMap map[string]string) []string {
	aliases := make([]string, 0, len(modelMap))
	for key := range modelMap {
		if shouldHideModelAlias(key) {
			continue
		}
		aliases = append(aliases, key)
	}
	sort.Strings(aliases)
	return aliases
}

func shouldHideModelAlias(id string) bool {
	id = strings.TrimSpace(id)
	return id == "" || strings.Contains(id, "*")
}

func cloneSourceModelEntry(provider config.ProviderConfig, publicModel string, entriesByID map[string]map[string]any) map[string]any {
	mapped := strings.TrimSpace(provider.ModelMap[publicModel])
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
