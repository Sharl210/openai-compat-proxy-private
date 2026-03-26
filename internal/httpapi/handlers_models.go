package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

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
		if !ok || !provider.SupportsModels {
			if statusStore != nil {
				statusStore.markFailed(requestID, "proxy_internal_error", "unsupported_provider_contract", "provider does not support models")
			}
			setRequestStatusHeaders(w, r, provider.ID, requestID, "proxy_internal_error")
			errorsx.WriteJSON(w, http.StatusBadRequest, "unsupported_provider_contract", "provider does not support models")
			return
		}
		client := upstream.NewClient(providerCfg.UpstreamBaseURL, providerCfg)
		authorization, err := authHeaderForUpstream(r, providerCfg)
		if err != nil {
			if statusStore != nil {
				statusStore.markFailed(requestID, "proxy_internal_error", "missing_upstream_auth", err.Error())
			}
			setRequestStatusHeaders(w, r, provider.ID, requestID, "proxy_internal_error")
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
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				setRequestStatusHeaders(w, r, provider.ID, requestID, "upstream_timeout")
				errorsx.WriteJSON(w, http.StatusGatewayTimeout, "upstream_timeout", "upstream request timed out")
				return
			}
			if statusStore != nil {
				statusStore.markFailed(requestID, "upstream_error", "upstream_error", err.Error())
			}
			setRequestStatusHeaders(w, r, provider.ID, requestID, "upstream_error")
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
	for publicModel := range provider.ModelMap {
		baseIDs = append(baseIDs, publicModel)
	}
	for _, item := range data {
		entry, _ := item.(map[string]any)
		id, _ := entry["id"].(string)
		if id != "" {
			baseIDs = append(baseIDs, id)
		}
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
		entries = append(entries, map[string]any{"id": id})
	}
	payload["data"] = entries
	encoded, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return encoded
}
