package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash"
	"time"
	"unicode/utf8"

	"openai-compat-proxy/internal/config"
	modelpkg "openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/tokenestimator"
	"openai-compat-proxy/internal/upstream"
)

const (
	estimatorCoordinateVersion = "measured-prefix:v2"
	maxEstimatorPrefixPoints   = 2048
)

type usageTotals struct {
	InputTokens  int64
	CachedTokens int64
}

type tokenEstimatorObservationInput struct {
	ProviderID               string
	EndpointType             string
	FinalUpstreamModel       string
	BaseEstimate             int64
	IncrementalEstimate      int64
	IncrementalEstimateValid bool
	Canon                    modelpkg.CanonicalRequest
	Snapshot                 estimatorSnapshot
	Coordinate               estimatorCoordinate
	Estimate                 estimatorEstimate
	Now                      time.Time
	Usage                    usageTotals
}

type tokenEstimatorObservationContextKey string

const tokenEstimatorObservationKey tokenEstimatorObservationContextKey = "token-estimator-observation"

type estimatorPrefixPoint struct {
	Fingerprint     string
	PrefixUnits     int64
	StructuralUnits int64
	LocalEstimate   int64
}

type estimatorCoordinate struct {
	Version                string
	WireContextFingerprint string
	PrefixFingerprint      string
	PrefixUnits            int64
	StructuralUnits        int64
	LocalEstimate          int64
	PrefixPoints           []estimatorPrefixPoint
}

type estimatorSnapshot struct {
	TextChars           int64
	InputItemCount      int64
	ReasoningItemCount  int64
	ToolCallCount       int64
	ToolResultCount     int64
	MultimodalItemCount int64
	StaticUnits         int64
	DynamicUnits        int64
	BaseEstimate        int64
	Coordinate          estimatorCoordinate
}

type estimatorEstimate struct {
	Point      int64
	Lower      int64
	Upper      int64
	Source     string
	Confidence string
}

type estimatorWireContext struct {
	Version                        string
	ProviderID                     string
	EndpointType                   string
	Model                          string
	Stream                         bool
	PreservedTopLevelFields        map[string]any
	IncludeUsage                   bool
	ResponseStore                  *bool
	ResponseInclude                []string
	ResponsePromptCacheKey         json.RawMessage
	ResponsePromptCacheOptions     json.RawMessage
	ResponseMultiAgent             json.RawMessage
	ResponsesOpenAIBeta            string
	Instructions                   string
	InstructionParts               []modelpkg.CanonicalContentPart
	ResponseItemReferencesByCallID map[string]string
	Temperature                    *float64
	TopP                           *float64
	MaxOutputTokens                *int
	OmitMaxOutputTokens            bool
	Stop                           []string
	Tools                          []modelpkg.CanonicalTool
	ToolChoice                     modelpkg.CanonicalToolChoice
	ParallelToolCalls              *bool
	Reasoning                      *modelpkg.CanonicalReasoning
	ReasoningModeOrigin            modelpkg.ReasoningModeOrigin
	PassThroughRawReasoning        bool
	SkipProviderSystemPrompt       bool
	HasSyntheticReasoningReplay    bool
	InputItemsAreOriginal          bool
}

type estimatorMessageFingerprint struct {
	Role              string
	ToolCallID        string
	ContentBlocks     []estimatorContentBlockFingerprint
	ToolCalls         []modelpkg.CanonicalToolCall
	RecoveredToolCall *modelpkg.CanonicalToolCall
	ReasoningContent  string
	ReasoningBlocks   []map[string]any
}

type estimatorContentBlockFingerprint struct {
	Type            string
	Part            modelpkg.CanonicalContentPart
	ToolCall        modelpkg.CanonicalToolCall
	ToolCallID      string
	ToolResultParts []modelpkg.CanonicalContentPart
	Raw             map[string]any
}

func buildEstimatorSnapshot(canon modelpkg.CanonicalRequest) estimatorSnapshot {
	return buildEstimatorSnapshotForContext("", "", canon)
}

