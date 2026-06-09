package tokenestimator

import (
	"fmt"
	"strings"
)

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
