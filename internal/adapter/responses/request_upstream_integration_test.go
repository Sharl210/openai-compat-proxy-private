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

func TestDecodeRequestToResponsesUpstreamPromotesDeveloperToolsIntoTopLevelTools(t *testing.T) {
	var receivedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		receivedBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_developer_tools","status":"completed"}`))
	}))
	defer server.Close()

	canon, err := responsesadapter.DecodeRequest(strings.NewReader(`{
		"model":"gpt-5.5",
		"input":[
			{"role":"developer","content":[{"type":"input_text","text":"developer prompt"}],"tools":[
				{"type":"namespace","name":"functions","tools":[{"type":"function","name":"bash","description":"Run shell","parameters":{"type":"object"}}]},
				{"type":"namespace","name":"collaboration","description":"Agent coordination tools","tools":[{"type":"function","name":"spawn_agent","description":"Spawn helper","parameters":{"type":"object"}}]}
			]},
			{"role":"user","content":[{"type":"input_text","text":"run ls"}]}
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
	if !ok || len(tools) != 2 {
		t.Fatalf("expected developer.tools promoted into top-level tools, got %#v", payload["tools"])
	}
	byName := make(map[string]map[string]any, len(tools))
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		name, _ := tool["name"].(string)
		byName[name] = tool
	}
	functions := byName["functions"]
	if got, _ := functions["type"].(string); got != "namespace" {
		t.Fatalf("expected functions namespace tool preserved, got %#v", functions)
	}
	if collaboration, ok := byName["collaboration"]; !ok || collaboration["description"] == nil {
		t.Fatalf("expected collaboration namespace tool preserved, got %#v", tools)
	}
	input, _ := payload["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("expected developer item plus user item preserved in input, got %#v", payload["input"])
	}
	developer, _ := input[0].(map[string]any)
	if got, _ := developer["role"].(string); got != "developer" {
		t.Fatalf("expected developer item preserved in input, got %#v", developer)
	}
	if _, exists := developer["tools"]; !exists {
		t.Fatalf("expected developer tools to stay preserved in input, got %#v", developer)
	}
	if _, exists := payload["instructions"]; exists {
		t.Fatalf("expected developer.tools content not to be rewritten into top-level instructions, got %#v", payload)
	}
}
