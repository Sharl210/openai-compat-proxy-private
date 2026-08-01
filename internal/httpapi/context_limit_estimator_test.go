package httpapi

import (
	"context"
	"testing"
	"time"

	modelpkg "openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/tokenestimator"
	"openai-compat-proxy/internal/upstream"
)

func TestBuildEstimatorSnapshotCountsResponsesReasoningAndToolShape(t *testing.T) {
	canon := modelpkg.CanonicalRequest{
		Model:              "gpt-5.4",
		Instructions:       "follow system",
		ResponseInputItems: []map[string]any{{"type": "reasoning", "summary": []map[string]any{{"text": "trace"}}}},
		Messages: []modelpkg.CanonicalMessage{{
			Role:            "assistant",
			ReasoningBlocks: []map[string]any{{"type": "reasoning", "encrypted_content": "enc_123"}},
			ToolCalls:       []modelpkg.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "search_web", Arguments: `{"q":"hello"}`}},
		}},
	}
	snap := buildEstimatorSnapshot(canon)
	if snap.TextChars <= 0 {
		t.Fatalf("expected text chars, got %#v", snap)
	}
	if snap.ReasoningItemCount == 0 {
		t.Fatalf("expected reasoning item count, got %#v", snap)
	}
	if snap.ToolCallCount == 0 {
		t.Fatalf("expected tool call count, got %#v", snap)
	}
}

func TestWithTokenEstimatorObservationDropsCanonicalRequest(t *testing.T) {
	canon := modelpkg.CanonicalRequest{
		Model: "gpt-5.4",
		ResponseInputItems: []map[string]any{
			{"type": "reasoning", "summary": []map[string]any{{"text": "trace"}}},
		},
		Messages: []modelpkg.CanonicalMessage{{
			Role: "assistant",
			ToolCalls: []modelpkg.CanonicalToolCall{{
				ID: "call_1", Type: "function", Name: "search_web",
			}},
		}},
	}

	ctx := withTokenEstimatorObservation(context.Background(), tokenEstimatorObservationInput{
		ProviderID:         "codex-2",
		EndpointType:       "responses",
		FinalUpstreamModel: "gpt-5.4",
		BaseEstimate:       123,
		Canon:              canon,
	})

	stored, ok := ctx.Value(tokenEstimatorObservationKey).(tokenEstimatorObservationInput)
	if !ok {
		t.Fatal("expected token estimator observation in context")
	}
	if stored.Canon.Model != "" || len(stored.Canon.Messages) != 0 || len(stored.Canon.ResponseInputItems) != 0 {
		t.Fatalf("expected context observation to drop canonical request, got %#v", stored.Canon)
	}
}

func TestEstimateCanonicalInputTokensStillUsesBaseEstimatorOnly(t *testing.T) {
	canon := modelpkg.CanonicalRequest{Model: "gpt-5.4", Messages: []modelpkg.CanonicalMessage{{Role: "user", Parts: []modelpkg.CanonicalContentPart{{Type: "text", Text: "hello world"}}}}}
	if got := estimateCanonicalInputTokens(canon); got <= 0 {
		t.Fatalf("expected positive estimate, got %d", got)
	}
}

func TestBuildObservationUsesFinalUpstreamModelAndUsageSplit(t *testing.T) {
	canon := modelpkg.CanonicalRequest{Model: "gpt-5.4", ResponseInputItems: []map[string]any{{"type": "reasoning"}}, Messages: []modelpkg.CanonicalMessage{{Role: "user", Parts: []modelpkg.CanonicalContentPart{{Type: "text", Text: "hello"}}}}}
	obs := buildTokenEstimatorObservation(tokenEstimatorObservationInput{
		ProviderID:         "codex-2",
		EndpointType:       "responses",
		FinalUpstreamModel: "gpt-5.4",
		BaseEstimate:       int64(123),
		Canon:              canon,
		Usage:              usageTotals{InputTokens: 400, CachedTokens: 300},
		Now:                time.Unix(1, 0).UTC(),
	})
	if obs.Bucket.Model != "gpt-5.4" || obs.Bucket.EndpointType != "responses" {
		t.Fatalf("unexpected bucket: %#v", obs.Bucket)
	}
	if obs.UncachedInputTokens != 100 {
		t.Fatalf("expected uncached 100, got %#v", obs)
	}
	if obs.FeatureCounts["reasoning_item_count"] == 0 {
		t.Fatalf("expected reasoning feature count, got %#v", obs.FeatureCounts)
	}
}

