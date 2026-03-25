package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveModelAndEffortPrefersRequestSuffixOverMappedSuffix(t *testing.T) {
	p := ProviderConfig{ModelMap: map[string]string{"gpt-5": "claude-sonnet-4-5-low"}}

	model, effort := p.ResolveModelAndEffort("gpt-5-high", true)
	if model != "claude-sonnet-4-5" {
		t.Fatalf("expected mapped model without suffix, got %q", model)
	}
	if effort != "high" {
		t.Fatalf("expected request suffix high to win, got %q", effort)
	}
}

func TestResolveModelAndEffortDoesNotParseSuffixWhenDisabled(t *testing.T) {
	p := ProviderConfig{ModelMap: map[string]string{"*": "claude-sonnet-4-5-low"}}

	model, effort := p.ResolveModelAndEffort("gpt-5-high", false)
	if model != "claude-sonnet-4-5-low" {
		t.Fatalf("expected mapped model to remain untouched when disabled, got %q", model)
	}
	if effort != "" {
		t.Fatalf("expected no effort override when disabled, got %q", effort)
	}
}

func TestResolveModelAndEffortUsesMappedSuffixWhenNoRequestSuffix(t *testing.T) {
	p := ProviderConfig{ModelMap: map[string]string{"gpt-5": "claude-sonnet-4-5-low"}}

	model, effort := p.ResolveModelAndEffort("gpt-5", true)
	if model != "claude-sonnet-4-5" {
		t.Fatalf("expected mapped model without suffix, got %q", model)
	}
	if effort != "low" {
		t.Fatalf("expected mapped suffix low, got %q", effort)
	}
}

func TestLoadProviderFileResolvesSystemPromptFilesRelativeToProviderEnv(t *testing.T) {
	rootDir := t.TempDir()
	promptDir := filepath.Join(rootDir, "prompts")
	if err := os.MkdirAll(promptDir, 0o755); err != nil {
		t.Fatalf("mkdir prompt dir: %v", err)
	}
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\n" +
		"SYSTEM_PROMPT_FILES=prompt.md, prompts/extra.md\n" +
		"SYSTEM_PROMPT_POSITION=append\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}

	expectedPaths := []string{
		filepath.Join(rootDir, "prompt.md"),
		filepath.Join(rootDir, "prompts", "extra.md"),
	}
	if len(provider.SystemPromptFiles) != len(expectedPaths) {
		t.Fatalf("expected %d resolved prompt paths, got %#v", len(expectedPaths), provider.SystemPromptFiles)
	}
	for i, expected := range expectedPaths {
		if provider.SystemPromptFiles[i] != expected {
			t.Fatalf("expected prompt path %q at index %d, got %q", expected, i, provider.SystemPromptFiles[i])
		}
	}
	if provider.SystemPromptPosition != SystemPromptPositionAppend {
		t.Fatalf("expected append position, got %q", provider.SystemPromptPosition)
	}
}

func TestLoadProviderFileTreatsBlankOrInvalidPromptPositionAsPrepend(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nSYSTEM_PROMPT_POSITION=sideways\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}

	if provider.SystemPromptPosition != SystemPromptPositionPrepend {
		t.Fatalf("expected invalid prompt position to fall back to prepend, got %q", provider.SystemPromptPosition)
	}

	providerBody = "PROVIDER_ID=openai\nSYSTEM_PROMPT_POSITION=\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("rewrite provider env: %v", err)
	}

	provider, err = loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error after blank position: %v", err)
	}
	if provider.SystemPromptPosition != SystemPromptPositionPrepend {
		t.Fatalf("expected blank prompt position to fall back to prepend, got %q", provider.SystemPromptPosition)
	}
}

func TestLoadProviderFileAllowsBlankSystemPromptFiles(t *testing.T) {
	rootDir := t.TempDir()
	providerEnvPath := filepath.Join(rootDir, "openai.env")
	providerBody := "PROVIDER_ID=openai\nSYSTEM_PROMPT_FILES=\n"
	if err := os.WriteFile(providerEnvPath, []byte(providerBody), 0o644); err != nil {
		t.Fatalf("write provider env: %v", err)
	}

	provider, err := loadProviderFile(providerEnvPath)
	if err != nil {
		t.Fatalf("loadProviderFile returned error: %v", err)
	}

	if len(provider.SystemPromptFiles) != 0 {
		t.Fatalf("expected blank prompt files to resolve to empty slice, got %#v", provider.SystemPromptFiles)
	}
	if provider.SystemPromptText != "" {
		t.Fatalf("expected blank prompt text, got %q", provider.SystemPromptText)
	}
}
