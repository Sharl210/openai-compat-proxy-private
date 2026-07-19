package httpapi

import (
	"context"
	"net/http"
	"strings"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/errorsx"
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

func ensureProviderModelAllowed(_ context.Context, r *http.Request, provider config.ProviderConfig, _ config.Config, requestedModel string, _ string) error {
	requestedModel = strings.TrimSpace(requestedModel)
	if info, ok := routeInfoFromRequest(r); ok && info.Legacy {
		if mappedModel, mapped := legacyRoutingModelFromRequest(r); mapped {
			requestedModel = strings.TrimSpace(mappedModel)
		}
	}
	if requestedModel == "" {
		return nil
	}
	if providerModelIsHidden(provider, requestedModel, requestEffortFromRouteContext(r)) {
		return hiddenModelAllowanceError()
	}
	return nil
}

func providerModelIsHidden(provider config.ProviderConfig, requestedModel string, requestEffort string) bool {
	if provider.HidesModel(requestedModel) {
		return true
	}
	mappedModel, mappedEffort := provider.ResolveModelAndEffortWithRequestEffort(requestedModel, requestEffort, provider.EnableReasoningEffortSuffix)
	if provider.HidesModel(mappedModel) {
		return true
	}
	if mappedEffort != "" && provider.HidesModel(mappedModel+"-"+mappedEffort) {
		return true
	}
	return false
}

func hiddenModelAllowanceError() error {
	return &modelAllowanceError{status: http.StatusBadRequest, code: "invalid_model", message: "requested model is hidden"}
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
