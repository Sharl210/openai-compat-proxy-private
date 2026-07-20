package tokenestimator

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
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

func TestRecordObservationBoundsSeenRequestIDsAndAllowsEvictedIDs(t *testing.T) {
	// Given
	mgr := NewManager(t.TempDir(), time.UTC, func() []string { return []string{"codex-2"} })
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
	firstID := "req-0"
	lastID := "req-" + strconv.Itoa(defaultRecentSampleLimit)

	// When
	for i := 0; i <= defaultRecentSampleLimit; i++ {
		if err := mgr.RecordObservation("req-"+strconv.Itoa(i), obs); err != nil {
			t.Fatalf("RecordObservation(%d) error: %v", i, err)
		}
	}
	if err := mgr.RecordObservation(lastID, obs); err != nil {
		t.Fatalf("RecordObservation recent duplicate error: %v", err)
	}
	if err := mgr.RecordObservation(firstID, obs); err != nil {
		t.Fatalf("RecordObservation evicted ID error: %v", err)
	}

	// Then
	if got := len(mgr.seenRequests); got != defaultRecentSampleLimit {
		t.Fatalf("expected %d retained request IDs, got %d", defaultRecentSampleLimit, got)
	}
	if got := len(mgr.seenRequestOrder); got != defaultRecentSampleLimit {
		t.Fatalf("expected %d retained request IDs in FIFO order, got %d", defaultRecentSampleLimit, got)
	}
	state := mgr.GetBucketState(obs.Bucket)
	if state == nil || state.SampleCount != int64(defaultRecentSampleLimit+2) {
		t.Fatalf("expected %d recorded observations, got %#v", defaultRecentSampleLimit+2, state)
	}
}

func TestRecordObservationEvictsLeastRecentlyUsedBucketAfterCommit(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, time.UTC, func() []string { return []string{"codex-2"} })
	mgr.bucketLimit = 2
	observation := func(model string) Observation {
		return Observation{Bucket: BucketKey{ProviderID: "codex-2", EndpointType: "responses", Model: model}, BaseEstimate: 100, InputTokens: 120, CachedTokens: 20, UncachedInputTokens: 100, Shape: ShapePlain}
	}
	for _, modelName := range []string{"model-a", "model-b"} {
		if err := mgr.RecordObservation("req-"+modelName, observation(modelName)); err != nil {
			t.Fatalf("RecordObservation(%s) error: %v", modelName, err)
		}
	}
	if state := mgr.GetBucketState(observation("model-a").Bucket); state == nil {
		t.Fatal("expected model-a state before eviction")
	}
	if err := mgr.RecordObservation("req-model-c", observation("model-c")); err != nil {
		t.Fatalf("RecordObservation(model-c) error: %v", err)
	}
	if _, found := mgr.buckets[observation("model-b").Bucket]; found {
		t.Fatalf("expected least-recently-used model-b to be evicted, buckets=%#v", mgr.buckets)
	}
	for _, modelName := range []string{"model-a", "model-c"} {
		jsonPath, txtPath := BucketPaths(root, observation(modelName).Bucket)
		if _, err := os.Stat(jsonPath); err != nil {
			t.Fatalf("expected committed %s JSON state: %v", modelName, err)
		}
		if _, err := os.Stat(txtPath); err != nil {
			t.Fatalf("expected committed %s summary state: %v", modelName, err)
		}
	}
	jsonPath, txtPath := BucketPaths(root, observation("model-b").Bucket)
	for _, path := range []string{jsonPath, txtPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected evicted state file removed after successor commit: path=%s err=%v", filepath.Base(path), err)
		}
	}
}

func TestManagerCachesLazilyLoadedBucketAndKeepsItRecent(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(root, time.UTC, func() []string { return []string{"codex-2"} })
	manager.bucketLimit = 2
	newState := func(modelName string) *BucketState {
		return &BucketState{
			SchemaVersion:         schemaVersion,
			EstimatorVersion:      estimatorVersion,
			ProviderID:            "codex-2",
			EndpointType:          "responses",
			FinalUpstreamRawModel: modelName,
			SafeModelName:         SafeModelName(modelName),
		}
	}
	keyA := BucketKey{ProviderID: "codex-2", EndpointType: "responses", Model: "model-a"}
	keyB := BucketKey{ProviderID: "codex-2", EndpointType: "responses", Model: "model-b"}
	keyC := BucketKey{ProviderID: "codex-2", EndpointType: "responses", Model: "model-c"}
	if err := SaveBucketState(root, keyA, newState(keyA.Model)); err != nil {
		t.Fatalf("save lazy bucket: %v", err)
	}
	if err := manager.RecordObservation("req-b", Observation{Bucket: keyB, BaseEstimate: 100, InputTokens: 120, UncachedInputTokens: 120, Shape: ShapePlain}); err != nil {
		t.Fatalf("record model-b: %v", err)
	}
	if state := manager.GetBucketState(keyA); state == nil {
		t.Fatal("expected lazy disk bucket to load")
	}
	if err := manager.RecordObservation("req-c", Observation{Bucket: keyC, BaseEstimate: 100, InputTokens: 120, UncachedInputTokens: 120, Shape: ShapePlain}); err != nil {
		t.Fatalf("record model-c: %v", err)
	}
	if _, found := manager.buckets[keyB]; found {
		t.Fatalf("expected model-b to be evicted after lazy model-a access, buckets=%#v", manager.buckets)
	}
	if _, found := manager.buckets[keyA]; !found {
		t.Fatalf("expected lazily loaded model-a to remain cached, buckets=%#v", manager.buckets)
	}
}

