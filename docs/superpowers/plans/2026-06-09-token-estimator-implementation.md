# Dynamic Token Estimator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build phase-1 dynamic token estimator infrastructure for `MODEL_LIMIT_CONTEXT_TOKENS`: a theory-driven base estimator, provider/endpoint/model bucket persistence under `providers/Token_Estimator`, upstream usage observation, suggested correction output, and reset-by-delete behavior through the existing admin UI file browser.

**Architecture:** Add a new `internal/tokenestimator` package that mirrors the proven `internal/cacheinfo` lifecycle and file I/O style, but stores estimator state per `provider_id + upstream_endpoint_type + final_upstream_raw_model`. Integrate it into startup and request lifecycles without changing runtime admission behavior yet: phase 1 observes, persists, and reports suggested corrections, while `context_limit.go` continues to use only the upgraded base estimator.

**Tech Stack:** Go, existing `cacheinfo`-style atomic JSON/TXT persistence, existing runtime store / admin UI / upstream usage pipeline, repository-native tests with `go test`.

---

## Planned File Structure

### New files

- `internal/tokenestimator/types.go`
  - 状态模型、样本模型、shape 分类、confidence/readiness 基础类型。
- `internal/tokenestimator/path.go`
  - `providers/Token_Estimator/...` 目录路径、safe model name、文件名映射。
- `internal/tokenestimator/io.go`
  - 原子 JSON/TXT 写入、读取、缺失恢复。
- `internal/tokenestimator/render.go`
  - 供管理台直接浏览的 TXT 文本摘要渲染。
- `internal/tokenestimator/manager.go`
  - bucket 生命周期、内存状态、样本更新、flush。
- `internal/tokenestimator/manager_test.go`
  - manager 行为测试。
- `internal/tokenestimator/io_test.go`
  - 路径、安全文件名、原子读写、删除重建测试。
- `internal/tokenestimator/render_test.go`
  - TXT 渲染测试。
- `internal/httpapi/context_limit_estimator.go`
  - 将当前 `context_limit.go` 的粗估逻辑拆成“可解释的基础估算器”。
- `internal/httpapi/context_limit_estimator_test.go`
  - 基础估算器分项测试。
- `docs/superpowers/plans/2026-06-09-token-estimator-implementation.md`
  - 当前计划文档。

### Modified files

- `cmd/proxy/main.go`
  - 启动和停止 token estimator manager。
- `internal/httpapi/routes.go`
  - 注入 / 读取 token estimator manager。
- `internal/httpapi/server.go`
  - server 构造时透传 manager。
- `internal/httpapi/context_limit.go`
  - 调用新的 base estimator，但仍不使用 learned correction 做 admission。
- `internal/httpapi/handlers_responses.go`
  - 在最终上游模型已确定后，准备 observation 所需上下文。
- `internal/httpapi/handlers_chat.go`
  - 同上。
- `internal/httpapi/handlers_anthropic.go`
  - 同上。
- `internal/httpapi/streaming.go`
  - 在拿到稳定 upstream usage 后调用 token estimator manager 记录样本。
- `internal/upstream/client.go`
  - 必要时补齐 observation 所需 final model / endpoint / usage 字段流转。
- `internal/httpapi/adminui_test.go`
  - 验证新目录在浏览和删除后可重建。

---

### Task 1: 定义 token estimator 状态模型与目录规则

**Files:**
- Create: `internal/tokenestimator/types.go`
- Create: `internal/tokenestimator/path.go`
- Test: `internal/tokenestimator/io_test.go`

- [ ] **Step 1: Write the failing path/state tests**

```go
package tokenestimator

import "testing"

func TestBucketKeyIncludesProviderEndpointAndModel(t *testing.T) {
	key := BucketKey{
		ProviderID:   "codex-2",
		EndpointType: "responses",
		Model:        "gpt-5.4",
	}
	if key.ProviderID != "codex-2" || key.EndpointType != "responses" || key.Model != "gpt-5.4" {
		t.Fatalf("unexpected bucket key: %#v", key)
	}
}

func TestSafeModelNameIsStableAndNonEmpty(t *testing.T) {
	safe := SafeModelName("gpt-5.4:reasoning/high")
	if safe == "" {
		t.Fatal("expected non-empty safe model name")
	}
	if safe == "gpt-5.4:reasoning/high" {
		t.Fatal("expected filesystem-safe transformation")
	}
}

func TestEstimatorPathsFollowProviderEndpointModelTree(t *testing.T) {
	jsonPath, txtPath := BucketPaths("/tmp/providers", BucketKey{
		ProviderID:   "codex-2",
		EndpointType: "responses",
		Model:        "gpt-5.4",
	})
	if want := "/tmp/providers/Token_Estimator/SYSTEM_JSON_FILES/codex-2/responses/"; len(jsonPath) <= len(want) || jsonPath[:len(want)] != want {
		t.Fatalf("unexpected json path: %s", jsonPath)
	}
	if want := "/tmp/providers/Token_Estimator/codex-2/responses/"; len(txtPath) <= len(want) || txtPath[:len(want)] != want {
		t.Fatalf("unexpected txt path: %s", txtPath)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -count=1 ./internal/tokenestimator -run 'TestBucketKeyIncludesProviderEndpointAndModel|TestSafeModelNameIsStableAndNonEmpty|TestEstimatorPathsFollowProviderEndpointModelTree'`

Expected: FAIL with missing package / missing symbols such as `BucketKey`, `SafeModelName`, `BucketPaths`.

- [ ] **Step 3: Write minimal type and path definitions**

