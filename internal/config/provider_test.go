package config

import "testing"

func TestResolveModelAndEffortPrefersRequestSuffixOverMappedSuffix(t *testing.T) {
	p := ProviderConfig{ModelMap: map[string]string{"gpt-5": "claude-sonnet-4-5-low"}}

	model, effort := p.ResolveModelAndEffort("gpt-5-high", true)
	if model != "claude-sonnet-4-5" {
		t.Fatalf("expected mapped model without suffix, got %q", model)
	}
	if effort != "high" {
		t.Fatalf("expected request suffix high to win, got %q", effort)
	}
}

func TestResolveModelAndEffortDoesNotParseSuffixWhenDisabled(t *testing.T) {
	p := ProviderConfig{ModelMap: map[string]string{"*": "claude-sonnet-4-5-low"}}

	model, effort := p.ResolveModelAndEffort("gpt-5-high", false)
	if model != "claude-sonnet-4-5-low" {
		t.Fatalf("expected mapped model to remain untouched when disabled, got %q", model)
	}
	if effort != "" {
		t.Fatalf("expected no effort override when disabled, got %q", effort)
	}
}

func TestResolveModelAndEffortUsesMappedSuffixWhenNoRequestSuffix(t *testing.T) {
	p := ProviderConfig{ModelMap: map[string]string{"gpt-5": "claude-sonnet-4-5-low"}}

	model, effort := p.ResolveModelAndEffort("gpt-5", true)
	if model != "claude-sonnet-4-5" {
		t.Fatalf("expected mapped model without suffix, got %q", model)
	}
	if effort != "low" {
		t.Fatalf("expected mapped suffix low, got %q", effort)
	}
}
