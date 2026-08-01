package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
	modelpkg "openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/tokenestimator"
)

func recordMeasuredPrefixForTest(t *testing.T, manager *tokenestimator.Manager, providerID, endpointType string, canon modelpkg.CanonicalRequest, inputTokens, cachedTokens int64) {
	t.Helper()
	snapshot := buildEstimatorSnapshotForContext(providerID, endpointType, canon)
	coordinate := snapshot.Coordinate
	if coordinate.WireContextFingerprint == "" || coordinate.PrefixFingerprint == "" {
		t.Fatal("expected a valid measured-prefix coordinate")
	}
	if err := manager.RecordPrefixMeasurement(tokenestimator.BucketKey{ProviderID: providerID, EndpointType: endpointType, Model: canon.Model}, tokenestimator.PrefixMeasurement{
		Version:                tokenestimator.PrefixMeasurementVersion,
		WireContextFingerprint: coordinate.WireContextFingerprint,
		PrefixFingerprint:      coordinate.PrefixFingerprint,
		PrefixUnits:            coordinate.PrefixUnits,
		StructuralUnits:        coordinate.StructuralUnits,
		LocalEstimate:          coordinate.LocalEstimate,
		InputTokens:            inputTokens,
		CachedTokens:           cachedTokens,
	}); err != nil {
		t.Fatalf("record measured prefix: %v", err)
	}
}

func measuredPrefixContext(manager *tokenestimator.Manager, providerID, endpointType string, canon modelpkg.CanonicalRequest) context.Context {
	ctx := withTokenEstimatorManager(context.Background(), manager)
	return withTokenEstimatorObservation(ctx, tokenEstimatorObservationInput{
		ProviderID:         providerID,
		EndpointType:       endpointType,
		FinalUpstreamModel: canon.Model,
		Canon:              canon,
	})
}

func TestMeasuredPrefixEstimatorUsesExactRepeatedFullPrefix(t *testing.T) {
	manager := tokenestimator.NewManager(t.TempDir(), time.UTC, func() []string { return []string{"openai"} })
	canon := modelpkg.CanonicalRequest{
		Model:        "gpt-5.6",
		Instructions: "stable provider prompt",
		Messages: []modelpkg.CanonicalMessage{
			{Role: "user", Parts: []modelpkg.CanonicalContentPart{{Type: "text", Text: "same prefix"}}},
			{Role: "assistant", Parts: []modelpkg.CanonicalContentPart{{Type: "text", Text: "same answer"}}},
		},
		Tools: []modelpkg.CanonicalTool{{Type: "function", Name: "lookup", Description: "lookup data"}},
	}
	ctx := measuredPrefixContext(manager, "openai", config.UpstreamEndpointTypeResponses, canon)
	local := estimateFromObservationInput(ctx, mustEstimatorObservation(t, ctx))
	if local.Source != "local" || local.Point <= 0 {
		t.Fatalf("expected cold local estimate, got %#v", local)
	}
	recordMeasuredPrefixForTest(t, manager, "openai", config.UpstreamEndpointTypeResponses, canon, 212000, 180000)

	repeatedCtx := measuredPrefixContext(manager, "openai", config.UpstreamEndpointTypeResponses, canon)
	got := estimateFromObservationInput(repeatedCtx, mustEstimatorObservation(t, repeatedCtx))
	if got.Source != "exact-prefix" || got.Confidence != "exact" || got.Point != 212000 || got.Lower != 212000 || got.Upper != 212000 {
		t.Fatalf("expected exact repeated prefix measurement, got %#v", got)
	}
}

func TestMeasuredPrefixEstimatorIsolatesSiblingBranches(t *testing.T) {
	manager := tokenestimator.NewManager(t.TempDir(), time.UTC, func() []string { return []string{"openai"} })
	parent := modelpkg.CanonicalRequest{
		Model: "gpt-5.6",
		Messages: []modelpkg.CanonicalMessage{
			{Role: "user", Parts: []modelpkg.CanonicalContentPart{{Type: "text", Text: "one"}}},
			{Role: "assistant", Parts: []modelpkg.CanonicalContentPart{{Type: "text", Text: "two"}}},
		},
	}
	branch3 := parent
	branch3.Messages = append(cloneCanonicalMessages(parent.Messages), modelpkg.CanonicalMessage{Role: "user", Parts: []modelpkg.CanonicalContentPart{{Type: "text", Text: "branch three"}}})
	branch4 := parent
	branch4.Messages = append(cloneCanonicalMessages(parent.Messages), modelpkg.CanonicalMessage{Role: "user", Parts: []modelpkg.CanonicalContentPart{{Type: "text", Text: "branch four with a different suffix"}}})
	recordMeasuredPrefixForTest(t, manager, "openai", config.UpstreamEndpointTypeChat, parent, 100, 20)

	ctx3 := measuredPrefixContext(manager, "openai", config.UpstreamEndpointTypeChat, branch3)
	ctx4 := measuredPrefixContext(manager, "openai", config.UpstreamEndpointTypeChat, branch4)
	got3 := estimateFromObservationInput(ctx3, mustEstimatorObservation(t, ctx3))
	got4 := estimateFromObservationInput(ctx4, mustEstimatorObservation(t, ctx4))
	if got3.Source != "measured-prefix-plus-local-suffix" || got4.Source != "measured-prefix-plus-local-suffix" {
		t.Fatalf("expected both branches to use their verified measured parent, got branch3=%#v branch4=%#v", got3, got4)
	}
	if got3.Point == got4.Point {
		t.Fatalf("expected sibling suffixes to remain isolated, got branch3=%d branch4=%d", got3.Point, got4.Point)
	}
	if got3.Point <= 100 || got4.Point <= 100 {
		t.Fatalf("expected both estimates to include a positive local suffix, got branch3=%d branch4=%d", got3.Point, got4.Point)
	}
}

