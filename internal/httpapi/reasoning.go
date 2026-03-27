package httpapi

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
