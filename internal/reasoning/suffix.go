package reasoning

import "openai-compat-proxy/internal/model"

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
	return model.SplitReasoningEffortSuffix(modelName)
}

func ExpandModelIDs(baseIDs []string, modelMapKeys []string, enabled bool) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(baseIDs)*5)
	for _, id := range baseIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if enabled {
			if _, _, ok := SplitSuffix(id); ok {
				continue
			}
			for _, effort := range model.ReasoningEfforts() {
				expanded := id + "-" + effort
				if !seen[expanded] {
					seen[expanded] = true
					out = append(out, expanded)
				}
			}
		}
	}
	return out
}
