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
	for _, needle := range []string{"codex-2", "responses", "gpt-5.4", "样本总数", "当前未缓存修正系数"} {
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


func TestRenderBucketStateUsesChineseLabels(t *testing.T) {
	text := RenderBucketState(BucketState{ProviderID: "codex", EndpointType: "responses", FinalUpstreamRawModel: "gpt-5.4", SampleCount: 3, AvgInputTokens: 120, AvgCachedTokens: 20, AvgUncachedInputTokens: 100, RuntimeReady: true})
	for _, needle := range []string{"提供商", "上游端点类型", "最终上游模型", "样本总数", "当前未缓存修正系数"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("expected Chinese label %q in %s", needle, text)
		}
	}
}

func TestRenderBucketStateExplainsLearnedFeaturesAndFormula(t *testing.T) {
	text := RenderBucketState(BucketState{
		ProviderID:                "codex",
		EndpointType:              "responses",
		FinalUpstreamRawModel:     "gpt-5.4",
		SampleCount:               123,
		UsableSampleCount:         123,
		ConfidenceLevel:           "warm",
		RuntimeReady:              true,
		AvgInputTokens:            218672,
		AvgCachedTokens:           216182,
		AvgUncachedInputTokens:    2490,
		AvgBaseEstimate:           173113,
		AvgTextChars:              692452,
		AvgInputItemCount:         64,
		AvgReasoningItemCount:     12,
		AvgToolCallCount:          6,
		AvgToolResultCount:        6,
		RollingTotalCorrection:    1.31,
		RollingUncachedCorrection: 0.29,
	})
	for _, needle := range []string{"学到的请求特征", "当前估算规则", "可信度判断", "文本字符", "输入项数量", "推理项数量", "工具调用数量", "平均总修正系数", "字符数 ÷ 4", "真实输入 token ÷ 基础估算 token", "总输入估算公式", "未缓存输入估算公式", "缓存命中估算公式", "混合估算方案"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("expected user-readable learning summary %q in %s", needle, text)
		}
	}
}
