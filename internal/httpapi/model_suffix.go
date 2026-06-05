package httpapi

import (
	"strings"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
)

const noPromptModelSuffix = "-noprompt"

func hasNoPromptModelSuffix(modelName string) bool {
	trimmed := strings.TrimSpace(modelName)
	return strings.HasSuffix(trimmed, noPromptModelSuffix) && len(trimmed) > len(noPromptModelSuffix)
}

func stripNoPromptModelSuffix(modelName string) (string, bool) {
	trimmed := strings.TrimSpace(modelName)
	if !hasNoPromptModelSuffix(trimmed) {
		return modelName, false
	}
	return strings.TrimSuffix(trimmed, noPromptModelSuffix), true
}

func applyNoPromptModelSuffix(req *model.CanonicalRequest, cfg config.Config) {
	if req == nil || !cfg.EnableNoPromptModelSuffix {
		return
	}
	modelName := strings.TrimSpace(req.Model)
	if !hasNoPromptModelSuffix(modelName) {
		return
	}
	req.Model = strings.TrimSuffix(modelName, noPromptModelSuffix)
	req.SkipProviderSystemPrompt = true
}
