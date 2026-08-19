package httpapi

import "openai-compat-proxy/internal/model"

// applyAutoReasoningModelSuffix discards all client reasoning parameters.
// The auto suffix deliberately sends no reasoning fields upstream.
func applyAutoReasoningModelSuffix(req *model.CanonicalRequest, intent model.ProxyModelIntent) {
	if req == nil || !intent.HasAuto {
		return
	}
	req.Reasoning = nil
	req.ReasoningModeOrigin = model.ReasoningModeOriginNone
	req.PassThroughRawReasoning = false
}
