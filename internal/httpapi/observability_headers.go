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
	headerClientToProxyModel                       = "X-Client-To-Proxy-Model"
	headerClientToProxyServiceTier                 = "X-Client-To-Proxy-Service-Tier"
	headerClientToProxyReasoningParameters         = "X-Client-To-Proxy-Reasoning-Parameters"
	headerClientToProxyReasoningEffort             = "X-Client-To-Proxy-Reasoning-Effort"
	headerClientToProxyReasoningMode               = "X-Client-To-Proxy-Reasoning-Mode"
	headerClientToProxyNoPrompt                    = "X-Client-To-Proxy-NoPrompt"
	headerSystemPromptAttach                       = "X-SYSTEM-PROMPT-ATTACH"
	headerCacheInfoTimezone                        = "X-Cache-Info-Timezone"
	headerThisUsageTokens                          = "X-This-Usage-Tokens"
	headerThisUsageCacheWriteTokens                = "X-This-Usage-Cache-Write-Tokens"
	headerProxyToUpstreamModel                     = "X-Proxy-To-Upstream-Model"
	headerProxyEstimatedInputTokens                = "X-Proxy-Estimated-Input-Tokens"
	headerProxyModelLimitContextTokens             = "X-Proxy-Model-Limit-Context-Tokens"
	headerProxyToUpstreamServiceTier               = "X-Proxy-To-Upstream-Service-Tier"
	headerProxyToUpstreamMaxOutputTokens           = "X-Proxy-To-Upstream-Max-Output-Tokens"
	headerProxyToUpstreamMasqueradeUserAgent       = "X-Proxy-To-Upstream-Masquerade-User-Agent"
	headerProxyToUpstreamClaudeMetadataDeviceID    = "X-Proxy-To-Upstream-Claude-Metadata-Device-Id"
	headerProxyToUpstreamClaudeMetadataAccountUUID = "X-Proxy-To-Upstream-Claude-Metadata-Account-Uuid"
	headerProxyToUpstreamClaudeMetadataSessionID   = "X-Proxy-To-Upstream-Claude-Metadata-Session-Id"
	headerProxyToUpstreamReasoningEffort           = "X-Proxy-To-Upstream-Reasoning-Effort"
	headerProxyToUpstreamReasoningParameters       = "X-Proxy-To-Upstream-Reasoning-Parameters"
	headerProxyToUpstreamReasoningMode             = "X-Proxy-To-Upstream-Reasoning-Mode"
	headerProxyUpstreamRetryCount                  = "X-Proxy-Upstream-Retry-Count"
	headerProxyUpstreamRetryDelay                  = "X-Proxy-Upstream-Retry-Delay"
	headerProxyUpstreamAnthropicCacheControl       = "X-Proxy-Upstream-Anthropic-Cache-Control"
	headerProviderTodayCacheRate                   = "X-Provider-Today-Cache-Rate"
	headerProviderHistoryCacheRate                 = "X-Provider-History-Cache-Rate"
	headerProviderTodayCacheWriteCoverage          = "X-Provider-Today-Cache-Write-Coverage"
	headerProviderHistoryCacheWriteCoverage        = "X-Provider-History-Cache-Write-Coverage"
	headerRootProviderTodayCacheRate               = "X-Root-Provider-Today-Cache-Rate"
	headerRootProviderHistoryCacheRate             = "X-Root-Provider-History-Cache-Rate"
	headerRootProviderTodayCacheWriteCoverage      = "X-Root-Provider-Today-Cache-Write-Coverage"
	headerRootProviderHistoryCacheWriteCoverage    = "X-Root-Provider-History-Cache-Write-Coverage"
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
		Mode:    reasoning.Mode,
	}
	if len(reasoning.Raw) > 0 {
		cloned.Raw = cloneMap(reasoning.Raw)
	}
	return cloned
}

func normalizeCanonicalModelAndReasoningForProvider(canon *modelpkg.CanonicalRequest, sourceModel string, requestEffort string, provider config.ProviderConfig, providerCfg config.Config) {
	normalizeCanonicalModelAndReasoningForProxyModelIntent(canon, sourceModel, requestEffort, provider, providerCfg, modelpkg.ProxyModelIntent{})
}

