package tokenestimator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultRecentSampleLimit = 64
	defaultSeenRequestLimit  = 64
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
	providersDir     string
	location         *time.Location
	enabledFn        func() []string
	mu               sync.RWMutex
	buckets          map[BucketKey]*BucketState
	seenRequests     map[string]struct{}
	seenRequestOrder []string
	recentLimit      int
	seenRequestLimit int
}

func NewManager(providersDir string, location *time.Location, enabledFn func() []string) *Manager {
	if location == nil {
		location = time.UTC
	}
	if enabledFn == nil {
		enabledFn = func() []string { return nil }
	}
	m := &Manager{
		providersDir:     providersDir,
		location:         location,
		enabledFn:        enabledFn,
		buckets:          map[BucketKey]*BucketState{},
		seenRequests:     map[string]struct{}{},
		recentLimit:      defaultRecentSampleLimit,
		seenRequestLimit: defaultSeenRequestLimit,
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

func (m *Manager) ConservativeAdmissionLimit(key BucketKey, configuredLimit int, currentShape ShapeClass) (int, bool) {
	if configuredLimit <= 0 {
		return 0, false
	}
	state := m.GetBucketState(key)
	if state == nil {
		return configuredLimit, true
	}
	samples := matchingRecentSamples(state, currentShape)
	best := configuredLimit
	if observed := conservativeObservedOverflowEstimateLimit(samples, configuredLimit); observed > 0 && observed < best {
		best = observed
	}
	if learned := conservativeLearnedEstimateLimit(samples, configuredLimit); learned > 0 && learned < best {
		best = learned
	}
	if best < 1 {
		best = 1
	}
	return best, true
}

func (m *Manager) CorrectedEstimate(key BucketKey, baseEstimate int, currentShape ShapeClass) (int, bool) {
	if baseEstimate <= 0 {
		return 0, false
	}
	state := m.GetBucketState(key)
	if state == nil {
		return baseEstimate, false
	}
	samples := matchingRecentSamples(state, currentShape)
	correction := correctedEstimateRatio(samples)
	if correction <= 0 {
		return baseEstimate, false
	}
	corrected := int(math.Round(float64(baseEstimate) * correction))
	if corrected < 1 {
		corrected = 1
	}
	return corrected, true
}

func matchingRecentSamples(state *BucketState, currentShape ShapeClass) []SampleSummary {
	if state == nil || len(state.RecentSamples) == 0 {
		return nil
	}
	if currentShape == "" {
		return append([]SampleSummary(nil), state.RecentSamples...)
	}
	matched := make([]SampleSummary, 0, len(state.RecentSamples))
	for _, sample := range state.RecentSamples {
		if sample.Shape == currentShape {
			matched = append(matched, sample)
		}
	}
	if len(matched) == 0 {
		return nil
	}
	return matched
}

func conservativeObservedOverflowEstimateLimit(samples []SampleSummary, configuredLimit int) int {
	if configuredLimit <= 0 || len(samples) == 0 {
		return 0
	}
	var overflowBaseEstimates []int64
	for _, sample := range samples {
		if sample.InputTokens <= int64(configuredLimit) || sample.BaseEstimate <= 0 {
			continue
		}
		overflowBaseEstimates = append(overflowBaseEstimates, sample.BaseEstimate)
	}
	if len(overflowBaseEstimates) < 3 {
		return 0
	}
	best := overflowBaseEstimates[0]
	for _, sampleBaseEstimate := range overflowBaseEstimates[1:] {
		if sampleBaseEstimate < best {
			best = sampleBaseEstimate
		}
	}
	const safetyFactor = 0.95
	guard := int(math.Floor(float64(best) * safetyFactor))
	if guard <= 0 {
		return 0
	}
	return guard
}

func conservativeLearnedEstimateLimit(samples []SampleSummary, configuredLimit int) int {
	if configuredLimit <= 0 || len(samples) == 0 {
		return 0
	}
	ratio := correctedEstimateRatio(samples)
	if ratio <= 0 {
		return 0
	}
	if ratio <= 1 {
		return 0
	}
	const safetyFactor = 0.95
	return int(math.Floor(float64(configuredLimit) / ratio * safetyFactor))
}

func correctedEstimateRatio(samples []SampleSummary) float64 {
	if len(samples) == 0 {
		return 0
	}
	var correction float64
	for _, sample := range samples {
		if sample.BaseEstimate <= 0 || sample.InputTokens <= 0 {
			continue
		}
		ratio := clip(float64(sample.InputTokens)/float64(sample.BaseEstimate), 0.25, 8.0)
		correction = ewma(correction, ratio)
	}
	return correction
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
	state.RuntimeReady = state.UsableSampleCount >= 16 && state.RollingUncachedCorrection > 0
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
	m.recordSeenRequestLocked(requestID)
	clone := *state
	return SaveBucketState(m.providersDir, obs.Bucket, &clone)
}

func (m *Manager) recordSeenRequestLocked(requestID string) {
	m.seenRequests[requestID] = struct{}{}
	m.seenRequestOrder = append(m.seenRequestOrder, requestID)
	if len(m.seenRequestOrder) <= m.seenRequestLimit {
		return
	}
	oldestRequestID := m.seenRequestOrder[0]
	delete(m.seenRequests, oldestRequestID)
	m.seenRequestOrder = m.seenRequestOrder[1:]
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
