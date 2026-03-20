package integration_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestModelsHandlerRelaysUpstreamModels(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer server-key" {
			t.Fatalf("unexpected authorization header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5"},{"id":"gpt-5.4"}]}`))
	}))
	defer stub.Close()

	server := newServerWithStubbedUpstream(t, stub.URL)
	defer server.Close()

	resp, err := http.Get(server.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["object"] != "list" {
		t.Fatalf("unexpected models body: %#v", body)
	}
	data, ok := body["data"].([]any)
	if !ok || len(data) != 2 {
		t.Fatalf("unexpected models data: %#v", body["data"])
	}
}

func TestModelsHandlerCanExposeReasoningSuffixModels(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.4"}]}`))
	}))
	defer stub.Close()

	server := newTestServerWithConfig(t, config.Config{
		ListenAddr: ":0",
		Providers: []config.ProviderConfig{{
			ID:                          "openai",
			Enabled:                     true,
			UpstreamBaseURL:             stub.URL,
			UpstreamAPIKey:              "server-key",
			SupportsModels:              true,
			ModelMap:                    map[string]string{"gpt-5": "gpt-5.4"},
			EnableReasoningEffortSuffix: true,
			ExposeReasoningSuffixModels: true,
		}},
	})
	defer server.Close()

	resp, err := http.Get(server.URL + "/openai/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	data, ok := body["data"].([]any)
	if !ok {
		t.Fatalf("unexpected models data: %#v", body["data"])
	}
	ids := map[string]bool{}
	for _, item := range data {
		entry, _ := item.(map[string]any)
		id, _ := entry["id"].(string)
		ids[id] = true
	}
	for _, expected := range []string{"gpt-5", "gpt-5-low", "gpt-5-medium", "gpt-5-high", "gpt-5-xhigh"} {
		if !ids[expected] {
			t.Fatalf("expected model %q in list, got %#v", expected, ids)
		}
	}
}

func TestModelsHandlerExposesMappedPublicModelsAndWildcardKey(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"vendor-default"}]}`))
	}))
	defer stub.Close()

	server := newTestServerWithConfig(t, config.Config{
		ListenAddr: ":0",
		Providers: []config.ProviderConfig{{
			ID:              "openai",
			Enabled:         true,
			UpstreamBaseURL: stub.URL,
			UpstreamAPIKey:  "server-key",
			SupportsModels:  true,
			ModelMap:        map[string]string{"gpt-5": "gpt-5.4", "*": "vendor-default"},
		}},
	})
	defer server.Close()

	resp, err := http.Get(server.URL + "/openai/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	data, ok := body["data"].([]any)
	if !ok {
		t.Fatalf("unexpected models data: %#v", body["data"])
	}
	ids := map[string]bool{}
	for _, item := range data {
		entry, _ := item.(map[string]any)
		id, _ := entry["id"].(string)
		ids[id] = true
	}
	for _, expected := range []string{"vendor-default", "gpt-5", "*"} {
		if !ids[expected] {
			t.Fatalf("expected model %q in list, got %#v", expected, ids)
		}
	}
}
