package httpapi

import (
	"fmt"
	"strings"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
)

type unsupportedAdaptiveThinkingError string

func (err unsupportedAdaptiveThinkingError) Error() string {
	return string(err)
}

func applyAdaptiveThinkingModelSuffix(req *model.CanonicalRequest, intent model.ProxyModelIntent, providerCfg config.Config) error {
	if req == nil || !intent.HasAdaptive {
		return nil
	}
	if providerCfg.UpstreamEndpointType != config.UpstreamEndpointTypeAnthropic {
		return unsupportedAdaptiveThinkingError(fmt.Sprintf("adaptive thinking requires UPSTREAM_ENDPOINT_TYPE=anthropic; current upstream endpoint type is %s", providerCfg.UpstreamEndpointType))
	}
	if !supportsAnthropicAdaptiveThinking(req.Model) {
		return unsupportedAdaptiveThinkingError(fmt.Sprintf("adaptive thinking is not supported by final Anthropic model %q", req.Model))
	}
	if !canEnableAnthropicThinkingForMessages(req.Messages) {
		return unsupportedAdaptiveThinkingError("adaptive thinking cannot be applied to unsigned reasoning replay")
	}
	if req.Reasoning == nil {
		req.Reasoning = &model.CanonicalReasoning{}
	}
	if strings.TrimSpace(req.Reasoning.Effort) == "none" {
		return unsupportedAdaptiveThinkingError("adaptive thinking cannot be combined with reasoning effort none")
	}
	if req.Reasoning.Raw == nil {
		req.Reasoning.Raw = map[string]any{}
	}
	req.Reasoning.Raw["thinking"] = map[string]any{"type": "adaptive"}
	if effort := strings.TrimSpace(req.Reasoning.Effort); effort != "" {
		req.Reasoning.Raw["output_config"] = map[string]any{"effort": anthropicAdaptiveEffortForSuffix(effort)}
	} else {
		delete(req.Reasoning.Raw, "output_config")
	}
	return nil
}
