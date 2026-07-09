package tokenestimator

import (
	"context"
	"testing"
	"time"
)

func TestManagerLoadsExistingBucketOnStartup(t *testing.T) {
	root := t.TempDir()
	key := BucketKey{ProviderID: "codex-2", EndpointType: "responses", Model: "gpt-5.4"}
	state := &BucketState{
		SchemaVersion:         1,
		EstimatorVersion:      1,
		ProviderID:            key.ProviderID,
		EndpointType:          key.EndpointType,
		FinalUpstreamRawModel: key.Model,
		SafeModelName:         SafeModelName(key.Model),
		SampleCount:           9,
	}
	if err := SaveBucketState(root, key, state); err != nil {
		t.Fatalf("SaveBucketState error: %v", err)
	}
	mgr := NewManager(root, time.UTC, func() []string { return []string{"codex-2"} })
	loaded := mgr.GetBucketState(key)
	if loaded == nil || loaded.SampleCount != 9 {
		t.Fatalf("expected preloaded state, got %#v", loaded)
	}
}

func TestRecordObservationUpdatesRollingState(t *testing.T) {
	mgr := NewManager(t.TempDir(), time.UTC, func() []string { return []string{"codex-2"} })
	obs := Observation{
		Bucket:              BucketKey{ProviderID: "codex-2", EndpointType: "responses", Model: "gpt-5.4"},
		BaseEstimate:        100000,
		InputTokens:         150000,
		CachedTokens:        120000,
		UncachedInputTokens: 30000,
		Shape:               ShapeStructuredResponses,
		FeatureCounts:       map[string]int64{"text_chars": 80000, "reasoning_items": 10},
		ProtocolSignature:   "responses:v1",
		EstimatorSignature:  "base-estimator:v1",
	}
	if err := mgr.RecordObservation("req-1", obs); err != nil {
		t.Fatalf("RecordObservation error: %v", err)
	}
	state := mgr.GetBucketState(obs.Bucket)
	if state == nil || state.SampleCount != 1 || state.AvgInputTokens != 150000 {
		t.Fatalf("unexpected state: %#v", state)
	}
	if state.RollingUncachedCorrection <= 0 {
		t.Fatalf("expected positive correction, got %#v", state)
	}
}

func TestRecordObservationDropsInvalidUsage(t *testing.T) {
	mgr := NewManager(t.TempDir(), time.UTC, func() []string { return []string{"codex-2"} })
	err := mgr.RecordObservation("req-1", Observation{
		Bucket:              BucketKey{ProviderID: "codex-2", EndpointType: "responses", Model: "gpt-5.4"},
		BaseEstimate:        100,
		InputTokens:         50,
		CachedTokens:        60,
		UncachedInputTokens: -10,
	})
	if err == nil {
		t.Fatal("expected invalid usage error")
	}
}

func TestManagerFlushPersistsBuckets(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, time.UTC, func() []string { return []string{"codex-2"} })
	obs := Observation{Bucket: BucketKey{ProviderID: "codex-2", EndpointType: "responses", Model: "gpt-5.4"}, BaseEstimate: 100, InputTokens: 120, CachedTokens: 20, UncachedInputTokens: 100, Shape: ShapePlain, ProtocolSignature: "responses:v1", EstimatorSignature: "base-estimator:v1"}
	if err := mgr.RecordObservation("req-1", obs); err != nil {
		t.Fatalf("RecordObservation error: %v", err)
	}
	if err := mgr.Flush(context.Background()); err != nil {
		t.Fatalf("Flush error: %v", err)
	}
	state, err := LoadBucketState(root, obs.Bucket)
	if err != nil || state == nil || state.SampleCount != 1 {
		t.Fatalf("expected flushed state, got %#v err=%v", state, err)
	}
}

func TestRecordObservationPersistsStateImmediately(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, time.UTC, func() []string { return []string{"codex-2"} })
	obs := Observation{
		Bucket:              BucketKey{ProviderID: "codex-2", EndpointType: "responses", Model: "gpt-5.4"},
		BaseEstimate:        100,
		InputTokens:         120,
		CachedTokens:        20,
		UncachedInputTokens: 100,
		Shape:               ShapePlain,
		ProtocolSignature:   "responses:v1",
		EstimatorSignature:  "base-estimator:v1",
	}
	if err := mgr.RecordObservation("req-persist", obs); err != nil {
		t.Fatalf("RecordObservation error: %v", err)
	}
	loaded, err := LoadBucketState(root, obs.Bucket)
	if err != nil {
		t.Fatalf("LoadBucketState error: %v", err)
	}
	if loaded == nil || loaded.SampleCount != 1 {
		t.Fatalf("expected persisted state, got %#v", loaded)
	}
}


