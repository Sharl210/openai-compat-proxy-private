package httpapi

import (
	"testing"

	modelpkg "openai-compat-proxy/internal/model"
)

func TestApplyResolvedReasoningEffortPreservesExistingRawFields(t *testing.T) {
	reasoning := &modelpkg.CanonicalReasoning{
		Effort:  "low",
		Summary: "detailed",
		Raw: map[string]any{
			"effort":  "low",
			"summary": "detailed",
			"foo":     "bar",
		},
	}

	updated := applyResolvedReasoningEffort(reasoning, "high")

	if updated == nil {
		t.Fatalf("expected reasoning to be preserved")
	}
	if updated.Effort != "high" {
		t.Fatalf("expected effort high, got %q", updated.Effort)
	}
	if updated.Summary != "detailed" {
		t.Fatalf("expected summary detailed, got %q", updated.Summary)
	}
	if got := updated.Raw["summary"]; got != "detailed" {
		t.Fatalf("expected raw summary to stay detailed, got %#v", updated.Raw)
	}
	if got := updated.Raw["foo"]; got != "bar" {
		t.Fatalf("expected raw custom field to be preserved, got %#v", updated.Raw)
	}
	if got := updated.Raw["effort"]; got != "high" {
		t.Fatalf("expected raw effort to be updated, got %#v", updated.Raw)
	}
}

func TestApplyResolvedReasoningEffortInitializesDefaultSummaryOnlyWhenMissing(t *testing.T) {
	updated := applyResolvedReasoningEffort(nil, "high")
	if updated == nil {
		t.Fatalf("expected reasoning to be created")
	}
	if updated.Effort != "high" {
		t.Fatalf("expected effort high, got %q", updated.Effort)
	}
	if updated.Summary != "auto" {
		t.Fatalf("expected default summary auto, got %q", updated.Summary)
	}
	if got := updated.Raw["summary"]; got != "auto" {
		t.Fatalf("expected raw summary auto, got %#v", updated.Raw)
	}
	if got := updated.Raw["effort"]; got != "high" {
		t.Fatalf("expected raw effort high, got %#v", updated.Raw)
	}
}

func TestMapResolvedReasoningEffortToAnthropicThinkingDisabledByDefault(t *testing.T) {
	reasoning := &modelpkg.CanonicalReasoning{Effort: "high", Summary: "auto", Raw: map[string]any{"effort": "high", "summary": "auto"}}

	updated := applyAnthropicThinkingFromResolvedEffort(reasoning, false, "claude-opus-4-6", nil)

	if updated == nil {
		t.Fatalf("expected reasoning to remain present")
	}
	if _, ok := updated.Raw["thinking"]; ok {
		t.Fatalf("expected thinking to remain unset when mapping is disabled, got %#v", updated.Raw)
	}
}

func TestMapResolvedReasoningEffortToAnthropicThinkingUsesAdaptiveOnSupportedModels(t *testing.T) {
	reasoning := &modelpkg.CanonicalReasoning{Effort: "high", Summary: "auto", Raw: map[string]any{"effort": "high", "summary": "auto"}}

	updated := applyAnthropicThinkingFromResolvedEffort(reasoning, true, "claude-opus-4-6", nil)

	if updated == nil {
		t.Fatalf("expected reasoning to remain present")
	}
	thinking, _ := updated.Raw["thinking"].(map[string]any)
	if thinking == nil {
		t.Fatalf("expected thinking config to be injected, got %#v", updated.Raw)
	}
	if got := thinking["type"]; got != "adaptive" {
		t.Fatalf("expected thinking type enabled, got %#v", updated.Raw)
	}
	if _, ok := thinking["budget_tokens"]; ok {
		t.Fatalf("expected adaptive thinking to avoid budget_tokens, got %#v", updated.Raw)
	}
	outputConfig, _ := updated.Raw["output_config"].(map[string]any)
	if got := outputConfig["effort"]; got != "high" {
		t.Fatalf("expected output_config effort high, got %#v", updated.Raw)
	}
}

func TestMapResolvedReasoningEffortToAnthropicThinkingUsesBudgetProfileOnLegacyModels(t *testing.T) {
	maxTokens := 20000
	reasoning := &modelpkg.CanonicalReasoning{Effort: "high", Summary: "auto", Raw: map[string]any{"effort": "high", "summary": "auto"}}

	updated := applyAnthropicThinkingFromResolvedEffort(reasoning, true, "claude-sonnet-4-5", &maxTokens)

	thinking, _ := updated.Raw["thinking"].(map[string]any)
	if got := thinking["type"]; got != "enabled" {
		t.Fatalf("expected legacy thinking type enabled, got %#v", updated.Raw)
	}
	if got := thinking["budget_tokens"]; got != 12000 {
		t.Fatalf("expected high effort budget 12000, got %#v", updated.Raw)
	}
}

func TestMapResolvedReasoningEffortToAnthropicThinkingPreservesExistingThinking(t *testing.T) {
	reasoning := &modelpkg.CanonicalReasoning{Effort: "high", Summary: "auto", Raw: map[string]any{"effort": "high", "thinking": map[string]any{"type": "enabled", "budget_tokens": 1234}}}

	updated := applyAnthropicThinkingFromResolvedEffort(reasoning, true, "claude-sonnet-4-5", nil)

	thinking, _ := updated.Raw["thinking"].(map[string]any)
	if got := thinking["budget_tokens"]; got != 1234 {
		t.Fatalf("expected existing thinking budget to be preserved, got %#v", updated.Raw)
	}
}
