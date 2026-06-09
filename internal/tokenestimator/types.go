package tokenestimator

import "time"

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
	RecordedAt          time.Time        `json:"recorded_at"`
	BaseEstimate        int64            `json:"base_estimate"`
	InputTokens         int64            `json:"input_tokens"`
	CachedTokens        int64            `json:"cached_tokens"`
	UncachedInputTokens int64            `json:"uncached_input_tokens"`
	Shape               ShapeClass       `json:"shape"`
	FeatureCounts       map[string]int64 `json:"feature_counts,omitempty"`
	DiscardedAsOutlier  bool             `json:"discarded_as_outlier"`
	ProtocolSignature   string           `json:"protocol_signature"`
	EstimatorSignature  string           `json:"estimator_signature"`
}

type BucketState struct {
	SchemaVersion             int             `json:"schema_version"`
	EstimatorVersion          int             `json:"estimator_version"`
	ProviderID                string          `json:"provider_id"`
	EndpointType              string          `json:"endpoint_type"`
	FinalUpstreamRawModel     string          `json:"final_upstream_raw_model"`
	SafeModelName             string          `json:"safe_model_name"`
	CreatedAt                 time.Time       `json:"created_at"`
	UpdatedAt                 time.Time       `json:"updated_at"`
	SampleCount               int64           `json:"sample_count"`
	UsableSampleCount         int64           `json:"usable_sample_count"`
	DiscardedSampleCount      int64           `json:"discarded_sample_count"`
	RecentSamples             []SampleSummary `json:"recent_samples_summary,omitempty"`
	ConfidenceLevel           string          `json:"confidence_level"`
	RuntimeReady              bool            `json:"runtime_ready"`
	ProtocolSignature         string          `json:"last_protocol_signature"`
	EstimatorSignature        string          `json:"last_estimator_signature"`
	AvgInputTokens            float64         `json:"avg_input_tokens"`
	AvgCachedTokens           float64         `json:"avg_cached_tokens"`
	AvgUncachedInputTokens    float64         `json:"avg_uncached_input_tokens"`
	AvgBaseEstimate           float64         `json:"avg_base_estimate"`
	AvgTotalRatio             float64         `json:"avg_total_ratio"`
	AvgUncachedRatio          float64         `json:"avg_uncached_ratio"`
	RollingTotalCorrection    float64         `json:"rolling_total_correction"`
	RollingUncachedCorrection float64         `json:"rolling_uncached_correction"`
	MaxInputTokens            int64           `json:"max_input_tokens"`
	OutlierCount              int64           `json:"outlier_count"`
	AvgTextChars              float64         `json:"avg_text_chars"`
	AvgInputItemCount         float64         `json:"avg_input_item_count"`
	AvgReasoningItemCount     float64         `json:"avg_reasoning_item_count"`
	AvgToolCallCount          float64         `json:"avg_tool_call_count"`
	AvgToolResultCount        float64         `json:"avg_tool_result_count"`
	AvgMultimodalItemCount    float64         `json:"avg_multimodal_item_count"`
}
