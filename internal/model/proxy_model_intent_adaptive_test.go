package model

import "testing"

func TestParseProxyModelIntentAdaptiveComposesWithProxyAxes(t *testing.T) {
	axes := ProxyModelIntentAxes{
		EnableReasoningEffort: true,
		EnablePro:             true,
		EnableAuto:            true,
		EnableAdaptive:        true,
		EnableNoPrompt:        true,
		EnableUltra:           true,
	}
	intent, ok := ParseProxyModelIntent("model-noprompt-ultra-pro-adaptive-high", []string{"model"}, axes)
	if !ok {
		t.Fatal("expected adaptive proxy model intent to parse")
	}
	if intent.BaseModel != "model" || intent.ReasoningEffort != "high" || intent.ReasoningMode != "pro" || !intent.HasAdaptive || !intent.HasNoPrompt || !intent.HasUltra || intent.HasAuto {
		t.Fatalf("unexpected adaptive proxy intent: %#v", intent)
	}
	if got := intent.CanonicalModel(); got != "model-high-pro-adaptive-ultra-noprompt" {
		t.Fatalf("expected canonical adaptive model, got %q", got)
	}
}

func TestParseProxyModelIntentAdaptiveVsAutoDistinct(t *testing.T) {
	axes := ProxyModelIntentAxes{EnableAuto: true, EnableAdaptive: true}
	adaptive, ok := ParseProxyModelIntent("model-adaptive", []string{"model"}, axes)
	if !ok || !adaptive.HasAdaptive || adaptive.HasAuto {
		t.Fatalf("expected -adaptive to parse as adaptive not auto, got %#v ok=%t", adaptive, ok)
	}
	auto, ok := ParseProxyModelIntent("model-auto", []string{"model"}, axes)
	if !ok || !auto.HasAuto || auto.HasAdaptive {
		t.Fatalf("expected -auto to parse as auto not adaptive, got %#v ok=%t", auto, ok)
	}
	// 完整字面模型优先：真实存在的 vendor-adaptive 不应被拆成代理后缀。
	literal, ok := ParseProxyModelIntent("vendor-adaptive", []string{"vendor-adaptive", "vendor"}, axes)
	if !ok || literal.BaseModel != "vendor-adaptive" || literal.HasAdaptive {
		t.Fatalf("expected exact literal vendor-adaptive to win, got %#v ok=%t", literal, ok)
	}
	if _, ok := ParseProxyModelIntent("model-adaptive-adaptive", []string{"model"}, axes); ok {
		t.Fatal("expected duplicate adaptive suffix to fail")
	}
}