func TestManagerRestoresPersistedAccessOrderAcrossRestart(t *testing.T) {
	root := t.TempDir()
	keyA := BucketKey{ProviderID: "codex-2", EndpointType: "responses", Model: "model-a"}
	keyB := BucketKey{ProviderID: "codex-2", EndpointType: "responses", Model: "model-b"}
	keyC := BucketKey{ProviderID: "codex-2", EndpointType: "responses", Model: "model-c"}
	for index, key := range []BucketKey{keyA, keyB, keyC} {
		state := &BucketState{
			SchemaVersion:         schemaVersion,
			EstimatorVersion:      estimatorVersion,
			ProviderID:            key.ProviderID,
			EndpointType:          key.EndpointType,
			FinalUpstreamRawModel: key.Model,
			SafeModelName:         SafeModelName(key.Model),
			LastAccessedAt:        time.Unix(int64(index+1), 0).UTC(),
		}
		if err := SaveBucketState(root, key, state); err != nil {
			t.Fatalf("save %s: %v", key.Model, err)
		}
	}
	manager := &Manager{
		providersDir: root,
		enabledFn:    func() []string { return []string{"codex-2"} },
		buckets:      map[BucketKey]*BucketState{},
		bucketLimit:  2,
	}
	if err := manager.loadExistingBuckets(); err != nil {
		t.Fatalf("load existing buckets: %v", err)
	}
	if _, found := manager.buckets[keyA]; found {
		t.Fatalf("expected oldest persisted access to be evicted, buckets=%#v", manager.buckets)
	}
	if _, found := manager.buckets[keyB]; !found {
		t.Fatalf("expected newer bucket model-b to remain, buckets=%#v", manager.buckets)
	}
	if _, found := manager.buckets[keyC]; !found {
		t.Fatalf("expected newest bucket model-c to remain, buckets=%#v", manager.buckets)
	}
}

func TestManagerPersistsReadAccessOrderAcrossRestart(t *testing.T) {
	root := t.TempDir()
	keyA := BucketKey{ProviderID: "codex-2", EndpointType: "responses", Model: "model-a"}
	keyB := BucketKey{ProviderID: "codex-2", EndpointType: "responses", Model: "model-b"}
	keyC := BucketKey{ProviderID: "codex-2", EndpointType: "responses", Model: "model-c"}
	for index, key := range []BucketKey{keyA, keyB, keyC} {
		state := &BucketState{
			SchemaVersion:         schemaVersion,
			EstimatorVersion:      estimatorVersion,
			ProviderID:            key.ProviderID,
			EndpointType:          key.EndpointType,
			FinalUpstreamRawModel: key.Model,
			SafeModelName:         SafeModelName(key.Model),
			LastAccessedAt:        time.Unix(int64(index+1), 0).UTC(),
		}
		if err := SaveBucketState(root, key, state); err != nil {
			t.Fatalf("save %s: %v", key.Model, err)
		}
	}

	manager := NewManager(root, time.UTC, func() []string { return []string{"codex-2"} })
	if state := manager.GetBucketState(keyA); state == nil {
		t.Fatal("expected model-a state to be loaded")
	}
	if err := manager.Flush(context.Background()); err != nil {
		t.Fatalf("flush read access state: %v", err)
	}

	restarted := &Manager{
		providersDir: root,
		location:     time.UTC,
		enabledFn:    func() []string { return []string{"codex-2"} },
		buckets:      map[BucketKey]*BucketState{},
		bucketLimit:  2,
	}
	if err := restarted.loadExistingBuckets(); err != nil {
		t.Fatalf("load existing buckets after restart: %v", err)
	}
	if _, found := restarted.buckets[keyB]; found {
		t.Fatalf("expected least-recently-read model-b to be evicted, buckets=%#v", restarted.buckets)
	}
	for _, key := range []BucketKey{keyA, keyC} {
		if _, found := restarted.buckets[key]; !found {
			t.Fatalf("expected %s to remain after restart, buckets=%#v", key.Model, restarted.buckets)
		}
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
