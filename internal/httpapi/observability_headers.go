package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"openai-compat-proxy/internal/cacheinfo"
	"openai-compat-proxy/internal/config"
	modelpkg "openai-compat-proxy/internal/model"
	reasoningpkg "openai-compat-proxy/internal/reasoning"
	"openai-compat-proxy/internal/upstream"
)

const (
	headerClientToProxyModel                 = "X-Client-To-Proxy-Model"
	headerClientToProxyServiceTier           = "X-Client-To-Proxy-Service-Tier"
	headerClientToProxyReasoningParameters   = "X-Client-To-Proxy-Reasoning-Parameters"
	headerClientToProxyReasoningEffort       = "X-Client-To-Proxy-Reasoning-Effort"
	headerClientToProxyNoPrompt              = "X-Client-To-Proxy-NoPrompt"
	headerSystemPromptAttach                 = "X-SYSTEM-PROMPT-ATTACH"
	headerCacheInfoTimezone                  = "X-Cache-Info-Timezone"
	headerThisUsageTokens                    = "X-This-Usage-Tokens"
	headerProxyToUpstreamModel               = "X-Proxy-To-Upstream-Model"
	headerProxyEstimatedInputTokens          = "X-Proxy-Estimated-Input-Tokens"
	headerProxyModelLimitContextTokens       = "X-Proxy-Model-Limit-Context-Tokens"
	headerProxyToUpstreamServiceTier         = "X-Proxy-To-Upstream-Service-Tier"
	headerProxyToUpstreamMaxOutputTokens     = "X-Proxy-To-Upstream-Max-Output-Tokens"
	headerProxyToUpstreamReasoningEffort     = "X-Proxy-To-Upstream-Reasoning-Effort"
	headerProxyToUpstreamReasoningParameters = "X-Proxy-To-Upstream-Reasoning-Parameters"
	headerProxyUpstreamRetryCount            = "X-Proxy-Upstream-Retry-Count"
	headerProxyUpstreamRetryDelay            = "X-Proxy-Upstream-Retry-Delay"
	headerProxyUpstreamAnthropicCacheControl = "X-Proxy-Upstream-Anthropic-Cache-Control"
	headerProviderTodayCacheRate             = "X-Provider-Today-Cache-Rate"
	headerProviderHistoryCacheRate           = "X-Provider-History-Cache-Rate"
	headerRootProviderTodayCacheRate         = "X-Root-Provider-Today-Cache-Rate"
	headerRootProviderHistoryCacheRate       = "X-Root-Provider-History-Cache-Rate"
)

const (
	clientReasoningProtocolResponses = "responses"
	clientReasoningProtocolChat      = "chat"
	clientReasoningProtocolMessages  = "messages"
)

func clientToProxyReasoningEffort(model string, reasoning *modelpkg.CanonicalReasoning, suffixEnabled bool) string {
	if suffixEnabled {
		if _, effort, ok := reasoningpkg.SplitSuffix(strings.TrimSpace(model)); ok {
			return effort
		}
	}
	if reasoning == nil {
		return ""
	}
	if inferred := upstream.InferReasoningEffortFromAnthropicRaw(reasoning.Raw); inferred != "" {
		return inferred
	}
	return strings.TrimSpace(reasoning.Effort)
}

func clientToProxyReasoningParameters(protocol string, model string, reasoning *modelpkg.CanonicalReasoning, suffixEnabled bool, maxOutputTokens *int) string {
	effective := clientToProxyEffectiveReasoning(protocol, model, reasoning, suffixEnabled, maxOutputTokens)
	if effective == nil {
		return ""
	}
	payload := map[string]any{}
	switch protocol {
	case clientReasoningProtocolResponses:
		if reasoningPayload := clientResponsesReasoningPayload(effective); len(reasoningPayload) > 0 {
			payload["reasoning"] = reasoningPayload
		}
	case clientReasoningProtocolChat:
		if len(effective.Raw) > 0 {
			payload["reasoning"] = upstream.NormalizeOpenAIReasoningPayloadForObservability(effective)
		} else if effective.Effort != "" {
			payload["reasoning_effort"] = effective.Effort
		}
	case clientReasoningProtocolMessages:
		if thinking, ok := effective.Raw["thinking"]; ok {
			payload["thinking"] = thinking
		}
		if outputConfig, ok := effective.Raw["output_config"]; ok {
			payload["output_config"] = outputConfig
		}
	}
	if len(payload) == 0 {
		return ""
	}
	encoded, err := upstream.MarshalObservabilityJSON(payload)
	if err != nil {
		return ""
	}
	return encoded
}

