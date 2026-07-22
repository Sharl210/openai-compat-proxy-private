package model

import "testing"

func TestParseProxyModelIntentAdaptiveComposesWithAllProxyAxes(t *testing.T) {
	axes := ProxyModelIntentAxes{
		EnableReasoningEffort: true,
		EnablePro:             true,
		EnableAdaptive:        true,
		EnableNoPrompt:        true,
		EnableUltra:           true,
	}
	intent, ok := ParseProxyModelIntent("model-noprompt-ultra-adaptive-pro-high", []string{"model"}, axes)
	if !ok {
		t.Fatal("expected adaptive proxy model intent to parse")
	}
	if intent.BaseModel != "model" || intent.ReasoningEffort != "high" || intent.ReasoningMode != "pro" || !intent.HasAdaptive || !intent.HasNoPrompt || !intent.HasUltra {
		t.Fatalf("unexpected adaptive proxy intent: %#v", intent)
	}
	if got := intent.CanonicalModel(); got != "model-high-pro-adaptive-ultra-noprompt" {
		t.Fatalf("expected canonical adaptive model, got %q", got)
	}
}

func TestParseProxyModelIntentAdaptivePreservesExactLiteralsAndRejectsDuplicates(t *testing.T) {
	axes := ProxyModelIntentAxes{EnableAdaptive: true}
	literal, ok := ParseProxyModelIntent("vendor-adaptive", []string{"vendor-adaptive", "vendor"}, axes)
	if !ok || literal.BaseModel != "vendor-adaptive" || literal.HasAdaptive {
		t.Fatalf("expected exact literal vendor-adaptive to win, got %#v ok=%t", literal, ok)
	}
	if _, ok := ParseProxyModelIntent("model-adaptive-adaptive", []string{"model"}, axes); ok {
		t.Fatal("expected duplicate adaptive suffix to fail")
	}
}