func buildEstimatorSnapshotForContext(providerID, endpointType string, canon modelpkg.CanonicalRequest) estimatorSnapshot {
	var snap estimatorSnapshot
	useResponsesInputWire := endpointType == config.UpstreamEndpointTypeResponses || (endpointType == "" && canon.ResponseInputItemsAreOriginal)
	var preparedResponsesInput upstream.ResponsesInputPreparation
	if useResponsesInputWire {
		preparedResponsesInput = upstream.PrepareResponsesInput(canon, len(canon.ResponseItemReferencesByCallID) > 0)
		snap.InputItemCount = int64(len(preparedResponsesInput.Input))
	} else {
		snap.InputItemCount = int64(len(canon.ResponseInputItems))
	}

	wireContext, wireUnits, wireFingerprintOK := buildEstimatorWireContext(providerID, endpointType, canon)
	snap.StaticUnits = wireUnits
	snap.TextChars += wireUnits

	prefixHasher := sha256.New()
	writeHashFrame(prefixHasher, "coordinate-version", []byte(estimatorCoordinateVersion))
	contentKind := "messages"
	if useResponsesInputWire || len(canon.Messages) == 0 {
		contentKind = "response-input-items"
	}
	writeHashFrame(prefixHasher, "content-kind", []byte(contentKind))

	coordinate := estimatorCoordinate{Version: estimatorCoordinateVersion}
	if wireFingerprintOK {
		coordinate.WireContextFingerprint = wireContext
	}
	var dynamicUnits int64
	var structuralUnits int64
	if useResponsesInputWire {
		for _, item := range preparedResponsesInput.Input {
			encoded := marshalEstimatorJSON(item)
			units := int64(utf8.RuneCount(encoded))
			structural, reasoning, tools, results, multimodal := responseInputItemFeatures(item)
			writeHashFrame(prefixHasher, "response-input-item", encoded)
			dynamicUnits += units
			structuralUnits += structural
			snap.TextChars += units
			snap.ReasoningItemCount += reasoning
			snap.ToolCallCount += tools
			snap.ToolResultCount += results
			snap.MultimodalItemCount += multimodal
			appendEstimatorPrefixPoint(&coordinate, prefixHasher, dynamicUnits, structuralUnits)
		}
		if len(canon.Messages) > 0 && !canon.ResponseInputItemsAreOriginal {
			for _, item := range canon.ResponseInputItems {
				_, reasoning, tools, results, multimodal := responseInputItemFeatures(item)
				snap.ReasoningItemCount += reasoning
				snap.ToolCallCount += tools
				snap.ToolResultCount += results
				snap.MultimodalItemCount += multimodal
			}
		}
	} else if len(canon.Messages) > 0 {
		for _, message := range canon.Messages {
			encoded, units, structural, reasoning, tools, results, multimodal := marshalEstimatorMessage(message)
			writeHashFrame(prefixHasher, "message", encoded)
			dynamicUnits += units
			structuralUnits += structural
			snap.TextChars += units
			snap.ReasoningItemCount += reasoning
			snap.ToolCallCount += tools
			snap.ToolResultCount += results
			snap.MultimodalItemCount += multimodal
			appendEstimatorPrefixPoint(&coordinate, prefixHasher, dynamicUnits, structuralUnits)
		}
		for _, item := range canon.ResponseInputItems {
			structural, reasoning, tools, results, multimodal := responseInputItemFeatures(item)
			snap.ReasoningItemCount += reasoning
			snap.ToolCallCount += tools
			snap.ToolResultCount += results
			snap.MultimodalItemCount += multimodal
			_ = structural
		}
	} else {
		for _, item := range canon.ResponseInputItems {
			encoded := marshalEstimatorJSON(item)
			units := int64(utf8.RuneCount(encoded))
			structural, reasoning, tools, results, multimodal := responseInputItemFeatures(item)
			writeHashFrame(prefixHasher, "response-input-item", encoded)
			dynamicUnits += units
			structuralUnits += structural
			snap.TextChars += units
			snap.ReasoningItemCount += reasoning
			snap.ToolCallCount += tools
			snap.ToolResultCount += results
			snap.MultimodalItemCount += multimodal
			appendEstimatorPrefixPoint(&coordinate, prefixHasher, dynamicUnits, structuralUnits)
		}
	}
	coordinate.PrefixFingerprint = hex.EncodeToString(prefixHasher.Sum(nil))
	coordinate.PrefixUnits = dynamicUnits
	coordinate.StructuralUnits = structuralUnits
	coordinate.LocalEstimate = localEstimatorTokens(dynamicUnits, structuralUnits)
	snap.DynamicUnits = dynamicUnits
	snap.Coordinate = coordinate
	snap.BaseEstimate = localEstimatorTokens(snap.StaticUnits, estimatorStaticStructuralUnits(canon)) + coordinate.LocalEstimate
	if snap.BaseEstimate <= 0 && (snap.TextChars > 0 || snap.InputItemCount > 0) {
		snap.BaseEstimate = 1
	}
	return snap
}

