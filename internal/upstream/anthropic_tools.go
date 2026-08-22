package upstream

import (
	"strings"

	"openai-compat-proxy/internal/model"
)

func expandAnthropicNamespaceTools(tools []model.CanonicalTool) []model.CanonicalTool {
	if len(tools) == 0 {
		return nil
	}
	expanded := make([]model.CanonicalTool, 0, len(tools))
	for _, tool := range tools {
		appendAnthropicTool(&expanded, tool)
	}
	return expanded
}

func appendAnthropicTool(dst *[]model.CanonicalTool, tool model.CanonicalTool) {
	toolType := strings.TrimSpace(tool.Type)
	if toolType == "" {
		toolType = strings.TrimSpace(stringValue(tool.Raw["type"]))
	}
	if toolType != "namespace" {
		*dst = append(*dst, tool)
		return
	}
	for _, rawTool := range nestedAnthropicTools(tool.Raw["tools"]) {
		appendAnthropicTool(dst, canonicalToolFromAnthropicRaw(rawTool))
	}
}

func nestedAnthropicTools(value any) []map[string]any {
	switch typed := value.(type) {
	case []any:
		tools := make([]map[string]any, 0, len(typed))
		for _, rawTool := range typed {
			if tool, ok := rawTool.(map[string]any); ok && len(tool) > 0 {
				tools = append(tools, tool)
			}
		}
		return tools
	case []map[string]any:
		return typed
	default:
		return nil
	}
}

func canonicalToolFromAnthropicRaw(raw map[string]any) model.CanonicalTool {
	normalized := cloneMap(raw)
	if function, ok := normalized["function"].(map[string]any); ok {
		if strings.TrimSpace(stringValue(normalized["name"])) == "" {
			normalized["name"] = function["name"]
		}
		if strings.TrimSpace(stringValue(normalized["description"])) == "" {
			normalized["description"] = function["description"]
		}
		parameters, hasParameters := normalized["parameters"].(map[string]any)
		functionParameters, hasFunctionParameters := function["parameters"].(map[string]any)
		if (!hasParameters || len(parameters) == 0) && hasFunctionParameters && len(functionParameters) > 0 {
			normalized["parameters"] = cloneMap(functionParameters)
		}
	}

	toolType := strings.TrimSpace(stringValue(normalized["type"]))
	if toolType == "" {
		if _, exists := normalized["tools"]; exists {
			toolType = "namespace"
		} else {
			toolType = "function"
		}
		normalized["type"] = toolType
	}
	parameters, _ := normalized["parameters"].(map[string]any)
	if len(parameters) == 0 {
		if schema, ok := normalized["input_schema"].(map[string]any); ok {
			parameters = cloneMap(schema)
			normalized["parameters"] = parameters
		}
	}
	return model.CanonicalTool{
		Type:        toolType,
		Name:        stringValue(normalized["name"]),
		Description: stringValue(normalized["description"]),
		Parameters:  parameters,
		Raw:         normalized,
	}
}