func clientToProxyEffectiveReasoning(protocol string, model string, reasoning *modelpkg.CanonicalReasoning, suffixEnabled bool, maxOutputTokens *int) *modelpkg.CanonicalReasoning {
	effective := cloneCanonicalReasoning(reasoning)
	if suffixEnabled {
		if _, effort, ok := reasoningpkg.SplitSuffix(strings.TrimSpace(model)); ok {
			effective = applyResolvedReasoningEffort(effective, effort)
			if protocol == clientReasoningProtocolMessages {
				effective = applyAnthropicThinkingFromResolvedEffort(effective, true, model, maxOutputTokens, 32000)
			}
		}
	}
	return effective
}

func clientResponsesReasoningPayload(reasoning *modelpkg.CanonicalReasoning) map[string]any {
	if reasoning == nil {
		return nil
	}
	if len(reasoning.Raw) > 0 {
		return upstream.NormalizeOpenAIReasoningPayloadForObservability(reasoning)
	}
	payload := map[string]any{}
	if reasoning.Effort != "" {
		payload["effort"] = reasoning.Effort
	}
	if reasoning.Summary != "" {
		payload["summary"] = reasoning.Summary
	}
	if len(payload) == 0 {
		return nil
	}
	if _, ok := payload["summary"]; !ok {
		payload["summary"] = "auto"
	}
	return payload
}

func cloneCanonicalReasoning(reasoning *modelpkg.CanonicalReasoning) *modelpkg.CanonicalReasoning {
	if reasoning == nil {
		return nil
	}
	cloned := &modelpkg.CanonicalReasoning{
		Effort:  reasoning.Effort,
		Summary: reasoning.Summary,
	}
	if len(reasoning.Raw) > 0 {
		cloned.Raw = cloneMap(reasoning.Raw)
	}
	return cloned
}

func normalizeCanonicalModelAndReasoningForProvider(canon *modelpkg.CanonicalRequest, sourceModel string, requestEffort string, provider config.ProviderConfig, providerCfg config.Config) {
	if canon == nil {
		return
	}
	if strings.TrimSpace(sourceModel) == "" {
		sourceModel = canon.Model
	}
	mappedModel, effort := provider.ResolveModelAndEffortWithRequestEffort(sourceModel, requestEffort, provider.EnableReasoningEffortSuffix)
	canon.Model = mappedModel
	canon.Reasoning = applyResolvedReasoningEffort(canon.Reasoning, effort)
	canon.PassThroughRawReasoning = providerCfg.UpstreamEndpointType == config.UpstreamEndpointTypeAnthropic && !provider.MapReasoningSuffixToAnthropicThinking
}

func finalizeAnthropicReasoningForUpstream(canon *modelpkg.CanonicalRequest, provider config.ProviderConfig, providerCfg config.Config) {
	if canon == nil {
		return
	}
	if providerCfg.UpstreamEndpointType != config.UpstreamEndpointTypeAnthropic {
		canon.PassThroughRawReasoning = false
		return
	}
	if !provider.MapReasoningSuffixToAnthropicThinking {
		canon.PassThroughRawReasoning = true
		return
	}
	canon.Reasoning = applyAnthropicThinkingFromResolvedEffort(canon.Reasoning, canEnableAnthropicThinkingForMessages(canon.Messages), canon.Model, canon.MaxOutputTokens, providerCfg.AnthropicMaxThinkingBudget)
	canon.PassThroughRawReasoning = false
}

func canEnableAnthropicThinkingForMessages(messages []modelpkg.CanonicalMessage) bool {
	if !hasAnthropicReplayHistory(messages) {
		return true
	}
	return hasRealAnthropicThinkingHistory(messages)
}

