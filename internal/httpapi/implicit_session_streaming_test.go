package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestImplicitSessionStreamingOutputMakesSessionReusableBeforeCompletion(t *testing.T) {
	lineageStore := newRequestLineageStore()
	firstOutput := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstMeta := make(chan requestLineage, 1)
	secondMeta := make(chan requestLineage, 1)
	var requestNumber atomic.Int32

	baseHistory := []map[string]any{
		{"type": "message", "role": "user", "content": []any{"one"}},
		{"type": "message", "role": "assistant", "content": []any{"ok"}},
	}

	handler := withRequestIDAndLineage(nil, lineageStore, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(withRequestLineageStore(r.Context(), lineageStore))
		requestIndex := requestNumber.Add(1)
		historyItems := append([]map[string]any(nil), baseHistory...)
		if requestIndex > 1 {
			historyItems = append(historyItems, map[string]any{
				"type":    "message",
				"role":    "user",
				"content": []any{"two"},
			})
		}
		r, sessionID := resolveImplicitProxySessionID(r, w, newImplicitSessionHistory(nil, historyItems))
		meta, ok := ensureResolvedRequestLineage(r.Context(), sessionID, "")
		if !ok {
			t.Errorf("expected request lineage allocation for request %d", requestIndex)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		writer := &ResponsesEventWriter{w: w, flusher: flusher}
		if requestIndex == 1 {
			firstMeta <- meta
			if err := writer.WriteEvent("response.output_text.delta", map[string]any{"delta": "first"}); err != nil {
				t.Errorf("write first response event: %v", err)
			}
			close(firstOutput)
			<-releaseFirst
			return
		}

		secondMeta <- meta
		if err := writer.WriteEvent("response.completed", map[string]any{"response": map[string]any{"status": "completed"}}); err != nil {
			t.Errorf("write second response event: %v", err)
		}
	}))
	server := httptest.NewServer(handler)
	defer server.Close()

	firstResponse := make(chan *http.Response, 1)
	firstError := make(chan error, 1)
	go func() {
		request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", nil)
		if err != nil {
			firstError <- err
			return
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			firstError <- err
			return
		}
		firstResponse <- response
	}()

	first := <-firstMeta
	<-firstOutput

	secondRequest, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	secondResponse, err := http.DefaultClient.Do(secondRequest)
	if err != nil {
		t.Fatalf("send overlapping second request: %v", err)
	}
	_, _ = io.ReadAll(secondResponse.Body)
	_ = secondResponse.Body.Close()
	second := <-secondMeta

	if second.ConversationID != first.ConversationID {
		t.Fatalf("expected overlapping request to reuse session %q, got %q", first.ConversationID, second.ConversationID)
	}
	if second.ConversationRequestID != "r000002" {
		t.Fatalf("expected overlapping request sequence r000002, got %q", second.ConversationRequestID)
	}
	if got := secondResponse.Header.Get(headerProxySessionID); got != first.ConversationID {
		t.Fatalf("expected response session header %q, got %q", first.ConversationID, got)
	}

	close(releaseFirst)
	select {
	case err := <-firstError:
		t.Fatalf("send first request: %v", err)
	case response := <-firstResponse:
		_, _ = io.ReadAll(response.Body)
		_ = response.Body.Close()
		if got := response.Header.Get(headerProxySessionID); got != first.ConversationID {
			t.Fatalf("expected first response session header %q, got %q", first.ConversationID, got)
		}
	}
}

func TestImplicitSessionFinalFailureDoesNotKeepSessionReusable(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		write func(http.ResponseWriter, http.Flusher) error
	}{
		{
			name: "responses",
			path: "/v1/responses",
			write: func(w http.ResponseWriter, flusher http.Flusher) error {
				writer := &ResponsesEventWriter{w: w, flusher: flusher}
				if err := writer.WriteEvent("response.output_text.delta", map[string]any{"delta": "partial"}); err != nil {
					return err
				}
				return writer.WriteEvent("response.failed", map[string]any{
					"type":        "response.failed",
					"health_flag": "test_failure",
					"message":     "stream failed after partial output",
				})
			},
		},
		{
			name: "chat",
			path: "/v1/chat/completions",
			write: func(w http.ResponseWriter, flusher http.Flusher) error {
				state := &chatStreamState{
					chunkID:         "chatcmpl_test",
					toolIDAliases:   map[string]string{},
					toolMeta:        map[string]map[string]string{},
					toolIndex:       map[string]int{},
					toolSent:        map[string]bool{},
					pendingToolArgs: map[string]string{},
				}
				writer := NewChatEventWriter(w, flusher, state, nil, nil)
				if err := writer.WriteEvent("response.output_text.delta", map[string]any{"delta": "partial"}); err != nil {
					return err
				}
				return writer.WriteEvent("response.failed", map[string]any{
					"type":        "response.failed",
					"health_flag": "test_failure",
					"message":     "stream failed after partial output",
				})
			},
		},
		{
			name: "anthropic",
			path: "/v1/messages",
			write: func(w http.ResponseWriter, flusher http.Flusher) error {
				state := &anthropicStreamState{
					pendingToolArgs:  map[string]string{},
					toolMeta:         map[string]map[string]string{},
					emittedToolItems: map[string]bool{},
				}
				writer := NewAnthropicEventWriter(w, flusher, state, nil, nil)
				if err := writer.WriteEvent("response.output_text.delta", map[string]any{"delta": "partial"}); err != nil {
					return err
				}
				return writer.WriteEvent("response.failed", map[string]any{
					"type":        "response.failed",
					"health_flag": "test_failure",
					"message":     "stream failed after partial output",
				})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lineageStore := newRequestLineageStore()
			historyItems := []map[string]any{
				{"type": "message", "role": "user", "content": []any{"same history"}},
			}
			handler := withRequestIDAndLineage(nil, lineageStore, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r = r.Clone(withRequestLineageStore(r.Context(), lineageStore))
				r, sessionID := resolveImplicitProxySessionID(r, w, newImplicitSessionHistory(nil, historyItems))
				if _, ok := ensureResolvedRequestLineage(r.Context(), sessionID, ""); !ok {
					t.Error("expected request lineage allocation")
					return
				}
				flusher := startSSE(w)
				if err := test.write(w, flusher); err != nil {
					t.Errorf("write terminal failure stream: %v", err)
				}
			}))

			firstRequest := httptest.NewRequest(http.MethodPost, test.path, nil)
			firstResponse := httptest.NewRecorder()
			handler.ServeHTTP(firstResponse, firstRequest)
			firstSessionID := firstResponse.Header().Get(headerProxySessionID)
			if firstSessionID == "" {
				t.Fatal("expected first response session ID")
			}

			secondRequest := httptest.NewRequest(http.MethodPost, test.path, nil)
			secondResponse := httptest.NewRecorder()
			handler.ServeHTTP(secondResponse, secondRequest)
			secondSessionID := secondResponse.Header().Get(headerProxySessionID)
			if secondSessionID == "" {
				t.Fatal("expected second response session ID")
			}
			if secondSessionID == firstSessionID {
				t.Fatalf("expected final stream failure to prevent reuse of session %q", firstSessionID)
			}
		})
	}
}
