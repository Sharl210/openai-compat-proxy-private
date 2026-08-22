package httpapi

import (
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
)

func TestApplyAdaptiveModelSuffixSetsAnthropicAdaptiveThinking(t *testing.T) {
	req := &model.CanonicalRequest{Model: "claude-sonnet", Reasoning: &model.CanonicalReasoning{Effort: "high", Raw: map[string]any{}}}
	providerCfg := config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic}
	err := applyAdaptiveModelSuffix(req, model.ProxyModelIntent{HasAdaptive: true, ReasoningEffort: "high"}, providerCfg)
	if err != nil {
		t.Fatalf("expected adaptive to apply on anthropic upstream, got %v", err)
	}
	if req.Reasoning == nil || req.Reasoning.Raw == nil {
		t.Fatal("expected reasoning raw to be set")
	}
	thinking, _ := req.Reasoning.Raw["thinking"].(map[string]any)
	if thinking == nil || thinking["type"] != "adaptive" {
		t.Fatalf("expected adaptive thinking, got %#v", req.Reasoning.Raw["thinking"])
	}
	outputCfg, _ := req.Reasoning.Raw["output_config"].(map[string]any)
	if outputCfg == nil || outputCfg["effort"] != "high" {
		t.Fatalf("expected output_config effort high, got %#v", req.Reasoning.Raw["output_config"])
	}
}

func TestApplyAdaptiveModelSuffixRejectsNonAnthropicUpstream(t *testing.T) {
	req := &model.CanonicalRequest{Model: "gpt-5"}
	providerCfg := config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeChat}
	err := applyAdaptiveModelSuffix(req, model.ProxyModelIntent{HasAdaptive: true}, providerCfg)
	if err == nil || !strings.Contains(err.Error(), "anthropic") {
		t.Fatalf("expected unsupported error on non-anthropic upstream, got %v", err)
	}
}

func TestApplyAdaptiveModelSuffixNoopWithoutFlag(t *testing.T) {
	req := &model.CanonicalRequest{Model: "claude-sonnet", Reasoning: &model.CanonicalReasoning{Effort: "high"}}
	providerCfg := config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeChat}
	if err := applyAdaptiveModelSuffix(req, model.ProxyModelIntent{}, providerCfg); err != nil {
		t.Fatalf("expected noop without adaptive flag, got %v", err)
	}
	if req.Reasoning.Effort != "high" {
		t.Fatalf("expected reasoning untouched, got %#v", req.Reasoning)
	}
}
