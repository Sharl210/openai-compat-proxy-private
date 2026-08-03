package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/logging"
	"openai-compat-proxy/internal/model"
)

func TestExplicitSessionRequestIDsAndPersistentRecords(t *testing.T) {
	root := t.TempDir()
	upstream := newSessionChatUpstream(t)
	defer upstream.Close()
	server := NewServer(sessionObservabilityConfig(root, upstream.URL, config.UpstreamEndpointTypeChat, true, false, false))
	defer server.Close()

	send := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(headerProxySessionID, "explicit-session")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}

	first := send()
	second := send()
	for index, response := range []*httptest.ResponseRecorder{first, second} {
		if response.Code != http.StatusOK {
			t.Fatalf("request %d failed: status=%d body=%s", index, response.Code, response.Body.String())
		}
		if got := response.Header().Get(headerProxySessionID); got != "explicit-session" {
			t.Fatalf("request %d returned unexpected session %q", index, got)
		}
	}
	if got := first.Header().Get(headerProxySessionRequestID); got != "r000001" {
		t.Fatalf("expected first session request ID r000001, got %q", got)
	}
	if got := second.Header().Get(headerProxySessionRequestID); got != "r000002" {
		t.Fatalf("expected second session request ID r000002, got %q", got)
	}
	if first.Header().Get("X-Request-Id") == second.Header().Get("X-Request-Id") {
		t.Fatal("expected globally unique X-Request-Id values")
	}

	records, err := logging.NewSessionRequestIndex(root).Lookup("explicit-session")
	if err != nil {
		t.Fatalf("lookup persistent session records: %v", err)
	}
	completed := completedSessionRecords(records)
	if len(completed) != 2 {
		t.Fatalf("expected two completed session records, got %#v", records)
	}
	if completed[0].ConversationRequestID != "r000001" || completed[1].ConversationRequestID != "r000002" {
		t.Fatalf("unexpected persistent sequence records: %#v", completed)
	}
	for _, record := range completed {
		if record.SessionID != "explicit-session" || record.RequestUID == "" || record.XRequestID == "" || record.Timestamp == "" || record.Status != http.StatusOK || record.Route != "/v1/chat/completions" {
			t.Fatalf("persistent record missing required fields: %#v", record)
		}
	}
}

func TestMissingSessionContinuesCanonicalHistoryForChatAndAnthropic(t *testing.T) {
	t.Run("chat", func(t *testing.T) {
		root := t.TempDir()
		upstream := newSessionChatUpstream(t)
		defer upstream.Close()
		server := NewServer(sessionObservabilityConfig(root, upstream.URL, config.UpstreamEndpointTypeChat, true, false, false))
		defer server.Close()

		first := sendSessionRequest(t, server, "/chat/completions", `{"model":"gpt-5","messages":[{"role":"user","content":"one"}]}`, nil)
		second := sendSessionRequest(t, server, "/v1/chat/completions", `{"model":"gpt-5","messages":[{"role":"user","content":"one"},{"role":"assistant","content":"ok"},{"role":"user","content":"two"}]}`, nil)
		assertContinuedGeneratedSession(t, first, second)
	})

	t.Run("anthropic", func(t *testing.T) {
		root := t.TempDir()
		upstream := newSessionAnthropicUpstream(t)
		defer upstream.Close()
		server := NewServer(sessionObservabilityConfig(root, upstream.URL, config.UpstreamEndpointTypeAnthropic, false, true, false))
		defer server.Close()

		first := sendSessionRequest(t, server, "/messages", `{"model":"gpt-5","max_tokens":64,"messages":[{"role":"user","content":"one"}]}`, map[string]string{"anthropic-version": "2023-06-01"})
		second := sendSessionRequest(t, server, "/v1/messages", `{"model":"gpt-5","max_tokens":64,"messages":[{"role":"user","content":"one"},{"role":"assistant","content":"ok"},{"role":"user","content":"two"}]}`, map[string]string{"anthropic-version": "2023-06-01"})
		assertContinuedGeneratedSession(t, first, second)
	})
}

