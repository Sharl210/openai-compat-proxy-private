package config

import (
	"strings"

	"openai-compat-proxy/internal/model"
)

func (c Config) ResolveV1ProxyModelIntent(modelName string) (model.ProxyModelIntent, bool) {
	return c.ResolveV1ProxyModelIntentWithTargetCandidatesAndRequestEffort(modelName, "", nil)
}

func (c Config) ResolveV1ProxyModelIntentWithTargetCandidates(modelName string, targetCandidates []string) (model.ProxyModelIntent, bool) {
	return c.ResolveV1ProxyModelIntentWithTargetCandidatesAndRequestEffort(modelName, "", targetCandidates)
}

func (c Config) ResolveV1ProxyModelIntentWithTargetCandidatesAndRequestEffort(modelName string, requestEffort string, targetCandidates []string) (model.ProxyModelIntent, bool) {
	provider := ProviderConfig{ModelMap: c.V1ModelMap}
	modelName = strings.TrimSpace(modelName)
	normalizedRequestEffort := normalizeReasoningEffort(requestEffort)
	intent, ok := model.ParseProxyModelIntent(modelName, staticModelMapAliasCandidates(c.V1ModelMap), model.ProxyModelIntentAxes{
		EnableReasoningEffort: true,
		EnablePro:             true,
		EnableAuto:            true,
		EnableAdaptive:        true,
		EnableNoPrompt:        c.EnableNoPromptModelSuffix,
		EnableUltra:           true,
	})
	if !ok {
		intent = model.ProxyModelIntent{BaseModel: modelName}
	}
	if normalizedRequestEffort != "" && intent.ReasoningEffort == "" {
		intent.ReasoningEffort = normalizedRequestEffort
	}
	if intent.BaseModel != "" {
		if mapped, resolved := provider.resolveMappedProxyModelIntentWithTargetCandidatesAndSourceProvider(intent, targetCandidates, true, true); resolved {
			return mapped, true
		}
	}
	return model.ProxyModelIntent{}, false
}

func (p ProviderConfig) ResolveMappedProxyModelIntent(intent model.ProxyModelIntent) (model.ProxyModelIntent, bool) {
	return p.resolveMappedProxyModelIntentWithTargetCandidates(intent, p.proxyModelIntentTargetCandidates(), true)
}

func (p ProviderConfig) resolveMappedProxyModelIntentWithTargetCandidates(intent model.ProxyModelIntent, targetCandidates []string, reverse bool) (model.ProxyModelIntent, bool) {
	return p.resolveMappedProxyModelIntentWithTargetCandidatesAndSourceProvider(intent, targetCandidates, reverse, false)
}

func (p ProviderConfig) resolveMappedProxyModelIntentWithTargetCandidatesAndSourceProvider(intent model.ProxyModelIntent, targetCandidates []string, reverse bool, useSourceProviderAsTarget bool) (model.ProxyModelIntent, bool) {
	for _, candidate := range proxyModelIntentMapCandidates(intent) {
		var mapped string
		var entry ModelMapEntry
		if useSourceProviderAsTarget {
			mapped, entry = p.resolveModelEntryWithStaticPriority(candidate, reverse)
		} else {
			mapped, entry = p.resolveModelEntryWithOrder(candidate, reverse)
		}
		if strings.TrimSpace(mapped) == "" {
			continue
		}
		target, parsed := parseModelMapTargetProxyModelIntentWithEntry(mapped, targetCandidates, entry, useSourceProviderAsTarget)
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
	return parseModelMapTargetProxyModelIntentWithEntry(modelName, literalCandidates, ModelMapEntry{}, false)
}

func parseModelMapTargetProxyModelIntentWithEntry(modelName string, literalCandidates []string, entry ModelMapEntry, useSourceProviderAsTarget bool) (model.ProxyModelIntent, bool) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return model.ProxyModelIntent{}, false
	}
	for _, literalCandidate := range literalCandidates {
		if strings.TrimSpace(literalCandidate) == modelName {
			return model.ProxyModelIntent{BaseModel: modelName, TargetProviderID: modelMapTargetProviderID(entry, useSourceProviderAsTarget)}, true
		}
	}

	axes := model.ProxyModelIntentAxes{
		EnableReasoningEffort: true,
		EnablePro:             true,
		EnableAuto:            true,
		EnableAdaptive:        true,
		EnableNoPrompt:        true,
		EnableUltra:           true,
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
		selected.TargetProviderID = modelMapTargetProviderID(entry, useSourceProviderAsTarget)
		return selected, true
	}
	return model.ProxyModelIntent{BaseModel: modelName, TargetProviderID: modelMapTargetProviderID(entry, useSourceProviderAsTarget)}, true
}

