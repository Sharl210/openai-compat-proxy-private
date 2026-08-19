package model

import "testing"

func TestParseProxyModelIntentAutoComposesWithAllProxyAxes(t *testing.T) {
	axes := ProxyModelIntentAxes{
		EnableReasoningEffort: true,
		EnablePro:             true,
		EnableAuto:            true,
		EnableNoPrompt:        true,
		EnableUltra:           true,
	}
	intent, ok := ParseProxyModelIntent("model-noprompt-ultra-auto-pro-high", []string{"model"}, axes)
	if !ok {
		t.Fatal("expected auto proxy model intent to parse")
	}
	if intent.BaseModel != "model" || intent.ReasoningEffort != "high" || intent.ReasoningMode != "pro" || !intent.HasAuto || !intent.HasNoPrompt || !intent.HasUltra {
		t.Fatalf("unexpected auto proxy intent: %#v", intent)
	}
	if got := intent.CanonicalModel(); got != "model-high-pro-auto-ultra-noprompt" {
		t.Fatalf("expected canonical auto model, got %q", got)
	}
}

func TestParseProxyModelIntentAutoPreservesExactLiteralsAndRejectsDuplicates(t *testing.T) {
	axes := ProxyModelIntentAxes{EnableAuto: true}
	literal, ok := ParseProxyModelIntent("vendor-auto", []string{"vendor-auto", "vendor"}, axes)
	if !ok || literal.BaseModel != "vendor-auto" || literal.HasAuto {
		t.Fatalf("expected exact literal vendor-auto to win, got %#v ok=%t", literal, ok)
	}
	if _, ok := ParseProxyModelIntent("model-auto-auto", []string{"model"}, axes); ok {
		t.Fatal("expected duplicate auto suffix to fail")
	}
}
