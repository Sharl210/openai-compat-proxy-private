package httpapi

import (
	"context"
	"fmt"
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

func TestRequestLineageStoreSupportsMultipleChildrenAndRecursiveDescendants(t *testing.T) {
	store := newRequestLineageStore()
	root := store.allocate("session-1", "request-root", "")
	store.recordResponseID(root, "response-root")
	store.recordFinalizedEstimate(root, requestLineageEstimatorFact{
		Bucket:                 tokenestimator.BucketKey{ProviderID: "provider-a", EndpointType: "responses", Model: "model-a"},
		WireContextFingerprint: "wire-root",
		LineageWireFingerprint: "wire-family",
		PrefixFingerprint:      "prefix-root",
		PrefixUnits:            100,
		StructuralUnits:        2,
		LocalEstimate:          25,
		InputTokens:            100,
	})
	if _, ok := store.parentFinalizedEstimate(requestLineage{ConversationID: root.ConversationID, ParentNodeID: root.NodeID}); ok {
		t.Fatal("expected an incomplete parent not to provide a confirmed baseline")
	}
	store.markCompleted(root)

	childA := store.allocate("session-1", "request-child-a", "response-root")
	childB := store.allocate("session-1", "request-child-b", "response-root")
	if childA.ParentNodeID != root.NodeID || childB.ParentNodeID != root.NodeID {
		t.Fatalf("expected both children to attach to root, childA=%#v childB=%#v root=%#v", childA, childB, root)
	}
	if childA.NodeID == childB.NodeID || childA.ConversationRequestSeq == childB.ConversationRequestSeq {
		t.Fatalf("expected independent child identities, childA=%#v childB=%#v", childA, childB)
	}

	store.recordResponseID(childA, "response-child-a")
	store.recordFinalizedEstimate(childA, requestLineageEstimatorFact{
		Bucket:                 tokenestimator.BucketKey{ProviderID: "provider-a", EndpointType: "responses", Model: "model-a"},
		WireContextFingerprint: "wire-root",
		LineageWireFingerprint: "wire-family",
		PrefixFingerprint:      "prefix-child-a",
		PrefixUnits:            140,
		StructuralUnits:        3,
		LocalEstimate:          35,
		InputTokens:            140,
	})
	store.markCompleted(childA)

	grandchild := store.allocate("session-1", "request-grandchild", "response-child-a")
	if grandchild.ParentNodeID != childA.NodeID || grandchild.ParentRequestUID != childA.RequestUID {
		t.Fatalf("expected recursive descendant to attach to child A, got %#v", grandchild)
	}
	parentFact, ok := store.parentFinalizedEstimate(grandchild)
	if !ok || parentFact.InputTokens != 140 {
		t.Fatalf("expected recursive baseline from child A, got fact=%#v ok=%v", parentFact, ok)
	}

	siblingFact, ok := store.parentFinalizedEstimate(childB)
	if !ok || siblingFact.InputTokens != 100 {
		t.Fatalf("expected sibling B to retain root baseline, got fact=%#v ok=%v", siblingFact, ok)
	}
}

func TestRequestLineageStoreCapsActiveNodesAndConversations(t *testing.T) {
	store := newRequestLineageStore()
	store.conversationLimit = 1
	store.nodesPerConversation = 2

	root := store.allocate("session-1", "request-root", "")
	child := store.allocate("session-1", "request-child", "")
	if root.NodeID == "" || child.NodeID == "" {
		t.Fatalf("expected initial lineage nodes, root=%#v child=%#v", root, child)
	}
	if got := store.allocate("session-1", "request-third", ""); got.NodeID != "" {
		t.Fatalf("expected active node capacity to reject the third node, got %#v", got)
	}
	if got := store.allocate("session-2", "request-other", ""); got.NodeID != "" {
		t.Fatalf("expected active conversation capacity to reject a new conversation, got %#v", got)
	}
	if got := len(store.conversations["session-1"].Nodes); got != 2 {
		t.Fatalf("expected bounded active node count, got %d", got)
	}

	store.recordResponseID(root, "response-root")
	store.recordResponseID(child, "response-child")
	store.markCompleted(root)
	store.markCompleted(child)
	if got := store.allocate("session-2", "request-other", ""); got.NodeID == "" {
		t.Fatal("expected completed conversation to make room for a new conversation")
	}
}

func TestRequestLineageEstimateUsesConfirmedUsageAndPreservesModelIsolation(t *testing.T) {
	store := newRequestLineageStore()
	carrier := newRequestLineageCarrier("request-child", "session-1")
	ctx := withRequestLineageCarrier(context.Background(), carrier)
	ctx = withRequestLineageStore(ctx, store)
	root := store.allocate("session-1", "request-root", "")
	store.recordResponseID(root, "response-root")
	store.recordFinalizedEstimate(root, requestLineageEstimatorFact{
		Bucket:                 tokenestimator.BucketKey{ProviderID: "provider-a", EndpointType: "responses", Model: "model-a"},
		LineageWireFingerprint: "wire-family",
		PrefixFingerprint:      "prefix-root",
		PrefixUnits:            10,
		StructuralUnits:        1,
		LocalEstimate:          10,
		InputTokens:            123,
	})
	store.markCompleted(root)
	child := store.allocate("session-1", "request-child", "response-root")
	carrier.mu.Lock()
	carrier.lineage = &child
	carrier.mu.Unlock()

	input := tokenEstimatorObservationInput{
		ProviderID:         "provider-a",
		EndpointType:       "responses",
		FinalUpstreamModel: "model-a",
		Snapshot:           estimatorSnapshot{BaseEstimate: 40},
		Coordinate: estimatorCoordinate{
			LineageWireFingerprint: "wire-family",
			PrefixFingerprint:      "prefix-child",
			PrefixUnits:            15,
			StructuralUnits:        2,
			LocalEstimate:          40,
			PrefixPoints: []estimatorPrefixPoint{{
				Fingerprint:     "prefix-root",
				PrefixUnits:     10,
				StructuralUnits: 1,
				LocalEstimate:   10,
			}},
		},
	}
	input.applyLineageEstimate(ctx)
	if !input.IncrementalEstimateValid || input.ConfirmedBaseline != 123 || input.IncrementalEstimate != 30 {
		t.Fatalf("expected confirmed baseline plus branch delta, got %#v", input)
	}
	estimate := estimateFromObservationInput(ctx, input)
	if estimate.Point != 153 || estimate.Source != "lineage-parent-plus-local-delta" {
		t.Fatalf("expected 123+30 lineage estimate, got %#v", estimate)
	}

	input.FinalUpstreamModel = "model-b"
	input.IncrementalEstimateValid = false
	input.ConfirmedBaseline = 0
	input.IncrementalEstimate = 0
	input.applyLineageEstimate(ctx)
	if input.IncrementalEstimateValid {
		t.Fatal("expected a different final upstream model not to reuse the parent baseline")
	}
}

func TestRequestLineageEstimateUsesLocalDeltaWhenChildDoesNotRepeatParentPrefix(t *testing.T) {
	store := newRequestLineageStore()
	carrier := newRequestLineageCarrier("request-child", "session-1")
	ctx := withRequestLineageCarrier(context.Background(), carrier)
	ctx = withRequestLineageStore(ctx, store)
	root := store.allocate("session-1", "request-root", "")
	store.recordResponseID(root, "response-root")
	store.recordFinalizedEstimate(root, requestLineageEstimatorFact{
		Bucket:                 tokenestimator.BucketKey{ProviderID: "provider-a", EndpointType: "responses", Model: "model-a"},
		LineageWireFingerprint: "wire-family",
		PrefixFingerprint:      "prefix-root",
		PrefixUnits:            10,
		StructuralUnits:        1,
		LocalEstimate:          10,
		InputTokens:            123,
	})
	store.markCompleted(root)
	child := store.allocate("session-1", "request-child", "response-root")
	carrier.mu.Lock()
	carrier.lineage = &child
	carrier.mu.Unlock()

	input := tokenEstimatorObservationInput{
		ProviderID:         "provider-a",
		EndpointType:       "responses",
		FinalUpstreamModel: "model-a",
		Snapshot:           estimatorSnapshot{BaseEstimate: 40},
		Coordinate: estimatorCoordinate{
			LineageWireFingerprint: "wire-family",
			PrefixFingerprint:      "prefix-child",
			PrefixUnits:            15,
			StructuralUnits:        2,
			LocalEstimate:          40,
			PrefixPoints:           []estimatorPrefixPoint{{Fingerprint: "unrelated-prefix", PrefixUnits: 10, StructuralUnits: 1, LocalEstimate: 10}},
		},
	}
	input.applyLineageEstimate(ctx)
	if !input.IncrementalEstimateValid || input.ConfirmedBaseline != 123 || input.IncrementalEstimate != 40 {
		t.Fatalf("expected local child estimate as branch delta, got %#v", input)
	}
}

func TestRecordTokenEstimatorUsageStoresLatestConfirmedUsageForCompletedParent(t *testing.T) {
	store := newRequestLineageStore()
	carrier := newRequestLineageCarrier("request-root", "session-1")
	ctx := withRequestLineageCarrier(context.Background(), carrier)
	ctx = withRequestLineageStore(ctx, store)
	meta, ok := ensureResolvedRequestLineage(ctx, "session-1", "")
	if !ok {
		t.Fatal("expected root lineage allocation")
	}
	ctx = withTokenEstimatorObservation(ctx, tokenEstimatorObservationInput{
		ProviderID:         "provider-a",
		EndpointType:       "responses",
		FinalUpstreamModel: "model-a",
		Snapshot:           estimatorSnapshot{BaseEstimate: 20},
		Coordinate:         estimatorCoordinate{LineageWireFingerprint: "wire-family"},
	})
	if err := recordTokenEstimatorUsage(ctx, meta.RequestUID, usageTotals{InputTokens: 101}); err != nil {
		t.Fatalf("record first usage: %v", err)
	}
	if err := recordTokenEstimatorUsage(ctx, meta.RequestUID, usageTotals{InputTokens: 137}); err != nil {
		t.Fatalf("record final usage: %v", err)
	}
	store.recordResponseID(meta, "response-root")
	store.markCompleted(meta)

	child := store.allocate("session-1", "request-child", "response-root")
	fact, ok := store.parentFinalizedEstimate(child)
	if !ok || fact.InputTokens != 137 {
		t.Fatalf("expected latest confirmed usage 137, got fact=%#v ok=%v", fact, ok)
	}
}

func TestFormatEstimatedInputTokensHeaderOnlyUsesLineageTupleWhenConfirmed(t *testing.T) {
	canon := modelpkg.CanonicalRequest{}
	ctx := context.WithValue(context.Background(), tokenEstimatorObservationKey, tokenEstimatorObservationInput{
		ConfirmedBaseline:        123,
		IncrementalEstimate:      30,
		IncrementalEstimateValid: true,
	})
	if got := formatEstimatedInputTokensHeader(ctx, canon, estimatorEstimate{Point: 153}); got != "153 (123+30)" {
		t.Fatalf("expected confirmed lineage header, got %q", got)
	}
	if got := formatEstimatedInputTokensHeader(context.Background(), canon, estimatorEstimate{Point: 153}); got != "153" {
		t.Fatalf("expected plain numeric header without confirmed lineage, got %q", got)
	}
}

func TestResponsesLineageHeaderUsesConfirmedUsageAcrossSiblingsAndDescendants(t *testing.T) {
	var calls atomic.Int32
	responses := []struct {
		id          string
		inputTokens int
	}{
		{id: "resp-root", inputTokens: 123},
		{id: "resp-child-a", inputTokens: 146},
		{id: "resp-child-b", inputTokens: 159},
		{id: "resp-grandchild", inputTokens: 170},
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		index := int(calls.Add(1)) - 1
		if index < 0 || index >= len(responses) {
			http.Error(w, "unexpected request", http.StatusInternalServerError)
			return
		}
		response := responses[index]
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":%q,"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":%d,"output_tokens":1,"total_tokens":%d}}`, response.id, response.inputTokens, response.inputTokens+1)
	}))
	defer upstream.Close()

	manager := tokenestimator.NewManager(t.TempDir(), time.UTC, func() []string { return []string{"openai"} })
	server := NewServerWithStore(config.NewStaticRuntimeStore(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
			Enabled:              true,
			ManualModels:         []string{"gpt-5.4"},
			SupportsResponses:    true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
		}},
	}), nil, manager)

	send := func(previousResponseID, input, sessionID string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"model":"gpt-5.4","previous_response_id":%q,"input":%q}`, previousResponseID, input)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if sessionID != "" {
			req.Header.Set(headerProxySessionID, sessionID)
		}
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}

	root := send("", "root", "")
	if root.Code != http.StatusOK {
		t.Fatalf("root request failed: status=%d body=%s", root.Code, root.Body.String())
	}
	sessionID := root.Header().Get(headerProxySessionID)
	if sessionID == "" {
		t.Fatal("expected root response to expose a proxy session ID")
	}

	childA := send("resp-root", "branch-a", sessionID)
	if childA.Code != http.StatusOK {
		t.Fatalf("child A request failed: status=%d body=%s", childA.Code, childA.Body.String())
	}
	childAHeader := childA.Header().Get(headerProxyEstimatedInputTokens)
	if !strings.Contains(childAHeader, "(123+") {
		t.Fatalf("expected child A estimate to use root confirmed usage, got %q", childAHeader)
	}

	childB := send("resp-root", "branch-b with a different local suffix", sessionID)
	if childB.Code != http.StatusOK {
		t.Fatalf("child B request failed: status=%d body=%s", childB.Code, childB.Body.String())
	}
	childBHeader := childB.Header().Get(headerProxyEstimatedInputTokens)
	if !strings.Contains(childBHeader, "(123+") {
		t.Fatalf("expected child B estimate to use root confirmed usage, got %q", childBHeader)
	}
	if childAHeader == childBHeader {
		t.Fatalf("expected sibling branch deltas to remain isolated, childA=%q childB=%q", childAHeader, childBHeader)
	}

	grandchild := send("resp-child-a", "recursive descendant", sessionID)
	if grandchild.Code != http.StatusOK {
		t.Fatalf("grandchild request failed: status=%d body=%s", grandchild.Code, grandchild.Body.String())
	}
	grandchildHeader := grandchild.Header().Get(headerProxyEstimatedInputTokens)
	if !strings.Contains(grandchildHeader, "(146+") {
		t.Fatalf("expected grandchild estimate to use child A confirmed usage, got %q", grandchildHeader)
	}
	if calls.Load() != int32(len(responses)) {
		t.Fatalf("expected exactly %d upstream calls, got %d", len(responses), calls.Load())
	}
}
