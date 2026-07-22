package config

import "testing"

func TestAdaptiveProxyModelIntentSurvivesRootAndProviderMappings(t *testing.T) {
	root := Config{
		EnableNoPromptModelSuffix: true,
		V1ModelMap: []ModelMapEntry{
			NewModelMapEntry("client", "root-target"),
		},
	}
	intent, ok := root.ResolveV1ProxyModelIntent("client-noprompt-ultra-adaptive-pro-high")
	if !ok {
		t.Fatal("expected root adaptive proxy model intent to resolve")
	}
	if intent.BaseModel != "root-target" || intent.ReasoningEffort != "high" || intent.ReasoningMode != "pro" || !intent.HasAdaptive || !intent.HasNoPrompt || !intent.HasUltra {
		t.Fatalf("unexpected root mapped intent: %#v", intent)
	}

	provider := ProviderConfig{ModelMap: []ModelMapEntry{
		NewModelMapEntry("root-target", "claude-opus-4-6"),
	}}
	mapped, ok := provider.ResolveMappedProxyModelIntent(intent)
	if !ok {
		t.Fatal("expected provider adaptive proxy model intent to resolve")
	}
	if mapped.BaseModel != "claude-opus-4-6" || !mapped.HasAdaptive || !mapped.HasNoPrompt || !mapped.HasUltra {
		t.Fatalf("expected private adaptive axes to survive provider mapping, got %#v", mapped)
	}
}