func TestMeasuredPrefixEstimatorFallsBackWhenWireContextChanges(t *testing.T) {
	manager := tokenestimator.NewManager(t.TempDir(), time.UTC, func() []string { return []string{"openai"} })
	canon := modelpkg.CanonicalRequest{
		Model:        "gpt-5.6",
		Instructions: "prompt-v1",
		Messages:     []modelpkg.CanonicalMessage{{Role: "user", Parts: []modelpkg.CanonicalContentPart{{Type: "text", Text: "stable content"}}}},
		Tools:        []modelpkg.CanonicalTool{{Type: "function", Name: "lookup"}},
	}
	recordMeasuredPrefixForTest(t, manager, "openai", config.UpstreamEndpointTypeResponses, canon, 500, 100)
	changed := canon
	changed.Instructions = "prompt-v2"
	ctx := measuredPrefixContext(manager, "openai", config.UpstreamEndpointTypeResponses, changed)
	got := estimateFromObservationInput(ctx, mustEstimatorObservation(t, ctx))
	if got.Source != "local" || got.Confidence != "uncertain" {
		t.Fatalf("expected changed wire context to fall back cold, got %#v", got)
	}
}

func TestMeasuredPrefixEstimatorHeaderUsesRawPointWithoutLimitScaling(t *testing.T) {
	manager := tokenestimator.NewManager(t.TempDir(), time.UTC, func() []string { return []string{"openai"} })
	canon := modelpkg.CanonicalRequest{
		Model:    "gpt-5.6",
		Messages: []modelpkg.CanonicalMessage{{Role: "user", Parts: []modelpkg.CanonicalContentPart{{Type: "text", Text: "measured content"}}}},
	}
	recordMeasuredPrefixForTest(t, manager, "openai", config.UpstreamEndpointTypeResponses, canon, 212000, 0)
	ctx := measuredPrefixContext(manager, "openai", config.UpstreamEndpointTypeResponses, canon)
	recorder := httptest.NewRecorder()
	provider := config.ProviderConfig{ModelLimitContextTokens: 598000}
	if writeContextLimitExceededIfNeeded(ctx, recorder, provider, canon, clientReasoningProtocolResponses) {
		t.Fatal("expected a measured point below the configured limit to continue")
	}
	if got := recorder.Header().Get(headerProxyEstimatedInputTokens); got != "212000" {
		t.Fatalf("expected unscaled point estimate 212000, got %q", got)
	}
}

func TestUncertainContextAdmissionReachesResponsesPreOutputProbe(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`event: response.created
data: {"response":{"id":"resp-tool-overflow"}}

event: response.output_item.added
data: {"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup"}}

event: response.function_call_arguments.delta
data: {"item_id":"fc_1","delta":"{\"query\":"}

event: error
data: {"error":{"code":"context_length_exceeded","message":"prompt is too long"}}

`))
	}))
	defer upstream.Close()
	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                      "openai",
			Enabled:                 true,
			SupportsResponses:       true,
			ManualModels:            []string{"gpt-5.6"},
			ModelLimitContextTokens: 10,
			UpstreamEndpointType:    config.UpstreamEndpointTypeResponses,
			UpstreamBaseURL:         upstream.URL,
			UpstreamAPIKey:          "test-key",
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6","stream":true,"input":"uncertain request"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if calls.Load() != 1 {
		t.Fatalf("expected uncertain request to reach upstream exactly once, got %d", calls.Load())
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected pre-output context overflow normalization, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "event: ") || !strings.Contains(rec.Body.String(), "context_length_exceeded") || !strings.Contains(rec.Body.String(), "prompt is too long") {
		t.Fatalf("expected HTTP error with preserved compaction keywords, got %s", rec.Body.String())
	}
}

func mustEstimatorObservation(t *testing.T, ctx context.Context) tokenEstimatorObservationInput {
	t.Helper()
	input, ok := tokenEstimatorObservationFromContext(ctx)
	if !ok {
		t.Fatal("expected token estimator observation context")
	}
	return input
}
