package integration_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestProviderScopedModelsRouteUsesSelectedProvider(t *testing.T) {
	openAIStub := newJSONUpstream(t, http.StatusOK, `{"object":"list","data":[{"id":"openai-model"}]}`)
	defer openAIStub.Close()

	anthropicStub := newJSONUpstream(t, http.StatusOK, `{"object":"list","data":[{"id":"anthropic-model"}]}`)
	defer anthropicStub.Close()

	server := newTestServerWithConfig(t, config.Config{
		ListenAddr: ":0",
		Providers: []config.ProviderConfig{
			{ID: "openai", Enabled: true, UpstreamBaseURL: openAIStub.URL, UpstreamAPIKey: "openai-key", SupportsModels: true},
			{ID: "anthropic", Enabled: true, UpstreamBaseURL: anthropicStub.URL, UpstreamAPIKey: "anthropic-key", SupportsModels: true},
		},
	})
	defer server.Close()

	resp, err := http.Get(server.URL + "/openai/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body := readBodyString(t, resp)
	if !strings.Contains(body, "openai-model") {
		t.Fatalf("expected openai provider body, got %s", body)
	}
}

func TestLegacyModelsRouteUsesDefaultProvider(t *testing.T) {
	openAIStub := newJSONUpstream(t, http.StatusOK, `{"object":"list","data":[{"id":"default-model"}]}`)
	defer openAIStub.Close()

	server := newTestServerWithConfig(t, config.Config{
		ListenAddr:           ":0",
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers:            []config.ProviderConfig{{ID: "openai", Enabled: true, IsDefault: true, UpstreamBaseURL: openAIStub.URL, UpstreamAPIKey: "openai-key", SupportsModels: true}},
	})
	defer server.Close()

	resp, err := http.Get(server.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body := readBodyString(t, resp)
	if !strings.Contains(body, "default-model") {
		t.Fatalf("expected default provider body, got %s", body)
	}
}

func newJSONUpstream(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

func readBodyString(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}

func TestProviderScopedRouteRejectsUnknownProvider(t *testing.T) {
	server := newTestServerWithConfig(t, config.Config{
		ListenAddr: ":0",
		Providers:  []config.ProviderConfig{{ID: "openai", Enabled: true, SupportsModels: true}},
	})
	defer server.Close()

	resp, err := http.Get(server.URL + "/missing/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestProviderScopedAnthropicAliasRouteUsesMessagesHandler(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n")
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}}\n\n")
	}))
	defer upstream.Close()

	server := newTestServerWithConfig(t, config.Config{
		ListenAddr: ":0",
		Providers:  []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "server-key", SupportsAnthropicMessages: true}},
	})
	defer server.Close()

	resp, err := http.Post(server.URL+"/openai/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet","max_tokens":128,"messages":[{"role":"user","content":"hi"}],"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := readBodyString(t, resp)
	if !strings.Contains(body, `"type":"message"`) {
		t.Fatalf("expected anthropic message response, got %s", body)
	}
}
