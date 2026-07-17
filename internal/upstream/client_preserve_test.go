package upstream

import (
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
)

func TestBuildResponsesUpstreamToolPayloadPreserveWebSearchOmitsFunctionFields(t *testing.T) {
	payload := buildResponsesUpstreamToolPayload(model.CanonicalTool{
		Type:        "web_search",
		Name:        "",
		Description: "",
	}, config.ResponsesToolCompatModePreserve)

	if got, _ := payload["type"].(string); got != "web_search" {
		t.Fatalf("expected preserved tool type web_search, got %#v", payload)
	}
	if _, exists := payload["description"]; exists {
		t.Fatalf("expected preserve web_search payload to omit description, got %#v", payload)
	}
	if _, exists := payload["parameters"]; exists {
		t.Fatalf("expected preserve web_search payload to omit parameters, got %#v", payload)
	}
	if _, exists := payload["name"]; exists {
		t.Fatalf("expected preserve web_search payload to omit empty name, got %#v", payload)
	}
}

func TestBuildResponsesUpstreamToolPayloadNormalizesNullFunctionParameters(t *testing.T) {
	payload := buildResponsesUpstreamToolPayload(model.CanonicalTool{
		Type:        "function",
		Name:        "get_current_time",
		Description: "Get current time",
		Raw: map[string]any{
			"type":        "function",
			"name":        "get_current_time",
			"description": "Get current time",
			"strict":      true,
			"parameters":  nil,
		},
	}, config.ResponsesToolCompatModePreserve)

	parameters, ok := payload["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("expected function tool parameters to be an object, got %#v", payload["parameters"])
	}
	if len(parameters) != 0 {
		t.Fatalf("expected empty function tool parameter schema, got %#v", parameters)
	}
	if strict, _ := payload["strict"].(bool); !strict {
		t.Fatalf("expected preserved strict field, got %#v", payload)
	}
}

func TestBuildResponsesUpstreamToolPayloadPreservesOfficialCodexRawToolFields(t *testing.T) {
	tools := []model.CanonicalTool{
		{
			Type:        "function",
			Name:        "shell_command",
			Description: "Run a shell command",
			Parameters:  map[string]any{"type": "object"},
			Raw: map[string]any{
				"type":        "function",
				"name":        "shell_command",
				"description": "Run a shell command",
				"strict":      true,
				"parameters":  map[string]any{"type": "object"},
			},
		},
		{
			Type:        "custom",
			Name:        "apply_patch",
			Description: "Apply a patch",
			Raw: map[string]any{
				"type":        "custom",
				"name":        "apply_patch",
				"description": "Apply a patch",
				"format":      map[string]any{"type": "grammar", "syntax": "lark"},
			},
		},
		{
			Type:        "namespace",
			Name:        "mcp__node_repl",
			Description: "Node REPL tools",
			Raw: map[string]any{
				"type":        "namespace",
				"name":        "mcp__node_repl",
				"description": "Node REPL tools",
				"tools":       []any{map[string]any{"type": "function", "name": "execute"}},
			},
		},
		{
			Type: "tool_search",
			Raw: map[string]any{
				"type":       "tool_search",
				"execution":  map[string]any{"type": "server"},
				"parameters": map[string]any{"type": "object"},
			},
		},
		{
			Type: "web_search",
			Raw: map[string]any{
				"type":                 "web_search",
				"external_web_access":  true,
				"search_content_types": []any{"webpage"},
			},
		},
	}

	payloads := buildResponsesUpstreamToolPayloads(tools, config.ResponsesToolCompatModePreserve)
	byType := map[string]map[string]any{}
	for _, payload := range payloads {
		payloadType, _ := payload["type"].(string)
		byType[payloadType] = payload
	}

	if got, _ := byType["function"]["strict"].(bool); !got {
		t.Fatalf("expected function strict=true to survive, got %#v", byType["function"])
	}
	format, _ := byType["custom"]["format"].(map[string]any)
	if got, _ := format["type"].(string); got != "grammar" {
		t.Fatalf("expected custom format to survive, got %#v", byType["custom"])
	}
	namespaceTools, _ := byType["namespace"]["tools"].([]any)
	if len(namespaceTools) != 1 {
		t.Fatalf("expected namespace nested tools to survive, got %#v", byType["namespace"])
	}
	if _, exists := byType["tool_search"]["name"]; exists {
		t.Fatalf("expected tool_search to remain nameless, got %#v", byType["tool_search"])
	}
	if execution, _ := byType["tool_search"]["execution"].(map[string]any); execution["type"] != "server" {
		t.Fatalf("expected tool_search execution to survive, got %#v", byType["tool_search"])
	}
	if _, exists := byType["web_search"]["name"]; exists {
		t.Fatalf("expected web_search to remain nameless, got %#v", byType["web_search"])
	}
	if got, _ := byType["web_search"]["external_web_access"].(bool); !got {
		t.Fatalf("expected web_search external_web_access to survive, got %#v", byType["web_search"])
	}
}
