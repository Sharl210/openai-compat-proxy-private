package httpapi

import (
	"strings"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/reasoning"
)

const noPromptModelSuffix = "-noprompt"

type parsedProxyModelSuffixes struct {
	baseModel       string
	reasoningEffort string
	hasNoPrompt     bool
}

func parseProxyModelSuffixes(modelName string) parsedProxyModelSuffixes {
	baseModel := strings.TrimSpace(modelName)
	parsed := parsedProxyModelSuffixes{baseModel: baseModel}
	if baseModel == "" {
		return parsed
	}

	for {
		next := false
		if strings.HasSuffix(baseModel, noPromptModelSuffix) && len(baseModel) > len(noPromptModelSuffix) {
			baseModel = strings.TrimSuffix(baseModel, noPromptModelSuffix)
			parsed.hasNoPrompt = true
			next = true
		}
		if strippedModel, effort, ok := reasoning.SplitSuffix(baseModel); ok {
			baseModel = strippedModel
			if parsed.reasoningEffort == "" {
				parsed.reasoningEffort = effort
			}
			next = true
		}
		if !next {
			break
		}
	}

	parsed.baseModel = baseModel
	return parsed
}

func hasNoPromptModelSuffix(modelName string) bool {
	return parseProxyModelSuffixes(modelName).hasNoPrompt
}

func stripNoPromptModelSuffix(modelName string) (string, bool) {
	parsed := parseProxyModelSuffixes(modelName)
	if !parsed.hasNoPrompt {
		return modelName, false
	}
	model := parsed.baseModel
	if parsed.reasoningEffort != "" {
		model += "-" + parsed.reasoningEffort
	}
	return model, true
}

func applyNoPromptModelSuffix(req *model.CanonicalRequest, cfg config.Config) {
	if req == nil || !cfg.EnableNoPromptModelSuffix {
		return
	}
	parsed := parseProxyModelSuffixes(req.Model)
	if !parsed.hasNoPrompt {
		return
	}
	req.Model = parsed.baseModel
	if parsed.reasoningEffort != "" {
		req.Model += "-" + parsed.reasoningEffort
	}
	req.SkipProviderSystemPrompt = true
}
