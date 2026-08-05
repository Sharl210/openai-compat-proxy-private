package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	responsesadapter "openai-compat-proxy/internal/adapter/responses"
	"openai-compat-proxy/internal/config"
	modelpkg "openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/tokenestimator"
	"openai-compat-proxy/internal/upstream"
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

func TestRecordResponsesLineageResultFallsBackToEstimateWithoutUsage(t *testing.T) {
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
		EndpointType:       "chat",
		FinalUpstreamModel: "model-a",
		Snapshot:           estimatorSnapshot{BaseEstimate: 280},
		Coordinate: estimatorCoordinate{
			LineageWireFingerprint: "wire-family",
			PrefixFingerprint:      "prefix-root",
			LocalEstimate:          280,
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model-a"}`)).WithContext(ctx)
	recordResponsesLineageResult(req, "response-root")
	store.markCompleted(meta)

	child := store.allocate("session-1", "request-child", "response-root")
	fact, ok := store.parentFinalizedEstimate(child)
	if !ok || fact.InputTokens != 280 {
		t.Fatalf("expected estimate fallback to establish parent baseline 280, got fact=%#v ok=%v", fact, ok)
	}
}

func TestFormatEstimatedInputTokensHeaderOnlyUsesLineageTupleWhenConfirmed(t *testing.T) {
	canon := modelpkg.CanonicalRequest{}
	ctx := context.WithValue(context.Background(), tokenEstimatorObservationKey, tokenEstimatorObservationInput{
		ConfirmedBaseline:        123,
		IncrementalEstimate:      30,
		IncrementalEstimateValid: true,
	})
	if got := formatEstimatedInputTokensHeader(ctx, canon, estimatorEstimate{Point: 153}); got != "153(123+30)" {
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

func TestResponsesLineageHeaderUsesEstimatedParentWhenUpstreamOmitsUsage(t *testing.T) {
	server := newResponsesLineageTestServer(t, []responsesLineageTestReply{
		{id: "resp-root", omitUsage: true},
		{id: "resp-child", omitUsage: true},
	})

	root := sendResponsesLineageRequest(t, server, `{"model":"gpt-5.4","input":"root"}`, "")
	requireResponsesLineageSuccess(t, "root without usage", root)
	rootEstimate := root.Header().Get(headerProxyEstimatedInputTokens)
	requirePlainEstimatedInputTokensHeader(t, rootEstimate)
	baseline, err := strconv.Atoi(rootEstimate)
	if err != nil {
		t.Fatalf("parse root estimate %q: %v", rootEstimate, err)
	}

	sessionID := root.Header().Get(headerProxySessionID)
	if sessionID == "" {
		t.Fatal("expected root response to expose a proxy session ID")
	}
	child := sendResponsesLineageRequest(t, server, `{"model":"gpt-5.4","previous_response_id":"resp-root","input":"child"}`, sessionID)
	requireResponsesLineageSuccess(t, "child without usage", child)
	requireConfirmedLineageEstimateHeader(t, child.Header().Get(headerProxyEstimatedInputTokens), baseline)
}

func TestResponsesImplicitHistoryLineageUsesMatchedParentInsteadOfNewestSessionRequest(t *testing.T) {
	server := newResponsesLineageTestServer(t, []responsesLineageTestReply{
		{id: "resp-root", inputTokens: 123},
		{id: "resp-unrelated", inputTokens: 777},
		{id: "resp-continuation", inputTokens: 160},
	})

	root := sendResponsesLineageRequest(t, server, `{"model":"gpt-5.4","input":[{"role":"user","content":"one"}]}`, "")
	requireResponsesLineageSuccess(t, "root", root)
	sessionID := root.Header().Get(headerProxySessionID)
	if sessionID == "" {
		t.Fatal("expected root request to establish a proxy session")
	}

	unrelated := sendResponsesLineageRequest(t, server, `{"model":"gpt-5.4","input":[{"role":"user","content":"unrelated"}]}`, sessionID)
	requireResponsesLineageSuccess(t, "unrelated", unrelated)

	continuation := sendResponsesLineageRequest(t, server, implicitResponsesContinuationInput(), "")
	requireResponsesLineageSuccess(t, "implicit continuation", continuation)
	if got := continuation.Header().Get(headerProxySessionID); got != sessionID {
		t.Fatalf("expected uniquely matched history to reuse session %q, got %q", sessionID, got)
	}
	requireConfirmedLineageEstimateHeader(t, continuation.Header().Get(headerProxyEstimatedInputTokens), 123)
}

func TestResponsesAmbiguousImplicitHistoryDoesNotInheritLineage(t *testing.T) {
	server := newResponsesLineageTestServer(t, []responsesLineageTestReply{
		{id: "resp-first", inputTokens: 123},
		{id: "resp-second", inputTokens: 777},
		{id: "resp-ambiguous", inputTokens: 160},
	})

	first := sendResponsesLineageRequest(t, server, `{"model":"gpt-5.4","input":[{"role":"user","content":"one"}]}`, "")
	requireResponsesLineageSuccess(t, "first root", first)
	firstSessionID := first.Header().Get(headerProxySessionID)
	second := sendResponsesLineageRequest(t, server, `{"model":"gpt-5.4","input":[{"role":"user","content":"one"}]}`, "")
	requireResponsesLineageSuccess(t, "second root", second)
	secondSessionID := second.Header().Get(headerProxySessionID)
	if firstSessionID == "" || secondSessionID == "" || firstSessionID == secondSessionID {
		t.Fatalf("expected independent roots to establish distinct sessions, first=%q second=%q", firstSessionID, secondSessionID)
	}

	continuation := sendResponsesLineageRequest(t, server, implicitResponsesContinuationInput(), "")
	requireResponsesLineageSuccess(t, "ambiguous continuation", continuation)
	if got := continuation.Header().Get(headerProxySessionID); got == firstSessionID || got == secondSessionID {
		t.Fatalf("expected ambiguous history not to reuse either matching session, got %q", got)
	}
	requirePlainEstimatedInputTokensHeader(t, continuation.Header().Get(headerProxyEstimatedInputTokens))
}

func TestResponsesExplicitSessionHistoryDoesNotInheritUnverifiedImplicitParent(t *testing.T) {
	server := newResponsesLineageTestServer(t, []responsesLineageTestReply{
		{id: "resp-root", inputTokens: 123},
		{id: "resp-explicit-continuation", inputTokens: 160},
	})
	const sessionID = "explicit-session"

	root := sendResponsesLineageRequest(t, server, `{"model":"gpt-5.4","input":[{"role":"user","content":"one"}]}`, sessionID)
	requireResponsesLineageSuccess(t, "explicit root", root)
	continuation := sendResponsesLineageRequest(t, server, implicitResponsesContinuationInput(), sessionID)
	requireResponsesLineageSuccess(t, "explicit continuation", continuation)
	if got := continuation.Header().Get(headerProxySessionID); got != sessionID {
		t.Fatalf("expected explicit session %q to remain unchanged, got %q", sessionID, got)
	}
	requirePlainEstimatedInputTokensHeader(t, continuation.Header().Get(headerProxyEstimatedInputTokens))
}

func TestResponsesImplicitHistoryBindsMatchedSessionToOriginalRequestContext(t *testing.T) {
	store := newRequestLineageStore()
	decoded, err := responsesadapter.DecodeRequest(strings.NewReader(implicitResponsesContinuationInput()))
	if err != nil {
		t.Fatalf("decode canonical Responses fixture: %v", err)
	}
	rootHistory := newImplicitSessionHistory(nil, decoded.ResponseInputItems[:2])
	store.implicitSessions.observeHistory("request-root", "session-root", "/v1/responses", "caller-1", rootHistory)
	store.implicitSessions.markCompleted("request-root")

	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(implicitResponsesContinuationInput()))
	request.Header.Set("Content-Type", "application/json")
	carrier := newRequestLineageCarrier("request-child", "")
	ctx := withRequestLineageCarrier(context.Background(), carrier)
	ctx = withRequestLineageStore(ctx, store)
	ctx = withInboundCallerIdentity(ctx, "caller-1")
	ctx = withRuntimeSnapshot(ctx, config.NewStaticRuntimeStore(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			ManualModels:      []string{"gpt-5.4"},
			SupportsResponses: true,
		}},
	}).Active())
	ctx = withRouteInfo(ctx, routeInfo{ProviderID: "openai", CanonicalPath: "/v1/responses", Legacy: true})
	request = request.WithContext(ctx)
	recorder := httptest.NewRecorder()

	initial, ok := decodeAndResolveResponsesRequest(recorder, request)
	if !ok || initial == nil {
		t.Fatalf("expected Responses request decoding and routing to succeed, status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := upstream.SessionIDFromContext(request.Context()); got != "session-root" {
		t.Fatalf("expected matched session in upstream context, got %q", got)
	}
	if got := proxySessionIDFromRequest(request); got != "session-root" {
		t.Fatalf("expected matched session in request context, got %q", got)
	}
	if initial.canon.SessionID != "session-root" {
		t.Fatalf("expected canonical request to use matched session, got %q", initial.canon.SessionID)
	}
}

