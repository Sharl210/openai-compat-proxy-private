package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
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
	if requestedModel == "" {
		return nil
	}
	info, ok := routeInfoFromRequest(r)
	if !ok {
		return nil
	}
	if !providerModelsListEnforcedForRequest(r, provider, info) {
		return nil
	}
	allowed, enforced, err := explicitProviderVisibleModelSet(ctx, provider, providerCfg, authorization)
	if err != nil {
		return err
	}
	if !enforced {
		return nil
	}
	if _, ok := allowed[requestedModel]; ok {
		return nil
	}
	if baseModel, _, ok := reasoning.SplitSuffix(requestedModel); ok {
		if _, exists := allowed[baseModel]; exists && provider.EnableReasoningEffortSuffix && !provider.HidesModel(requestedModel) {
			return nil
		}
	}
	return &modelAllowanceError{status: http.StatusBadRequest, code: "invalid_model", message: "requested model is not in models list"}
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
	if typed, ok := err.(*modelAllowanceError); ok {
		errorsx.WriteJSON(w, typed.status, typed.code, typed.message)
		return
	}
	errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
}

func explicitProviderVisibleModelSet(ctx context.Context, provider config.ProviderConfig, providerCfg config.Config, authorization string) (map[string]struct{}, bool, error) {
	if provider.SupportsModels {
		client := upstream.NewClient(providerCfg.UpstreamBaseURL, providerCfg)
		status, body, _, err := client.Models(ctx, authorization)
		if err != nil {
			return nil, false, err
		}
		if status == http.StatusNotFound {
			if fallback, ok := configuredModelsFallbackBody(provider); ok {
				set, err := modelIDSetFromBody(fallback)
				return set, true, err
			}
			return nil, false, nil
		} else if status >= 200 && status < 300 {
			set, err := modelIDSetFromBody(rewriteModelsBody(body, provider))
			return set, true, err
		} else {
			return nil, false, &modelAllowanceError{status: http.StatusBadGateway, code: "upstream_error", message: fmt.Sprintf("failed to load provider models list: status %d", status)}
		}
	}
	ids := provider.VisibleModelIDs()
	if len(ids) == 0 {
		return nil, false, nil
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set, true, nil
}

func modelIDSetFromBody(body []byte) (map[string]struct{}, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	data, _ := payload["data"].([]any)
	ids := make(map[string]struct{}, len(data))
	for _, item := range data {
		entry, _ := item.(map[string]any)
		id, _ := entry["id"].(string)
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		ids[id] = struct{}{}
	}
	return ids, nil
}