func normalizeCanonicalModelAndReasoningForProxyModelIntent(canon *modelpkg.CanonicalRequest, sourceModel string, requestEffort string, provider config.ProviderConfig, providerCfg config.Config, intent modelpkg.ProxyModelIntent) {
	if canon == nil {
		return
	}
	if strings.TrimSpace(intent.BaseModel) != "" {
		canon.Model = intent.BaseModel
		canon.Reasoning = applyResolvedReasoningEffort(canon.Reasoning, intent.ReasoningEffort)
	} else if strings.TrimSpace(sourceModel) == "" {
		sourceModel = canon.Model
		mappedModel, effort := provider.ResolveModelAndEffortWithRequestEffort(sourceModel, requestEffort, provider.EnableReasoningEffortSuffix)
		canon.Model = mappedModel
		canon.Reasoning = applyResolvedReasoningEffort(canon.Reasoning, effort)
	} else {
		mappedModel, effort := provider.ResolveModelAndEffortWithRequestEffort(sourceModel, requestEffort, provider.EnableReasoningEffortSuffix)
		canon.Model = mappedModel
		canon.Reasoning = applyResolvedReasoningEffort(canon.Reasoning, effort)
	}
	if canon.Reasoning == nil {
		summary := strings.TrimSpace(provider.ReasoningSummaryDetail)
		if summary == "" {
			summary = strings.TrimSpace(providerCfg.ReasoningSummaryDetail)
		}
		if summary != "" {
			canon.Reasoning = &modelpkg.CanonicalReasoning{
				Summary: summary,
				Raw:     map[string]any{"summary": summary},
			}
		}
	}
	if canon.Reasoning != nil {
		configuredSummary := strings.TrimSpace(provider.ReasoningSummaryDetail)
		if configuredSummary == "" {
			configuredSummary = strings.TrimSpace(providerCfg.ReasoningSummaryDetail)
		}
		summary := strings.TrimSpace(canon.Reasoning.Summary)
		if summary == "" || (summary == config.ReasoningSummaryDetailAuto && configuredSummary != "" && configuredSummary != config.ReasoningSummaryDetailAuto) {
			if configuredSummary != "" {
				canon.Reasoning.Summary = configuredSummary
			}
		}
		if canon.Reasoning.Raw == nil {
			canon.Reasoning.Raw = map[string]any{}
		}
		if strings.TrimSpace(canon.Reasoning.Summary) != "" {
			canon.Reasoning.Raw["summary"] = canon.Reasoning.Summary
		}
	}
	canon.PassThroughRawReasoning = providerCfg.UpstreamEndpointType == config.UpstreamEndpointTypeAnthropic && !provider.MapReasoningSuffixToAnthropicThinking
}

