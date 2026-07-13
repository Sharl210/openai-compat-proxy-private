package config

import (
	"strings"

	"openai-compat-proxy/internal/model"
)

type ProviderProxyModelResolution struct {
	SourceIntent   model.ProxyModelIntent
	ResolvedIntent model.ProxyModelIntent
}

func (p ProviderConfig) ResolveExternalProxyModelIntentWithCandidates(externalModel string, rootNoPrompt bool, rootReasoningMode bool, additionalCandidates []string) (ProviderProxyModelResolution, bool) {
	internalModel, ok := p.InternalModelID(externalModel, true)
	if !ok {
		return ProviderProxyModelResolution{}, false
	}
	var sourceIntent model.ProxyModelIntent
	var parsed bool
	if len(additionalCandidates) > 0 {
		sourceIntent, parsed = model.ParseProxyModelIntent(internalModel, additionalCandidates, p.proxyModelIntentAxes(internalModel, rootNoPrompt, rootReasoningMode))
	} else {
		sourceIntent, parsed = p.ParseProxyModelIntentWithReasoningMode(internalModel, rootNoPrompt, rootReasoningMode)
	}
	if !parsed {
		if p.AllowsLiteralModelMapTarget(internalModel) {
			intent := model.ProxyModelIntent{BaseModel: internalModel}
			return ProviderProxyModelResolution{SourceIntent: intent, ResolvedIntent: intent}, true
		}
		return ProviderProxyModelResolution{}, false
	}
	if p.HidesModel(sourceIntent.CanonicalModel()) {
		return ProviderProxyModelResolution{}, false
	}
	matchedAlias := p.ProxyModelIntentAllowsAlias(sourceIntent)
	if !matchedAlias && !proxyModelCandidateContains(additionalCandidates, sourceIntent.BaseModel) && !p.AllowsInternalProxyModelIntent(sourceIntent, rootNoPrompt) {
		return ProviderProxyModelResolution{}, false
	}
	resolvedIntent := sourceIntent
	if mappedIntent, mapped := p.ResolveMappedProxyModelIntent(sourceIntent); mapped {
		resolvedIntent = mappedIntent
	}
	if p.HidesModel(resolvedIntent.CanonicalModel()) {
		return ProviderProxyModelResolution{}, false
	}
	if len(additionalCandidates) > 0 && resolvedIntent.ReasoningMode == "pro" && p.ResolveReasoningModeProCapability(ProxyModelIntentRoutingModel(resolvedIntent)) == ReasoningModeProCapabilityUnsupported {
		return ProviderProxyModelResolution{}, false
	}
	return ProviderProxyModelResolution{SourceIntent: sourceIntent, ResolvedIntent: resolvedIntent}, true
}

func proxyModelCandidateContains(candidates []string, modelName string) bool {
	modelName = strings.TrimSpace(modelName)
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == modelName {
			return true
		}
	}
	return false
}