func buildEstimatorWireContext(providerID, endpointType string, canon modelpkg.CanonicalRequest) (string, int64, bool) {
	preservedTopLevelFields := canon.PreservedTopLevelFields
	instructionParts := canon.InstructionParts
	if endpointType == config.UpstreamEndpointTypeResponses {
		prepared := upstream.PrepareResponsesInput(canon, len(canon.ResponseItemReferencesByCallID) > 0)
		preservedTopLevelFields = mergeEstimatorWireFields(canon.PreservedTopLevelFields, prepared.PreservedTopLevelFields)
		if len(canon.ResponseItemReferencesByCallID) > 0 {
			delete(preservedTopLevelFields, "previous_response_id")
		}
		instructionParts = nil
	}
	wire := estimatorWireContext{
		Version:                        estimatorCoordinateVersion,
		ProviderID:                     providerID,
		EndpointType:                   endpointType,
		Model:                          canon.Model,
		Stream:                         canon.Stream,
		PreservedTopLevelFields:        preservedTopLevelFields,
		IncludeUsage:                   canon.IncludeUsage,
		ResponseStore:                  canon.ResponseStore,
		ResponseInclude:                canon.ResponseInclude,
		ResponsePromptCacheKey:         canon.ResponsePromptCacheKey,
		ResponsePromptCacheOptions:     canon.ResponsePromptCacheOptions,
		ResponseMultiAgent:             canon.ResponseMultiAgent,
		ResponsesOpenAIBeta:            canon.ResponsesOpenAIBeta,
		Instructions:                   canon.Instructions,
		InstructionParts:               instructionParts,
		ResponseItemReferencesByCallID: canon.ResponseItemReferencesByCallID,
		Temperature:                    canon.Temperature,
		TopP:                           canon.TopP,
		MaxOutputTokens:                canon.MaxOutputTokens,
		OmitMaxOutputTokens:            canon.OmitMaxOutputTokens,
		Stop:                           canon.Stop,
		Tools:                          canon.Tools,
		ToolChoice:                     canon.ToolChoice,
		ParallelToolCalls:              canon.ParallelToolCalls,
		Reasoning:                      canon.Reasoning,
		ReasoningModeOrigin:            canon.ReasoningModeOrigin,
		PassThroughRawReasoning:        canon.PassThroughRawReasoning,
		SkipProviderSystemPrompt:       canon.SkipProviderSystemPrompt,
		HasSyntheticReasoningReplay:    canon.HasSyntheticReasoningReplay,
		InputItemsAreOriginal:          canon.ResponseInputItemsAreOriginal,
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return "", 0, false
	}
	payload := make([]byte, 0, len(estimatorCoordinateVersion)+1+len(encoded))
	payload = append(payload, estimatorCoordinateVersion...)
	payload = append(payload, 0)
	payload = append(payload, encoded...)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), int64(utf8.RuneCount(encoded)), true
}

func mergeEstimatorWireFields(base, extra map[string]any) map[string]any {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	merged := make(map[string]any, len(base)+len(extra))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range extra {
		merged[key] = value
	}
	return merged
}

func appendEstimatorPrefixPoint(coordinate *estimatorCoordinate, hasher hash.Hash, prefixUnits, structuralUnits int64) {
	if coordinate == nil || len(coordinate.PrefixPoints) >= maxEstimatorPrefixPoints {
		if coordinate != nil && len(coordinate.PrefixPoints) == maxEstimatorPrefixPoints {
			coordinate.PrefixPoints[maxEstimatorPrefixPoints-1] = estimatorPrefixPoint{
				Fingerprint:     hex.EncodeToString(hasher.Sum(nil)),
				PrefixUnits:     prefixUnits,
				StructuralUnits: structuralUnits,
				LocalEstimate:   localEstimatorTokens(prefixUnits, structuralUnits),
			}
		}
		return
	}
	coordinate.PrefixPoints = append(coordinate.PrefixPoints, estimatorPrefixPoint{
		Fingerprint:     hex.EncodeToString(hasher.Sum(nil)),
		PrefixUnits:     prefixUnits,
		StructuralUnits: structuralUnits,
		LocalEstimate:   localEstimatorTokens(prefixUnits, structuralUnits),
	})
}

