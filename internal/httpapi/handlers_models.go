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

func handleModels(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		providerCfg := providerConfigForRequest(r, cfg)
		client := upstream.NewClient(providerCfg.UpstreamBaseURL)
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
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				errorsx.WriteJSON(w, http.StatusGatewayTimeout, "upstream_timeout", "upstream request timed out")
				return
			}
			errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
			return
		}

		if contentType == "" {
			contentType = "application/json"
		}
		if provider, ok := providerForRequest(r, cfg); ok {
			body = rewriteModelsBody(body, provider)
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
}

func rewriteModelsBody(body []byte, provider config.ProviderConfig) []byte {
	if !provider.ExposeReasoningSuffixModels || !provider.EnableReasoningEffortSuffix {
		return body
	}
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
	expanded := reasoning.ExpandModelIDs(baseIDs, true)
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
