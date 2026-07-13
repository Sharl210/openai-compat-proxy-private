package model

import "testing"

func TestParseProxyModelIntent_exposesApprovedSemanticFields(t *testing.T) {
	intent, ok := ParseProxyModelIntent("model-low-pro-noprompt", []string{"model"}, ProxyModelIntentAxes{
		EnableReasoningEffort: true,
		EnablePro:             true,
		EnableNoPrompt:        true,
	})
	if !ok {
		t.Fatal("expected model intent to match")
	}
	if intent.ReasoningEffort != "low" {
		t.Fatalf("expected reasoning effort low, got %q", intent.ReasoningEffort)
	}
	if intent.ReasoningMode != "pro" {
		t.Fatalf("expected reasoning mode pro, got %q", intent.ReasoningMode)
	}
	if !intent.HasNoPrompt {
		t.Fatal("expected noprompt marker")
	}
}

func TestParseProxyModelIntent_whenProxySuffixesUseAnyOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		model     string
		effort    string
		pro       bool
		noprompt  bool
		canonical string
	}{
		{name: "pro only", model: "model-pro", pro: true, canonical: "model-pro"},
		{name: "noprompt only", model: "model-noprompt", noprompt: true, canonical: "model-noprompt"},
		{name: "effort only", model: "model-low", effort: "low", canonical: "model-low"},
		{name: "effort then pro", model: "model-low-pro", effort: "low", pro: true, canonical: "model-low-pro"},
		{name: "pro then effort", model: "model-pro-low", effort: "low", pro: true, canonical: "model-low-pro"},
		{name: "pro then noprompt", model: "model-pro-noprompt", pro: true, noprompt: true, canonical: "model-pro-noprompt"},
		{name: "noprompt then pro", model: "model-noprompt-pro", pro: true, noprompt: true, canonical: "model-pro-noprompt"},
		{name: "effort then noprompt", model: "model-low-noprompt", effort: "low", noprompt: true, canonical: "model-low-noprompt"},
		{name: "noprompt then effort", model: "model-noprompt-low", effort: "low", noprompt: true, canonical: "model-low-noprompt"},
		{name: "effort pro noprompt", model: "model-low-pro-noprompt", effort: "low", pro: true, noprompt: true, canonical: "model-low-pro-noprompt"},
		{name: "effort noprompt pro", model: "model-low-noprompt-pro", effort: "low", pro: true, noprompt: true, canonical: "model-low-pro-noprompt"},
		{name: "pro effort noprompt", model: "model-pro-low-noprompt", effort: "low", pro: true, noprompt: true, canonical: "model-low-pro-noprompt"},
		{name: "pro noprompt effort", model: "model-pro-noprompt-low", effort: "low", pro: true, noprompt: true, canonical: "model-low-pro-noprompt"},
		{name: "noprompt effort pro", model: "model-noprompt-low-pro", effort: "low", pro: true, noprompt: true, canonical: "model-low-pro-noprompt"},
		{name: "noprompt pro effort", model: "model-noprompt-pro-low", effort: "low", pro: true, noprompt: true, canonical: "model-low-pro-noprompt"},
		{name: "specified mixed order", model: "model-noprompt-pro-high", effort: "high", pro: true, noprompt: true, canonical: "model-high-pro-noprompt"},
		{name: "specified canonical order", model: "model-high-noprompt-pro", effort: "high", pro: true, noprompt: true, canonical: "model-high-pro-noprompt"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			intent, ok := ParseProxyModelIntent(test.model, []string{"model"}, ProxyModelIntentAxes{
				EnableReasoningEffort: true,
				EnablePro:             true,
				EnableNoPrompt:        true,
			})
			if !ok {
				t.Fatal("expected model intent to match")
			}
			if intent.BaseModel != "model" {
				t.Fatalf("expected base model model, got %q", intent.BaseModel)
			}
			if intent.ReasoningEffort != test.effort {
				t.Fatalf("expected reasoning effort %q, got %q", test.effort, intent.ReasoningEffort)
			}
			expectedReasoningMode := ""
			if test.pro {
				expectedReasoningMode = "pro"
			}
			if intent.ReasoningMode != expectedReasoningMode {
				t.Fatalf("expected reasoning mode %q, got %q", expectedReasoningMode, intent.ReasoningMode)
			}
			if intent.HasNoPrompt != test.noprompt {
				t.Fatalf("expected noprompt %t, got %t", test.noprompt, intent.HasNoPrompt)
			}
			if got := intent.CanonicalModel(); got != test.canonical {
				t.Fatalf("expected canonical model %q, got %q", test.canonical, got)
			}
		})
	}
}