func TestManagerConservativeAdmissionLimitUsesSmallerLearnedBound(t *testing.T) {
	mgr := NewManager(t.TempDir(), time.UTC, func() []string { return []string{"codex"} })
	obs := Observation{Bucket: BucketKey{ProviderID: "codex", EndpointType: "responses", Model: "gpt-5.4"}, BaseEstimate: 100, InputTokens: 390, CachedTokens: 0, UncachedInputTokens: 390, Shape: ShapePlain, ProtocolSignature: "responses:v1", EstimatorSignature: "base-estimator:v1"}
	if err := mgr.RecordObservation("req-overflow", obs); err != nil {
		t.Fatalf("RecordObservation error: %v", err)
	}
	limit, ok := mgr.ConservativeAdmissionLimit(obs.Bucket, 300, ShapePlain)
	if !ok {
		t.Fatal("expected conservative admission limit")
	}
	if limit >= 300 {
		t.Fatalf("expected learned/observed guard to tighten below configured limit, got %d", limit)
	}
}

func TestManagerConservativeAdmissionLimitDoesNotUseLastOverflowSampleAsGlobalClamp(t *testing.T) {
	mgr := NewManager(t.TempDir(), time.UTC, func() []string { return []string{"codex"} })
	key := BucketKey{ProviderID: "codex", EndpointType: "responses", Model: "gpt-5.4"}
	for i := 0; i < 24; i++ {
		obs := Observation{
			Bucket:              key,
			BaseEstimate:        100000,
			InputTokens:         108000,
			CachedTokens:        12000,
			UncachedInputTokens: 96000,
			Shape:               ShapePlain,
			ProtocolSignature:   "responses:v1",
			EstimatorSignature:  "base-estimator:v1",
		}
		if err := mgr.RecordObservation("req-stable-"+time.Unix(int64(i), 0).UTC().Format("150405"), obs); err != nil {
			t.Fatalf("RecordObservation stable error: %v", err)
		}
	}
	spike := Observation{
		Bucket:              key,
		BaseEstimate:        100000,
		InputTokens:         390000,
		CachedTokens:        0,
		UncachedInputTokens: 390000,
		Shape:               ShapeToolHeavy,
		ProtocolSignature:   "responses:v1",
		EstimatorSignature:  "base-estimator:v1",
	}
	if err := mgr.RecordObservation("req-spike", spike); err != nil {
		t.Fatalf("RecordObservation spike error: %v", err)
	}
	limit, ok := mgr.ConservativeAdmissionLimit(key, 300000, ShapePlain)
	if !ok {
		t.Fatal("expected conservative admission limit")
	}
	if limit < 250000 {
		t.Fatalf("expected a single latest spike to not globally clamp later requests, got %d", limit)
	}
}

func TestManagerCorrectedEstimateUsesMatchingShapeRatio(t *testing.T) {
	mgr := NewManager(t.TempDir(), time.UTC, func() []string { return []string{"codex"} })
	obs := Observation{
		Bucket:              BucketKey{ProviderID: "codex", EndpointType: "responses", Model: "gpt-5.4"},
		BaseEstimate:        120,
		InputTokens:         300,
		CachedTokens:        0,
		UncachedInputTokens: 300,
		Shape:               ShapePlain,
		ProtocolSignature:   "responses:v1",
		EstimatorSignature:  "base-estimator:v1",
	}
	if err := mgr.RecordObservation("req-corrected-estimate", obs); err != nil {
		t.Fatalf("RecordObservation error: %v", err)
	}
	got, ok := mgr.CorrectedEstimate(obs.Bucket, 120, ShapePlain)
	if !ok {
		t.Fatal("expected corrected estimate from prior observation")
	}
	if got != 300 {
		t.Fatalf("expected corrected estimate 300, got %d", got)
	}
}

func TestManagerCorrectedEstimateFallsBackWithoutSamples(t *testing.T) {
	mgr := NewManager(t.TempDir(), time.UTC, func() []string { return []string{"codex"} })
	got, ok := mgr.CorrectedEstimate(BucketKey{ProviderID: "codex", EndpointType: "responses", Model: "gpt-5.4"}, 120, ShapePlain)
	if ok {
		t.Fatal("expected no corrected estimate without prior samples")
	}
	if got != 120 {
		t.Fatalf("expected base estimate fallback 120, got %d", got)
	}
}
