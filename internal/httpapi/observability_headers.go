package httpapi

import (
	"net/http"
	"strings"

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
	headerCacheInfoTimezone                  = "X-Cache-Info-Timezone"
	headerProxyToUpstreamModel               = "X-Proxy-To-Upstream-Model"
	headerProxyToUpstreamServiceTier         = "X-Proxy-To-Upstream-Service-Tier"
	headerProxyToUpstreamReasoningParameters = "X-Proxy-To-Upstream-Reasoning-Parameters"
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
				effective = applyAnthropicThinkingFromResolvedEffort(effective, true, model, maxOutputTokens)
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

func normalizeCanonicalModelAndReasoningForProvider(canon *modelpkg.CanonicalRequest, provider config.ProviderConfig, providerCfg config.Config) {
	if canon == nil {
		return
	}
	mappedModel, effort := provider.ResolveModelAndEffort(canon.Model, provider.EnableReasoningEffortSuffix)
	canon.Model = mappedModel
	canon.Reasoning = applyResolvedReasoningEffort(canon.Reasoning, effort)
	if providerCfg.UpstreamEndpointType == config.UpstreamEndpointTypeAnthropic {
		canon.Reasoning = applyAnthropicThinkingFromResolvedEffort(canon.Reasoning, provider.MapReasoningSuffixToAnthropicThinking, canon.Model, canon.MaxOutputTokens)
	}
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

func setDirectionalObservabilityHeaders(w http.ResponseWriter, providerCfg config.Config, canon modelpkg.CanonicalRequest, clientModel string, clientServiceTier string, clientReasoningParameters string, clientReasoningEffort string) error {
	preview, err := upstream.PreviewRequestObservability(canon, providerCfg.UpstreamEndpointType, providerCfg.MasqueradeTarget, providerCfg.InjectClaudeCodeMetadataUserID, providerCfg.InjectClaudeCodeSystemPrompt)
	if err != nil {
		return err
	}
	if value := strings.TrimSpace(clientModel); value != "" {
		w.Header().Set(headerClientToProxyModel, value)
	}
	w.Header().Set(headerClientToProxyServiceTier, strings.TrimSpace(clientServiceTier))
	if value := strings.TrimSpace(clientReasoningParameters); value != "" {
		w.Header().Set(headerClientToProxyReasoningParameters, value)
	}
	if value := strings.TrimSpace(clientReasoningEffort); value != "" {
		w.Header().Set(headerClientToProxyReasoningEffort, value)
	}
	if value := strings.TrimSpace(preview.UpstreamModel); value != "" {
		w.Header().Set(headerProxyToUpstreamModel, value)
	}
	w.Header().Set(headerProxyToUpstreamServiceTier, strings.TrimSpace(preview.UpstreamServiceTier))
	if value := strings.TrimSpace(preview.ReasoningParameters); value != "" {
		w.Header().Set(headerProxyToUpstreamReasoningParameters, value)
	}
	return nil
}

func clearTransparencyHeaders(w http.ResponseWriter) {
	for _, header := range []string{
		"X-Env-Version",
		headerCacheInfoTimezone,
		"X-Provider-Name",
		"X-Provider-Version",
		"X-SYSTEM-PROMPT-ATTACH",
		headerClientToProxyModel,
		headerClientToProxyServiceTier,
		headerClientToProxyReasoningParameters,
		headerClientToProxyReasoningEffort,
		headerProxyToUpstreamModel,
		headerProxyToUpstreamServiceTier,
		headerProxyToUpstreamReasoningParameters,
	} {
		w.Header().Del(header)
	}
}
