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
	candidates := mergeProxyModelIntentCandidates(additionalCandidates, p.proxyModelIntentCandidates())
	var sourceIntent model.ProxyModelIntent
	var parsed bool
	if len(candidates) > 0 {
		sourceIntent, parsed = model.ParseProxyModelIntent(internalModel, candidates, p.proxyModelIntentAxes(internalModel, rootNoPrompt, rootReasoningMode))
	} else {
		sourceIntent, parsed = p.ParseProxyModelIntentWithReasoningMode(internalModel, rootNoPrompt, rootReasoningMode)
	}
	if !parsed {
		if p.AllowsLiteralModelMapTarget(internalModel) {
			intent := model.ProxyModelIntent{BaseModel: internalModel}
			return ProviderProxyModelResolution{SourceIntent: intent, ResolvedIntent: intent}, true
		}
		// 正则 MANUAL_MODELS 匹配的展示模型（如 RAW_MODEL_NAME_REPLACE 替换后的伪原始名）
		// 也应视为合法路由候选；这类模型无 suffix、不是 MODEL_MAP 字面量 target。
		if p.manualModelMatches(internalModel) {
			intent := model.ProxyModelIntent{BaseModel: internalModel}
			return ProviderProxyModelResolution{SourceIntent: intent, ResolvedIntent: intent}, true
		}
		return ProviderProxyModelResolution{}, false
	}
	if p.HidesModel(sourceIntent.CanonicalModel()) {
		return ProviderProxyModelResolution{}, false
	}
	matchedAlias := p.ProxyModelIntentAllowsAlias(sourceIntent)
	if !matchedAlias && !proxyModelCandidateContains(candidates, sourceIntent.BaseModel) && !p.AllowsInternalProxyModelIntent(sourceIntent, rootNoPrompt) {
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

func mergeProxyModelIntentCandidates(primary []string, fallback []string) []string {
	merged := make([]string, 0, len(primary)+len(fallback))
	seen := make(map[string]struct{}, len(primary)+len(fallback))
	for _, candidates := range [][]string{primary, fallback} {
		for _, candidate := range candidates {
			candidate = strings.TrimSpace(candidate)
			if candidate == "" {
				continue
			}
			if _, exists := seen[candidate]; exists {
				continue
			}
			seen[candidate] = struct{}{}
			merged = append(merged, candidate)
		}
	}
	return merged
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