func TestRecordObservationAfterSuccessfulUsage(t *testing.T) {
	mgr := tokenestimator.NewManager(t.TempDir(), time.UTC, func() []string { return []string{"codex-2"} })
	ctx := withTokenEstimatorManager(context.Background(), mgr)
	ctx = withTokenEstimatorObservation(ctx, tokenEstimatorObservationInput{
		ProviderID:         "codex-2",
		EndpointType:       "responses",
		FinalUpstreamModel: "gpt-5.4",
		BaseEstimate:       120,
		Canon:              modelpkg.CanonicalRequest{Model: "gpt-5.4", Messages: []modelpkg.CanonicalMessage{{Role: "user", Parts: []modelpkg.CanonicalContentPart{{Type: "text", Text: "hello"}}}}},
	})
	if err := recordTokenEstimatorUsage(ctx, "req-1", usageTotals{InputTokens: 240, CachedTokens: 120}); err != nil {
		t.Fatalf("recordTokenEstimatorUsage error: %v", err)
	}
	state := mgr.GetBucketState(tokenestimator.BucketKey{ProviderID: "codex-2", EndpointType: "responses", Model: "gpt-5.4"})
	if state == nil || state.SampleCount != 1 {
		t.Fatalf("expected recorded state, got %#v", state)
	}
}

func TestEstimateCanonicalInputTokensUsesPriorObservationCorrectionWhenAvailable(t *testing.T) {
	mgr := tokenestimator.NewManager(t.TempDir(), time.UTC, func() []string { return []string{"codex-2"} })
	canon := modelpkg.CanonicalRequest{
		Model: "gpt-5.4",
		Messages: []modelpkg.CanonicalMessage{{
			Role:  "user",
			Parts: []modelpkg.CanonicalContentPart{{Type: "text", Text: "hello world"}},
		}},
	}
	baseEstimate := estimateCanonicalInputTokens(canon)
	ctx := withTokenEstimatorManager(context.Background(), mgr)
	ctx = withTokenEstimatorObservation(ctx, tokenEstimatorObservationInput{
		ProviderID:         "codex-2",
		EndpointType:       "responses",
		FinalUpstreamModel: "gpt-5.4",
		BaseEstimate:       int64(baseEstimate),
		Canon:              canon,
	})
	if err := recordTokenEstimatorUsage(ctx, "req-correct-next", usageTotals{InputTokens: int64(baseEstimate * 3), CachedTokens: 0}); err != nil {
		t.Fatalf("recordTokenEstimatorUsage error: %v", err)
	}
	got := estimateCanonicalInputTokensWithContext(ctx, canon)
	if got <= baseEstimate {
		t.Fatalf("expected corrected estimate above base estimate, got base=%d corrected=%d", baseEstimate, got)
	}
	if got != baseEstimate*3 {
		t.Fatalf("expected corrected estimate %d, got %d", baseEstimate*3, got)
	}
	if cold := estimateCanonicalInputTokens(canon); cold != baseEstimate {
		t.Fatalf("expected context-free cold fallback to remain %d, got %d", baseEstimate, cold)
	}
}

func TestFullContextAdmissionUsesExactMeasuredPrefixWhenAvailable(t *testing.T) {
	mgr := tokenestimator.NewManager(t.TempDir(), time.UTC, func() []string { return []string{"codex-2"} })
	canon := modelpkg.CanonicalRequest{Model: "gpt-5.4", Messages: []modelpkg.CanonicalMessage{{Role: "user", Parts: []modelpkg.CanonicalContentPart{{Type: "text", Text: "a long enough prompt for a meaningful estimate"}}}}}
	baseEstimate := estimateCanonicalInputTokens(canon)
	ctx := withTokenEstimatorManager(context.Background(), mgr)
	ctx = withTokenEstimatorObservation(ctx, tokenEstimatorObservationInput{
		ProviderID:         "codex-2",
		EndpointType:       "responses",
		FinalUpstreamModel: "gpt-5.4",
		BaseEstimate:       int64(baseEstimate),
		Canon:              canon,
	})
	if err := recordTokenEstimatorUsage(ctx, "req-low-correction", usageTotals{InputTokens: int64(baseEstimate / 2)}); err != nil {
		t.Fatalf("record low correction observation: %v", err)
	}
	if corrected := correctedCanonicalInputTokensWithContext(ctx, canon, baseEstimate); corrected != baseEstimate/2 {
		t.Fatalf("expected exact measured prefix estimate %d, got %d", baseEstimate/2, corrected)
	}
	if got := fullContextAdmissionEstimateWithContext(ctx, canon); got != baseEstimate/2 {
		t.Fatalf("expected exact measured prefix point, got=%d measured=%d", got, baseEstimate/2)
	}
}