```go
package tokenestimator

import (
	"crypto/sha1"
	"encoding/hex"
	"path/filepath"
	"strings"
	"time"
)

type BucketKey struct {
	ProviderID   string
	EndpointType string
	Model        string
}

type ShapeClass string

const (
	ShapePlain               ShapeClass = "plain"
	ShapeStructuredResponses ShapeClass = "structured_responses"
	ShapeReasoningHeavy      ShapeClass = "reasoning_heavy"
	ShapeToolHeavy           ShapeClass = "tool_heavy"
	ShapeMultimodal          ShapeClass = "multimodal"
)

type SampleSummary struct {
	RecordedAt           time.Time         `json:"recorded_at"`
	BaseEstimate         int64             `json:"base_estimate"`
	InputTokens          int64             `json:"input_tokens"`
	CachedTokens         int64             `json:"cached_tokens"`
	UncachedInputTokens  int64             `json:"uncached_input_tokens"`
	Shape                ShapeClass        `json:"shape"`
	FeatureCounts        map[string]int64  `json:"feature_counts,omitempty"`
	DiscardedAsOutlier   bool              `json:"discarded_as_outlier"`
	ProtocolSignature    string            `json:"protocol_signature"`
	EstimatorSignature   string            `json:"estimator_signature"`
}

type BucketState struct {
	SchemaVersion         int             `json:"schema_version"`
	EstimatorVersion      int             `json:"estimator_version"`
	ProviderID            string          `json:"provider_id"`
	EndpointType          string          `json:"endpoint_type"`
	FinalUpstreamRawModel string          `json:"final_upstream_raw_model"`
	SafeModelName         string          `json:"safe_model_name"`
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`
	SampleCount           int64           `json:"sample_count"`
	UsableSampleCount     int64           `json:"usable_sample_count"`
	DiscardedSampleCount  int64           `json:"discarded_sample_count"`
	RecentSamples         []SampleSummary `json:"recent_samples_summary,omitempty"`
	ConfidenceLevel       string          `json:"confidence_level"`
	RuntimeReady          bool            `json:"runtime_ready"`
	ProtocolSignature     string          `json:"last_protocol_signature"`
	EstimatorSignature    string          `json:"last_estimator_signature"`

	AvgInputTokens         float64 `json:"avg_input_tokens"`
	AvgCachedTokens        float64 `json:"avg_cached_tokens"`
	AvgUncachedInputTokens float64 `json:"avg_uncached_input_tokens"`
	AvgBaseEstimate        float64 `json:"avg_base_estimate"`
	AvgTotalRatio          float64 `json:"avg_total_ratio"`
	AvgUncachedRatio       float64 `json:"avg_uncached_ratio"`
	RollingTotalCorrection float64 `json:"rolling_total_correction"`
	RollingUncachedCorrection float64 `json:"rolling_uncached_correction"`
	MaxInputTokens         int64   `json:"max_input_tokens"`
	OutlierCount           int64   `json:"outlier_count"`

	AvgTextChars           float64 `json:"avg_text_chars"`
	AvgInputItemCount      float64 `json:"avg_input_item_count"`
	AvgReasoningItemCount  float64 `json:"avg_reasoning_item_count"`
	AvgToolCallCount       float64 `json:"avg_tool_call_count"`
	AvgToolResultCount     float64 `json:"avg_tool_result_count"`
	AvgMultimodalItemCount float64 `json:"avg_multimodal_item_count"`
}

func SafeModelName(model string) string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return "unknown-model"
	}
	h := sha1.Sum([]byte(trimmed))
	base := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		" ", "_",
		"\t", "_",
	).Replace(trimmed)
	return base + "__" + hex.EncodeToString(h[:4])
}

