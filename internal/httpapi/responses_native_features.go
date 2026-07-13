package httpapi

import (
	"encoding/json"
	"strings"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/upstream"
)

func applyResponsesPromptCacheHintDrop(req *model.CanonicalRequest, provider config.ProviderConfig, providerCfg config.Config) {
	if req == nil || !provider.AllowResponsesPromptCacheHintDrop || providerCfg.UpstreamEndpointType == config.UpstreamEndpointTypeResponses {
		return
	}
	req.ResponsePromptCacheKey = nil
	req.ResponsePromptCacheOptions = nil
}

func unsupportedResponsesNativeFeature(req model.CanonicalRequest, provider config.ProviderConfig, providerCfg config.Config) string {
	if req.ParallelToolCalls != nil && !*req.ParallelToolCalls && !provider.SupportsParallelToolCallsControl {
		return "provider does not support parallel tool calls control"
	}
	if err := upstream.CheckResponsesFeatureCompatibility(req, providerCfg.UpstreamEndpointType); err != nil {
		return err.Error()
	}
	if providerCfg.UpstreamEndpointType != config.UpstreamEndpointTypeResponses {
		return ""
	}
	if hasProgrammaticToolCalling(req.Tools) && !provider.SupportsProgrammaticToolCalling {
		return "provider does not support programmatic tool calling"
	}
	if responseMultiAgentEnabled(req.ResponseMultiAgent) && !provider.SupportsResponsesMultiAgent {
		return "provider does not support responses multi-agent"
	}
	return ""
}

func hasProgrammaticToolCalling(tools []model.CanonicalTool) bool {
	for _, tool := range tools {
		if strings.TrimSpace(tool.Type) == "programmatic_tool_calling" {
			return true
		}
	}
	return false
}

func responseMultiAgentEnabled(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var multiAgent struct {
		Enabled bool `json:"enabled"`
	}
	return json.Unmarshal(raw, &multiAgent) == nil && multiAgent.Enabled
}