func TestParseProxyModelIntent_whenCandidateMatchesExactly(t *testing.T) {
	t.Parallel()

	for _, candidate := range []string{"gpt-5.5-pro", "vendor-high", "vendor-max", "vendor-noprompt"} {
		candidate := candidate
		t.Run(candidate, func(t *testing.T) {
			t.Parallel()

			intent, ok := ParseProxyModelIntent(candidate, []string{candidate}, ProxyModelIntentAxes{
				EnableReasoningEffort: true,
				EnablePro:             true,
				EnableNoPrompt:        true,
			})
			if !ok {
				t.Fatal("expected exact natural candidate to match")
			}
			if intent.BaseModel != candidate {
				t.Fatalf("expected base model %q, got %q", candidate, intent.BaseModel)
			}
			if intent.ReasoningEffort != "" {
				t.Fatalf("expected no reasoning effort, got %q", intent.ReasoningEffort)
			}
			if intent.ReasoningMode != "" {
				t.Fatalf("expected no reasoning mode, got %q", intent.ReasoningMode)
			}
			if intent.HasNoPrompt {
				t.Fatal("expected noprompt to remain a natural candidate token")
			}
			if got := intent.CanonicalModel(); got != candidate {
				t.Fatalf("expected canonical model %q, got %q", candidate, got)
			}
		})
	}
}

func TestParseProxyModelIntent_whenCandidatesOverlap(t *testing.T) {
	t.Parallel()

	intent, ok := ParseProxyModelIntent("vendor-high-pro", []string{"vendor", "vendor-high"}, ProxyModelIntentAxes{EnablePro: true})
	if !ok {
		t.Fatal("expected longest candidate suffix match")
	}
	if intent.BaseModel != "vendor-high" {
		t.Fatalf("expected longest base candidate vendor-high, got %q", intent.BaseModel)
	}
	if intent.ReasoningMode != "pro" {
		t.Fatalf("expected reasoning mode pro, got %q", intent.ReasoningMode)
	}
	if got := intent.CanonicalModel(); got != "vendor-high-pro" {
		t.Fatalf("expected canonical model vendor-high-pro, got %q", got)
	}
}

func TestParseProxyModelIntent_whenTailCannotBeCompletelyConsumed(t *testing.T) {
	t.Parallel()

	for _, modelName := range []string{"vendor-high-typo-pro", "vendor-pro-typo", "vendor-typo-high"} {
		modelName := modelName
		t.Run(modelName, func(t *testing.T) {
			t.Parallel()

			_, ok := ParseProxyModelIntent(modelName, []string{"vendor"}, ProxyModelIntentAxes{
				EnableReasoningEffort: true,
				EnablePro:             true,
				EnableNoPrompt:        true,
			})
			if ok {
				t.Fatal("expected unconsumable suffix tail to fail")
			}
		})
	}
}

func TestParseProxyModelIntent_whenAxisIsDisabled(t *testing.T) {
	t.Parallel()

	_, ok := ParseProxyModelIntent("model-pro", []string{"model"}, ProxyModelIntentAxes{})
	if ok {
		t.Fatal("expected disabled marker axis to leave the suffix unconsumable")
	}
}

func TestParseProxyModelIntentNormalizesUltraWithOtherAxes(t *testing.T) {
	axes := ProxyModelIntentAxes{
		EnableReasoningEffort: true,
		EnablePro:             true,
		EnableNoPrompt:        true,
		EnableUltra:           true,
	}
	for _, input := range []string{
		"gpt-5.6-high-ultra-noprompt",
		"gpt-5.6-noprompt-ultra-high",
		"gpt-5.6-ultra-high-noprompt",
	} {
		intent, ok := ParseProxyModelIntent(input, []string{"gpt-5.6"}, axes)
		if !ok || intent.BaseModel != "gpt-5.6" || intent.ReasoningEffort != "high" || !intent.HasNoPrompt || !intent.HasUltra {
			t.Fatalf("unexpected intent for %q: %#v, ok=%v", input, intent, ok)
		}
		if got := intent.CanonicalModel(); got != "gpt-5.6-high-ultra-noprompt" {
			t.Fatalf("expected canonical ultra model, got %q", got)
		}
	}
}

func TestParseProxyModelIntentKeepsLiteralUltraModelAndRejectsDuplicateTailAxes(t *testing.T) {
	axes := ProxyModelIntentAxes{EnableReasoningEffort: true, EnablePro: true, EnableNoPrompt: true, EnableUltra: true}
	literal, ok := ParseProxyModelIntent("vendor-ultra", []string{"vendor-ultra", "vendor"}, axes)
	if !ok || literal.BaseModel != "vendor-ultra" || literal.HasUltra {
		t.Fatalf("literal model was not preserved: %#v", literal)
	}

	for _, input := range []string{
		"gpt-5.6-pro-pro",
		"gpt-5.6-noprompt-noprompt",
		"gpt-5.6-low-high-pro",
		"gpt-5.6-ultra-ultra",
	} {
		t.Run(input, func(t *testing.T) {
			if _, ok := ParseProxyModelIntent(input, []string{"gpt-5.6"}, axes); ok {
				t.Fatal("expected duplicate tail axes to fail")
			}
		})
	}
}
