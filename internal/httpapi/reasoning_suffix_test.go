package httpapi

import (
	"testing"

	"openai-compat-proxy/internal/config"
	modelpkg "openai-compat-proxy/internal/model"
)

func TestApplyResolvedReasoningEffortPreservesExistingRawFields(t *testing.T) {
	reasoning := &modelpkg.CanonicalReasoning{
		Effort:  "low",
		Summary: "detailed",
		Raw: map[string]any{
			"effort":        "low",
			"summary":       "detailed",
			"foo":           "bar",
			"thinking":      map[string]any{"type": "disabled"},
			"output_config": map[string]any{"effort": "low"},
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
	if _, ok := updated.Raw["thinking"]; ok {
		t.Fatalf("expected suffix effort to clear conflicting thinking config, got %#v", updated.Raw)
	}
	if _, ok := updated.Raw["output_config"]; ok {
		t.Fatalf("expected suffix effort to clear conflicting output_config, got %#v", updated.Raw)
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

func TestApplyResolvedReasoningEffortNoneDisablesReasoning(t *testing.T) {
	reasoning := &modelpkg.CanonicalReasoning{Effort: "high", Summary: "auto", Raw: map[string]any{"effort": "high", "summary": "auto", "thinking": map[string]any{"type": "enabled", "budget_tokens": 2048}}}

	updated := applyResolvedReasoningEffort(reasoning, "none")

	if updated == nil {
		t.Fatalf("expected disabled reasoning marker to remain present")
	}
	if updated.Effort != "none" {
		t.Fatalf("expected effort none, got %q", updated.Effort)
	}
	if got := updated.Raw["effort"]; got != "none" {
		t.Fatalf("expected raw effort none, got %#v", updated.Raw)
	}
	if _, ok := updated.Raw["thinking"]; ok {
		t.Fatalf("expected none suffix to clear thinking, got %#v", updated.Raw)
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

func TestMapResolvedReasoningEffortToAnthropicThinkingOverridesDisabledThinking(t *testing.T) {
	maxTokens := 4096
	reasoning := &modelpkg.CanonicalReasoning{Effort: "low", Summary: "auto", Raw: map[string]any{"effort": "low", "summary": "auto", "thinking": map[string]any{"type": "disabled"}, "output_config": map[string]any{"effort": "high"}}}

	updated := applyAnthropicThinkingFromResolvedEffort(reasoning, true, "claude-sonnet-4-5", &maxTokens)

	thinking, _ := updated.Raw["thinking"].(map[string]any)
	if got := thinking["type"]; got != "enabled" {
		t.Fatalf("expected suffix effort to override disabled thinking, got %#v", updated.Raw)
	}
	if got := thinking["budget_tokens"]; got != 1024 {
		t.Fatalf("expected low suffix thinking budget, got %#v", updated.Raw)
	}
	if _, ok := updated.Raw["output_config"]; ok {
		t.Fatalf("expected legacy model budget profile to replace stale output_config, got %#v", updated.Raw)
	}
}

func TestNormalizeCanonicalModelUsesExplicitReasoningEffortForModelMapSourceSuffix(t *testing.T) {
	provider := config.ProviderConfig{
		ModelMap:                    []config.ModelMapEntry{config.NewModelMapEntry("client-gpt-high", "upstream-gpt")},
		EnableReasoningEffortSuffix: false,
	}
	canon := &modelpkg.CanonicalRequest{
		Model:     "client-gpt",
		Reasoning: &modelpkg.CanonicalReasoning{Effort: "high", Summary: "auto", Raw: map[string]any{"effort": "high", "summary": "auto"}},
	}

	normalizeCanonicalModelAndReasoningForProvider(canon, provider, config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeResponses})

	if canon.Model != "upstream-gpt" {
		t.Fatalf("expected explicit reasoning effort to match suffixed MODEL_MAP source, got %q", canon.Model)
	}
	if canon.Reasoning == nil || canon.Reasoning.Effort != "high" {
		t.Fatalf("expected explicit effort high to remain, got %#v", canon.Reasoning)
	}
}

func TestNormalizeCanonicalModelHiddenSuffixDoesNotBlockExplicitReasoningModelMap(t *testing.T) {
	provider := config.ProviderConfig{
		ModelMap:                    []config.ModelMapEntry{config.NewModelMapEntry("client-gpt-high", "upstream-gpt")},
		HiddenModels:                []string{"client-gpt-high"},
		EnableReasoningEffortSuffix: false,
	}
	canon := &modelpkg.CanonicalRequest{
		Model:     "client-gpt",
		Reasoning: &modelpkg.CanonicalReasoning{Effort: "high", Summary: "auto", Raw: map[string]any{"effort": "high", "summary": "auto"}},
	}

	normalizeCanonicalModelAndReasoningForProvider(canon, provider, config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeResponses})

	if canon.Model != "upstream-gpt" {
		t.Fatalf("expected MODEL_MAP to use explicit effort even when hidden suffix controls model list, got %q", canon.Model)
	}
}

func TestNormalizeCanonicalModelTargetSuffixWorksWhenClientSuffixDisabled(t *testing.T) {
	provider := config.ProviderConfig{
		ModelMap:                    []config.ModelMapEntry{config.NewModelMapEntry("client-gpt", "upstream-gpt-low")},
		EnableReasoningEffortSuffix: false,
	}
	canon := &modelpkg.CanonicalRequest{Model: "client-gpt"}

	normalizeCanonicalModelAndReasoningForProvider(canon, provider, config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeResponses})

	if canon.Model != "upstream-gpt" {
		t.Fatalf("expected target suffix to be stripped from upstream model, got %q", canon.Model)
	}
	if canon.Reasoning == nil || canon.Reasoning.Effort != "low" {
		t.Fatalf("expected target suffix to set effort low, got %#v", canon.Reasoning)
	}
}

func TestNormalizeCanonicalModelMapSourceSuffixWorksWhenClientSuffixDisabled(t *testing.T) {
	provider := config.ProviderConfig{
		ModelMap:                    []config.ModelMapEntry{config.NewModelMapEntry("client-gpt-high", "upstream-gpt")},
		EnableReasoningEffortSuffix: false,
	}
	canon := &modelpkg.CanonicalRequest{Model: "client-gpt-high"}

	normalizeCanonicalModelAndReasoningForProvider(canon, provider, config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeResponses})

	if canon.Model != "upstream-gpt" {
		t.Fatalf("expected explicit MODEL_MAP suffix source to match, got %q", canon.Model)
	}
	if canon.Reasoning == nil || canon.Reasoning.Effort != "high" {
		t.Fatalf("expected explicit MODEL_MAP suffix source to set effort high, got %#v", canon.Reasoning)
	}
}

func TestMapResolvedReasoningEffortNoneToAnthropicDisabledThinking(t *testing.T) {
	reasoning := &modelpkg.CanonicalReasoning{Effort: "none", Summary: "auto", Raw: map[string]any{"effort": "none", "summary": "auto"}}

	updated := applyAnthropicThinkingFromResolvedEffort(reasoning, true, "claude-sonnet-4-5", nil)

	thinking, _ := updated.Raw["thinking"].(map[string]any)
	if got := thinking["type"]; got != "disabled" {
		t.Fatalf("expected none suffix to map to disabled thinking, got %#v", updated.Raw)
	}
}
