package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestModelProMessagesRoutes_preserveModeThroughAnthropicReasoningNormalization(t *testing.T) {
	cases := []struct {
		name           string
		endpointType   string
		body           string
		upstreamReply  string
		expectedEffort string
	}{
		{
			name:           "disabled thinking to responses upstream",
			endpointType:   config.UpstreamEndpointTypeResponses,
			body:           `{"model":"model-pro","max_tokens":128,"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hello"}]}`,
			upstreamReply:  `{"id":"resp_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`,
			expectedEffort: "none",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// Given
			var upstreamPayload map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/models" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"model","object":"model"}]}`))
					return
				}
				if err := json.NewDecoder(req.Body).Decode(&upstreamPayload); err != nil {
					t.Fatalf("decode upstream request: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(testCase.upstreamReply))
			}))
			defer upstream.Close()
			server := NewServer(reasoningModeRouteConfig(upstream.URL, testCase.endpointType))
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(testCase.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("anthropic-version", "2023-06-01")
			rec := httptest.NewRecorder()

			// When
			server.ServeHTTP(rec, req)

			// Then
			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			reasoning, _ := upstreamPayload["reasoning"].(map[string]any)
			if got := reasoning["mode"]; got != "pro" {
				t.Fatalf("expected upstream mode pro, got %#v", upstreamPayload)
			}
			if got := reasoning["effort"]; got != testCase.expectedEffort {
				t.Fatalf("expected upstream effort %q, got %#v", testCase.expectedEffort, upstreamPayload)
			}
			if got := reasoning["summary"]; got != "auto" {
				t.Fatalf("expected upstream summary auto, got %#v", upstreamPayload)
			}
		})
	}
}

func TestModelProMessagesRouteFallsBackWithoutModeForChatUpstream(t *testing.T) {
	// Given
	upstreamCalls := 0
	var upstreamPayload map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"model","object":"model"}]}`))
			return
		}
		upstreamCalls++
		if err := json.NewDecoder(req.Body).Decode(&upstreamPayload); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	server := NewServer(reasoningModeRouteConfig(upstream.URL, config.UpstreamEndpointTypeChat))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"model-pro","max_tokens":128,"thinking":{"type":"adaptive","output_config":{"effort":"high"}},"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("expected one chat upstream call, got %d", upstreamCalls)
	}
	reasoning, _ := upstreamPayload["reasoning"].(map[string]any)
	if _, exists := reasoning["mode"]; exists {
		t.Fatalf("expected automatic fallback to remove reasoning mode, got %#v", upstreamPayload)
	}
}