func BucketPaths(providersDir string, key BucketKey) (jsonPath string, txtPath string) {
	safe := SafeModelName(key.Model)
	jsonPath = filepath.Join(providersDir, "Token_Estimator", "SYSTEM_JSON_FILES", key.ProviderID, key.EndpointType, safe+".json")
	txtPath = filepath.Join(providersDir, "Token_Estimator", key.ProviderID, key.EndpointType, safe+".txt")
	return jsonPath, txtPath
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -count=1 ./internal/tokenestimator -run 'TestBucketKeyIncludesProviderEndpointAndModel|TestSafeModelNameIsStableAndNonEmpty|TestEstimatorPathsFollowProviderEndpointModelTree'`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
GIT_MASTER=1 git add internal/tokenestimator/types.go internal/tokenestimator/path.go internal/tokenestimator/io_test.go
GIT_MASTER=1 git commit -m "功能(tokenestimator): 定义分桶状态与路径模型"
```

### Task 2: 实现 Token_Estimator 的原子读写与文本渲染

**Files:**
- Create: `internal/tokenestimator/io.go`
- Create: `internal/tokenestimator/render.go`
- Test: `internal/tokenestimator/io_test.go`
- Test: `internal/tokenestimator/render_test.go`

- [ ] **Step 1: Write the failing I/O and render tests**

```go
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
		ProviderID:            "codex-2",
		EndpointType:          "responses",
		FinalUpstreamRawModel: "gpt-5.4",
		SampleCount:           12,
		ConfidenceLevel:       "warming",
		RuntimeReady:          false,
		AvgBaseEstimate:       12345,
		AvgInputTokens:        23456,
		AvgCachedTokens:       20000,
		AvgUncachedInputTokens: 3456,
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -count=1 ./internal/tokenestimator -run 'TestSaveAndLoadBucketState|TestLoadBucketStateMissingReturnsNil|TestRenderBucketStateIncludesCoreFields|TestDeleteBucketDirectoryAllowsColdStartRebuild'`

Expected: FAIL with missing `SaveBucketState`, `LoadBucketState`, `RenderBucketState`.

- [ ] **Step 3: Implement atomic JSON/TXT persistence and text renderer**

```go
package tokenestimator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func LoadBucketState(providersDir string, key BucketKey) (*BucketState, error) {
	jsonPath, _ := BucketPaths(providersDir, key)
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var state BucketState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse %s: %w", jsonPath, err)
	}
	return &state, nil
}

func SaveBucketState(providersDir string, key BucketKey, state *BucketState) error {
	jsonPath, txtPath := BucketPaths(providersDir, key)
	if err := os.MkdirAll(filepath.Dir(jsonPath), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(txtPath), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWrite(jsonPath, data); err != nil {
		return err
	}
	return atomicWrite(txtPath, []byte(RenderBucketState(*state)))
}

func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
	}

func RenderBucketState(state BucketState) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("provider_id: %s\n", state.ProviderID))
	b.WriteString(fmt.Sprintf("endpoint_type: %s\n", state.EndpointType))
	b.WriteString(fmt.Sprintf("final_upstream_raw_model: %s\n", state.FinalUpstreamRawModel))
	b.WriteString(fmt.Sprintf("sample_count: %d\n", state.SampleCount))
	b.WriteString(fmt.Sprintf("usable_sample_count: %d\n", state.UsableSampleCount))
	b.WriteString(fmt.Sprintf("confidence_level: %s\n", state.ConfidenceLevel))
	b.WriteString(fmt.Sprintf("runtime_ready: %t\n", state.RuntimeReady))
	b.WriteString(fmt.Sprintf("avg_base_estimate: %.2f\n", state.AvgBaseEstimate))
	b.WriteString(fmt.Sprintf("avg_input_tokens: %.2f\n", state.AvgInputTokens))
	b.WriteString(fmt.Sprintf("avg_cached_tokens: %.2f\n", state.AvgCachedTokens))
	b.WriteString(fmt.Sprintf("avg_uncached_input_tokens: %.2f\n", state.AvgUncachedInputTokens))
	b.WriteString(fmt.Sprintf("suggested_uncached_correction: %.4f\n", state.RollingUncachedCorrection))
	return b.String()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -count=1 ./internal/tokenestimator -run 'TestSaveAndLoadBucketState|TestLoadBucketStateMissingReturnsNil|TestRenderBucketStateIncludesCoreFields|TestDeleteBucketDirectoryAllowsColdStartRebuild'`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
GIT_MASTER=1 git add internal/tokenestimator/io.go internal/tokenestimator/render.go internal/tokenestimator/io_test.go internal/tokenestimator/render_test.go
GIT_MASTER=1 git commit -m "功能(tokenestimator): 增加状态读写与可视摘要"
```

### Task 3: 实现 manager 的生命周期、样本更新和稳健统计

**Files:**
- Create: `internal/tokenestimator/manager.go`
- Test: `internal/tokenestimator/manager_test.go`

- [ ] **Step 1: Write the failing manager tests**

```go
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
		Bucket: BucketKey{ProviderID: "codex-2", EndpointType: "responses", Model: "gpt-5.4"},
		BaseEstimate:        100000,
		InputTokens:         150000,
		CachedTokens:        120000,
		UncachedInputTokens: 30000,
		Shape:               ShapeStructuredResponses,
		FeatureCounts: map[string]int64{"text_chars": 80000, "reasoning_items": 10},
		ProtocolSignature:  "responses:v1",
		EstimatorSignature: "base-estimator:v1",
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -count=1 ./internal/tokenestimator -run 'TestManagerLoadsExistingBucketOnStartup|TestRecordObservationUpdatesRollingState|TestRecordObservationDropsInvalidUsage|TestManagerFlushPersistsBuckets'`

Expected: FAIL with missing `NewManager`, `Observation`, `RecordObservation`, `Flush`, `GetBucketState`.

- [ ] **Step 3: Implement the manager with bounded recent samples and conservative stats**

```go
package tokenestimator

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

const (
	defaultRecentSampleLimit = 64
	schemaVersion            = 1
	estimatorVersion         = 1
)

var ErrInvalidObservation = errors.New("invalid token estimator observation")

type Observation struct {
	Bucket              BucketKey
	BaseEstimate        int64
	InputTokens         int64
	CachedTokens        int64
	UncachedInputTokens int64
	Shape               ShapeClass
	FeatureCounts       map[string]int64
	ProtocolSignature   string
	EstimatorSignature  string
	RecordedAt          time.Time
}

type Manager struct {
	providersDir   string
	location       *time.Location
	enabledFn      func() []string
	mu             sync.RWMutex
	buckets        map[BucketKey]*BucketState
	seenRequests   map[string]struct{}
	recentLimit    int
}

func NewManager(providersDir string, location *time.Location, enabledFn func() []string) *Manager {
	if location == nil {
		location = time.UTC
	}
	m := &Manager{
		providersDir: providersDir,
		location:     location,
		enabledFn:    enabledFn,
		buckets:      map[BucketKey]*BucketState{},
		seenRequests: map[string]struct{}{},
		recentLimit:  defaultRecentSampleLimit,
	}
	_ = m.loadExistingBuckets()
	return m
}

func (m *Manager) loadExistingBuckets() error {
	providerIDs := m.enabledFn()
	for _, providerID := range providerIDs {
		for _, endpointType := range []string{"responses", "chat", "anthropic"} {
			root := filepath.Join(m.providersDir, "Token_Estimator", "SYSTEM_JSON_FILES", providerID, endpointType)
			entries, err := os.ReadDir(root)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return err
			}
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
					continue
				}
				data, err := os.ReadFile(filepath.Join(root, entry.Name()))
				if err != nil {
					return err
				}
				var state BucketState
				if err := json.Unmarshal(data, &state); err != nil {
					return err
				}
				key := BucketKey{ProviderID: state.ProviderID, EndpointType: state.EndpointType, Model: state.FinalUpstreamRawModel}
				m.buckets[key] = &state
			}
		}
	}
	return nil
}

func (m *Manager) GetBucketState(key BucketKey) *BucketState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	state := m.buckets[key]
	if state == nil {
		loaded, _ := LoadBucketState(m.providersDir, key)
		return loaded
	}
	clone := *state
	return &clone
}

