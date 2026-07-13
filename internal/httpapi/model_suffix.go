package httpapi

import (
	"net/http"
	"strings"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/reasoning"
)

const noPromptModelSuffix = "-noprompt"

type parsedProxyModelSuffixes struct {
	baseModel       string
	reasoningEffort string
	hasNoPrompt     bool
}

func parseProxyModelSuffixes(modelName string) parsedProxyModelSuffixes {
	baseModel := strings.TrimSpace(modelName)
	parsed := parsedProxyModelSuffixes{baseModel: baseModel}
	if baseModel == "" {
		return parsed
	}

	for {
		next := false
		if strings.HasSuffix(baseModel, noPromptModelSuffix) && len(baseModel) > len(noPromptModelSuffix) {
			baseModel = strings.TrimSuffix(baseModel, noPromptModelSuffix)
			parsed.hasNoPrompt = true
			next = true
		}
		if strippedModel, effort, ok := reasoning.SplitSuffix(baseModel); ok {
			baseModel = strippedModel
			if parsed.reasoningEffort == "" {
				parsed.reasoningEffort = effort
			}
			next = true
		}
		if !next {
			break
		}
	}

	parsed.baseModel = baseModel
	return parsed
}

func hasNoPromptModelSuffix(modelName string) bool {
	return parseProxyModelSuffixes(modelName).hasNoPrompt
}

func stripNoPromptModelSuffix(modelName string) (string, bool) {
	parsed := parseProxyModelSuffixes(modelName)
	if !parsed.hasNoPrompt {
		return modelName, false
	}
	model := parsed.baseModel
	if parsed.reasoningEffort != "" {
		model += "-" + parsed.reasoningEffort
	}
	return model, true
}

func applyNoPromptModelSuffix(req *model.CanonicalRequest, cfg config.Config) {
	if req == nil || !cfg.EnableNoPromptModelSuffix {
		return
	}
	parsed := parseProxyModelSuffixes(req.Model)
	if !parsed.hasNoPrompt {
		return
	}
	req.Model = parsed.baseModel
	if parsed.reasoningEffort != "" {
		req.Model += "-" + parsed.reasoningEffort
	}
	req.SkipProviderSystemPrompt = true
}

func prepareProviderClientModel(req *model.CanonicalRequest, resolvedModel string, provider config.ProviderConfig, cfg config.Config) string {
	if req == nil {
		return strings.TrimSpace(resolvedModel)
	}
	if !provider.HidesModel(req.Model) {
		applyNoPromptModelSuffix(req, cfg)
	}
	if strings.TrimSpace(resolvedModel) != "" {
		req.Model = resolvedModel
	}
	if !provider.HidesModel(req.Model) {
		applyNoPromptModelSuffix(req, cfg)
	}
	return req.Model
}

type providerClientModelRequest struct {
	req           *model.CanonicalRequest
	httpRequest   *http.Request
	resolvedModel string
	provider      config.ProviderConfig
	config        config.Config
}

func sourceModelBeforeProviderMapping(r *http.Request, rawClientModel string, selectedModel string, provider config.ProviderConfig) string {
	sourceModel := rawClientModel
	if info, ok := routeInfoFromRequest(r); ok && info.Legacy {
		if strings.HasPrefix(strings.TrimSpace(rawClientModel), "[") && strings.TrimSpace(selectedModel) != "" {
			sourceModel = selectedModel
		} else if routedModel, routed := legacyRoutingModelFromRequest(r); routed {
			sourceModel = routedModel
		}
	}
	if internalModel, ok := provider.InternalModelID(sourceModel, true); ok {
		return internalModel
	}
	return sourceModel
}

func prepareProviderClientModelForRequest(input providerClientModelRequest) string {
	if input.req == nil {
		return strings.TrimSpace(input.resolvedModel)
	}
	if intent, ok := proxyModelIntentFromRequest(input.httpRequest); ok && intent.HasNoPrompt && input.provider.EffectiveNoPromptModelSuffix(input.config.EnableNoPromptModelSuffix) && !input.provider.HidesModel(intent.CanonicalModel()) {
		input.req.SkipProviderSystemPrompt = true
	}
	if discovery, ok := defaultOverlayDiscoveryFromRequest(input.httpRequest); ok && discovery.ProviderID == input.provider.ID && discovery.RawModelID == input.req.Model {
		if _, exact := discovery.VisibleModelIDs[discovery.RawModelID]; exact {
			input.req.Model = input.resolvedModel
			return input.req.Model
		}
	}
	return prepareProviderClientModel(input.req, input.resolvedModel, input.provider, input.config)
}
