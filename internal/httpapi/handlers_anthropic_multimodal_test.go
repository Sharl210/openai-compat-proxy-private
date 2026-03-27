package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestAnthropicMessagesAcceptsImageURLContent(t *testing.T) {
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		upstreamBody = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "anthropic",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                        "anthropic",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			SupportsAnthropicMessages: true,
			SupportsResponses:         true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"claude-sonnet-4-5",
		"max_tokens":128,
		"messages":[{
			"role":"user",
			"content":[
				{"type":"image","source":{"type":"url","url":"https://example.com/cat.png"}},
				{"type":"text","text":"描述这张图"}
			]
		}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected anthropic image request to succeed, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(upstreamBody, `"type":"input_image"`) || !strings.Contains(upstreamBody, `"image_url":"https://example.com/cat.png"`) {
		t.Fatalf("expected upstream body to include input_image URL, got %s", upstreamBody)
	}
	if strings.Contains(upstreamBody, `"image_url":{"url"`) {
		t.Fatalf("expected image_url to be flattened to string, got %s", upstreamBody)
	}
}

func TestChatCompletionsAcceptsImageURLContent(t *testing.T) {
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		upstreamBody = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstream.URL,
			UpstreamAPIKey:    "test-key",
			SupportsChat:      true,
			SupportsResponses: true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5",
		"messages":[{
			"role":"user",
			"content":[
				{"type":"text","text":"描述这张图"},
				{"type":"image_url","image_url":{"url":"https://example.com/cat.png","detail":"high"}}
			]
		}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected chat image request to succeed, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(upstreamBody, `"image_url":"https://example.com/cat.png"`) || !strings.Contains(upstreamBody, `"detail":"high"`) {
		t.Fatalf("expected upstream body to flatten image_url and preserve detail, got %s", upstreamBody)
	}
	if strings.Contains(upstreamBody, `"image_url":{"url"`) {
		t.Fatalf("expected image_url to be flattened to string, got %s", upstreamBody)
	}
}
