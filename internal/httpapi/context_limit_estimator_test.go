package httpapi

import (
	"context"
	"testing"
	"time"

	modelpkg "openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/tokenestimator"
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
