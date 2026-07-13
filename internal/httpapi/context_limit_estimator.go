package httpapi

import (
	"context"
	"encoding/json"
	"time"
	"unicode/utf8"

	modelpkg "openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/tokenestimator"
)

type usageTotals struct {
	InputTokens  int64
	CachedTokens int64
}

type tokenEstimatorObservationInput struct {
	ProviderID         string
	EndpointType       string
	FinalUpstreamModel string
	BaseEstimate       int64
	Canon              modelpkg.CanonicalRequest
	Snapshot           estimatorSnapshot
	Now                time.Time
	Usage              usageTotals
}

type tokenEstimatorObservationContextKey string

const tokenEstimatorObservationKey tokenEstimatorObservationContextKey = "token-estimator-observation"

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
		typeName, _ := item["type"].(string)
		switch typeName {
		case "reasoning":
			snap.ReasoningItemCount++
		case "function_call_output":
			snap.ToolResultCount++
		case "function_call":
			snap.ToolCallCount++
		}
	}

	for _, part := range canon.InstructionParts {
		snap.TextChars += int64(estimateContentPartChars(part))
		if isMultimodalPart(part) {
			snap.MultimodalItemCount++
		}
	}

	for _, msg := range canon.Messages {
		snap.TextChars += int64(utf8.RuneCountInString(msg.Role))
		snap.TextChars += int64(utf8.RuneCountInString(msg.ToolCallID))
		snap.TextChars += int64(utf8.RuneCountInString(msg.ReasoningContent))
		snap.ReasoningItemCount += int64(len(msg.ReasoningBlocks))

		if len(msg.OrderedContent) > 0 {
			for _, block := range msg.OrderedContent {
				snap.TextChars += int64(estimateContentPartChars(block.Part))
				if isMultimodalPart(block.Part) {
					snap.MultimodalItemCount++
				}
				switch block.Type {
				case "tool_use":
					snap.ToolCallCount++
				case "tool_result":
					snap.ToolResultCount++
				}
				snap.TextChars += int64(utf8.RuneCountInString(block.ToolCall.Name))
				snap.TextChars += int64(utf8.RuneCountInString(block.ToolCall.Arguments))
				snap.TextChars += int64(utf8.RuneCountInString(block.ToolCallID))
				for _, part := range block.ToolResultParts {
					snap.TextChars += int64(estimateContentPartChars(part))
					if isMultimodalPart(part) {
						snap.MultimodalItemCount++
					}
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
		for _, toolCall := range msg.ToolCalls {
			snap.TextChars += int64(utf8.RuneCountInString(toolCall.Name))
			snap.TextChars += int64(utf8.RuneCountInString(toolCall.Arguments))
		}

		if msg.Role == "tool" && len(msg.Parts) > 0 {
			snap.ToolResultCount++
		}
	}

	if len(canon.Messages) == 0 {
		for _, item := range canon.ResponseInputItems {
			if encoded, err := json.Marshal(item); err == nil {
				snap.TextChars += int64(utf8.RuneCount(encoded))
			}
		}
	}

	for _, tool := range canon.Tools {
		snap.ToolCallCount++
		snap.TextChars += int64(utf8.RuneCountInString(tool.Name))
		snap.TextChars += int64(utf8.RuneCountInString(tool.Description))
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

func withTokenEstimatorObservation(ctx context.Context, input tokenEstimatorObservationInput) context.Context {
	input.Snapshot = buildEstimatorSnapshot(input.Canon)
	input.Canon = modelpkg.CanonicalRequest{}
	return context.WithValue(ctx, tokenEstimatorObservationKey, input)
}

func buildTokenEstimatorObservation(input tokenEstimatorObservationInput) tokenestimator.Observation {
	snap := input.Snapshot
	if snap == (estimatorSnapshot{}) && input.Canon.Model != "" {
		snap = buildEstimatorSnapshot(input.Canon)
	}
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

func estimateCanonicalInputTokensWithContext(ctx context.Context, canon modelpkg.CanonicalRequest) int {
	baseEstimate := estimateCanonicalInputTokens(canon)
	if ctx == nil || baseEstimate <= 0 {
		return baseEstimate
	}
	mgr, _ := ctx.Value(tokenEstimatorManagerKey).(*tokenestimator.Manager)
	if mgr == nil {
		return baseEstimate
	}
	input, _ := ctx.Value(tokenEstimatorObservationKey).(tokenEstimatorObservationInput)
	if input.ProviderID == "" || input.EndpointType == "" || input.FinalUpstreamModel == "" {
		return baseEstimate
	}
	key := tokenestimator.BucketKey{
		ProviderID:   input.ProviderID,
		EndpointType: input.EndpointType,
		Model:        input.FinalUpstreamModel,
	}
	shape := classifyEstimatorShape(buildEstimatorSnapshot(canon))
	if corrected, ok := mgr.CorrectedEstimate(key, baseEstimate, shape); ok {
		return corrected
	}
	return baseEstimate
}

func classifyEstimatorShape(snap estimatorSnapshot) tokenestimator.ShapeClass {
	if snap.MultimodalItemCount > 0 {
		return tokenestimator.ShapeMultimodal
	}
	if snap.ToolCallCount+snap.ToolResultCount >= 6 {
		return tokenestimator.ShapeToolHeavy
	}
	if snap.ReasoningItemCount >= 3 {
		return tokenestimator.ShapeReasoningHeavy
	}
	if snap.InputItemCount >= 4 {
		return tokenestimator.ShapeStructuredResponses
	}
	return tokenestimator.ShapePlain
}
