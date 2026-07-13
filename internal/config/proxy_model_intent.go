package config

import (
	"strings"

	"openai-compat-proxy/internal/model"
)

func (c Config) ResolveV1ProxyModelIntent(modelName string) (model.ProxyModelIntent, bool) {
	return c.ResolveV1ProxyModelIntentWithTargetCandidates(modelName, nil)
}

func (c Config) ResolveV1ProxyModelIntentWithTargetCandidates(modelName string, targetCandidates []string) (model.ProxyModelIntent, bool) {
	provider := ProviderConfig{ModelMap: c.V1ModelMap}
	intent, ok := model.ParseProxyModelIntent(modelName, staticModelMapAliasCandidates(c.V1ModelMap), model.ProxyModelIntentAxes{
		EnableReasoningEffort: true,
		EnablePro:             true,
		EnableNoPrompt:        c.EnableNoPromptModelSuffix,
		EnableUltra:           true,
	})
	if ok {
		if mapped, resolved := provider.resolveMappedProxyModelIntentWithTargetCandidates(intent, targetCandidates, false); resolved {
			return mapped, true
		}
	}

	baseModel := strings.TrimSpace(modelName)
	if mapped, _ := provider.resolveModelEntry(baseModel); strings.TrimSpace(mapped) != "" {
		target, parsed := parseModelMapTargetProxyModelIntent(mapped, targetCandidates)
		if parsed {
			return mergeProxyModelIntentMapTarget(model.ProxyModelIntent{BaseModel: baseModel}, target), true
		}
		if baseModel != "" {
			return model.ProxyModelIntent{BaseModel: mapped}, true
		}
	}
	return model.ProxyModelIntent{}, false
}

func (p ProviderConfig) ResolveMappedProxyModelIntent(intent model.ProxyModelIntent) (model.ProxyModelIntent, bool) {
	return p.resolveMappedProxyModelIntentWithTargetCandidates(intent, p.proxyModelIntentTargetCandidates(), true)
}

func (p ProviderConfig) resolveMappedProxyModelIntentWithTargetCandidates(intent model.ProxyModelIntent, targetCandidates []string, reverse bool) (model.ProxyModelIntent, bool) {
	for _, candidate := range proxyModelIntentMapCandidates(intent) {
		mapped, _ := p.resolveModelEntryWithOrder(candidate, reverse)
		if strings.TrimSpace(mapped) == "" {
			continue
		}
		target, parsed := parseModelMapTargetProxyModelIntent(mapped, targetCandidates)
		if !parsed {
			continue
		}
		mappedIntent := mergeProxyModelIntentMapTarget(intent, target)
		mappedIntent.HasModelMapAlias = true
		return mappedIntent, true
	}
	return intent, false
}

func parseModelMapTargetProxyModelIntent(modelName string, literalCandidates []string) (model.ProxyModelIntent, bool) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return model.ProxyModelIntent{}, false
	}
	for _, literalCandidate := range literalCandidates {
		if strings.TrimSpace(literalCandidate) == modelName {
			return model.ProxyModelIntent{BaseModel: modelName}, true
		}
	}

	axes := model.ProxyModelIntentAxes{
		EnableReasoningEffort: true,
		EnablePro:             true,
	}
	var selected model.ProxyModelIntent
	for offset := 0; offset < len(modelName); {
		next := strings.IndexByte(modelName[offset:], '-')
		if next < 0 {
			break
		}
		offset += next
		candidate := modelName[:offset]
		intent, parsed := model.ParseProxyModelIntent(modelName, []string{candidate}, axes)
		if parsed && (selected.BaseModel == "" || len(intent.BaseModel) < len(selected.BaseModel)) {
			selected = intent
		}
		offset++
	}
	if selected.BaseModel != "" {
		return selected, true
	}
	return model.ProxyModelIntent{BaseModel: modelName}, true
}

func mergeProxyModelIntentMapTarget(source model.ProxyModelIntent, target model.ProxyModelIntent) model.ProxyModelIntent {
	source.BaseModel = target.BaseModel
	if target.ReasoningEffort != "" {
		source.ReasoningEffort = target.ReasoningEffort
	}
	if target.ReasoningMode != "" {
		source.ReasoningMode = target.ReasoningMode
	}
	return source
}

func staticModelMapAliasCandidates(entries []ModelMapEntry) []string {
	candidates := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsStaticKey {
			candidates = append(candidates, entry.UnescapedKey)
		}
	}
	return candidates
}

