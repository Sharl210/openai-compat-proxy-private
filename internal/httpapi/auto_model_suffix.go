package httpapi

import "openai-compat-proxy/internal/model"

// applyAutoReasoningModelSuffix 只保留 auto 的路由标记语义，不向上游传递任何推理控制。
func applyAutoReasoningModelSuffix(req *model.CanonicalRequest, intent model.ProxyModelIntent) {
	if req == nil || !intent.HasAuto {
		return
	}
	req.Reasoning = nil
	req.ReasoningModeOrigin = model.ReasoningModeOriginNone
	req.PassThroughRawReasoning = false
}
