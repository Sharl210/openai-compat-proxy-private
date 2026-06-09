package tokenestimator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSaveAndLoadBucketState(t *testing.T) {
	root := t.TempDir()
	state := &BucketState{
		SchemaVersion:         1,
		EstimatorVersion:      1,
		ProviderID:            "codex-2",
		EndpointType:          "responses",
		FinalUpstreamRawModel: "gpt-5.4",
		SafeModelName:         SafeModelName("gpt-5.4"),
		CreatedAt:             time.Now().UTC(),
		UpdatedAt:             time.Now().UTC(),
		SampleCount:           3,
	}
	key := BucketKey{ProviderID: "codex-2", EndpointType: "responses", Model: "gpt-5.4"}
	if err := SaveBucketState(root, key, state); err != nil {
		t.Fatalf("SaveBucketState error: %v", err)
	}
	loaded, err := LoadBucketState(root, key)
	if err != nil {
		t.Fatalf("LoadBucketState error: %v", err)
	}
	if loaded.FinalUpstreamRawModel != "gpt-5.4" || loaded.SampleCount != 3 {
		t.Fatalf("unexpected loaded state: %#v", loaded)
	}
	jsonPath, txtPath := BucketPaths(root, key)
	if _, err := os.Stat(jsonPath); err != nil {
		t.Fatalf("expected json path, got %v", err)
	}
	if _, err := os.Stat(txtPath); err != nil {
		t.Fatalf("expected txt path, got %v", err)
	}
}

func TestLoadBucketStateMissingReturnsNil(t *testing.T) {
	root := t.TempDir()
	state, err := LoadBucketState(root, BucketKey{ProviderID: "codex-2", EndpointType: "responses", Model: "gpt-5.4"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != nil {
		t.Fatalf("expected nil state, got %#v", state)
	}
}

func TestRenderBucketStateIncludesCoreFields(t *testing.T) {
	state := BucketState{
		ProviderID:                "codex-2",
		EndpointType:              "responses",
		FinalUpstreamRawModel:     "gpt-5.4",
		SampleCount:               12,
		ConfidenceLevel:           "warming",
		RuntimeReady:              false,
		AvgBaseEstimate:           12345,
		AvgInputTokens:            23456,
		AvgCachedTokens:           20000,
		AvgUncachedInputTokens:    3456,
		RollingUncachedCorrection: 1.42,
	}
	text := RenderBucketState(state)
	for _, needle := range []string{"codex-2", "responses", "gpt-5.4", "sample_count", "suggested_uncached_correction"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("expected %q in render output: %s", needle, text)
		}
	}
}

func TestDeleteBucketDirectoryAllowsColdStartRebuild(t *testing.T) {
	root := t.TempDir()
	key := BucketKey{ProviderID: "codex-2", EndpointType: "responses", Model: "gpt-5.4"}
	state := &BucketState{SchemaVersion: 1, EstimatorVersion: 1, ProviderID: key.ProviderID, EndpointType: key.EndpointType, FinalUpstreamRawModel: key.Model, SafeModelName: SafeModelName(key.Model)}
	if err := SaveBucketState(root, key, state); err != nil {
		t.Fatalf("SaveBucketState error: %v", err)
	}
	jsonPath, _ := BucketPaths(root, key)
	providerDir := filepath.Dir(filepath.Dir(jsonPath))
	if err := os.RemoveAll(providerDir); err != nil {
		t.Fatalf("RemoveAll error: %v", err)
	}
	if err := SaveBucketState(root, key, state); err != nil {
		t.Fatalf("expected cold-start rebuild after delete, got %v", err)
	}
}
