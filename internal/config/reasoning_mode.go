package config

import (
	"fmt"
	"regexp"
	"strings"
)

type ReasoningModeProCapability string

const (
	ReasoningModeProCapabilitySupported   ReasoningModeProCapability = "supported"
	ReasoningModeProCapabilityUnsupported ReasoningModeProCapability = "unsupported"
	ReasoningModeProCapabilityProbe       ReasoningModeProCapability = "probe"
)

type ModelPatternRule struct {
	Pattern   string
	IsExact   bool
	PatternRE *regexp.Regexp
}

type ReasoningModeProCapabilityRule struct {
	Pattern    string
	Capability ReasoningModeProCapability
	IsExact    bool
	PatternRE  *regexp.Regexp
}

func (c Config) DefaultProReasoningModeEnabledForFinalUpstreamModel(modelName string) bool {
	return c.EffectiveDefaultProReasoningMode() && !modelPatternRulesMatch(c.DefaultProReasoningModeExcludedModels, strings.TrimSpace(modelName))
}

func (c Config) EffectiveEnableReasoningModeSuffix() bool {
	return !c.EnableReasoningModeSuffixSet || c.EnableReasoningModeSuffix
}

func (c Config) EffectiveDefaultProReasoningMode() bool {
	return !c.DefaultProReasoningModeSet || c.DefaultProReasoningMode
}

func (p ProviderConfig) EffectiveEnableReasoningModeSuffix(rootEnabled bool) bool {
	if p.EnableReasoningModeSuffixSet {
		return p.EnableReasoningModeSuffix
	}
	return rootEnabled
}

func (p ProviderConfig) ResolveReasoningModeProCapability(finalUpstreamModel string) ReasoningModeProCapability {
	modelName := strings.TrimSpace(finalUpstreamModel)
	for index := len(p.ReasoningModeProCapabilityRules) - 1; index >= 0; index-- {
		rule := p.ReasoningModeProCapabilityRules[index]
		if rule.IsExact && rule.Pattern == modelName {
			return rule.Capability
		}
	}
	for index := len(p.ReasoningModeProCapabilityRules) - 1; index >= 0; index-- {
		rule := p.ReasoningModeProCapabilityRules[index]
		if rule.PatternRE != nil && rule.PatternRE.MatchString(modelName) {
			return rule.Capability
		}
	}
	return p.EffectiveReasoningModeProCapability()
}

func (p ProviderConfig) EffectiveReasoningModeProCapability() ReasoningModeProCapability {
	if p.ReasoningModeProCapability == "" {
		return ReasoningModeProCapabilityProbe
	}
	return p.ReasoningModeProCapability
}

func parseReasoningModeProCapability(value string) (ReasoningModeProCapability, error) {
	switch ReasoningModeProCapability(strings.ToLower(strings.TrimSpace(value))) {
	case ReasoningModeProCapabilitySupported:
		return ReasoningModeProCapabilitySupported, nil
	case ReasoningModeProCapabilityUnsupported:
		return ReasoningModeProCapabilityUnsupported, nil
	case ReasoningModeProCapabilityProbe:
		return ReasoningModeProCapabilityProbe, nil
	default:
		return "", fmt.Errorf("invalid reasoning-mode pro capability: %q", value)
	}
}

func parseModelPatternRules(raw string, key string, path string) ([]ModelPatternRule, error) {
	patterns := parseCommaSeparatedList(raw)
	rules := make([]ModelPatternRule, 0, len(patterns))
	for _, pattern := range patterns {
		compiled, err := compileModelPatternStrict(pattern)
		if err != nil {
			return nil, ErrInvalidConfig(fmt.Sprintf("invalid %s pattern in %s: %q", key, path, pattern))
		}
		rules = append(rules, ModelPatternRule{Pattern: pattern, IsExact: !strings.HasPrefix(pattern, "#re:"), PatternRE: compiled})
	}
	return rules, nil
}

func parseReasoningModeProCapabilityRules(raw string, key string, path string) ([]ReasoningModeProCapabilityRule, error) {
	parts := parseCommaSeparatedList(raw)
	rules := make([]ReasoningModeProCapabilityRule, 0, len(parts))
	for _, part := range parts {
		separator := strings.LastIndex(part, ":")
		if separator <= 0 || separator == len(part)-1 {
			return nil, ErrInvalidConfig(fmt.Sprintf("invalid %s in %s: %q", key, path, raw))
		}
		pattern := strings.TrimSpace(part[:separator])
		capability, err := parseReasoningModeProCapability(part[separator+1:])
		if err != nil {
			return nil, ErrInvalidConfig(fmt.Sprintf("invalid %s in %s: %q", key, path, raw))
		}
		compiled, err := compileModelPatternStrict(pattern)
		if err != nil {
			return nil, ErrInvalidConfig(fmt.Sprintf("invalid %s in %s: %q", key, path, raw))
		}
		rules = append(rules, ReasoningModeProCapabilityRule{
			Pattern: pattern, Capability: capability, IsExact: !strings.HasPrefix(pattern, "#re:"), PatternRE: compiled,
		})
	}
	return rules, nil
}

func modelPatternRulesMatch(rules []ModelPatternRule, modelName string) bool {
	for _, rule := range rules {
		if rule.IsExact && rule.Pattern == modelName {
			return true
		}
		if rule.PatternRE != nil && rule.PatternRE.MatchString(modelName) {
			return true
		}
	}
	return false
}

func modelPatternRulesEqual(left []ModelPatternRule, right []ModelPatternRule) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Pattern != right[index].Pattern || left[index].IsExact != right[index].IsExact {
			return false
		}
	}
	return true
}