func TestMissingSessionAccessLogUsesResolvedSessionAndLineage(t *testing.T) {
	root := initMiddlewareTestLogger(t)
	upstream := newSessionChatUpstream(t)
	defer upstream.Close()
	serverConfig := sessionObservabilityConfig(root, upstream.URL, config.UpstreamEndpointTypeChat, true, false, false)
	serverConfig.Providers[0].UpstreamAPIKey = ""
	server := NewServer(serverConfig)
	defer server.Close()

	first := sendSessionRequest(t, server, "/v1/chat/completions", `{"model":"gpt-5","messages":[{"role":"user","content":"one"}]}`, map[string]string{"X-Upstream-Authorization": "Bearer test-key"})
	second := sendSessionRequest(t, server, "/v1/chat/completions", `{"model":"gpt-5","messages":[{"role":"user","content":"one"},{"role":"assistant","content":"ok"},{"role":"user","content":"two"}]}`, nil)

	sessionID := first.Header().Get(headerProxySessionID)
	if sessionID == "" || second.Header().Get(headerProxySessionID) != sessionID {
		t.Fatalf("expected both responses to use one generated session, first=%q second=%q", sessionID, second.Header().Get(headerProxySessionID))
	}
	for _, test := range []struct {
		name      string
		requestID string
		sequence  string
	}{
		{name: "first", requestID: first.Header().Get("X-Request-Id"), sequence: "r000001"},
		{name: "second", requestID: second.Header().Get("X-Request-Id"), sequence: "r000002"},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertClientToProxyRequestLog(t, root, test.requestID, sessionID, test.sequence)
		})
	}
}

func TestMissingSessionResponsesAccessLogUsesResolvedSessionAndLineage(t *testing.T) {
	root := initMiddlewareTestLogger(t)
	upstream := newSessionResponsesUpstream(t)
	defer upstream.Close()
	server := NewServer(sessionObservabilityConfig(root, upstream.URL, config.UpstreamEndpointTypeResponses, false, false, true))
	defer server.Close()

	first := sendSessionRequest(t, server, "/responses", `{"model":"gpt-5","input":[{"role":"user","content":"one"}]}`, nil)
	second := sendSessionRequest(t, server, "/v1/responses", `{"model":"gpt-5","input":[{"role":"user","content":"one"},{"role":"assistant","content":[{"type":"output_text","text":"ok"}]},{"role":"user","content":"two"}]}`, nil)
	assertContinuedGeneratedSession(t, first, second)
	assertClientToProxyRequestLog(t, root, first.Header().Get("X-Request-Id"), first.Header().Get(headerProxySessionID), "r000001")
	assertClientToProxyRequestLog(t, root, second.Header().Get("X-Request-Id"), second.Header().Get(headerProxySessionID), "r000002")
}

