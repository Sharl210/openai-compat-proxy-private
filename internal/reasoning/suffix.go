package reasoning

import (
	"strings"

	"openai-compat-proxy/internal/model"
)

var supportedSuffixes = []string{"-xhigh", "-medium", "-high", "-low"}

func ApplyModelSuffix(req model.CanonicalRequest, enabled bool) model.CanonicalRequest {
	if !enabled {
		return req
	}
	baseModel, effort, ok := SplitSuffix(req.Model)
	if !ok {
		return req
	}
	req.Model = baseModel
	if req.Reasoning == nil {
		req.Reasoning = &model.CanonicalReasoning{
			Effort:  effort,
			Summary: "auto",
			Raw:     map[string]any{"effort": effort, "summary": "auto"},
		}
		return req
	}
	req.Reasoning.Effort = effort
	if req.Reasoning.Raw == nil {
		req.Reasoning.Raw = map[string]any{}
	}
	req.Reasoning.Raw["effort"] = effort
	if req.Reasoning.Summary == "" {
		req.Reasoning.Summary = "auto"
	}
	if _, ok := req.Reasoning.Raw["summary"]; !ok {
		req.Reasoning.Raw["summary"] = req.Reasoning.Summary
	}
	return req
}

func SplitSuffix(modelName string) (string, string, bool) {
	for _, suffix := range supportedSuffixes {
		if strings.HasSuffix(modelName, suffix) && len(modelName) > len(suffix) {
			return strings.TrimSuffix(modelName, suffix), strings.TrimPrefix(suffix, "-"), true
		}
	}
	return modelName, "", false
}

func ExpandModelIDs(baseIDs []string, modelMapKeys []string, enabled bool) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(baseIDs)*5)
	hiddenPatterns := map[string]bool{}
	for _, k := range modelMapKeys {
		if strings.Contains(k, "*") {
			hiddenPatterns[k] = true
		}
	}
	for _, id := range baseIDs {
		if id == "" || seen[id] || hiddenPatterns[id] || strings.Contains(id, "*") {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if enabled {
			if _, _, ok := SplitSuffix(id); ok {
				continue
			}
			for _, suffix := range supportedSuffixes {
				expanded := id + suffix
				if !seen[expanded] {
					seen[expanded] = true
					out = append(out, expanded)
				}
			}
		}
	}
	return out
}
