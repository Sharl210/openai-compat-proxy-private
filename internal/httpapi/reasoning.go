package httpapi

import (
	"net/http"
	"strings"
)

import "openai-compat-proxy/internal/config"
import modelpkg "openai-compat-proxy/internal/model"

func applyProxyModelIntentReasoningMode(req *http.Request, canon *modelpkg.CanonicalRequest) {
	if canon == nil {
		return
	}
	intent, ok := proxyModelIntentFromRequest(req)
	if !ok {
		return
	}
	mode := modelpkg.ReasoningMode(strings.TrimSpace(intent.ReasoningMode))
	if mode == "" {
		return
	}
	if canon.Reasoning == nil {
		canon.Reasoning = &modelpkg.CanonicalReasoning{}
	}
	if canon.Reasoning.Raw == nil {
		canon.Reasoning.Raw = map[string]any{}
	}
	canon.Reasoning.Mode = mode
	canon.Reasoning.Raw["mode"] = string(mode)
	canon.ReasoningModeOrigin = modelpkg.ReasoningModeOriginSuffix
}

func enforceSuffixReasoningModePrecedence(canon *modelpkg.CanonicalRequest) {
	if canon == nil || canon.ReasoningModeOrigin != modelpkg.ReasoningModeOriginSuffix {
		return
	}
	if canon.Reasoning == nil {
		canon.Reasoning = &modelpkg.CanonicalReasoning{}
	}
	if canon.Reasoning.Raw == nil {
		canon.Reasoning.Raw = map[string]any{}
	}
	canon.Reasoning.Mode = modelpkg.ReasoningModePro
	canon.Reasoning.Raw["mode"] = string(modelpkg.ReasoningModePro)
}

func applyDefaultProReasoningMode(canon *modelpkg.CanonicalRequest, providerCfg config.Config) {
	if canon == nil || canon.ReasoningModeOrigin == modelpkg.ReasoningModeOriginSuffix {
		return
	}
	if !providerCfg.DefaultProReasoningModeEnabledForFinalUpstreamModel(canon.Model) {
		return
	}
	if canon.Reasoning == nil {
		canon.Reasoning = &modelpkg.CanonicalReasoning{}
	}
	if canon.Reasoning.Raw == nil {
		canon.Reasoning.Raw = map[string]any{}
	}
	canon.Reasoning.Mode = modelpkg.ReasoningModePro
	canon.Reasoning.Raw["mode"] = string(modelpkg.ReasoningModePro)
	canon.ReasoningModeOrigin = modelpkg.ReasoningModeOriginProxyDefault
}

func applyResolvedReasoningEffort(reasoning *modelpkg.CanonicalReasoning, effort string) *modelpkg.CanonicalReasoning {
	if effort == "" {
		return reasoning
	}
	if reasoning == nil {
		reasoning = &modelpkg.CanonicalReasoning{}
	}
	reasoning.Effort = effort
	if reasoning.Summary == "" {
		reasoning.Summary = "auto"
	}
	if reasoning.Raw == nil {
		reasoning.Raw = map[string]any{}
	}
	if effort == "none" {
		delete(reasoning.Raw, "thinking")
		delete(reasoning.Raw, "output_config")
		reasoning.Raw["effort"] = effort
		if _, ok := reasoning.Raw["summary"]; !ok {
			reasoning.Raw["summary"] = reasoning.Summary
		}
		return reasoning
	}
	delete(reasoning.Raw, "thinking")
	delete(reasoning.Raw, "output_config")
	reasoning.Raw["effort"] = effort
	if _, ok := reasoning.Raw["summary"]; !ok {
		reasoning.Raw["summary"] = reasoning.Summary
	}
	return reasoning
}

func applyAnthropicThinkingFromResolvedEffort(reasoning *modelpkg.CanonicalReasoning, enabled bool, model string, maxOutputTokens *int, maxThinkingBudget int) *modelpkg.CanonicalReasoning {
	if !enabled || reasoning == nil || reasoning.Effort == "" {
		return reasoning
	}
	if reasoning.Raw == nil {
		reasoning.Raw = map[string]any{}
	}
	if reasoning.Effort == "none" {
		delete(reasoning.Raw, "output_config")
		reasoning.Raw["thinking"] = map[string]any{"type": "disabled"}
		return reasoning
	}
	if thinking, ok := reasoning.Raw["thinking"].(map[string]any); ok && strings.TrimSpace(stringValue(thinking["type"])) != "disabled" {
		return reasoning
	}
	delete(reasoning.Raw, "thinking")
	delete(reasoning.Raw, "output_config")
	if supportsAnthropicAdaptiveThinking(model) {
		reasoning.Raw["thinking"] = map[string]any{"type": "adaptive"}
		reasoning.Raw["output_config"] = map[string]any{"effort": anthropicAdaptiveEffortForSuffix(reasoning.Effort)}
		return reasoning
	}
	budget := anthropicThinkingBudgetForEffort(reasoning.Effort, maxOutputTokens, maxThinkingBudget)
	if budget > 0 {
		reasoning.Raw["thinking"] = map[string]any{
			"type":          "enabled",
			"budget_tokens": budget,
		}
	}
	return reasoning
}

func anthropicAdaptiveEffortForSuffix(effort string) string {
	switch effort {
	case "max":
		return "max"
	case "xhigh":
		return "xhigh"
	case "high":
		return "high"
	case "medium":
		return "medium"
	case "low":
		return "low"
	case "minimal":
		return "low"
	default:
		return "medium"
	}
}

func anthropicThinkingBudgetForEffort(effort string, maxOutputTokens *int, maxThinkingBudget int) int {
	ratioNum, ratioDen := anthropicThinkingBudgetRatio(effort)
	if ratioDen == 0 || maxThinkingBudget < 1024 {
		return 0
	}
	budget := (maxThinkingBudget * ratioNum) / ratioDen
	if budget < 1024 {
		budget = 1024
	}
	if budget > maxThinkingBudget {
		budget = maxThinkingBudget
	}
	if maxOutputTokens != nil && *maxOutputTokens > 0 {
		limit := *maxOutputTokens - 1
		if limit < 1024 {
			return 0
		}
		if budget > limit {
			budget = limit
		}
	}
	return budget
}

func anthropicThinkingBudgetRatio(effort string) (ratioNumerator int, ratioDenominator int) {
	switch effort {
	case "minimal":
		return 1, 16
	case "low":
		return 1, 8
	case "medium":
		return 1, 4
	case "high":
		return 1, 2
	case "xhigh":
		return 1, 1
	case "max":
		return 1, 1
	default:
		return 0, 0
	}
}

func supportsAnthropicAdaptiveThinking(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(normalized, "opus-4-6") || strings.Contains(normalized, "opus-4.6") || strings.Contains(normalized, "opus-4-7") || strings.Contains(normalized, "opus-4.7") || strings.Contains(normalized, "opus-4-8") || strings.Contains(normalized, "opus-4.8")
}
