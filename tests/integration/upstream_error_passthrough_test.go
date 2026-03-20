package integration_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestResponsesHandlerPassesThroughUpstreamJSONError(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream bad request","type":"invalid_request_error","param":"model","code":"bad_model"}}`))
	}))
	defer stub.Close()

	server := newServerWithStubbedUpstream(t, stub.URL)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"x","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected upstream status 400, got %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	errBody, _ := body["error"].(map[string]any)
	if errBody["message"] != "upstream bad request" || errBody["code"] != "bad_model" || errBody["type"] != "invalid_request_error" || errBody["param"] != "model" {
		t.Fatalf("expected passthrough upstream error, got %#v", body)
	}
}

func TestChatHandlerPassesThroughUpstreamJSONError(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream bad request","type":"invalid_request_error","param":"model","code":"bad_model"}}`))
	}))
	defer stub.Close()

	server := newServerWithStubbedUpstream(t, stub.URL)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"x","messages":[{"role":"user","content":"hi"}],"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected upstream status 400, got %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	errBody, _ := body["error"].(map[string]any)
	if errBody["message"] != "upstream bad request" || errBody["code"] != "bad_model" {
		t.Fatalf("expected passthrough upstream error, got %#v", body)
	}
}

func TestAnthropicHandlerPassesThroughUpstreamJSONError(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream bad request","type":"invalid_request_error","param":"model","code":"bad_model"}}`))
	}))
	defer stub.Close()

	server := newTestServerWithConfig(t, config.Config{ListenAddr: ":0", Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: stub.URL, UpstreamAPIKey: "server-key", SupportsAnthropicMessages: true}}})
	defer server.Close()

	resp, err := http.Post(server.URL+"/openai/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected upstream status 400, got %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	errBody, _ := body["error"].(map[string]any)
	if errBody["message"] != "upstream bad request" || errBody["code"] != "bad_model" {
		t.Fatalf("expected passthrough upstream error, got %#v", body)
	}
}
