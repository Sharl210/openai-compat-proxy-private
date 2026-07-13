package upstream

import (
	"encoding/json"
	"strings"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
)

type ResponsesFeatureCompatibilityDecision string

const (
	ResponsesFeatureCompatibilityDecisionAllow  ResponsesFeatureCompatibilityDecision = "allow"
	ResponsesFeatureCompatibilityDecisionMap    ResponsesFeatureCompatibilityDecision = "map"
	ResponsesFeatureCompatibilityDecisionReject ResponsesFeatureCompatibilityDecision = "reject"
)

type ResponsesFeatureCompatibility struct {
	Decision ResponsesFeatureCompatibilityDecision
	Feature  string
}

type UnsupportedFeatureError struct {
	Feature string
}

func (e *UnsupportedFeatureError) Error() string {
	return "unsupported upstream feature: " + e.Feature + " requires a responses upstream"
}

func ClassifyResponsesFeatureCompatibility(req model.CanonicalRequest, upstreamEndpointType string) ResponsesFeatureCompatibility {
	if upstreamEndpointType == config.UpstreamEndpointTypeResponses {
		return ResponsesFeatureCompatibility{Decision: ResponsesFeatureCompatibilityDecisionAllow}
	}
	if hasProgrammaticToolCalling(req.Tools) {
		return ResponsesFeatureCompatibility{Decision: ResponsesFeatureCompatibilityDecisionReject, Feature: "programmatic tool calling"}
	}
	if multiAgentEnabled(req.ResponseMultiAgent) {
		return ResponsesFeatureCompatibility{Decision: ResponsesFeatureCompatibilityDecisionReject, Feature: "responses multi-agent"}
	}
	if hasUnsupportedPersistedResponsesItem(req.ResponseInputItems, upstreamEndpointType) {
		return ResponsesFeatureCompatibility{Decision: ResponsesFeatureCompatibilityDecisionReject, Feature: "persisted responses item"}
	}
	if hasEncryptedReasoningInclude(req.ResponseInclude) {
		return ResponsesFeatureCompatibility{Decision: ResponsesFeatureCompatibilityDecisionReject, Feature: "persisted reasoning include"}
	}
	if hasReasoningContext(req.Reasoning) {
		return ResponsesFeatureCompatibility{Decision: ResponsesFeatureCompatibilityDecisionReject, Feature: "reasoning context"}
	}
	if hasReasoningMode(req.Reasoning) {
		return ResponsesFeatureCompatibility{Decision: ResponsesFeatureCompatibilityDecisionReject, Feature: "reasoning mode"}
	}
	if hasJSONValue(req.ResponsePromptCacheKey) || hasJSONValue(req.ResponsePromptCacheOptions) {
		return ResponsesFeatureCompatibility{Decision: ResponsesFeatureCompatibilityDecisionReject, Feature: "responses prompt cache controls"}
	}
	if hasOriginalImageDetail(req) {
		return ResponsesFeatureCompatibility{Decision: ResponsesFeatureCompatibilityDecisionReject, Feature: "image detail original"}
	}
	if req.Reasoning != nil && strings.TrimSpace(req.Reasoning.Effort) != "" {
		return ResponsesFeatureCompatibility{Decision: ResponsesFeatureCompatibilityDecisionMap, Feature: "reasoning effort"}
	}
	return ResponsesFeatureCompatibility{Decision: ResponsesFeatureCompatibilityDecisionAllow}
}

func CheckResponsesFeatureCompatibility(req model.CanonicalRequest, upstreamEndpointType string) error {
	compatibility := ClassifyResponsesFeatureCompatibility(req, upstreamEndpointType)
	if compatibility.Decision == ResponsesFeatureCompatibilityDecisionReject {
		return &UnsupportedFeatureError{Feature: compatibility.Feature}
	}
	return nil
}

func hasProgrammaticToolCalling(tools []model.CanonicalTool) bool {
	for _, tool := range tools {
		if strings.TrimSpace(tool.Type) == "programmatic_tool_calling" {
			return true
		}
	}
	return false
}

func multiAgentEnabled(raw json.RawMessage) bool {
	if !hasJSONValue(raw) {
		return false
	}
	var multiAgent struct {
		Enabled bool `json:"enabled"`
	}
	return json.Unmarshal(raw, &multiAgent) == nil && multiAgent.Enabled
}

func hasUnsupportedPersistedResponsesItem(items []map[string]any, upstreamEndpointType string) bool {
	for _, item := range items {
		itemType, _ := item["type"].(string)
		switch itemType {
		case "compaction", "item_reference", "program", "program_output":
			return true
		}
		if _, ok := item["phase"]; ok {
			return true
		}
		if _, encrypted := item["encrypted_content"]; encrypted && upstreamEndpointType != config.UpstreamEndpointTypeAnthropic {
			return true
		}
	}
	return false
}

func hasEncryptedReasoningInclude(includes []string) bool {
	for _, include := range includes {
		if strings.TrimSpace(include) == "reasoning.encrypted_content" {
			return true
		}
	}
	return false
}

func hasReasoningContext(reasoning *model.CanonicalReasoning) bool {
	if reasoning == nil {
		return false
	}
	_, ok := reasoning.Raw["context"]
	return ok
}

func hasReasoningMode(reasoning *model.CanonicalReasoning) bool {
	return reasoning != nil && strings.TrimSpace(string(reasoning.Mode)) != ""
}

func hasJSONValue(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}

func hasOriginalImageDetail(req model.CanonicalRequest) bool {
	if hasOriginalImageDetailInParts(req.InstructionParts) {
		return true
	}
	for _, message := range req.Messages {
		if hasOriginalImageDetailInParts(message.Parts) {
			return true
		}
		for _, block := range message.OrderedContent {
			if hasOriginalImageDetailInParts([]model.CanonicalContentPart{block.Part}) {
				return true
			}
		}
	}
	return false
}

func hasOriginalImageDetailInParts(parts []model.CanonicalContentPart) bool {
	for _, part := range parts {
		if part.Type != "input_image" && part.Type != "image_url" {
			continue
		}
		imageURL, _ := part.Raw["image_url"].(map[string]any)
		if strings.TrimSpace(stringValue(imageURL["detail"])) == "original" {
			return true
		}
	}
	return false
}
