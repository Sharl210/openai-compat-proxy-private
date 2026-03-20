package integration_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestProviderReasoningSuffixAppliesBeforeModelMapping(t *testing.T) {
	var receivedModel string
	var receivedEffort string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		receivedModel, _ = body["model"].(string)
		if reasoning, ok := body["reasoning"].(map[string]any); ok {
			receivedEffort, _ = reasoning["effort"].(string)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {}\n\n"))
	}))
	defer stub.Close()

	server := newTestServerWithConfig(t, config.Config{
		ListenAddr: ":0",
		Providers: []config.ProviderConfig{{
			ID:                          "openai",
			Enabled:                     true,
			UpstreamBaseURL:             stub.URL,
			UpstreamAPIKey:              "server-key",
			SupportsResponses:           true,
			ModelMap:                    map[string]string{"gpt-5": "gpt-5.4"},
			EnableReasoningEffortSuffix: true,
		}},
	})
	defer server.Close()

	resp, err := http.Post(server.URL+"/openai/v1/responses", "application/json", strings.NewReader(`{"model":"gpt-5-high","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if receivedModel != "gpt-5.4" {
		t.Fatalf("expected mapped model gpt-5.4, got %q", receivedModel)
	}
	if receivedEffort != "high" {
		t.Fatalf("expected reasoning effort high, got %q", receivedEffort)
	}
}

func TestProviderReasoningSuffixDisabledLeavesModelUntouched(t *testing.T) {
	var receivedModel string
	var hasReasoning bool
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		receivedModel, _ = body["model"].(string)
		_, hasReasoning = body["reasoning"]
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"delta\":\"ok\"}\n\n"))
		_, _ = w.Write([]byte("event: response.completed\ndata: {}\n\n"))
	}))
	defer stub.Close()

	server := newTestServerWithConfig(t, config.Config{
		ListenAddr: ":0",
		Providers: []config.ProviderConfig{{
			ID:                          "openai",
			Enabled:                     true,
			UpstreamBaseURL:             stub.URL,
			UpstreamAPIKey:              "server-key",
			SupportsChat:                true,
			EnableReasoningEffortSuffix: false,
		}},
	})
	defer server.Close()

	resp, err := http.Post(server.URL+"/openai/v1/chat/completions", "application/json", strings.NewReader(`{"model":"custom-high","messages":[{"role":"user","content":"hi"}],"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if receivedModel != "custom-high" {
		t.Fatalf("expected model to remain custom-high, got %q", receivedModel)
	}
	if hasReasoning {
		t.Fatal("expected no reasoning object when suffix feature is disabled")
	}
}