func (p ProviderConfig) proxyModelIntentTargetCandidates() []string {
	return append([]string(nil), p.VisibleModelIDs()...)
}

func (p ProviderConfig) ParseProxyModelIntent(modelName string, rootNoPrompt bool) (model.ProxyModelIntent, bool) {
	return p.ParseProxyModelIntentWithReasoningMode(modelName, rootNoPrompt, true)
}

func (p ProviderConfig) ParseProxyModelIntentWithReasoningMode(modelName string, rootNoPrompt bool, rootReasoningMode bool) (model.ProxyModelIntent, bool) {
	return p.ParseProxyModelIntentWithReasoningModeCandidates(modelName, rootNoPrompt, rootReasoningMode, nil)
}

func (p ProviderConfig) ParseProxyModelIntentWithReasoningModeCandidates(modelName string, rootNoPrompt bool, rootReasoningMode bool, additionalCandidates []string) (model.ProxyModelIntent, bool) {
	modelName = strings.TrimSpace(modelName)
	candidates := append(p.proxyModelIntentCandidates(), additionalCandidates...)
	return model.ParseProxyModelIntent(modelName, candidates, model.ProxyModelIntentAxes{
		EnableReasoningEffort: p.EnableReasoningEffortSuffix || p.HasManualReasonSuffixForModel(modelName),
		EnablePro:             p.EffectiveEnableReasoningModeSuffix(rootReasoningMode),
		EnableNoPrompt:        p.EffectiveNoPromptModelSuffix(rootNoPrompt),
		EnableUltra:           true,
	})
}

func (p ProviderConfig) ProxyModelIntentAllowsAlias(intent model.ProxyModelIntent) bool {
	if strings.TrimSpace(intent.BaseModel) == "" || p.HidesModel(intent.CanonicalModel()) {
		return false
	}
	for _, candidate := range proxyModelIntentMapCandidates(intent) {
		if mapped, _ := p.resolveModelEntryWithOrder(candidate, true); strings.TrimSpace(mapped) != "" {
			return !p.HidesModel(mapped)
		}
	}
	return false
}

func (p ProviderConfig) HasProxyModelIntentCandidatePrefix(modelName string) bool {
	modelName = strings.TrimSpace(modelName)
	for _, candidate := range p.proxyModelIntentCandidates() {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" && strings.HasPrefix(modelName, candidate+"-") {
			return true
		}
	}
	return false
}

func (p ProviderConfig) AllowsInternalProxyModelIntent(intent model.ProxyModelIntent, rootNoPrompt bool) bool {
	if p.HidesModel(intent.CanonicalModel()) {
		return false
	}
	return providerAllowsInternalVisibleModel(p, ProxyModelIntentRoutingModel(intent), rootNoPrompt)
}

func (p ProviderConfig) AllowsLiteralModelMapTarget(modelName string) bool {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" || p.HidesModel(modelName) {
		return false
	}
	for _, entry := range p.ModelMap {
		if !entry.TargetHasCaptures && strings.TrimSpace(entry.UnescapedTarget) == modelName {
			return true
		}
	}
	return false
}

func ProxyModelIntentRoutingModel(intent model.ProxyModelIntent) string {
	modelName := intent.BaseModel
	if intent.ReasoningEffort != "" {
		modelName += "-" + intent.ReasoningEffort
	}
	return modelName
}

func (p ProviderConfig) proxyModelIntentCandidates() []string {
	candidates := append([]string(nil), p.VisibleModelIDs()...)
	candidates = append(candidates, staticModelMapAliasCandidates(p.ModelMap)...)
	return candidates
}

func proxyModelIntentMapCandidates(intent model.ProxyModelIntent) []string {
	candidates := make([]string, 0, 4)
	if intent.ReasoningEffort != "" && intent.ReasoningMode != "" {
		candidates = append(candidates, intent.BaseModel+"-"+intent.ReasoningEffort+"-"+intent.ReasoningMode)
	}
	if intent.ReasoningMode != "" {
		candidates = append(candidates, intent.BaseModel+"-"+intent.ReasoningMode)
	}
	if intent.ReasoningEffort != "" {
		candidates = append(candidates, intent.BaseModel+"-"+intent.ReasoningEffort)
	}
	candidates = append(candidates, intent.BaseModel)
	return candidates
}