type responsesLineageTestReply struct {
	id          string
	inputTokens int
	omitUsage   bool
}

func newResponsesLineageTestServer(t *testing.T, replies []responsesLineageTestReply) *Server {
	t.Helper()
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		index := int(calls.Add(1)) - 1
		if index < 0 || index >= len(replies) {
			http.Error(w, "unexpected request", http.StatusInternalServerError)
			return
		}
		reply := replies[index]
		w.Header().Set("Content-Type", "application/json")
		body := fmt.Sprintf(`{"id":%q,"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]`, reply.id)
		if !reply.omitUsage {
			body += fmt.Sprintf(`,"usage":{"input_tokens":%d,"output_tokens":1,"total_tokens":%d}`, reply.inputTokens, reply.inputTokens+1)
		}
		body += `}`
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(upstream.Close)

	manager := tokenestimator.NewManager(t.TempDir(), time.UTC, func() []string { return []string{"openai"} })
	return NewServerWithStore(config.NewStaticRuntimeStore(config.Config{
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
}

func sendResponsesLineageRequest(t *testing.T, server *Server, body, sessionID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		req.Header.Set(headerProxySessionID, sessionID)
	}
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

func implicitResponsesContinuationInput() string {
	return `{"model":"gpt-5.4","input":[{"role":"user","content":"one"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]},{"role":"user","content":"two"}]}`
}

func requireResponsesLineageSuccess(t *testing.T, name string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("%s request failed: status=%d body=%s", name, rec.Code, rec.Body.String())
	}
}

func requireConfirmedLineageEstimateHeader(t *testing.T, value string, baseline int) {
	t.Helper()
	open := strings.IndexByte(value, '(')
	if open <= 0 || !strings.HasSuffix(value, ")") {
		t.Fatalf("expected total(confirmed+increment) estimate header, got %q", value)
	}
	total, err := strconv.Atoi(value[:open])
	if err != nil {
		t.Fatalf("parse total estimate %q: %v", value, err)
	}
	parts := strings.Split(strings.TrimSuffix(value[open+1:], ")"), "+")
	if len(parts) != 2 {
		t.Fatalf("expected confirmed+increment tuple, got %q", value)
	}
	confirmed, err := strconv.Atoi(parts[0])
	if err != nil {
		t.Fatalf("parse confirmed estimate %q: %v", value, err)
	}
	increment, err := strconv.Atoi(parts[1])
	if err != nil {
		t.Fatalf("parse incremental estimate %q: %v", value, err)
	}
	if confirmed != baseline || increment <= 0 || total != confirmed+increment {
		t.Fatalf("expected total(%d+positive increment), got %q", baseline, value)
	}
}

func requirePlainEstimatedInputTokensHeader(t *testing.T, value string) {
	t.Helper()
	if strings.Contains(value, "(") {
		t.Fatalf("expected a plain estimated-token header without inherited lineage, got %q", value)
	}
	if tokens, err := strconv.Atoi(value); err != nil || tokens <= 0 {
		t.Fatalf("expected a positive plain estimated-token header, got %q", value)
	}
}
