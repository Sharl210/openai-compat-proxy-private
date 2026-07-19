package config

import (
	"testing"

	"openai-compat-proxy/internal/model"
)

func TestConfigResolveV1ProxyModelIntent_mapsLongestLiteralAliasTail(t *testing.T) {
	// Given
	cfg := Config{
		EnableNoPromptModelSuffix: true,
		V1ModelMap: []ModelMapEntry{
			NewModelMapEntry("vendor", "upstream-base"),
			NewModelMapEntry("vendor-high", "upstream-priority"),
		},
	}

	// When
	intent, ok := cfg.ResolveV1ProxyModelIntent("vendor-high-noprompt-pro")

	// Then
	if !ok {
		t.Fatal("expected literal alias with a fully consumed proxy tail to resolve")
	}
	if intent.BaseModel != "upstream-priority" {
		t.Fatalf("expected longest literal alias target upstream-priority, got %q", intent.BaseModel)
	}
	if intent.ReasoningMode != "pro" || !intent.HasNoPrompt {
		t.Fatalf("expected pro+noprompt axes to remain attached, got %#v", intent)
	}
}

func TestConfigResolveV1ProxyModelIntent_mergesConfiguredTargetAxes(t *testing.T) {
	// Given
	cfg := Config{
		EnableNoPromptModelSuffix: true,
		V1ModelMap: []ModelMapEntry{
			NewModelMapEntry("client", "upstream-low-pro"),
		},
	}

	// When
	intent, ok := cfg.ResolveV1ProxyModelIntent("client-high-noprompt-ultra")

	// Then
	if !ok {
		t.Fatal("expected mapped proxy model intent")
	}
	if intent.BaseModel != "upstream" || intent.ReasoningEffort != "low" || intent.ReasoningMode != "pro" {
		t.Fatalf("expected target base and axes to override source, got %#v", intent)
	}
	if !intent.HasNoPrompt || !intent.HasUltra {
		t.Fatalf("expected client-private axes to remain attached, got %#v", intent)
	}
}

func TestConfigResolveV1ProxyModelIntent_keepsLiteralProTargetWhenCandidateExists(t *testing.T) {
	// Given
	cfg := Config{V1ModelMap: []ModelMapEntry{NewModelMapEntry("client", "vendor-pro")}}

	// When
	intent, ok := cfg.ResolveV1ProxyModelIntentWithTargetCandidates("client", []string{"vendor-pro"})

	// Then
	if !ok {
		t.Fatal("expected mapped proxy model intent")
	}
	if intent.BaseModel != "vendor-pro" || intent.ReasoningMode != "" {
		t.Fatalf("expected literal vendor-pro target to take precedence, got %#v", intent)
	}
}

func TestProviderConfigResolveMappedProxyModelIntent_appliesOnlyOneMap(t *testing.T) {
	// Given
	provider := ProviderConfig{ModelMap: []ModelMapEntry{
		NewModelMapEntry("client", "first-target"),
		NewModelMapEntry("first-target", "second-target"),
	}}
	source := model.ProxyModelIntent{
		BaseModel:       "client",
		ReasoningEffort: "high",
		ReasoningMode:   "pro",
		HasNoPrompt:     true,
		HasUltra:        true,
	}

	// When
	intent, mapped := provider.ResolveMappedProxyModelIntent(source)

	// Then
	if !mapped {
		t.Fatal("expected provider MODEL_MAP to resolve")
	}
	if intent.BaseModel != "first-target" || intent.ReasoningEffort != "high" || intent.ReasoningMode != "pro" {
		t.Fatalf("expected bare target to retain source routing axes, got %#v", intent)
	}
	if !intent.HasNoPrompt || !intent.HasUltra {
		t.Fatalf("expected client-private axes to remain attached, got %#v", intent)
	}
}

func TestProviderConfigResolveExternalProxyModelIntentKeepsManualCandidatesAlongsideRealtimeModels(t *testing.T) {
	provider := ProviderConfig{
		ManualModels:                []string{"manual-base"},
		EnableReasoningEffortSuffix: true,
	}

	resolution, ok := provider.ResolveExternalProxyModelIntentWithCandidates(
		"manual-base-high-noprompt",
		true,
		false,
		[]string{"upstream-only"},
	)

	if !ok {
		t.Fatal("expected manual base to remain a proxy-intent candidate when realtime models are also supplied")
	}
	if resolution.SourceIntent.BaseModel != "manual-base" || resolution.SourceIntent.ReasoningEffort != "high" || !resolution.SourceIntent.HasNoPrompt {
		t.Fatalf("expected manual base with high+noprompt axes, got %#v", resolution.SourceIntent)
	}
}