func TestMissingSessionFallbackAccessLogIsEmittedOnce(t *testing.T) {
	root := initMiddlewareTestLogger(t)
	lineageStore := newRequestLineageStore(logging.NewSessionRequestIndex(root))
	priorHistory := []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "one"}}}}
	lineageStore.implicitSessions.observe("prior-request", "stable-session", "/v1/chat/completions", "anonymous", priorHistory)
	lineageStore.implicitSessions.markCompleted("prior-request")
	handler := withRequestIDAndLineage(nil, lineageStore, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(withRequestLineageStore(r.Context(), lineageStore))
		continuedHistory := append(append([]model.CanonicalMessage{}, priorHistory...), model.CanonicalMessage{
			Role:  "assistant",
			Parts: []model.CanonicalContentPart{{Type: "text", Text: "ok"}},
		}, model.CanonicalMessage{
			Role:  "user",
			Parts: []model.CanonicalContentPart{{Type: "text", Text: "two"}},
		})
		r, sessionID := resolveImplicitProxySessionID(r, w, newImplicitSessionHistory(continuedHistory, nil))
		if sessionID != "stable-session" {
			t.Errorf("expected implicit handler resolution to use stable-session, got %q", sessionID)
		}
		if _, ok := ensureResolvedRequestLineage(r.Context(), sessionID, ""); !ok {
			t.Error("expected fallback handler to resolve request lineage")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assertClientToProxyRequestLog(t, root, rec.Header().Get("X-Request-Id"), "stable-session", "r000001")
}

func TestMissingSessionContinuesCanonicalHistoryForResponses(t *testing.T) {
	root := t.TempDir()
	upstream := newSessionResponsesUpstream(t)
	defer upstream.Close()
	server := NewServer(sessionObservabilityConfig(root, upstream.URL, config.UpstreamEndpointTypeResponses, false, false, true))
	defer server.Close()

	first := sendSessionRequest(t, server, "/responses", `{"model":"gpt-5","input":[{"role":"user","content":"one"}]}`, nil)
	second := sendSessionRequest(t, server, "/v1/responses", `{"model":"gpt-5","input":[{"role":"user","content":"one"},{"role":"assistant","content":[{"type":"output_text","text":"ok"}]},{"role":"user","content":"two"}]}`, nil)
	assertContinuedGeneratedSession(t, first, second)
}

func TestMissingSessionContinuesResponsesHistoryAcrossToolRoundNormalization(t *testing.T) {
	root := t.TempDir()
	upstream := newSessionResponsesUpstream(t)
	defer upstream.Close()
	server := NewServer(sessionObservabilityConfig(root, upstream.URL, config.UpstreamEndpointTypeResponses, false, false, true))
	defer server.Close()

	first := sendSessionRequest(t, server, "/v1/responses", `{"model":"gpt-5","input":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"use tools"}]},
		{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"plan"}]},
		{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{}"}
	]}`, nil)
	second := sendSessionRequest(t, server, "/responses", `{"model":"gpt-5","input":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"use tools"}]},
		{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"plan"}]},
		{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{}"},
		{"type":"function_call_output","id":"fco_1","call_id":"call_1","output":"result"},
		{"type":"function_call","id":"fc_2","call_id":"call_2","name":"lookup","arguments":"{}"},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}
	]}`, nil)

	assertContinuedGeneratedSession(t, first, second)
}

func TestResponsesPreviousResponseIDInheritsSessionAndExplicitSessionConflictIsRecorded(t *testing.T) {
	root := t.TempDir()
	upstream := newSessionResponsesUpstream(t)
	defer upstream.Close()
	server := NewServer(sessionObservabilityConfig(root, upstream.URL, config.UpstreamEndpointTypeResponses, false, false, true))
	defer server.Close()

	rootResponse := sendSessionRequest(t, server, "/v1/responses", `{"model":"gpt-5","input":[{"role":"user","content":"root"}]}`, nil)
	if rootResponse.Code != http.StatusOK {
		t.Fatalf("root Responses request failed: status=%d body=%s", rootResponse.Code, rootResponse.Body.String())
	}
	rootSessionID := rootResponse.Header().Get(headerProxySessionID)
	if rootSessionID == "" || rootResponse.Header().Get(headerProxySessionRequestID) != "r000001" {
		t.Fatalf("expected root session and r000001, got session=%q request=%q", rootSessionID, rootResponse.Header().Get(headerProxySessionRequestID))
	}
	var rootPayload map[string]any
	if err := json.Unmarshal(rootResponse.Body.Bytes(), &rootPayload); err != nil {
		t.Fatalf("decode root Responses response: %v", err)
	}
	rootResponseID, _ := rootPayload["id"].(string)
	if rootResponseID == "" {
		t.Fatalf("expected root response ID, got %s", rootResponse.Body.String())
	}

	inherited := sendSessionRequest(t, server, "/v1/responses", fmt.Sprintf(`{"model":"gpt-5","previous_response_id":%q,"input":[{"role":"user","content":"child"}]}`, rootResponseID), nil)
	if inherited.Code != http.StatusOK {
		t.Fatalf("inherited Responses request failed: status=%d body=%s", inherited.Code, inherited.Body.String())
	}
	if got := inherited.Header().Get(headerProxySessionID); got != rootSessionID {
		t.Fatalf("expected previous_response_id session inheritance %q, got %q", rootSessionID, got)
	}
	if got := inherited.Header().Get(headerProxySessionRequestID); got != "r000002" {
		t.Fatalf("expected inherited session request ID r000002, got %q", got)
	}

	conflict := sendSessionRequest(t, server, "/v1/responses", fmt.Sprintf(`{"model":"gpt-5","previous_response_id":%q,"input":[{"role":"user","content":"conflict"}]}`, rootResponseID), map[string]string{headerProxySessionID: "explicit-other-session"})
	if conflict.Code != http.StatusOK {
		t.Fatalf("conflicting Responses request failed: status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	if got := conflict.Header().Get(headerProxySessionID); got != "explicit-other-session" {
		t.Fatalf("explicit session did not win conflict: got %q", got)
	}
	if got := conflict.Header().Get(headerProxySessionRequestID); got != "r000001" {
		t.Fatalf("expected independent conflicting session sequence r000001, got %q", got)
	}
	records, err := logging.NewSessionRequestIndex(root).Lookup("explicit-other-session")
	if err != nil {
		t.Fatalf("lookup conflicting session: %v", err)
	}
	completed := completedSessionRecords(records)
	if len(completed) != 1 || !completed[0].SessionConflict || completed[0].SessionConflictWith != rootSessionID {
		t.Fatalf("expected explicit-session conflict marker without merge, got %#v", completed)
	}
}

func TestParseFailureStillCreatesPersistentSessionIndexRecord(t *testing.T) {
	root := t.TempDir()
	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		LogFilePath:                 root,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
			Enabled:              true,
			ManualModels:         []string{"gpt-5"},
			SupportsChat:         true,
			UpstreamBaseURL:      "https://upstream.invalid",
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeChat,
		}},
	})
	defer server.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected parse failure status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	sessionID := rec.Header().Get(headerProxySessionID)
	if sessionID == "" {
		t.Fatal("expected generated session ID on parse failure")
	}
	records, err := logging.NewSessionRequestIndex(root).Lookup(sessionID)
	if err != nil {
		t.Fatalf("lookup parse-failure session records: %v", err)
	}
	completed := completedSessionRecords(records)
	if len(completed) != 1 || completed[0].Status != http.StatusBadRequest || completed[0].Route != "/v1/chat/completions" {
		t.Fatalf("expected usable parse-failure index record, got %#v", completed)
	}
}