func marshalEstimatorMessage(message modelpkg.CanonicalMessage) ([]byte, int64, int64, int64, int64, int64, int64) {
	fingerprint := estimatorMessageFingerprint{
		Role:              message.Role,
		ToolCallID:        message.ToolCallID,
		RecoveredToolCall: message.RecoveredToolCall,
		ReasoningContent:  message.ReasoningContent,
		ReasoningBlocks:   message.ReasoningBlocks,
	}
	var units int64
	structural := int64(1)
	var reasoning, tools, results, multimodal int64
	reasoning += int64(len(message.ReasoningBlocks))
	if message.Role == "tool" && len(message.Parts) > 0 {
		results++
	}
	if len(message.OrderedContent) > 0 {
		for _, block := range message.OrderedContent {
			fingerprint.ContentBlocks = append(fingerprint.ContentBlocks, estimatorContentBlockFingerprint{
				Type:            block.Type,
				Part:            block.Part,
				ToolCall:        block.ToolCall,
				ToolCallID:      block.ToolCallID,
				ToolResultParts: block.ToolResultParts,
				Raw:             block.Raw,
			})
			if isMultimodalPart(block.Part) {
				multimodal++
			}
			switch block.Type {
			case "tool_use":
				tools++
			case "tool_result":
				results++
			}
		}
	} else {
		tools += int64(len(message.ToolCalls))
		for _, part := range message.Parts {
			fingerprint.ContentBlocks = append(fingerprint.ContentBlocks, estimatorContentBlockFingerprint{Type: "content", Part: part})
			if isMultimodalPart(part) {
				multimodal++
			}
		}
		fingerprint.ToolCalls = message.ToolCalls
	}
	structural += tools + results + reasoning + multimodal
	encoded := marshalEstimatorJSON(fingerprint)
	units = int64(utf8.RuneCount(encoded))
	return encoded, units, structural, reasoning, tools, results, multimodal
}

func responseInputItemFeatures(item map[string]any) (int64, int64, int64, int64, int64) {
	structural := int64(1)
	var reasoning, tools, results, multimodal int64
	switch stringValue(item["type"]) {
	case "reasoning":
		reasoning++
	case "function_call":
		tools++
	case "function_call_output":
		results++
	}
	if isEstimatorMultimodalItem(item) {
		multimodal++
	}
	if content, ok := item["content"].([]map[string]any); ok {
		for _, part := range content {
			if isEstimatorMultimodalItem(part) {
				multimodal++
			}
		}
	}
	if content, ok := item["content"].([]any); ok {
		for _, value := range content {
			part, ok := value.(map[string]any)
			if ok && isEstimatorMultimodalItem(part) {
				multimodal++
			}
		}
	}
	structural += reasoning + tools + results + multimodal
	return structural, reasoning, tools, results, multimodal
}

func isEstimatorMultimodalItem(item map[string]any) bool {
	if item == nil {
		return false
	}
	if item["image_url"] != nil || item["image"] != nil || item["input_image"] != nil || item["input_file"] != nil || item["input_audio"] != nil {
		return true
	}
	switch stringValue(item["type"]) {
	case "image_url", "input_image", "input_file", "input_audio":
		return true
	default:
		return false
	}
}

func estimatorStaticStructuralUnits(canon modelpkg.CanonicalRequest) int64 {
	structural := int64(1 + len(canon.Tools))
	if canon.Reasoning != nil {
		structural++
	}
	if canon.ToolChoice.Mode != "" || canon.ToolChoice.Name != "" {
		structural++
	}
	return structural
}

func marshalEstimatorJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte("<estimator-unserializable>")
	}
	return encoded
}

func writeHashFrame(hasher hash.Hash, label string, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(label)))
	_, _ = hasher.Write(length[:])
	_, _ = hasher.Write([]byte(label))
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = hasher.Write(length[:])
	_, _ = hasher.Write(value)
}

func localEstimatorTokens(units, structuralUnits int64) int64 {
	if units <= 0 && structuralUnits <= 0 {
		return 0
	}
	if units < 0 {
		units = 0
	}
	if structuralUnits < 0 {
		structuralUnits = 0
	}
	return (units+3)/4 + structuralUnits*2
}

