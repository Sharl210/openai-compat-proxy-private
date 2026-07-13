package model

import "strings"

var reasoningEfforts = []string{
	"minimal",
	"xhigh",
	"medium",
	"high",
	"low",
	"none",
	"max",
}

type ProxyModelIntentAxes struct {
	EnableReasoningEffort bool
	EnablePro             bool
	EnableNoPrompt        bool
	EnableUltra           bool
}

type ProxyModelIntent struct {
	BaseModel        string
	ReasoningEffort  string
	ReasoningMode    string
	HasNoPrompt      bool
	HasUltra         bool
	HasModelMapAlias bool
}

func (intent ProxyModelIntent) CanonicalModel() string {
	modelName := intent.BaseModel
	if intent.ReasoningEffort != "" {
		modelName += "-" + intent.ReasoningEffort
	}
	if intent.ReasoningMode == "pro" {
		modelName += "-pro"
	}
	if intent.HasUltra {
		modelName += "-ultra"
	}
	if intent.HasNoPrompt {
		modelName += "-noprompt"
	}
	return modelName
}

func ParseProxyModelIntent(modelName string, candidates []string, axes ProxyModelIntentAxes) (ProxyModelIntent, bool) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return ProxyModelIntent{}, false
	}

	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == modelName {
			return ProxyModelIntent{
				BaseModel: candidate,
			}, true
		}
	}

	var selected ProxyModelIntent
	matched := false
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || !strings.HasPrefix(modelName, candidate) {
			continue
		}

		intent, ok := parseProxyModelIntentTail(candidate, strings.TrimPrefix(modelName, candidate), axes)
		if !ok || (matched && len(candidate) <= len(selected.BaseModel)) {
			continue
		}
		selected = intent
		matched = true
	}
	return selected, matched
}

func SplitReasoningEffortSuffix(modelName string) (string, string, bool) {
	for _, effort := range reasoningEfforts {
		suffix := "-" + effort
		if strings.HasSuffix(modelName, suffix) && len(modelName) > len(suffix) {
			return strings.TrimSuffix(modelName, suffix), effort, true
		}
	}
	return modelName, "", false
}

func ReasoningEfforts() []string {
	return append([]string(nil), reasoningEfforts...)
}

func parseProxyModelIntentTail(candidate string, tail string, axes ProxyModelIntentAxes) (ProxyModelIntent, bool) {
	if tail == "" || !strings.HasPrefix(tail, "-") {
		return ProxyModelIntent{}, false
	}

	intent := ProxyModelIntent{
		BaseModel: candidate,
	}
	seenEffort := false
	seenPro := false
	seenNoPrompt := false
	seenUltra := false
	for tail != "" {
		if axes.EnableReasoningEffort {
			if effort, remaining, ok := consumeLeadingReasoningEffort(tail); ok {
				if seenEffort {
					return ProxyModelIntent{}, false
				}
				intent.ReasoningEffort = effort
				seenEffort = true
				tail = remaining
				continue
			}
		}
		if axes.EnablePro && strings.HasPrefix(tail, "-pro") {
			if seenPro {
				return ProxyModelIntent{}, false
			}
			intent.ReasoningMode = "pro"
			seenPro = true
			tail = strings.TrimPrefix(tail, "-pro")
			continue
		}
		if axes.EnableUltra && strings.HasPrefix(tail, "-ultra") {
			if seenUltra {
				return ProxyModelIntent{}, false
			}
			intent.HasUltra = true
			seenUltra = true
			tail = strings.TrimPrefix(tail, "-ultra")
			continue
		}
		if axes.EnableNoPrompt && strings.HasPrefix(tail, "-noprompt") {
			if seenNoPrompt {
				return ProxyModelIntent{}, false
			}
			intent.HasNoPrompt = true
			seenNoPrompt = true
			tail = strings.TrimPrefix(tail, "-noprompt")
			continue
		}
		return ProxyModelIntent{}, false
	}
	return intent, true
}

func consumeLeadingReasoningEffort(value string) (string, string, bool) {
	for _, effort := range reasoningEfforts {
		suffix := "-" + effort
		if strings.HasPrefix(value, suffix) {
			return effort, strings.TrimPrefix(value, suffix), true
		}
	}
	return "", value, false
}
