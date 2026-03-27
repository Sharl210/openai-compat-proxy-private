package config

import (
	"testing"
	"time"
)

func TestDefaultDownstreamNonStreamStrategyIsProxyBuffer(t *testing.T) {
	if got := Default().DownstreamNonStreamStrategy; got != DownstreamNonStreamStrategyProxyBuffer {
		t.Fatalf("expected default downstream non-stream strategy %q, got %q", DownstreamNonStreamStrategyProxyBuffer, got)
	}
}

func TestLoadFromEnvParsesDownstreamNonStreamStrategy(t *testing.T) {
	t.Setenv("DOWNSTREAM_NON_STREAM_STRATEGY", DownstreamNonStreamStrategyUpstreamNonStream)

	cfg := LoadFromEnv()
	if got := cfg.DownstreamNonStreamStrategy; got != DownstreamNonStreamStrategyUpstreamNonStream {
		t.Fatalf("expected downstream non-stream strategy %q, got %q", DownstreamNonStreamStrategyUpstreamNonStream, got)
	}
}

func TestLoadFromEnvParsesTimeouts(t *testing.T) {
	t.Setenv("CONNECT_TIMEOUT", "11s")
	t.Setenv("FIRST_BYTE_TIMEOUT", "45s")
	t.Setenv("IDLE_TIMEOUT", "75s")
	t.Setenv("TOTAL_TIMEOUT", "12m")

	cfg := LoadFromEnv()
	if cfg.ConnectTimeout != 11*time.Second {
		t.Fatalf("expected ConnectTimeout 11s, got %v", cfg.ConnectTimeout)
	}
	if cfg.FirstByteTimeout != 45*time.Second {
		t.Fatalf("expected FirstByteTimeout 45s, got %v", cfg.FirstByteTimeout)
	}
	if cfg.IdleTimeout != 75*time.Second {
		t.Fatalf("expected IdleTimeout 75s, got %v", cfg.IdleTimeout)
	}
	if cfg.TotalTimeout != 12*time.Minute {
		t.Fatalf("expected TotalTimeout 12m, got %v", cfg.TotalTimeout)
	}
}

func TestValidateRootEnvValuesRejectsInvalidTimeout(t *testing.T) {
	err := ValidateRootEnvValues(map[string]string{"TOTAL_TIMEOUT": "abc"})
	if err == nil {
		t.Fatalf("expected invalid TOTAL_TIMEOUT to fail validation")
	}
}

func TestValidateRootEnvValuesRejectsInvalidDownstreamNonStreamStrategy(t *testing.T) {
	err := ValidateRootEnvValues(map[string]string{"DOWNSTREAM_NON_STREAM_STRATEGY": "bad-mode"})
	if err == nil {
		t.Fatalf("expected invalid DOWNSTREAM_NON_STREAM_STRATEGY to fail validation")
	}
}

func TestDefaultFirstByteTimeoutIsTwentyMinutes(t *testing.T) {
	if got := Default().FirstByteTimeout; got != 20*time.Minute {
		t.Fatalf("expected default FirstByteTimeout 20m, got %v", got)
	}
}
