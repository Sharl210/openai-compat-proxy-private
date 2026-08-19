package config

import "testing"

func TestAutoProxyModelIntentSurvivesRootAndProviderMappings(t *testing.T) {
	root := Config{
		EnableNoPromptModelSuffix: true,
		V1ModelMap: []ModelMapEntry{
			NewModelMapEntry("client", "root-target"),
		},
	}
	intent, ok := root.ResolveV1ProxyModelIntent("client-noprompt-ultra-auto-pro-high")
	if !ok {
		t.Fatal("expected root auto proxy model intent to resolve")
	}
	if intent.BaseModel != "root-target" || intent.ReasoningEffort != "high" || intent.ReasoningMode != "pro" || !intent.HasAuto || !intent.HasNoPrompt || !intent.HasUltra {
		t.Fatalf("unexpected root mapped intent: %#v", intent)
	}

	provider := ProviderConfig{ModelMap: []ModelMapEntry{
		NewModelMapEntry("root-target", "claude-opus-4-6"),
	}}
	mapped, ok := provider.ResolveMappedProxyModelIntent(intent)
	if !ok {
		t.Fatal("expected provider auto proxy model intent to resolve")
	}
	if mapped.BaseModel != "claude-opus-4-6" || !mapped.HasAuto || !mapped.HasNoPrompt || !mapped.HasUltra {
		t.Fatalf("expected private auto axes to survive provider mapping, got %#v", mapped)
	}
}