func TestEnsureResolvedRequestLineageIsIdempotentUnderConcurrency(t *testing.T) {
	store := newRequestLineageStore(logging.NewSessionRequestIndex(t.TempDir()))
	carrier := newRequestLineageCarrier("request-id", "same-session")
	ctx := withRequestLineageStore(withRequestLineageCarrier(context.Background(), carrier), store)
	const callCount = 20
	metas := make(chan requestLineage, callCount)
	var group sync.WaitGroup
	for i := 0; i < callCount; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			meta, ok := ensureResolvedRequestLineage(ctx, "same-session", "")
			if !ok {
				t.Errorf("expected idempotent lineage allocation")
				return
			}
			metas <- meta
		}()
	}
	group.Wait()
	close(metas)
	for meta := range metas {
		if meta.ConversationRequestSeq != 1 || meta.ConversationRequestID != "r000001" {
			t.Fatalf("duplicate ensure allocated unexpected lineage: %#v", meta)
		}
	}
	if got := len(store.conversations["same-session"].Nodes); got != 1 {
		t.Fatalf("expected one active lineage node, got %d", got)
	}
}

func TestRequestLineageStoreRestartAndConcurrentAllocationContinueSequence(t *testing.T) {
	root := t.TempDir()
	firstStore := newRequestLineageStore(logging.NewSessionRequestIndex(root))
	if got := firstStore.allocate("restart-session", "req-first", "").ConversationRequestSeq; got != 1 {
		t.Fatalf("expected first sequence 1, got %d", got)
	}

	secondStore := newRequestLineageStore(logging.NewSessionRequestIndex(root))
	if got := secondStore.allocate("restart-session", "req-after-restart", "").ConversationRequestSeq; got != 2 {
		t.Fatalf("expected sequence 2 after restart, got %d", got)
	}

	const requestCount = 16
	sequences := make(chan uint64, requestCount)
	var group sync.WaitGroup
	for i := 0; i < requestCount; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			sequences <- secondStore.allocate("restart-session", fmt.Sprintf("req-concurrent-%d", index), "").ConversationRequestSeq
		}(i)
	}
	group.Wait()
	close(sequences)
	got := make([]int, 0, requestCount)
	for sequence := range sequences {
		got = append(got, int(sequence))
	}
	sort.Ints(got)
	for index, sequence := range got {
		if sequence != index+3 {
			t.Fatalf("expected concurrent sequence %d at position %d, got %d", index+3, index, sequence)
		}
	}
}