func normalizeCanonicalModelAndReasoningForResolvedProxyModelIntent(canon *modelpkg.CanonicalRequest, sourceModel string, requestEffort string, provider config.ProviderConfig, providerCfg config.Config, intent modelpkg.ProxyModelIntent) {
	if intent.HasModelMapAlias || intent.ReasoningEffort != "" || intent.ReasoningMode != "" || intent.HasNoPrompt || intent.HasUltra {
		normalizeCanonicalModelAndReasoningForProxyModelIntent(canon, sourceModel, requestEffort, provider, providerCfg, intent)
		return
	}
	normalizeCanonicalModelAndReasoningForProvider(canon, sourceModel, requestEffort, provider, providerCfg)
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

func setDirectionalObservabilityHeaders(w http.ResponseWriter, r *http.Request, provider config.ProviderConfig, providerCfg config.Config, providerID string, canon *modelpkg.CanonicalRequest, clientModel string, clientServiceTier string, clientReasoningParameters string, clientReasoningEffort string, reasoningModeFallback *reasoningModeFallbackCoordinator) error {
	return setDirectionalObservabilityHeadersWithClientReasoningMode(w, r, provider, providerCfg, providerID, canon, clientModel, clientServiceTier, clientReasoningParameters, clientReasoningEffort, "", reasoningModeFallback)
}

func setDirectionalObservabilityHeadersWithClientReasoningMode(w http.ResponseWriter, r *http.Request, provider config.ProviderConfig, providerCfg config.Config, providerID string, canon *modelpkg.CanonicalRequest, clientModel string, clientServiceTier string, clientReasoningParameters string, clientReasoningEffort string, clientReasoningMode string, reasoningModeFallback *reasoningModeFallbackCoordinator) error {
	if canon == nil {
		return nil
	}
	upstream.PrepareClaudeMetadataForRequest(canon, providerCfg)
	preview, err := upstream.PreviewRequestObservability(*canon, providerCfg.UpstreamEndpointType, providerCfg.MasqueradeTarget, providerCfg.InjectClaudeCodeMetadataUserID, providerCfg.InjectClaudeCodeSystemPrompt)
	if err != nil {
		return err
	}
	setProviderSystemPromptAttachHeader(w, provider, *canon)
	w.Header().Set(headerClientToProxyModel, strings.TrimSpace(clientModel))
	w.Header().Set(headerClientToProxyServiceTier, strings.TrimSpace(clientServiceTier))
	w.Header().Set(headerClientToProxyReasoningParameters, strings.TrimSpace(clientReasoningParameters))
	w.Header().Set(headerClientToProxyReasoningEffort, strings.TrimSpace(clientReasoningEffort))
	w.Header().Set(headerClientToProxyReasoningMode, strings.TrimSpace(clientReasoningMode))
	w.Header().Set(headerProxyToUpstreamReasoningMode, proxyToUpstreamReasoningMode(*canon, reasoningModeFallback))
	if canon.SkipProviderSystemPrompt {
		w.Header().Set(headerClientToProxyNoPrompt, "true")
	} else {
		w.Header().Set(headerClientToProxyNoPrompt, "false")
	}
	w.Header().Set(headerProxyToUpstreamModel, strings.TrimSpace(preview.UpstreamModel))
	w.Header().Set(headerProxyEstimatedInputTokens, strconv.Itoa(estimateCanonicalInputTokens(*canon)))
	setProxyModelLimitContextHeader(w, provider, *canon)
	w.Header().Set(headerProxyToUpstreamServiceTier, strings.TrimSpace(preview.UpstreamServiceTier))
	if !canon.OmitMaxOutputTokens && canon.MaxOutputTokens != nil && *canon.MaxOutputTokens > 0 {
		w.Header().Set(headerProxyToUpstreamMaxOutputTokens, strconv.Itoa(*canon.MaxOutputTokens))
	} else {
		w.Header().Set(headerProxyToUpstreamMaxOutputTokens, "")
	}
	w.Header().Set(headerProxyToUpstreamReasoningEffort, strings.TrimSpace(proxyToUpstreamReasoningEffort(*canon, preview)))
	w.Header().Set(headerProxyToUpstreamReasoningParameters, strings.TrimSpace(preview.ReasoningParameters))
	w.Header().Set(headerThisUsageTokens, "")
	w.Header().Set(headerThisUsageCacheWriteTokens, "")
	for _, header := range []string{
		headerProviderTodayCacheWriteCoverage,
		headerProviderHistoryCacheWriteCoverage,
		headerRootProviderTodayCacheWriteCoverage,
		headerRootProviderHistoryCacheWriteCoverage,
	} {
		w.Header().Set(header, formatCacheRatePercentage(0))
	}
	w.Header().Set(headerProxyUpstreamRetryCount, strconv.Itoa(providerCfg.UpstreamRetryCount))
	w.Header().Set(headerProxyUpstreamRetryDelay, providerCfg.UpstreamRetryDelay.String())
	w.Header().Set(headerProxyUpstreamAnthropicCacheControl, strings.TrimSpace(providerCfg.UpstreamCacheControl))
	w.Header().Set(headerProxyToUpstreamMasqueradeUserAgent, upstream.FinalMasqueradeUserAgent(providerCfg.UpstreamUserAgent, providerCfg.MasqueradeTarget, providerCfg.UpstreamMasqueradeClientVersion))
	setClaudeMetadataObservabilityHeaders(w, providerCfg, *canon)
	setCacheRateHeaders(w, r, providerID)
	return nil
}

func clientReasoningModeForRequest(rawClientModel string, canon modelpkg.CanonicalRequest, provider config.ProviderConfig, providerCfg config.Config) string {
	intent, parsed := provider.ParseProxyModelIntentWithReasoningMode(rawClientModel, providerCfg.EnableNoPromptModelSuffix, providerCfg.EffectiveEnableReasoningModeSuffix())
	if parsed && intent.ReasoningMode != "" {
		return intent.ReasoningMode
	}
	return clientToProxyReasoningMode(canon)
}

func setReasoningModeObservabilityHeaders(w http.ResponseWriter, canon modelpkg.CanonicalRequest, fallback *reasoningModeFallbackCoordinator) {
	if _, exists := w.Header()[http.CanonicalHeaderKey(headerClientToProxyReasoningMode)]; !exists {
		w.Header().Set(headerClientToProxyReasoningMode, clientToProxyReasoningMode(canon))
	}
	setProxyToUpstreamReasoningModeHeader(w, canon, fallback)
}

func refreshFallbackUpstreamReasoningObservabilityHeaders(w http.ResponseWriter, canon modelpkg.CanonicalRequest, providerCfg config.Config) error {
	preview, err := upstream.PreviewRequestObservability(canon, providerCfg.UpstreamEndpointType, providerCfg.MasqueradeTarget, providerCfg.InjectClaudeCodeMetadataUserID, providerCfg.InjectClaudeCodeSystemPrompt)
	if err != nil {
		return err
	}
	w.Header().Set(headerProxyToUpstreamReasoningEffort, strings.TrimSpace(proxyToUpstreamReasoningEffort(canon, preview)))
	w.Header().Set(headerProxyToUpstreamReasoningParameters, strings.TrimSpace(preview.ReasoningParameters))
	return nil
}

func setProxyToUpstreamReasoningModeHeader(w http.ResponseWriter, canon modelpkg.CanonicalRequest, fallback *reasoningModeFallbackCoordinator) {
	w.Header().Set(headerProxyToUpstreamReasoningMode, proxyToUpstreamReasoningMode(canon, fallback))
}

func clientToProxyReasoningMode(canon modelpkg.CanonicalRequest) string {
	switch canon.ReasoningModeOrigin {
	case modelpkg.ReasoningModeOriginSuffix:
		return string(modelpkg.ReasoningModePro)
	case modelpkg.ReasoningModeOriginBody:
		if canon.Reasoning != nil {
			return strings.TrimSpace(string(canon.Reasoning.Mode))
		}
	}
	return ""
}

func proxyToUpstreamReasoningMode(canon modelpkg.CanonicalRequest, fallback *reasoningModeFallbackCoordinator) string {
	if fallback != nil && (fallback.modeUnsupported || fallback.retried) {
		return "pro_failed:model_unsupported"
	}
	if canon.Reasoning == nil {
		return ""
	}
	return strings.TrimSpace(string(canon.Reasoning.Mode))
}

func setClaudeMetadataObservabilityHeaders(w http.ResponseWriter, providerCfg config.Config, canon modelpkg.CanonicalRequest) {
	if !shouldExposeClaudeMetadataObservabilityHeaders(providerCfg, canon) {
		w.Header().Set(headerProxyToUpstreamClaudeMetadataDeviceID, "")
		w.Header().Set(headerProxyToUpstreamClaudeMetadataAccountUUID, "")
		w.Header().Set(headerProxyToUpstreamClaudeMetadataSessionID, "")
		return
	}
	w.Header().Set(headerProxyToUpstreamClaudeMetadataDeviceID, strings.TrimSpace(canon.ClaudeMetadata.DeviceID))
	w.Header().Set(headerProxyToUpstreamClaudeMetadataAccountUUID, strings.TrimSpace(canon.ClaudeMetadata.AccountUUID))
	w.Header().Set(headerProxyToUpstreamClaudeMetadataSessionID, strings.TrimSpace(canon.ClaudeMetadata.SessionID))
}

func shouldExposeClaudeMetadataObservabilityHeaders(providerCfg config.Config, canon modelpkg.CanonicalRequest) bool {
	return strings.TrimSpace(providerCfg.UpstreamEndpointType) == config.UpstreamEndpointTypeAnthropic &&
		providerCfg.MasqueradeTarget == config.MasqueradeTargetClaude &&
		providerCfg.InjectClaudeCodeMetadataUserID &&
		canon.ClaudeMetadata != nil
}

func clearClaudeMetadataObservabilityHeaders(w http.ResponseWriter) {
	w.Header().Set(headerProxyToUpstreamClaudeMetadataDeviceID, "")
	w.Header().Set(headerProxyToUpstreamClaudeMetadataAccountUUID, "")
	w.Header().Set(headerProxyToUpstreamClaudeMetadataSessionID, "")
}

func formatThisUsageTokens(usage map[string]any) string {
	if len(usage) == 0 {
		return ""
	}
	inputTokens, ok := usageNumberAsInt64(usage["input_tokens"])
	if !ok || inputTokens == 0 {
		if promptTokens, promptOK := usageNumberAsInt64(usage["prompt_tokens"]); promptOK {
			inputTokens = promptTokens
			ok = true
		}
	}
	if !ok || inputTokens <= 0 {
		return ""
	}
	outputTokens, ok := usageNumberAsInt64(usage["output_tokens"])
	if !ok || outputTokens == 0 {
		if completionTokens, completionOK := usageNumberAsInt64(usage["completion_tokens"]); completionOK {
			outputTokens = completionTokens
			ok = true
		}
	}
	if !ok || outputTokens < 0 {
		return ""
	}
	cachedTokens := int64(0)
	if n, ok := cachedTokensFromUsage(usage); ok && n > 0 {
		cachedTokens = n
	}
	if cacheWriteTokens, reported := cacheWriteTokensFromUsage(usage); reported {
		return "↑ " + formatUsageTokensNumber(inputTokens) + "(" + formatUsageTokensNumber(cachedTokens) + " cached, " + formatUsageTokensNumber(cacheWriteTokens) + " cache-write) | ↓ " + formatUsageTokensNumber(outputTokens)
	}
	return formatThisUsageTokensValue(inputTokens, cachedTokens, outputTokens)
}

func formatThisUsageCacheWriteTokens(usage map[string]any) string {
	cacheWriteTokens, reported := cacheWriteTokensFromUsage(usage)
	if !reported {
		return ""
	}
	return strconv.FormatInt(cacheWriteTokens, 10)
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
		setCacheWriteCoverageHeaderPair(w, headerProviderTodayCacheWriteCoverage, headerProviderHistoryCacheWriteCoverage, providerStats)
	}
	if info.Legacy {
		if rootStats := rootProviderStatsSnapshot(r, manager); rootStats != nil {
			setCacheRateHeaderPair(w, headerRootProviderTodayCacheRate, headerRootProviderHistoryCacheRate, rootStats)
			setCacheWriteCoverageHeaderPair(w, headerRootProviderTodayCacheWriteCoverage, headerRootProviderHistoryCacheWriteCoverage, rootStats)
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

func setCacheWriteCoverageHeaderPair(w http.ResponseWriter, todayHeader string, historyHeader string, stats *cacheinfo.ProviderStats) {
	if stats == nil {
		return
	}
	w.Header().Set(todayHeader, formatCacheRatePercentage(cacheinfo.DailyCacheWriteCoverage(*stats)))
	w.Header().Set(historyHeader, formatCacheRatePercentage(cacheinfo.ProviderCacheWriteCoverage(*stats)))
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
		headerProviderTodayCacheWriteCoverage,
		headerProviderHistoryCacheWriteCoverage,
		"X-Root-Env-Version",
		headerRootProviderTodayCacheRate,
		headerRootProviderHistoryCacheRate,
		headerRootProviderTodayCacheWriteCoverage,
		headerRootProviderHistoryCacheWriteCoverage,
		headerCacheInfoTimezone,
		headerThisUsageTokens,
		headerThisUsageCacheWriteTokens,
		"X-Provider-Version",
		headerSystemPromptAttach,
		headerClientToProxyModel,
		headerClientToProxyServiceTier,
		headerClientToProxyReasoningParameters,
		headerClientToProxyReasoningEffort,
		headerClientToProxyReasoningMode,
		headerClientToProxyNoPrompt,
		headerProxyToUpstreamModel,
		headerProxyEstimatedInputTokens,
		headerProxyModelLimitContextTokens,
		headerProxyToUpstreamServiceTier,
		headerProxyToUpstreamMaxOutputTokens,
		headerProxyToUpstreamReasoningEffort,
		headerProxyToUpstreamReasoningParameters,
		headerProxyToUpstreamReasoningMode,
		headerProxyUpstreamRetryCount,
		headerProxyUpstreamRetryDelay,
		headerProxyUpstreamAnthropicCacheControl,
		headerProxyToUpstreamMasqueradeUserAgent,
		headerProxyToUpstreamClaudeMetadataDeviceID,
		headerProxyToUpstreamClaudeMetadataAccountUUID,
		headerProxyToUpstreamClaudeMetadataSessionID,
	} {
		w.Header().Del(header)
	}
}
