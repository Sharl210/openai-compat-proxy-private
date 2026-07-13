package httpapi

import (
	"encoding/json"
	"fmt"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
)

func applyUltraMultiAgent(req *model.CanonicalRequest, intent model.ProxyModelIntent, provider config.ProviderConfig, providerCfg config.Config) error {
	if req == nil || !intent.HasUltra {
		return nil
	}
	if providerCfg.UpstreamEndpointType != config.UpstreamEndpointTypeResponses {
		return unsupportedUltraMultiAgentError(fmt.Sprintf("ultra multi-agent requires UPSTREAM_ENDPOINT_TYPE=responses; current upstream endpoint type is %s", providerCfg.UpstreamEndpointType))
	}
	if !provider.SupportsResponsesMultiAgent {
		return unsupportedUltraMultiAgentError("provider configuration does not enable ultra multi-agent; set SUPPORTS_RESPONSES_MULTI_AGENT=true")
	}
	maxConcurrent := provider.UltraMaxConcurrentSubagents
	if maxConcurrent < 1 {
		maxConcurrent = config.Default().UltraMaxConcurrentSubagents
	}
	if len(req.ResponseMultiAgent) > 0 {
		if !responseMultiAgentMatches(req.ResponseMultiAgent, maxConcurrent) {
			return unsupportedUltraMultiAgentError("client multi_agent conflicts with -ultra")
		}
		return nil
	}
	req.ResponseMultiAgent = json.RawMessage(fmt.Sprintf(`{"enabled":true,"max_concurrent_subagents":%d}`, maxConcurrent))
	return nil
}

func responseMultiAgentMatches(raw json.RawMessage, maxConcurrent int) bool {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || len(values) != 2 {
		return false
	}
	var enabled bool
	var configuredMax int
	if err := json.Unmarshal(values["enabled"], &enabled); err != nil {
		return false
	}
	if err := json.Unmarshal(values["max_concurrent_subagents"], &configuredMax); err != nil {
		return false
	}
	return enabled && configuredMax == maxConcurrent
}

type unsupportedUltraMultiAgentError string

func (err unsupportedUltraMultiAgentError) Error() string {
	return string(err)
}
