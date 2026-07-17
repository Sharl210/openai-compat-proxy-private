package responses_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	responsesadapter "openai-compat-proxy/internal/adapter/responses"
	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/upstream"
)

func TestDecodeRequestToResponsesUpstreamPreservesToolShapes(t *testing.T) {
	var receivedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		receivedBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_tool_shapes","status":"completed"}`))
	}))
	defer server.Close()

	canon, err := responsesadapter.DecodeRequest(strings.NewReader(`{
		"model":"grok-4.5",
		"input":[{"role":"user","content":"Use a tool."}],
		"tools":[
			{"type":"function","name":"zeta","description":"Zeta tool","strict":true,"vendor_field":"z","parameters":{"type":"object","properties":{"value":{"type":"string"}}}},
			{"type":"function","name":"get_current_time","description":"Get current time","strict":true,"vendor_field":"null","parameters":null},
			{"type":"function","name":"alpha","description":"Alpha tool"}
		]
	}`))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	client := upstream.NewClient(server.URL, config.Config{
		UpstreamEndpointType:    config.UpstreamEndpointTypeResponses,
		ResponsesToolCompatMode: config.ResponsesToolCompatModePreserve,
	})
	if _, err := client.Response(context.Background(), canon, ""); err != nil {
		t.Fatalf("upstream Response error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(receivedBody, &payload); err != nil {
		t.Fatalf("unmarshal upstream payload: %v", err)
	}
	tools, ok := payload["tools"].([]any)
	if !ok || len(tools) != 3 {
		t.Fatalf("expected three upstream tools, got %#v", payload["tools"])
	}

	alpha, _ := tools[0].(map[string]any)
	currentTime, _ := tools[1].(map[string]any)
	zeta, _ := tools[2].(map[string]any)
	if alpha["name"] != "alpha" || currentTime["name"] != "get_current_time" || zeta["name"] != "zeta" {
		t.Fatalf("expected stable tool name ordering, got %#v", tools)
	}
	if _, exists := alpha["parameters"]; exists {
		t.Fatalf("expected absent parameters to remain absent, got %#v", alpha)
	}
	parameters, ok := currentTime["parameters"].(map[string]any)
	if !ok || len(parameters) != 0 {
		t.Fatalf("expected null parameters to become an empty object, got %#v", currentTime["parameters"])
	}
	if strict, _ := currentTime["strict"].(bool); !strict {
		t.Fatalf("expected strict field to survive null normalization, got %#v", currentTime)
	}
	if vendorField, _ := currentTime["vendor_field"].(string); vendorField != "null" {
		t.Fatalf("expected raw vendor field to survive null normalization, got %#v", currentTime)
	}
	nonEmptyParameters, ok := zeta["parameters"].(map[string]any)
	if !ok || nonEmptyParameters["type"] != "object" {
		t.Fatalf("expected non-empty parameters to remain an object, got %#v", zeta["parameters"])
	}
}
