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

func TestDefaultCacheInfoTimezoneIsAsiaShanghai(t *testing.T) {
	if got := Default().CacheInfoTimezone; got != "Asia/Shanghai" {
		t.Fatalf("expected default cache info timezone %q, got %q", "Asia/Shanghai", got)
	}
}

func TestLoadFromEnvParsesDownstreamNonStreamStrategy(t *testing.T) {
	t.Setenv("DOWNSTREAM_NON_STREAM_STRATEGY", DownstreamNonStreamStrategyUpstreamNonStream)

	cfg := LoadFromEnv()
	if got := cfg.DownstreamNonStreamStrategy; got != DownstreamNonStreamStrategyUpstreamNonStream {
		t.Fatalf("expected downstream non-stream strategy %q, got %q", DownstreamNonStreamStrategyUpstreamNonStream, got)
	}
}

func TestLoadFromEnvParsesCacheInfoTimezone(t *testing.T) {
	t.Setenv("CACHE_INFO_TIMEZONE", "UTC")

	cfg := LoadFromEnv()
	if got := cfg.CacheInfoTimezone; got != "UTC" {
		t.Fatalf("expected cache info timezone %q, got %q", "UTC", got)
	}
}

func TestConfigCacheInfoLocationResolvesTimezone(t *testing.T) {
	cfg := Config{CacheInfoTimezone: "Asia/Shanghai"}

	location, err := cfg.CacheInfoLocation()
	if err != nil {
		t.Fatalf("expected CacheInfoLocation to resolve timezone, got error: %v", err)
	}
	if got := location.String(); got != "Asia/Shanghai" {
		t.Fatalf("expected resolved timezone %q, got %q", "Asia/Shanghai", got)
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

func TestValidateRootEnvValuesAcceptsIANATimezone(t *testing.T) {
	testCases := []string{"UTC", "Asia/Shanghai"}
	for _, timezone := range testCases {
		t.Run(timezone, func(t *testing.T) {
			err := ValidateRootEnvValues(map[string]string{"CACHE_INFO_TIMEZONE": timezone})
			if err != nil {
				t.Fatalf("expected valid CACHE_INFO_TIMEZONE %q to pass validation, got %v", timezone, err)
			}
		})
	}
}

func TestValidateRootEnvValuesRejectsInvalidCacheInfoTimezone(t *testing.T) {
	err := ValidateRootEnvValues(map[string]string{"CACHE_INFO_TIMEZONE": "foo"})
	if err == nil {
		t.Fatalf("expected invalid CACHE_INFO_TIMEZONE to fail validation")
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
