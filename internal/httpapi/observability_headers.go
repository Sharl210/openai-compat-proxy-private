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
	headerClientToProxyReasoningEffort       = "X-Client-To-Proxy-Reasoning-Effort"
	headerProxyToUpstreamModel               = "X-Proxy-To-Upstream-Model"
	headerProxyToUpstreamReasoningParameters = "X-Proxy-To-Upstream-Reasoning-Parameters"
)

func clientToProxyReasoningEffort(model string, reasoning *modelpkg.CanonicalReasoning) string {
	if _, effort, ok := reasoningpkg.SplitSuffix(strings.TrimSpace(model)); ok {
		return effort
	}
	if reasoning == nil {
		return ""
	}
	return strings.TrimSpace(reasoning.Effort)
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

func setDirectionalObservabilityHeaders(w http.ResponseWriter, providerCfg config.Config, canon modelpkg.CanonicalRequest, clientModel string, clientReasoningEffort string) error {
	preview, err := upstream.PreviewRequestObservability(canon, providerCfg.UpstreamEndpointType, providerCfg.MasqueradeTarget, providerCfg.InjectClaudeCodeMetadataUserID, providerCfg.InjectClaudeCodeSystemPrompt)
	if err != nil {
		return err
	}
	if value := strings.TrimSpace(clientModel); value != "" {
		w.Header().Set(headerClientToProxyModel, value)
	}
	if value := strings.TrimSpace(clientReasoningEffort); value != "" {
		w.Header().Set(headerClientToProxyReasoningEffort, value)
	}
	if value := strings.TrimSpace(preview.UpstreamModel); value != "" {
		w.Header().Set(headerProxyToUpstreamModel, value)
	}
	if value := strings.TrimSpace(preview.ReasoningParameters); value != "" {
		w.Header().Set(headerProxyToUpstreamReasoningParameters, value)
	}
	return nil
}