func (m *Manager) RecordObservation(requestID string, obs Observation) error {
	if obs.BaseEstimate <= 0 || obs.InputTokens < 0 || obs.CachedTokens < 0 || obs.UncachedInputTokens < 0 || obs.InputTokens < obs.CachedTokens {
		return ErrInvalidObservation
	}
	if obs.RecordedAt.IsZero() {
		obs.RecordedAt = time.Now().In(m.location)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.seenRequests[requestID]; exists {
		return nil
	}
	state := m.ensureBucketLocked(obs)
	state.SampleCount++
	state.UsableSampleCount++
	state.UpdatedAt = obs.RecordedAt
	state.ProtocolSignature = obs.ProtocolSignature
	state.EstimatorSignature = obs.EstimatorSignature
	state.FinalUpstreamRawModel = obs.Bucket.Model
	state.ProviderID = obs.Bucket.ProviderID
	state.EndpointType = obs.Bucket.EndpointType
	state.SafeModelName = SafeModelName(obs.Bucket.Model)

	ratioTotal := float64(obs.InputTokens) / float64(obs.BaseEstimate)
	ratioUncached := float64(obs.UncachedInputTokens) / float64(obs.BaseEstimate)
	ratioTotal = clip(ratioTotal, 0.25, 8.0)
	ratioUncached = clip(ratioUncached, 0.05, 8.0)

	state.AvgBaseEstimate = rollingMean(state.AvgBaseEstimate, float64(obs.BaseEstimate), state.UsableSampleCount)
	state.AvgInputTokens = rollingMean(state.AvgInputTokens, float64(obs.InputTokens), state.UsableSampleCount)
	state.AvgCachedTokens = rollingMean(state.AvgCachedTokens, float64(obs.CachedTokens), state.UsableSampleCount)
	state.AvgUncachedInputTokens = rollingMean(state.AvgUncachedInputTokens, float64(obs.UncachedInputTokens), state.UsableSampleCount)
	state.AvgTotalRatio = rollingMean(state.AvgTotalRatio, ratioTotal, state.UsableSampleCount)
	state.AvgUncachedRatio = rollingMean(state.AvgUncachedRatio, ratioUncached, state.UsableSampleCount)
	state.RollingTotalCorrection = ewma(state.RollingTotalCorrection, ratioTotal)
	state.RollingUncachedCorrection = ewma(state.RollingUncachedCorrection, ratioUncached)
	state.MaxInputTokens = max64(state.MaxInputTokens, obs.InputTokens)
	state.AvgTextChars = rollingMean(state.AvgTextChars, float64(obs.FeatureCounts["text_chars"]), state.UsableSampleCount)
	state.AvgInputItemCount = rollingMean(state.AvgInputItemCount, float64(obs.FeatureCounts["input_item_count"]), state.UsableSampleCount)
	state.AvgReasoningItemCount = rollingMean(state.AvgReasoningItemCount, float64(obs.FeatureCounts["reasoning_item_count"]), state.UsableSampleCount)
	state.AvgToolCallCount = rollingMean(state.AvgToolCallCount, float64(obs.FeatureCounts["tool_call_count"]), state.UsableSampleCount)
	state.AvgToolResultCount = rollingMean(state.AvgToolResultCount, float64(obs.FeatureCounts["tool_result_count"]), state.UsableSampleCount)
	state.AvgMultimodalItemCount = rollingMean(state.AvgMultimodalItemCount, float64(obs.FeatureCounts["multimodal_item_count"]), state.UsableSampleCount)
	state.ConfidenceLevel = confidenceLabel(state.UsableSampleCount)
	state.RuntimeReady = false
	state.RecentSamples = append(state.RecentSamples, SampleSummary{
		RecordedAt:          obs.RecordedAt,
		BaseEstimate:        obs.BaseEstimate,
		InputTokens:         obs.InputTokens,
		CachedTokens:        obs.CachedTokens,
		UncachedInputTokens: obs.UncachedInputTokens,
		Shape:               obs.Shape,
		FeatureCounts:       obs.FeatureCounts,
		ProtocolSignature:   obs.ProtocolSignature,
		EstimatorSignature:  obs.EstimatorSignature,
	})
	if len(state.RecentSamples) > m.recentLimit {
		state.RecentSamples = state.RecentSamples[len(state.RecentSamples)-m.recentLimit:]
	}
	m.seenRequests[requestID] = struct{}{}
	return nil
}

func (m *Manager) ensureBucketLocked(obs Observation) *BucketState {
	if state := m.buckets[obs.Bucket]; state != nil {
		return state
	}
	loaded, _ := LoadBucketState(m.providersDir, obs.Bucket)
	if loaded != nil {
		m.buckets[obs.Bucket] = loaded
		return loaded
	}
	state := &BucketState{
		SchemaVersion:         schemaVersion,
		EstimatorVersion:      estimatorVersion,
		ProviderID:            obs.Bucket.ProviderID,
		EndpointType:          obs.Bucket.EndpointType,
		FinalUpstreamRawModel: obs.Bucket.Model,
		SafeModelName:         SafeModelName(obs.Bucket.Model),
		CreatedAt:             obs.RecordedAt,
		UpdatedAt:             obs.RecordedAt,
		ConfidenceLevel:       "cold",
		RuntimeReady:          false,
	}
	m.buckets[obs.Bucket] = state
	return state
}

func (m *Manager) Flush(ctx context.Context) error {
	m.mu.RLock()
	buckets := make(map[BucketKey]*BucketState, len(m.buckets))
	for k, v := range m.buckets {
		clone := *v
		buckets[k] = &clone
	}
	m.mu.RUnlock()
	for key, state := range buckets {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := SaveBucketState(m.providersDir, key, state); err != nil {
			return fmt.Errorf("flush %v: %w", key, err)
		}
	}
	return nil
}

func rollingMean(current float64, sample float64, count int64) float64 {
	if count <= 1 || current == 0 {
		return sample
	}
	return current + (sample-current)/float64(count)
}

func ewma(current float64, sample float64) float64 {
	if current == 0 {
		return sample
	}
	const alpha = 0.2
	return current*(1-alpha) + sample*alpha
}

func clip(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}

func max64(a, b int64) int64 {
	if b > a {
		return b
	}
	return a
}

func confidenceLabel(samples int64) string {
	switch {
	case samples >= 64:
		return "warm"
	case samples >= 16:
		return "warming"
	default:
		return "cold"
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -count=1 ./internal/tokenestimator -run 'TestManagerLoadsExistingBucketOnStartup|TestRecordObservationUpdatesRollingState|TestRecordObservationDropsInvalidUsage|TestManagerFlushPersistsBuckets'`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
GIT_MASTER=1 git add internal/tokenestimator/manager.go internal/tokenestimator/manager_test.go
GIT_MASTER=1 git commit -m "功能(tokenestimator): 增加分桶状态管理器"
```

### Task 4: 提炼基础估算器并覆盖 Responses/Reasoning/Tool 特征采样

**Files:**
- Create: `internal/httpapi/context_limit_estimator.go`
- Modify: `internal/httpapi/context_limit.go`
- Test: `internal/httpapi/context_limit_estimator_test.go`
- Test: `internal/httpapi/context_limit_test.go`

- [ ] **Step 1: Write the failing estimator tests**

```go
package httpapi

import (
	"testing"

	modelpkg "openai-compat-proxy/internal/model"
)

func TestBuildEstimatorSnapshotCountsResponsesReasoningAndToolShape(t *testing.T) {
	canon := modelpkg.CanonicalRequest{
		Model:        "gpt-5.4",
		Instructions: "follow system",
		ResponseInputItems: []map[string]any{{"type": "reasoning", "summary": []map[string]any{{"text": "trace"}}}},
		Messages: []modelpkg.CanonicalMessage{{
			Role: "assistant",
			ReasoningBlocks: []map[string]any{{"type": "reasoning", "encrypted_content": "enc_123"}},
			ToolCalls: []modelpkg.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "search_web", Arguments: `{"q":"hello"}`}},
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -count=1 ./internal/httpapi -run 'TestBuildEstimatorSnapshotCountsResponsesReasoningAndToolShape|TestEstimateCanonicalInputTokensStillUsesBaseEstimatorOnly'`

Expected: FAIL with missing `buildEstimatorSnapshot` or missing snapshot fields.

- [ ] **Step 3: Implement the base estimator snapshot and wire context limit to it**

```go
package httpapi

import (
	"encoding/json"
	"unicode/utf8"

	modelpkg "openai-compat-proxy/internal/model"
)

type estimatorSnapshot struct {
	TextChars           int64
	InputItemCount      int64
	ReasoningItemCount  int64
	ToolCallCount       int64
	ToolResultCount     int64
	MultimodalItemCount int64
	BaseEstimate        int64
}

func buildEstimatorSnapshot(canon modelpkg.CanonicalRequest) estimatorSnapshot {
	var snap estimatorSnapshot
	snap.TextChars += int64(utf8.RuneCountInString(canon.Model))
	snap.TextChars += int64(utf8.RuneCountInString(canon.Instructions))
	snap.InputItemCount = int64(len(canon.ResponseInputItems))
	for _, item := range canon.ResponseInputItems {
		if kind, _ := item["type"].(string); kind == "reasoning" {
			snap.ReasoningItemCount++
		}
	}
	for _, part := range canon.InstructionParts {
		snap.TextChars += int64(estimateContentPartChars(part))
		if isMultimodalPart(part) {
			snap.MultimodalItemCount++
		}
	}
	for _, msg := range canon.Messages {
		snap.TextChars += int64(utf8.RuneCountInString(msg.Role) + utf8.RuneCountInString(msg.ToolCallID) + utf8.RuneCountInString(msg.ReasoningContent))
		snap.ReasoningItemCount += int64(len(msg.ReasoningBlocks))
		if len(msg.OrderedContent) > 0 {
			for _, block := range msg.OrderedContent {
				snap.TextChars += int64(estimateContentPartChars(block.Part))
				if block.Type == "tool_use" {
					snap.ToolCallCount++
				}
				if block.Type == "tool_result" {
					snap.ToolResultCount++
				}
				if isMultimodalPart(block.Part) {
					snap.MultimodalItemCount++
				}
			}
			continue
		}
		for _, part := range msg.Parts {
			snap.TextChars += int64(estimateContentPartChars(part))
			if isMultimodalPart(part) {
				snap.MultimodalItemCount++
			}
		}
		snap.ToolCallCount += int64(len(msg.ToolCalls))
	}
	for _, tool := range canon.Tools {
		snap.ToolCallCount++
		snap.TextChars += int64(utf8.RuneCountInString(tool.Name) + utf8.RuneCountInString(tool.Description))
		if encoded, err := json.Marshal(tool.Parameters); err == nil {
			snap.TextChars += int64(utf8.RuneCount(encoded))
		}
	}
	if snap.TextChars > 0 {
		snap.BaseEstimate = (snap.TextChars + 3) / 4
	}
	return snap
}

func isMultimodalPart(part modelpkg.CanonicalContentPart) bool {
	return part.ImageURL != "" || part.MimeType != "" || part.Type == "input_file" || part.Type == "input_audio"
}
```

And update `internal/httpapi/context_limit.go` to:

```go
func estimateCanonicalInputTokens(canon modelpkg.CanonicalRequest) int {
	snap := buildEstimatorSnapshot(canon)
	if snap.BaseEstimate <= 0 {
		return 0
	}
	return int(snap.BaseEstimate)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -count=1 ./internal/httpapi -run 'TestBuildEstimatorSnapshotCountsResponsesReasoningAndToolShape|TestEstimateCanonicalInputTokensStillUsesBaseEstimatorOnly|TestEstimateCanonicalInputTokensDoesNotDoubleCountProjectedResponsesItems|TestEstimateCanonicalInputTokensDoesNotDoubleCountAnthropicOrderedContent'`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
GIT_MASTER=1 git add internal/httpapi/context_limit_estimator.go internal/httpapi/context_limit.go internal/httpapi/context_limit_estimator_test.go internal/httpapi/context_limit_test.go
GIT_MASTER=1 git commit -m "修复(httpapi): 提炼结构化基础估算器"
```

### Task 5: 将 token estimator manager 接入启动和请求上下文

**Files:**
- Modify: `cmd/proxy/main.go`
- Modify: `internal/httpapi/routes.go`
- Modify: `internal/httpapi/server.go`
- Test: `internal/httpapi/adminui_test.go`

- [ ] **Step 1: Write the failing integration tests**

```go
package httpapi

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"openai-compat-proxy/internal/tokenestimator"
)

func TestWithTokenEstimatorManagerRoundTrip(t *testing.T) {
	mgr := tokenestimator.NewManager(t.TempDir(), time.UTC, func() []string { return []string{"openai"} })
	ctx := withTokenEstimatorManager(context.Background(), mgr)
	req := newTestRequestWithContext(ctx)
	if got := tokenEstimatorManagerFromRequest(req); got == nil {
		t.Fatal("expected token estimator manager from request")
	}
}

func TestAdminUIAllowsDeletingTokenEstimatorFiles(t *testing.T) {
	root := t.TempDir()
	providersDir := filepath.Join(root, "providers")
	key := tokenestimator.BucketKey{ProviderID: "openai", EndpointType: "responses", Model: "gpt-5.4"}
	state := &tokenestimator.BucketState{SchemaVersion: 1, EstimatorVersion: 1, ProviderID: key.ProviderID, EndpointType: key.EndpointType, FinalUpstreamRawModel: key.Model, SafeModelName: tokenestimator.SafeModelName(key.Model)}
	if err := tokenestimator.SaveBucketState(providersDir, key, state); err != nil {
		t.Fatalf("SaveBucketState error: %v", err)
	}
	ui := newAdminUITestHarness(t, providersDir)
	jsonPath, _ := tokenestimator.BucketPaths(providersDir, key)
	adminPath := "/providers/Token_Estimator/SYSTEM_JSON_FILES/openai/responses/" + filepath.Base(jsonPath)
	if err := ui.deleteAdminFile(adminPath); err != nil {
		t.Fatalf("deleteAdminFile error: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -count=1 ./internal/httpapi -run 'TestWithTokenEstimatorManagerRoundTrip|TestAdminUIAllowsDeletingTokenEstimatorFiles'`

Expected: FAIL with missing `withTokenEstimatorManager` or manager wiring.

- [ ] **Step 3: Wire the manager through startup and request context**

Add in `internal/httpapi/routes.go` next to cache info context helpers:

```go
const tokenEstimatorManagerKey routeContextKey = "token-estimator-manager"

func withTokenEstimatorManager(ctx context.Context, manager *tokenestimator.Manager) context.Context {
	if manager == nil {
		return ctx
	}
	return context.WithValue(ctx, tokenEstimatorManagerKey, manager)
}

func tokenEstimatorManagerFromRequest(r *http.Request) *tokenestimator.Manager {
	manager, _ := r.Context().Value(tokenEstimatorManagerKey).(*tokenestimator.Manager)
	return manager
}
```

Update server construction in `internal/httpapi/server.go` to store a `TokenEstimatorManager *tokenestimator.Manager` and inject it alongside cache info manager when building per-request context.

Update `cmd/proxy/main.go` to initialize the manager similarly to `cacheinfo.NewManager`:

```go
var estimatorMgr *tokenestimator.Manager
if cfg.ProvidersDir != "" {
	estimatorMgr = tokenestimator.NewManager(cfg.ProvidersDir, location, func() []string {
		snapshot := store.Active()
		if snapshot == nil {
			return nil
		}
		ids := make([]string, 0, len(snapshot.Config.Providers))
		for _, provider := range snapshot.Config.Providers {
			if provider.Enabled {
				ids = append(ids, provider.ID)
			}
		}
		return ids
	})
	defer func() {
		_ = estimatorMgr.Flush(context.Background())
	}()
}

if err := http.ListenAndServe(cfg.ListenAddr, httpapi.NewServerWithStore(store, cacheMgr, estimatorMgr)); err != nil {
	log.Fatal(err)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -count=1 ./internal/httpapi -run 'TestWithTokenEstimatorManagerRoundTrip|TestAdminUIAllowsDeletingTokenEstimatorFiles'`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
GIT_MASTER=1 git add cmd/proxy/main.go internal/httpapi/routes.go internal/httpapi/server.go internal/httpapi/adminui_test.go
GIT_MASTER=1 git commit -m "功能(tokenestimator): 接入启动与请求上下文"
```

### Task 6: 在成功请求终态采集 usage 并记录 observation

**Files:**
- Modify: `internal/httpapi/handlers_responses.go`
- Modify: `internal/httpapi/handlers_chat.go`
- Modify: `internal/httpapi/handlers_anthropic.go`
- Modify: `internal/httpapi/streaming.go`
- Modify: `internal/upstream/client.go`
- Test: `internal/httpapi/context_limit_estimator_test.go`
- Test: `internal/httpapi/observability_headers_test.go`

- [ ] **Step 1: Write the failing observation tests**

```go
package httpapi

import (
	"context"
	"testing"
	"time"

	"openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/tokenestimator"
)

func TestBuildObservationUsesFinalUpstreamModelAndUsageSplit(t *testing.T) {
	canon := model.CanonicalRequest{Model: "gpt-5.4", ResponseInputItems: []map[string]any{{"type": "reasoning"}}, Messages: []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}}}}}
	obs := buildTokenEstimatorObservation(tokenEstimatorObservationInput{
		ProviderID:        "codex-2",
		EndpointType:      "responses",
		FinalUpstreamModel:"gpt-5.4",
		BaseEstimate:      int64(123),
		Canon:             canon,
		Usage:             usageTotals{InputTokens: 400, CachedTokens: 300},
		Now:               time.Unix(1, 0).UTC(),
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
		Canon:              model.CanonicalRequest{Model: "gpt-5.4", Messages: []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}}}}},
	})
	if err := recordTokenEstimatorUsage(ctx, "req-1", usageTotals{InputTokens: 240, CachedTokens: 120}); err != nil {
		t.Fatalf("recordTokenEstimatorUsage error: %v", err)
	}
	state := mgr.GetBucketState(tokenestimator.BucketKey{ProviderID: "codex-2", EndpointType: "responses", Model: "gpt-5.4"})
	if state == nil || state.SampleCount != 1 {
		t.Fatalf("expected recorded state, got %#v", state)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -count=1 ./internal/httpapi -run 'TestBuildObservationUsesFinalUpstreamModelAndUsageSplit|TestRecordObservationAfterSuccessfulUsage'`

Expected: FAIL with missing observation helpers.

- [ ] **Step 3: Implement observation building and terminal usage recording**

Add a new helper in `internal/httpapi/context_limit_estimator.go` or adjacent file:

```go
type usageTotals struct {
	InputTokens  int64
	CachedTokens int64
}

type tokenEstimatorObservationInput struct {
	ProviderID         string
	EndpointType       string
	FinalUpstreamModel string
	BaseEstimate       int64
	Canon              model.CanonicalRequest
	Now                time.Time
	Usage              usageTotals
}

type tokenEstimatorObservationContextKey string

const tokenEstimatorObservationKey tokenEstimatorObservationContextKey = "token-estimator-observation"

func withTokenEstimatorObservation(ctx context.Context, input tokenEstimatorObservationInput) context.Context {
	return context.WithValue(ctx, tokenEstimatorObservationKey, input)
}

func buildTokenEstimatorObservation(input tokenEstimatorObservationInput) tokenestimator.Observation {
	snap := buildEstimatorSnapshot(input.Canon)
	uncached := input.Usage.InputTokens - input.Usage.CachedTokens
	if uncached < 0 {
		uncached = 0
	}
	return tokenestimator.Observation{
		Bucket: tokenestimator.BucketKey{
			ProviderID:   input.ProviderID,
			EndpointType: input.EndpointType,
			Model:        input.FinalUpstreamModel,
		},
		BaseEstimate:        input.BaseEstimate,
		InputTokens:         input.Usage.InputTokens,
		CachedTokens:        input.Usage.CachedTokens,
		UncachedInputTokens: uncached,
		Shape:               classifyEstimatorShape(snap),
		FeatureCounts: map[string]int64{
			"text_chars":            snap.TextChars,
			"input_item_count":      snap.InputItemCount,
			"reasoning_item_count":  snap.ReasoningItemCount,
			"tool_call_count":       snap.ToolCallCount,
			"tool_result_count":     snap.ToolResultCount,
			"multimodal_item_count": snap.MultimodalItemCount,
		},
		ProtocolSignature:  input.EndpointType + ":v1",
		EstimatorSignature: "base-estimator:v1",
		RecordedAt:         input.Now,
	}
}
```

Then call `withTokenEstimatorObservation(...)` in each handler after final upstream model is fixed and after base estimate is available, but before upstream execution.

Add `recordTokenEstimatorUsage(ctx, requestID, usageTotals)` in `streaming.go` or helper file. It should:

```go
func recordTokenEstimatorUsage(ctx context.Context, requestID string, usage usageTotals) error {
	mgr, _ := ctx.Value(tokenEstimatorManagerKey).(*tokenestimator.Manager)
	if mgr == nil {
		return nil
	}
	input, _ := ctx.Value(tokenEstimatorObservationKey).(tokenEstimatorObservationInput)
	if input.ProviderID == "" || input.FinalUpstreamModel == "" || input.BaseEstimate <= 0 {
		return nil
	}
	input.Usage = usage
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	}
	return mgr.RecordObservation(requestID, buildTokenEstimatorObservation(input))
}
```

Update the terminal usage handling path in `streaming.go` to call this right after stable usage totals are finalized, reusing the same point that currently records cache info.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -count=1 ./internal/httpapi -run 'TestBuildObservationUsesFinalUpstreamModelAndUsageSplit|TestRecordObservationAfterSuccessfulUsage'`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
GIT_MASTER=1 git add internal/httpapi/handlers_responses.go internal/httpapi/handlers_chat.go internal/httpapi/handlers_anthropic.go internal/httpapi/streaming.go internal/upstream/client.go internal/httpapi/context_limit_estimator.go internal/httpapi/context_limit_estimator_test.go internal/httpapi/observability_headers_test.go
GIT_MASTER=1 git commit -m "功能(tokenestimator): 记录上游 usage 观测样本"
```

### Task 7: 暴露 suggested correction 与 readiness，可通过管理台文件删除重置

**Files:**
- Modify: `internal/tokenestimator/render.go`
- Modify: `internal/httpapi/adminui_test.go`
- Test: `internal/tokenestimator/render_test.go`
- Test: `internal/tokenestimator/io_test.go`

- [ ] **Step 1: Write the failing visibility/reset tests**

```go
package tokenestimator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderBucketStateShowsSuggestedCorrectionAndRuntimeReadyFalse(t *testing.T) {
	text := RenderBucketState(BucketState{
		ProviderID:                 "codex-2",
		EndpointType:               "responses",
		FinalUpstreamRawModel:      "gpt-5.4",
		SampleCount:                32,
		UsableSampleCount:          30,
		ConfidenceLevel:            "warming",
		RuntimeReady:               false,
		RollingUncachedCorrection:  1.37,
	})
	for _, needle := range []string{"suggested_uncached_correction: 1.3700", "runtime_ready: false", "confidence_level: warming"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("expected %q in render output: %s", needle, text)
		}
	}
}