func localEstimatorInterval(units, structuralUnits, point int64) estimatorEstimate {
	if point <= 0 {
		return estimatorEstimate{}
	}
	lower := (units + 5) / 6
	upper := (units + 1) / 2
	if lower < 1 {
		lower = 1
	}
	lower += structuralUnits
	upper += structuralUnits * 4
	if lower > point {
		lower = point
	}
	if upper < point {
		upper = point
	}
	return estimatorEstimate{Point: point, Lower: lower, Upper: upper, Source: "local", Confidence: "uncertain"}
}

func withTokenEstimatorObservation(ctx context.Context, input tokenEstimatorObservationInput) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if input.Snapshot.Coordinate.PrefixFingerprint == "" && input.Snapshot.BaseEstimate <= 0 {
		input.Snapshot = buildEstimatorSnapshotForContext(input.ProviderID, input.EndpointType, input.Canon)
	}
	input.Coordinate = input.Snapshot.Coordinate
	if input.BaseEstimate <= 0 {
		input.BaseEstimate = input.Snapshot.BaseEstimate
	}
	input.Estimate = estimateFromObservationInput(ctx, input)
	input.Canon = modelpkg.CanonicalRequest{}
	return context.WithValue(ctx, tokenEstimatorObservationKey, input)
}

func buildTokenEstimatorObservation(input tokenEstimatorObservationInput) tokenestimator.Observation {
	snap := input.Snapshot
	if snap.Coordinate.PrefixFingerprint == "" && snap.BaseEstimate <= 0 && input.Canon.Model != "" {
		snap = buildEstimatorSnapshotForContext(input.ProviderID, input.EndpointType, input.Canon)
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
		ProtocolSignature:  input.EndpointType + ":v2",
		EstimatorSignature: estimatorCoordinateVersion,
		RecordedAt:         input.Now,
	}
}

func recordTokenEstimatorUsage(ctx context.Context, requestID string, usage usageTotals) error {
	if ctx == nil {
		return nil
	}
	mgr, _ := ctx.Value(tokenEstimatorManagerKey).(*tokenestimator.Manager)
	if mgr == nil {
		return nil
	}
	input, _ := ctx.Value(tokenEstimatorObservationKey).(tokenEstimatorObservationInput)
	if input.ProviderID == "" || input.FinalUpstreamModel == "" || usage.InputTokens <= 0 {
		return nil
	}
	if input.BaseEstimate <= 0 {
		input.BaseEstimate = input.Snapshot.BaseEstimate
	}
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	}
	input.Usage = usage
	if input.BaseEstimate > 0 {
		if err := mgr.RecordObservation(requestID, buildTokenEstimatorObservation(input)); err != nil {
			return err
		}
	}
	coordinate := input.Coordinate
	if coordinate.WireContextFingerprint == "" || coordinate.PrefixFingerprint == "" {
		return nil
	}
	return mgr.RecordPrefixMeasurement(tokenestimator.BucketKey{
		ProviderID:   input.ProviderID,
		EndpointType: input.EndpointType,
		Model:        input.FinalUpstreamModel,
	}, tokenestimator.PrefixMeasurement{
		Version:                tokenestimator.PrefixMeasurementVersion,
		WireContextFingerprint: coordinate.WireContextFingerprint,
		PrefixFingerprint:      coordinate.PrefixFingerprint,
		PrefixUnits:            coordinate.PrefixUnits,
		StructuralUnits:        coordinate.StructuralUnits,
		LocalEstimate:          coordinate.LocalEstimate,
		InputTokens:            usage.InputTokens,
		CachedTokens:           usage.CachedTokens,
		RecordedAt:             input.Now,
	})
}

func estimateCanonicalInputTokens(canon modelpkg.CanonicalRequest) int {
	snap := buildEstimatorSnapshot(canon)
	return int(snap.BaseEstimate)
}

func estimateCanonicalInputTokensWithContext(ctx context.Context, canon modelpkg.CanonicalRequest) int {
	if input, ok := tokenEstimatorObservationFromContext(ctx); ok {
		return int(estimateFromObservationInput(ctx, input).Point)
	}
	return estimateCanonicalInputTokens(canon)
}

