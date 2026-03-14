package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/upstream"
)

func TestUpstreamClientSendsInputListBody(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		input, ok := body["input"].([]any)
		if !ok || len(input) == 0 {
			t.Fatalf("expected non-empty input list, got %#v", body["input"])
		}
		if body["stream"] != true {
			t.Fatalf("expected stream=true, got %#v", body["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {}\n\n"))
	}))
	defer stub.Close()

	client := upstream.NewClient(stub.URL)
	_, err := client.Stream(context.Background(), sampleCanonicalRequest(), "Bearer server-key")
	if err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamClientSendsToolsInRequestBody(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		tools, ok := body["tools"].([]any)
		if !ok || len(tools) != 1 {
			t.Fatalf("expected one tool, got %#v", body["tools"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {}\n\n"))
	}))
	defer stub.Close()

	req := sampleCanonicalRequest()
	req.Tools = []model.CanonicalTool{{
		Type:        "function",
		Name:        "get_weather",
		Description: "Get weather",
		Parameters:  map[string]any{"type": "object"},
	}}

	client := upstream.NewClient(stub.URL)
	_, err := client.Stream(context.Background(), req, "Bearer server-key")
	if err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamClientMapsAssistantTextHistoryToOutputText(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}

		input, ok := body["input"].([]any)
		if !ok || len(input) != 2 {
			t.Fatalf("expected two input messages, got %#v", body["input"])
		}

		assistant, ok := input[1].(map[string]any)
		if !ok {
			t.Fatalf("expected assistant message object, got %#v", input[1])
		}
		if assistant["role"] != "assistant" {
			t.Fatalf("expected assistant role, got %#v", assistant["role"])
		}

		content, ok := assistant["content"].([]any)
		if !ok || len(content) != 1 {
			t.Fatalf("expected assistant content list, got %#v", assistant["content"])
		}

		part, ok := content[0].(map[string]any)
		if !ok {
			t.Fatalf("expected assistant content object, got %#v", content[0])
		}
		if part["type"] != "output_text" {
			t.Fatalf("expected assistant text to map to output_text, got %#v", part["type"])
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {}\n\n"))
	}))
	defer stub.Close()

	req := model.CanonicalRequest{
		Model:  "gpt-x",
		Stream: true,
		Messages: []model.CanonicalMessage{
			{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "hi"}}},
			{Role: "assistant", Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}}},
		},
	}

	client := upstream.NewClient(stub.URL)
	_, err := client.Stream(context.Background(), req, "Bearer server-key")
	if err != nil {
		t.Fatal(err)
	}
}