func TestResponsesEstimatorUsesActualInputItemBranch(t *testing.T) {
	canon := modelpkg.CanonicalRequest{
		Model:                         "gpt-5.6",
		ResponseInputItemsAreOriginal: true,
		ResponseInputItems: []map[string]any{
			{"role": "developer", "content": "must be filtered"},
			{"type": "function_call_output", "call_id": "call-1", "output": "result"},
		},
		Messages: []modelpkg.CanonicalMessage{{
			Role:  "user",
			Parts: []modelpkg.CanonicalContentPart{{Type: "text", Text: "must not be used"}},
		}},
	}

	prepared := upstream.PrepareResponsesInput(canon, false)
	snapshot := buildEstimatorSnapshotForContext("openai", "responses", canon)
	if len(prepared.Input) != 1 || snapshot.InputItemCount != int64(len(prepared.Input)) {
		t.Fatalf("expected estimator to use prepared Responses input items, prepared=%#v snapshot=%#v", prepared, snapshot)
	}
	if snapshot.ToolResultCount != 1 || snapshot.ToolCallCount != 0 {
		t.Fatalf("expected function_call_output shape, got %#v", snapshot)
	}

	changedMessage := canon
	changedMessage.Messages = []modelpkg.CanonicalMessage{{
		Role:  "user",
		Parts: []modelpkg.CanonicalContentPart{{Type: "text", Text: "different message must still be ignored"}},
	}}
	changedSnapshot := buildEstimatorSnapshotForContext("openai", "responses", changedMessage)
	if changedSnapshot.Coordinate.PrefixFingerprint != snapshot.Coordinate.PrefixFingerprint {
		t.Fatalf("expected original input branch to ignore Messages changes, before=%s after=%s", snapshot.Coordinate.PrefixFingerprint, changedSnapshot.Coordinate.PrefixFingerprint)
	}
}

func TestResponsesEstimatorWireContextUsesMergedInputFieldsAndInstructionsBranch(t *testing.T) {
	canon := modelpkg.CanonicalRequest{
		Model:            "gpt-5.6",
		Instructions:     "stable instructions",
		InstructionParts: []modelpkg.CanonicalContentPart{{Type: "text", Text: "ignored for Responses"}},
		PreservedTopLevelFields: map[string]any{
			"temperature": 0.2,
		},
		ResponseInputItems: []map[string]any{{
			"type":                                "message",
			"__openai_compat_responses_top_level": map[string]any{"previous_response_id": "resp-1"},
		}},
	}
	first, _, ok := buildEstimatorWireContext("openai", "responses", canon)
	if !ok || first == "" {
		t.Fatalf("expected a wire context fingerprint")
	}

	changedInstructionParts := canon
	changedInstructionParts.InstructionParts = []modelpkg.CanonicalContentPart{{Type: "text", Text: "different ignored part"}}
	second, _, ok := buildEstimatorWireContext("openai", "responses", changedInstructionParts)
	if !ok || second != first {
		t.Fatalf("expected Responses wire context to ignore unused InstructionParts, first=%s second=%s", first, second)
	}

	changedPreservedField := canon
	changedPreservedField.ResponseInputItems = []map[string]any{{
		"type":                                "message",
		"__openai_compat_responses_top_level": map[string]any{"previous_response_id": "resp-2"},
	}}
	third, _, ok := buildEstimatorWireContext("openai", "responses", changedPreservedField)
	if !ok || third == first {
		t.Fatalf("expected merged preserved input fields to affect wire context, first=%s third=%s", first, third)
	}
}