func fullContextAdmissionEstimateWithContext(ctx context.Context, canon modelpkg.CanonicalRequest) int {
	if input, ok := tokenEstimatorObservationFromContext(ctx); ok {
		return int(estimateFromObservationInput(ctx, input).Point)
	}
	return estimateCanonicalInputTokens(canon)
}

func tokenEstimatorObservationFromContext(ctx context.Context) (tokenEstimatorObservationInput, bool) {
	if ctx == nil {
		return tokenEstimatorObservationInput{}, false
	}
	input, ok := ctx.Value(tokenEstimatorObservationKey).(tokenEstimatorObservationInput)
	return input, ok
}

func estimateFromObservationInput(ctx context.Context, input tokenEstimatorObservationInput) estimatorEstimate {
	snapshot := input.Snapshot
	if snapshot.BaseEstimate <= 0 {
		return estimatorEstimate{}
	}
	local := localEstimatorInterval(snapshot.StaticUnits+snapshot.DynamicUnits, estimatorStaticStructuralUnitsFromSnapshot(snapshot), snapshot.BaseEstimate)
	coordinate := input.Coordinate
	if coordinate.WireContextFingerprint == "" || coordinate.PrefixFingerprint == "" {
		return local
	}
	mgr, _ := ctx.Value(tokenEstimatorManagerKey).(*tokenestimator.Manager)
	if mgr == nil {
		return local
	}
	key := tokenestimator.BucketKey{ProviderID: input.ProviderID, EndpointType: input.EndpointType, Model: input.FinalUpstreamModel}
	if measured, ok := mgr.LookupPrefixMeasurement(key, coordinate.WireContextFingerprint, coordinate.PrefixFingerprint); ok {
		return estimatorEstimate{Point: measured.InputTokens, Lower: measured.InputTokens, Upper: measured.InputTokens, Source: "exact-prefix", Confidence: "exact"}
	}
	for index := len(coordinate.PrefixPoints) - 1; index >= 0; index-- {
		point := coordinate.PrefixPoints[index]
		if point.Fingerprint == "" || point.Fingerprint == coordinate.PrefixFingerprint {
			continue
		}
		measured, ok := mgr.LookupPrefixMeasurement(key, coordinate.WireContextFingerprint, point.Fingerprint)
		if !ok || measured.PrefixUnits > coordinate.PrefixUnits || measured.StructuralUnits > coordinate.StructuralUnits || measured.LocalEstimate > coordinate.LocalEstimate {
			continue
		}
		suffixUnits := coordinate.PrefixUnits - measured.PrefixUnits
		suffixStructural := coordinate.StructuralUnits - measured.StructuralUnits
		suffixPoint := localEstimatorTokens(suffixUnits, suffixStructural)
		suffix := localEstimatorInterval(suffixUnits, suffixStructural, suffixPoint)
		return estimatorEstimate{
			Point:      measured.InputTokens + suffix.Point,
			Lower:      measured.InputTokens + suffix.Lower,
			Upper:      measured.InputTokens + suffix.Upper,
			Source:     "measured-prefix-plus-local-suffix",
			Confidence: "uncertain",
		}
	}
	return local
}

func estimatorStaticStructuralUnitsFromSnapshot(snap estimatorSnapshot) int64 {
	structural := snap.InputItemCount
	if snap.ReasoningItemCount > 0 {
		structural++
	}
	structural += snap.ToolCallCount + snap.ToolResultCount + snap.MultimodalItemCount
	return structural + 1
}

func incrementalEstimateFromContext(ctx context.Context) (int, bool) {
	input, ok := tokenEstimatorObservationFromContext(ctx)
	if !ok || !input.IncrementalEstimateValid || input.IncrementalEstimate <= 0 {
		return 0, false
	}
	return int(input.IncrementalEstimate), true
}

func correctedCanonicalInputTokensWithContext(ctx context.Context, canon modelpkg.CanonicalRequest, baseEstimate int) int {
	if input, ok := tokenEstimatorObservationFromContext(ctx); ok {
		return int(estimateFromObservationInput(ctx, input).Point)
	}
	if baseEstimate > 0 {
		return baseEstimate
	}
	return estimateCanonicalInputTokens(canon)
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

func isMultimodalPart(part modelpkg.CanonicalContentPart) bool {
	return part.ImageURL != "" || part.MimeType != "" || part.Type == "input_file" || part.Type == "input_audio"
}