func TestDeletingJsonFileAllowsSingleBucketReset(t *testing.T) {
	root := t.TempDir()
	key := BucketKey{ProviderID: "codex-2", EndpointType: "responses", Model: "gpt-5.4"}
	state := &BucketState{SchemaVersion: 1, EstimatorVersion: 1, ProviderID: key.ProviderID, EndpointType: key.EndpointType, FinalUpstreamRawModel: key.Model, SafeModelName: SafeModelName(key.Model)}
	if err := SaveBucketState(root, key, state); err != nil {
		t.Fatalf("SaveBucketState error: %v", err)
	}
	jsonPath, _ := BucketPaths(root, key)
	if err := os.Remove(jsonPath); err != nil {
		t.Fatalf("Remove json error: %v", err)
	}
	if state, err := LoadBucketState(root, key); err != nil || state != nil {
		t.Fatalf("expected nil state after delete, got %#v err=%v", state, err)
	}
	rebuilt := &BucketState{SchemaVersion: 1, EstimatorVersion: 1, ProviderID: key.ProviderID, EndpointType: key.EndpointType, FinalUpstreamRawModel: key.Model, SafeModelName: SafeModelName(key.Model)}
	if err := SaveBucketState(root, key, rebuilt); err != nil {
		t.Fatalf("expected save to recreate deleted bucket, got %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -count=1 ./internal/tokenestimator -run 'TestRenderBucketStateShowsSuggestedCorrectionAndRuntimeReadyFalse|TestDeletingJsonFileAllowsSingleBucketReset'`

Expected: FAIL because render output lacks final visibility semantics or reset behavior is incomplete.

- [ ] **Step 3: Finalize visibility semantics for phase 1**

Ensure `RenderBucketState` prints at least:

```go
b.WriteString(fmt.Sprintf("confidence_level: %s\n", state.ConfidenceLevel))
b.WriteString(fmt.Sprintf("runtime_ready: %t\n", state.RuntimeReady))
b.WriteString(fmt.Sprintf("suggested_total_correction: %.4f\n", state.RollingTotalCorrection))
b.WriteString(fmt.Sprintf("suggested_uncached_correction: %.4f\n", state.RollingUncachedCorrection))
b.WriteString(fmt.Sprintf("sample_count: %d\n", state.SampleCount))
b.WriteString(fmt.Sprintf("usable_sample_count: %d\n", state.UsableSampleCount))
```

And ensure loading after file deletion returns `nil`, while the next `SaveBucketState` recreates the path tree automatically.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -count=1 ./internal/tokenestimator -run 'TestRenderBucketStateShowsSuggestedCorrectionAndRuntimeReadyFalse|TestDeletingJsonFileAllowsSingleBucketReset' && go test -count=1 ./internal/httpapi -run 'TestAdminUIAllowsDeletingTokenEstimatorFiles'`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
GIT_MASTER=1 git add internal/tokenestimator/render.go internal/tokenestimator/render_test.go internal/tokenestimator/io_test.go internal/httpapi/adminui_test.go
GIT_MASTER=1 git commit -m "文档(tokenestimator): 输出建议修正与重置语义"
```

### Task 8: 完整回归、文档同步与老规矩发布准备

**Files:**
- Modify: `README.md`
- Modify: `.env.example`
- Modify: `providers/openai.env.example`
- Test: existing suites only

- [ ] **Step 1: Write the failing documentation checklist as assertions in your task log**

```text
Need README/.env.example/providers/openai.env.example to explain:
- Token_Estimator phase-1 is observation only
- It is stored under providers/Token_Estimator
- Deleting bucket files resets learned history
- Runtime correction is not active in phase 1
```

- [ ] **Step 2: Update documentation minimally**

Add to `README.md` a short section near `MODEL_LIMIT_CONTEXT_TOKENS` and `Cache_Info` explaining:

```markdown
### Token_Estimator（阶段 1）

- 代理会按 `provider + 上游协议类型 + 最终上游模型` 在 `PROVIDERS_DIR/Token_Estimator/` 下记录本地估算与上游 usage 的观测状态。
- 当前阶段只生成建议修正值和 readiness 状态，不直接参与 `MODEL_LIMIT_CONTEXT_TOKENS` 的拦截判断。
- 删除对应 provider / model 文件后，该桶会重新冷启动学习。
```

Add concise comments to `.env.example` / `providers/openai.env.example` only if new config switches are introduced by this phase. If phase 1 introduces no new user-facing config, do **not** invent config just to document something.

- [ ] **Step 3: Run targeted verification**

Run:

```bash
go test -count=1 ./internal/tokenestimator ./internal/httpapi ./internal/upstream ./internal/config
go test -count=1 ./...
go build -o bin/openai-compat-proxy ./cmd/proxy
```

Expected: all PASS / build exits 0.

- [ ] **Step 4: Commit**

```bash
GIT_MASTER=1 git add README.md .env.example providers/openai.env.example
GIT_MASTER=1 git commit -m "文档(tokenestimator): 说明阶段一观测模式"
```

- [ ] **Step 5: Final release checklist for later execution**

```text
- Verify Token_Estimator files are visible in admin UI
- Verify deleting a model bucket file rebuilds on next successful request
- Verify no hot-reload loop is triggered by estimator writes
- Verify suggested correction is persisted but runtime admission remains unchanged
- After implementation is complete, follow the project's normal mypush/release/deploy/health-check flow
```

## Self-Review

- **Spec coverage:**
  - 分桶键（provider/endpoint/model）→ Tasks 1, 3, 6
  - `providers/Token_Estimator` 目录与 UI 删除重置 → Tasks 2, 5, 7
  - 理论驱动基础估算器 → Task 4
  - total/cached/uncached 分离 → Tasks 3, 6
  - phase-1 只观察不拦截 → Tasks 4, 7, 8
  - 老规矩交付 → Task 8
- **Placeholder scan:** No `TBD` / `TODO` placeholders remain. Every code-touching step includes explicit code or explicit update content.
- **Type consistency:** `BucketKey`, `BucketState`, `Observation`, `usageTotals`, `buildEstimatorSnapshot`, and `recordTokenEstimatorUsage` are introduced in earlier tasks before later tasks depend on them.

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-06-09-token-estimator-implementation.md`. Two execution options:

**1. Subagent-Driven (recommended)** - I dispatch a fresh subagent per task, review between tasks, fast iteration

**2. Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints

**Which approach?**