func modelMapTargetProviderID(entry ModelMapEntry, useSourceProviderAsTarget bool) string {
	if targetProviderID := strings.TrimSpace(entry.TargetProviderID); targetProviderID != "" {
		return targetProviderID
	}
	if useSourceProviderAsTarget {
		return strings.TrimSpace(entry.SourceProviderID)
	}
	return ""
}

func mergeProxyModelIntentMapTarget(source model.ProxyModelIntent, target model.ProxyModelIntent) model.ProxyModelIntent {
	if source.ReasoningEffort != "" && target.ReasoningEffort != "" {
		suffix := "-" + source.ReasoningEffort
		if strings.HasSuffix(target.BaseModel, suffix) && strings.TrimSuffix(target.BaseModel, suffix) == source.BaseModel {
			target.BaseModel = strings.TrimSuffix(target.BaseModel, suffix)
		}
	}
	source.BaseModel = target.BaseModel
	if target.ReasoningEffort != "" {
		source.ReasoningEffort = target.ReasoningEffort
	}
	if target.ReasoningMode != "" {
		source.ReasoningMode = target.ReasoningMode
	}
	if target.HasAuto {
		source.HasAuto = true
	}
	if target.HasAdaptive {
		source.HasAdaptive = true
	}
	if target.HasNoPrompt {
		source.HasNoPrompt = true
	}
	if target.HasUltra {
		source.HasUltra = true
	}
	if target.TargetProviderID != "" {
		source.TargetProviderID = target.TargetProviderID
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
	return p.RoutingModelCandidates()
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
	return model.ParseProxyModelIntent(modelName, candidates, p.proxyModelIntentAxes(modelName, rootNoPrompt, rootReasoningMode))
}

func (p ProviderConfig) proxyModelIntentAxes(modelName string, rootNoPrompt bool, rootReasoningMode bool) model.ProxyModelIntentAxes {
	return model.ProxyModelIntentAxes{
		EnableReasoningEffort: p.EnableReasoningEffortSuffix || p.HasManualReasonSuffixForModel(modelName),
		EnablePro:             p.EffectiveEnableReasoningModeSuffix(rootReasoningMode),
		EnableAuto:            true,
		EnableAdaptive:        true,
		EnableNoPrompt:        p.EffectiveNoPromptModelSuffix(rootNoPrompt),
		EnableUltra:           true,
	}
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
		if !p.ModelMapEntryAppliesToProvider(entry) {
			continue
		}
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
	return p.RoutingModelCandidates()
}

func (p ProviderConfig) RoutingModelCandidates() []string {
	candidates := make([]string, 0, len(p.ManualModels)+len(p.ModelMap)*2)
	seen := make(map[string]struct{}, cap(candidates))
	add := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return
		}
		if _, exists := seen[candidate]; exists {
			return
		}
		seen[candidate] = struct{}{}
		candidates = append(candidates, candidate)
	}
	for _, manualModel := range p.ManualModels {
		manualModel = strings.TrimSpace(manualModel)
		if baseModel, ok := manualReasonSuffixStaticBase(manualModel); ok {
			add(baseModel)
			continue
		}
		if manualModel == "" || !isStaticModelPattern(manualModel) {
			continue
		}
		add(manualModel)
	}
	for _, entry := range p.ModelMap {
		if !p.ModelMapEntryAppliesToProvider(entry) || !entry.IsStaticKey {
			continue
		}
		add(entry.UnescapedKey)
	}
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
