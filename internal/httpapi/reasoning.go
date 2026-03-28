package httpapi

import "strings"

import modelpkg "openai-compat-proxy/internal/model"

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
	reasoning.Raw["effort"] = effort
	if _, ok := reasoning.Raw["summary"]; !ok {
		reasoning.Raw["summary"] = reasoning.Summary
	}
	return reasoning
}

func applyAnthropicThinkingFromResolvedEffort(reasoning *modelpkg.CanonicalReasoning, enabled bool, model string, maxOutputTokens *int) *modelpkg.CanonicalReasoning {
	if !enabled || reasoning == nil || reasoning.Effort == "" {
		return reasoning
	}
	if reasoning.Raw == nil {
		reasoning.Raw = map[string]any{}
	}
	if _, ok := reasoning.Raw["thinking"]; ok {
		return reasoning
	}
	if supportsAnthropicAdaptiveThinking(model) {
		reasoning.Raw["thinking"] = map[string]any{"type": "adaptive"}
		reasoning.Raw["output_config"] = map[string]any{"effort": anthropicAdaptiveEffortForSuffix(reasoning.Effort)}
		return reasoning
	}
	budget := anthropicThinkingBudgetForEffort(reasoning.Effort, maxOutputTokens)
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
	case "xhigh":
		return "max"
	case "high":
		return "high"
	case "medium":
		return "medium"
	case "low":
		return "low"
	default:
		return "medium"
	}
}

func anthropicThinkingBudgetForEffort(effort string, maxOutputTokens *int) int {
	ratioNum, ratioDen, floor, ceiling := anthropicThinkingBudgetProfile(effort)
	if ratioDen == 0 {
		return 0
	}
	budget := floor
	if maxOutputTokens != nil && *maxOutputTokens > 0 {
		budget = (*maxOutputTokens * ratioNum) / ratioDen
		if budget <= 0 {
			budget = 1
		}
		if budget < floor {
			budget = floor
		}
		if budget > ceiling {
			budget = ceiling
		}
		if budget > *maxOutputTokens {
			budget = *maxOutputTokens
		}
	} else if budget > ceiling {
		budget = ceiling
	}
	return budget
}

func anthropicThinkingBudgetProfile(effort string) (ratioNumerator int, ratioDenominator int, floor int, ceiling int) {
	switch effort {
	case "low":
		return 1, 4, 1024, 4096
	case "medium":
		return 2, 5, 2048, 8192
	case "high":
		return 3, 5, 4096, 16384
	case "xhigh":
		return 4, 5, 8192, 32768
	default:
		return 0, 0, 0, 0
	}
}

func supportsAnthropicAdaptiveThinking(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(normalized, "opus-4-6") || strings.Contains(normalized, "opus-4.6")
}
