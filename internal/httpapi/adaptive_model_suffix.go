package httpapi

import (
	"fmt"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
)

// applyAdaptiveModelSuffix 处理 -adaptive 后缀：请求 Anthropic 原生 adaptive thinking。
// 与 -auto（抛弃所有推理参数、全上游通用）不同，-adaptive 只对最终 anthropic 上游生效：
// 在 canonical reasoning 中设置 thinking:{"type":"adaptive"} 与 output_config.effort，
// 让上游按原生 adaptive thinking 处理。非 anthropic 上游在请求上游前返回本地 400。
func applyAdaptiveModelSuffix(req *model.CanonicalRequest, intent model.ProxyModelIntent, providerCfg config.Config) error {
	if req == nil || !intent.HasAdaptive {
		return nil
	}
	if providerCfg.UpstreamEndpointType != config.UpstreamEndpointTypeAnthropic {
		return unsupportedAdaptiveModelSuffixError(fmt.Sprintf("adaptive suffix requires UPSTREAM_ENDPOINT_TYPE=anthropic; current upstream endpoint type is %s", providerCfg.UpstreamEndpointType))
	}
	if req.Reasoning == nil {
		req.Reasoning = &model.CanonicalReasoning{}
	}
	if req.Reasoning.Raw == nil {
		req.Reasoning.Raw = map[string]any{}
	}
	req.Reasoning.Raw["thinking"] = map[string]any{"type": "adaptive"}
	if intent.ReasoningEffort != "" && intent.ReasoningEffort != "none" {
		req.Reasoning.Raw["output_config"] = map[string]any{"effort": anthropicAdaptiveEffortForSuffix(intent.ReasoningEffort)}
	}
	// adaptive 与 auto 语义互斥：auto 抛弃所有推理参数，adaptive 显式请求原生 adaptive thinking。
	req.ReasoningModeOrigin = model.ReasoningModeOriginNone
	return nil
}

type unsupportedAdaptiveModelSuffixError string

func (err unsupportedAdaptiveModelSuffixError) Error() string {
	return string(err)
}
