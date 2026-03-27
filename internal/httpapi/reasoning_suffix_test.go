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