func sessionObservabilityConfig(root, upstreamURL, endpoint string, supportsChat, supportsAnthropic, supportsResponses bool) config.Config {
	return config.Config{
		DefaultProvider:             "provider",
		EnableLegacyV1Routes:        true,
		LogFilePath:                 root,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                        "provider",
			Enabled:                   true,
			ManualModels:              []string{"gpt-5"},
			SupportsChat:              supportsChat,
			SupportsAnthropicMessages: supportsAnthropic,
			SupportsResponses:         supportsResponses,
			UpstreamBaseURL:           upstreamURL,
			UpstreamAPIKey:            "test-key",
			UpstreamEndpointType:      endpoint,
		}},
	}
}

func sendSessionRequest(t *testing.T, server *Server, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

func assertContinuedGeneratedSession(t *testing.T, first, second *httptest.ResponseRecorder) {
	t.Helper()
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("expected successful requests, first=%d second=%d", first.Code, second.Code)
	}
	firstSession := first.Header().Get(headerProxySessionID)
	secondSession := second.Header().Get(headerProxySessionID)
	if firstSession == "" || secondSession == "" || firstSession != secondSession {
		t.Fatalf("expected canonical history to continue one generated session, first=%q second=%q", firstSession, secondSession)
	}
	if first.Header().Get(headerProxySessionRequestID) != "r000001" || second.Header().Get(headerProxySessionRequestID) != "r000002" {
		t.Fatalf("expected continued generated session IDs r000001/r000002, first=%q second=%q", first.Header().Get(headerProxySessionRequestID), second.Header().Get(headerProxySessionRequestID))
	}
}

func assertClientToProxyRequestLog(t *testing.T, logDir, requestID, sessionID, requestSequence string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(logDir, requestID+".txt"))
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	clientIndex := -1
	responseIndex := -1
	clientEventCount := 0
	var clientEvent map[string]any
	for index, line := range lines {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode log line %d: %v", index, err)
		}
		switch eventName, _ := event["event"].(string); eventName {
		case "clientToProxyRequest":
			clientEventCount++
			clientIndex = index
			clientEvent = event
		case "proxyToClientResponse":
			responseIndex = index
		}
	}
	if clientEventCount != 1 || clientIndex < 0 || responseIndex < 0 || clientEvent == nil {
		t.Fatalf("expected one client event and one response event, got %s", data)
	}
	if clientIndex >= responseIndex {
		t.Fatalf("expected client event before response event, client=%d response=%d", clientIndex, responseIndex)
	}
	if got, _ := clientEvent["session_id"].(string); got != sessionID {
		t.Fatalf("client event used session %q, expected %q", got, sessionID)
	}
	if got, _ := clientEvent["conversation_request_id"].(string); got != requestSequence {
		t.Fatalf("client event used request sequence %q, expected %q", got, requestSequence)
	}
}

func completedSessionRecords(records []logging.SessionRequestRecord) []logging.SessionRequestRecord {
	completed := make([]logging.SessionRequestRecord, 0, len(records))
	for _, record := range records {
		if record.Event == "completed" {
			completed = append(completed, record)
		}
	}
	sort.Slice(completed, func(left, right int) bool {
		return completed[left].ConversationRequestSeq < completed[right].ConversationRequestSeq
	})
	return completed
}

func newSessionChatUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_session","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
}

func newSessionAnthropicUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_session","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
}

func newSessionResponsesUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	var calls atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		responseID := fmt.Sprintf("resp-session-%d", calls.Add(1))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":%q,"object":"response","output":[{"id":"msg_session","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`, responseID)
	}))
}
