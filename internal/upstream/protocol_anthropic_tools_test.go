package upstream

import (
	"encoding/json"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
)

func TestBuildAnthropicRequestBodyExpandsNamespaceToolFunctions(t *testing.T) {
	// Given
	request := model.CanonicalRequest{
		Model: "claude-sonnet",
		Tools: []model.CanonicalTool{{
			Type:        "namespace",
			Name:        "functions",
			Description: "Namespace tools",
			Raw: map[string]any{
				"type": "namespace",
				"name": "functions",
				"tools": []any{
					map[string]any{
						"type":        "function",
						"name":        "bash",
						"description": "Run a shell command",
						"parameters": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"command": map[string]any{"type": "string"},
							},
						},
					},
				},
			},
		}},
	}

	// When
	body, err := buildAnthropicRequestBody(request, "", false, false, config.UpstreamCacheControlNoChange)

	// Then
	if err != nil {
		t.Fatalf("buildAnthropicRequestBody error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode Anthropic request body: %v", err)
	}
	tools, _ := payload["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected one expanded Anthropic tool, got %#v", payload["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if got, _ := tool["name"].(string); got != "bash" {
		t.Fatalf("expected nested function name bash, got %#v", tool)
	}
	if got, _ := tool["description"].(string); got != "Run a shell command" {
		t.Fatalf("expected nested function description, got %#v", tool)
	}
	schema, _ := tool["input_schema"].(map[string]any)
	properties, _ := schema["properties"].(map[string]any)
	if _, ok := properties["command"]; !ok {
		t.Fatalf("expected nested function schema to survive, got %#v", tool["input_schema"])
	}
}

func TestExpandAnthropicNamespaceToolsPreservesMixedNestedSchemas(t *testing.T) {
	tools := []model.CanonicalTool{
		{
			Type:        "function",
			Name:        "zeta",
			Description: "ordinary function",
			Parameters:  map[string]any{"type": "object"},
		},
		{
			Type: "namespace",
			Raw: map[string]any{
				"type": "namespace",
				"tools": []any{
					map[string]any{
						"type": "namespace",
						"tools": []any{
							map[string]any{
								"type": "function",
								"function": map[string]any{
									"name":        "alpha",
									"description": "nested function",
									"parameters": map[string]any{
										"type":       "object",
										"properties": map[string]any{"query": map[string]any{"type": "string"}},
									},
								},
								"parameters": nil,
							},
						},
					},
					map[string]any{
						"type":        "function",
						"name":        "beta",
						"description": "input schema function",
						"input_schema": map[string]any{
							"type":       "object",
							"properties": map[string]any{"path": map[string]any{"type": "string"}},
						},
					},
				},
			},
		},
	}

	expanded := expandAnthropicNamespaceTools(tools)
	if len(expanded) != 3 {
		t.Fatalf("expected ordinary function plus two nested functions, got %#v", expanded)
	}
	if got := []string{expanded[0].Name, expanded[1].Name, expanded[2].Name}; !equalStrings(got, []string{"zeta", "alpha", "beta"}) {
		t.Fatalf("unexpected expansion order: %#v", got)
	}
	if _, ok := expanded[1].Parameters["properties"].(map[string]any); !ok {
		t.Fatalf("expected nested function.parameters to survive parameters:null, got %#v", expanded[1])
	}
	if _, ok := expanded[2].Parameters["properties"].(map[string]any); !ok {
		t.Fatalf("expected input_schema to become parameters, got %#v", expanded[2])
	}

	body, err := buildAnthropicRequestBody(model.CanonicalRequest{Model: "claude-sonnet", Tools: tools}, "", false, false, config.UpstreamCacheControlNoChange)
	if err != nil {
		t.Fatalf("buildAnthropicRequestBody error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode Anthropic request body: %v", err)
	}
	encodedTools, _ := payload["tools"].([]any)
	if len(encodedTools) != 3 {
		t.Fatalf("expected three encoded Anthropic tools, got %#v", payload["tools"])
	}
	if got := []string{encodedTools[0].(map[string]any)["name"].(string), encodedTools[1].(map[string]any)["name"].(string), encodedTools[2].(map[string]any)["name"].(string)}; !equalStrings(got, []string{"alpha", "beta", "zeta"}) {
		t.Fatalf("expected expanded Anthropic tools to be sorted by name, got %#v", got)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
