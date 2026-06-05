package httpapi

import (
	"strings"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
)

const noPromptModelSuffix = "-noprompt"

func applyNoPromptModelSuffix(req *model.CanonicalRequest, cfg config.Config) {
	if req == nil || !cfg.EnableNoPromptModelSuffix {
		return
	}
	modelName := strings.TrimSpace(req.Model)
	if !strings.HasSuffix(modelName, noPromptModelSuffix) || len(modelName) <= len(noPromptModelSuffix) {
		return
	}
	req.Model = strings.TrimSuffix(modelName, noPromptModelSuffix)
	req.SkipProviderSystemPrompt = true
}