func hasAnthropicReplayHistory(messages []modelpkg.CanonicalMessage) bool {
	for _, msg := range messages {
		if msg.Role == "assistant" || msg.Role == "tool" || msg.ToolCallID != "" || len(msg.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

func hasRealAnthropicThinkingHistory(messages []modelpkg.CanonicalMessage) bool {
	for _, msg := range messages {
		if msg.Role != "assistant" {
			continue
		}
		if strings.TrimSpace(msg.ReasoningContent) != "" && !isSyntheticReasoningSummary(msg.ReasoningContent) {
			return true
		}
		for _, block := range msg.ReasoningBlocks {
			if len(block) == 0 || isSyntheticReasoningBlock(block) {
				continue
			}
			blockType := strings.TrimSpace(stringValue(block["type"]))
			if blockType == "thinking" || blockType == "redacted_thinking" {
				return true
			}
		}
	}
	return false
}

func applyProviderMaxOutputTokens(canon *modelpkg.CanonicalRequest, provider config.ProviderConfig) {
	if canon == nil {
		return
	}
	effort := ""
	if canon.Reasoning != nil {
		effort = strings.TrimSpace(canon.Reasoning.Effort)
	}
	resolved := provider.ResolveUpstreamMaxOutputTokensForReasoning(strings.TrimSpace(canon.Model), effort)
	if resolved == 0 {
		return
	}
	if resolved == -1 {
		if canon.MaxOutputTokens == nil || provider.ForceUpstreamMaxOutputTokens {
			canon.MaxOutputTokens = nil
			canon.OmitMaxOutputTokens = true
		}
		return
	}
	if canon.MaxOutputTokens != nil && !provider.ForceUpstreamMaxOutputTokens {
		return
	}
	maxOutputTokens := resolved
	canon.MaxOutputTokens = &maxOutputTokens
	canon.OmitMaxOutputTokens = false
}

func serviceTierFromTopLevelFields(fields map[string]any) string {
	if len(fields) == 0 {
		return ""
	}
	if value, ok := fields["service_tier"]; ok {
		if text, ok := value.(string); ok {
			return strings.TrimSpace(text)
		}
	}
	if value, ok := fields["serviceTier"]; ok {
		if text, ok := value.(string); ok {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func applyProviderOpenAIServiceTierOverride(canon *modelpkg.CanonicalRequest, provider config.ProviderConfig, providerCfg config.Config) {
	if canon == nil {
		return
	}
	if strings.TrimSpace(provider.OpenAIServiceTier) == "" {
		if providerCfg.UpstreamEndpointType != config.UpstreamEndpointTypeResponses && providerCfg.UpstreamEndpointType != config.UpstreamEndpointTypeChat {
			return
		}
		if canon.PreservedTopLevelFields == nil {
			return
		}
		if _, exists := canon.PreservedTopLevelFields["service_tier"]; exists {
			delete(canon.PreservedTopLevelFields, "serviceTier")
			return
		}
		if alias, exists := canon.PreservedTopLevelFields["serviceTier"]; exists {
			delete(canon.PreservedTopLevelFields, "serviceTier")
			canon.PreservedTopLevelFields["service_tier"] = alias
		}
		return
	}
	if providerCfg.UpstreamEndpointType != config.UpstreamEndpointTypeResponses && providerCfg.UpstreamEndpointType != config.UpstreamEndpointTypeChat {
		return
	}
	if canon.PreservedTopLevelFields == nil {
		canon.PreservedTopLevelFields = map[string]any{}
	}
	delete(canon.PreservedTopLevelFields, "serviceTier")
	canon.PreservedTopLevelFields["service_tier"] = provider.OpenAIServiceTier
}

func setDirectionalObservabilityHeaders(w http.ResponseWriter, r *http.Request, provider config.ProviderConfig, providerCfg config.Config, providerID string, canon modelpkg.CanonicalRequest, clientModel string, clientServiceTier string, clientReasoningParameters string, clientReasoningEffort string) error {
	preview, err := upstream.PreviewRequestObservability(canon, providerCfg.UpstreamEndpointType, providerCfg.MasqueradeTarget, providerCfg.InjectClaudeCodeMetadataUserID, providerCfg.InjectClaudeCodeSystemPrompt)
	if err != nil {
		return err
	}
	setProviderSystemPromptAttachHeader(w, provider, canon)
	w.Header().Set(headerClientToProxyModel, strings.TrimSpace(clientModel))
	w.Header().Set(headerClientToProxyServiceTier, strings.TrimSpace(clientServiceTier))
	w.Header().Set(headerClientToProxyReasoningParameters, strings.TrimSpace(clientReasoningParameters))
	w.Header().Set(headerClientToProxyReasoningEffort, strings.TrimSpace(clientReasoningEffort))
	if canon.SkipProviderSystemPrompt {
		w.Header().Set(headerClientToProxyNoPrompt, "true")
	} else {
		w.Header().Set(headerClientToProxyNoPrompt, "false")
	}
	w.Header().Set(headerProxyToUpstreamModel, strings.TrimSpace(preview.UpstreamModel))
	w.Header().Set(headerProxyEstimatedInputTokens, strconv.Itoa(estimateCanonicalInputTokens(canon)))
	setProxyModelLimitContextHeader(w, provider, canon)
	w.Header().Set(headerProxyToUpstreamServiceTier, strings.TrimSpace(preview.UpstreamServiceTier))
	if !canon.OmitMaxOutputTokens && canon.MaxOutputTokens != nil && *canon.MaxOutputTokens > 0 {
		w.Header().Set(headerProxyToUpstreamMaxOutputTokens, strconv.Itoa(*canon.MaxOutputTokens))
	} else {
		w.Header().Set(headerProxyToUpstreamMaxOutputTokens, "")
	}
	w.Header().Set(headerProxyToUpstreamReasoningEffort, strings.TrimSpace(proxyToUpstreamReasoningEffort(canon, preview)))
	w.Header().Set(headerProxyToUpstreamReasoningParameters, strings.TrimSpace(preview.ReasoningParameters))
	w.Header().Set(headerThisUsageTokens, "")
	w.Header().Set(headerProxyUpstreamRetryCount, strconv.Itoa(providerCfg.UpstreamRetryCount))
	w.Header().Set(headerProxyUpstreamRetryDelay, providerCfg.UpstreamRetryDelay.String())
	w.Header().Set(headerProxyUpstreamAnthropicCacheControl, strings.TrimSpace(providerCfg.UpstreamCacheControl))
	setCacheRateHeaders(w, r, providerID)
	return nil
}

func formatThisUsageTokens(usage map[string]any) string {
	if len(usage) == 0 {
		return ""
	}
	inputTokens, ok := usageNumberAsInt64(usage["input_tokens"])
	if !ok || inputTokens <= 0 {
		return ""
	}
	outputTokens, ok := usageNumberAsInt64(usage["output_tokens"])
	if !ok || outputTokens < 0 {
		return ""
	}
	cachedTokens := int64(0)
	if details, _ := usage["input_tokens_details"].(map[string]any); len(details) > 0 {
		if n, ok := usageNumberAsInt64(details["cached_tokens"]); ok && n > 0 {
			cachedTokens = n
		}
	} else if details, _ := usage["prompt_tokens_details"].(map[string]any); len(details) > 0 {
		if n, ok := usageNumberAsInt64(details["cached_tokens"]); ok && n > 0 {
			cachedTokens = n
		}
	} else if n, ok := usageNumberAsInt64(usage["cached_tokens"]); ok && n > 0 {
		cachedTokens = n
	}
	return formatThisUsageTokensValue(inputTokens, cachedTokens, outputTokens)
}

func formatThisUsageTokensValue(inputTokens int64, cachedTokens int64, outputTokens int64) string {
	return "↑ " + formatUsageTokensNumber(inputTokens) + "(" + formatUsageTokensNumber(cachedTokens) + " cached) | ↓ " + formatUsageTokensNumber(outputTokens)
}

func formatUsageTokensNumber(value int64) string {
	return formatThousands(value)
}

func formatThousands(value int64) string {
	negative := value < 0
	if negative {
		value = -value
	}
	if value < 1000 {
		if negative {
			return "-" + strconv.FormatInt(value, 10)
		}
		return strconv.FormatInt(value, 10)
	}
	digits := strconv.FormatInt(value, 10)
	parts := make([]byte, 0, len(digits)+len(digits)/3)
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			parts = append(parts, ',')
		}
		parts = append(parts, byte(r))
	}
	if negative {
		return "-" + string(parts)
	}
	return string(parts)
}

func setCacheRateHeaders(w http.ResponseWriter, r *http.Request, providerID string) {
	if r == nil {
		return
	}
	manager := cacheInfoManagerFromRequest(r)
	if manager == nil {
		return
	}
	info, ok := routeInfoFromRequest(r)
	if !ok {
		return
	}
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		providerID = strings.TrimSpace(info.ProviderID)
	}
	if providerStats, ok := manager.ProviderStatsSnapshot(providerID); ok {
		setCacheRateHeaderPair(w, headerProviderTodayCacheRate, headerProviderHistoryCacheRate, providerStats)
	}
	if info.Legacy {
		if rootStats := rootProviderStatsSnapshot(r, manager); rootStats != nil {
			setCacheRateHeaderPair(w, headerRootProviderTodayCacheRate, headerRootProviderHistoryCacheRate, rootStats)
		}
	}
}

func rootProviderStatsSnapshot(r *http.Request, manager *cacheinfo.Manager) *cacheinfo.ProviderStats {
	snapshot, ok := runtimeSnapshotFromRequest(r)
	if !ok || snapshot == nil || manager == nil {
		return nil
	}
	statsList := make([]*cacheinfo.ProviderStats, 0, len(snapshot.DefaultProviderIDs))
	for _, id := range snapshot.DefaultProviderIDs {
		if stats, ok := manager.ProviderStatsSnapshot(id); ok {
			statsList = append(statsList, stats)
		}
	}
	if len(statsList) == 0 {
		return nil
	}
	loc, err := snapshot.Config.CacheInfoLocation()
	if err != nil || loc == nil {
		loc = time.UTC
	}
	aggregated := cacheinfo.AggregateProviderStats(snapshot.Config.CacheInfoTimezone, time.Now().In(loc), statsList)
	return &aggregated
}

func setCacheRateHeaderPair(w http.ResponseWriter, todayHeader string, historyHeader string, stats *cacheinfo.ProviderStats) {
	if stats == nil {
		return
	}
	w.Header().Set(todayHeader, formatCacheRatePercentage(cacheinfo.DailyCacheRate(*stats)))
	w.Header().Set(historyHeader, formatCacheRatePercentage(cacheinfo.ProviderCacheRate(*stats)))
}

func formatCacheRatePercentage(rate float64) string {
	return strconv.FormatFloat(rate, 'f', 2, 64) + " %"
}

func proxyToUpstreamReasoningEffort(canon modelpkg.CanonicalRequest, preview upstream.RequestObservabilityPreview) string {
	if strings.TrimSpace(preview.ReasoningParameters) == "" {
		return ""
	}
	if canon.Reasoning != nil {
		if effort := strings.TrimSpace(canon.Reasoning.Effort); effort != "" {
			return effort
		}
		if inferred := upstream.InferReasoningEffortFromAnthropicRaw(canon.Reasoning.Raw); inferred != "" {
			return inferred
		}
	}
	return ""
}

func setProviderSystemPromptAttachHeader(w http.ResponseWriter, provider config.ProviderConfig, canon modelpkg.CanonicalRequest) {
	if canon.SkipProviderSystemPrompt {
		w.Header().Set(headerSystemPromptAttach, "")
		return
	}
	if strings.TrimSpace(provider.SystemPromptText) == "" || strings.TrimSpace(provider.SystemPromptFilesRaw) == "" {
		w.Header().Set(headerSystemPromptAttach, "")
		return
	}
	w.Header().Set(headerSystemPromptAttach, provider.SystemPromptPosition+":"+provider.SystemPromptFilesRaw)
}

func clearTransparencyHeaders(w http.ResponseWriter) {
	for _, header := range []string{
		"X-Provider-Name",
		headerProviderTodayCacheRate,
		headerProviderHistoryCacheRate,
		"X-Root-Env-Version",
		headerRootProviderTodayCacheRate,
		headerRootProviderHistoryCacheRate,
		headerCacheInfoTimezone,
		headerThisUsageTokens,
		"X-Provider-Version",
		headerSystemPromptAttach,
		headerClientToProxyModel,
		headerClientToProxyServiceTier,
		headerClientToProxyReasoningParameters,
		headerClientToProxyReasoningEffort,
		headerClientToProxyNoPrompt,
		headerProxyToUpstreamModel,
		headerProxyEstimatedInputTokens,
		headerProxyModelLimitContextTokens,
		headerProxyToUpstreamServiceTier,
		headerProxyToUpstreamMaxOutputTokens,
		headerProxyToUpstreamReasoningEffort,
		headerProxyToUpstreamReasoningParameters,
		headerProxyUpstreamRetryCount,
		headerProxyUpstreamRetryDelay,
		headerProxyUpstreamAnthropicCacheControl,
	} {
		w.Header().Del(header)
	}
}
