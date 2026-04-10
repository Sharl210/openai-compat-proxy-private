package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/syntaxrepair"
)

const (
	liveCacheMatrixEnabledEnv                    = "LIVE_CACHE_MATRIX_ENABLED"
	liveCacheBaseURLEnv                          = "LIVE_CACHE_BASE_URL"
	liveCacheAPIKeyEnv                           = "LIVE_CACHE_API_KEY"
	liveCacheRoundsEnv                           = "LIVE_CACHE_ROUNDS"
	liveCacheProvidersJSONEnv                    = "LIVE_CACHE_PROVIDER_MATRIX_JSON"
	liveCacheRequestTimeoutEnv                   = "LIVE_CACHE_REQUEST_TIMEOUT"
	liveCacheCanonicalTurnCount                  = 5
	liveCacheTurnOneMinInputChars                = 10001
	liveCacheFollowUpMinInputChars               = 5001
	liveCacheMaxOutputTokens                     = 128
	liveCacheTurnOneChatResponsesMaxOutputTokens = 256
	liveCacheTurnOneAnthropicMaxOutputTokens     = 256
	liveCacheAnthropicMessagesTurnOneMaxTokens   = 512
)

var (
	liveCacheDownstreams = []string{"responses", "chat", "messages"}
	liveCacheUpstreams   = []string{config.UpstreamEndpointTypeResponses, config.UpstreamEndpointTypeChat, config.UpstreamEndpointTypeAnthropic}
)

type liveCacheProviderSpec struct {
	Label         string `json:"label"`
	ProviderID    string `json:"provider_id"`
	Model         string `json:"model"`
	DirectBaseURL string `json:"direct_base_url"`
	DirectAPIKey  string `json:"direct_api_key"`
}

type liveCacheRuntimeConfig struct {
	BaseURL string
	APIKey  string
	Rounds  int
	Timeout time.Duration

	Providers map[string]liveCacheProviderSpec
}

type liveCacheObservation struct {
	Round                 int
	ExecutionPath         liveCacheExecutionPath
	Downstream            string
	Upstream              string
	Turn                  int
	ProviderID            string
	Model                 string
	CanonicalDigest       string
	PayloadDigest         string
	InputDigest           string
	InputChars            int
	MessageCount          int
	ToolCount             int
	ToolShape             string
	RawUsageInputTokens   int64
	RawUsageCachedTokens  int64
	RawUsageCreatedTokens int64
	RawUsageOutputTokens  int64
	RawUsageTotalTokens   int64
	UsageInputTokens      int64
	UsageCachedTokens     int64
	UsageCreatedTokens    int64
	UsageOutputTokens     int64
	UsageTotalTokens      int64
	CacheRatio            float64
	OutputPreview         string
	ResponseID            string
	HasToolCall           bool
	MissingUsage          bool
	BelowMinInputChars    bool
	Note                  string
}

type liveCacheExecutionPath string

const (
	liveCacheExecutionPathProxy  liveCacheExecutionPath = "proxy-group"
	liveCacheExecutionPathDirect liveCacheExecutionPath = "direct-control"
)

type liveCacheScenarioSpec struct {
	Round        int
	Upstream     string
	SharedCorpus string
	ToolName     string
	Turns        []liveCacheScenarioTurn
}

type liveCacheScenarioTurn struct {
	Number            int
	InputText         string
	MinInputChars     int
	ExpectToolCall    bool
	IncludeToolResult bool
	ToolResultJSON    string
}

type liveCacheScenarioBinding struct {
	ExecutionPath   liveCacheExecutionPath
	Scenario        liveCacheScenarioSpec
	CanonicalDigest string
}

type liveCacheToolCall struct {
	ID        string
	Name      string
	Arguments string
	Input     map[string]any
}

type liveCacheResponsesState struct {
	History  []map[string]any
	ToolCall liveCacheToolCall
}

type liveCacheChatState struct {
	Messages []map[string]any
	ToolCall liveCacheToolCall
}

type liveCacheAnthropicState struct {
	Messages         []map[string]any
	ToolUse          liveCacheToolCall
	AssistantContent []map[string]any
}

type liveCacheRawMetrics struct {
	InputTokens   int64
	CachedTokens  int64
	CreatedTokens int64
	OutputTokens  int64
	TotalTokens   int64
}

type liveCacheNormalizedMetrics struct {
	InputTokens  int64
	CachedTokens int64
	CacheRatio   float64
}

type liveCacheRawMetricsRow struct {
	Round                   int
	ExecutionPath           liveCacheExecutionPath
	Downstream              string
	Upstream                string
	ProviderID              string
	Model                   string
	TurnClass               string
	Turns                   int
	AvgRawInputTokens       float64
	AvgRawCachedTokens      float64
	AvgRawCreatedTokens     float64
	AvgRawOutputTokens      float64
	AvgRawTotalTokens       float64
	AvgNormalizedCacheRatio float64
}

type liveCacheBaselineRow struct {
	Round                    int
	Downstream               string
	Upstream                 string
	ProviderID               string
	Model                    string
	TurnClass                string
	Turns                    int
	AvgDirectNormalizedRatio float64
}

type liveCachePreservationLossRow struct {
	Round                    int
	Downstream               string
	Upstream                 string
	ProviderID               string
	Model                    string
	TurnClass                string
	Turns                    int
	ExcludedTurns            int
	BaselineMissing          bool
	AvgProxyNormalizedRatio  float64
	AvgDirectNormalizedRatio float64
	Preservation             *float64
	Loss                     *float64
	Attribution              string
	Caveat                   string
}

type liveCacheRequestFingerprint struct {
	PayloadDigest string
	MessageCount  int
	ToolCount     int
	ToolShape     string
}

type liveCacheCombo struct {
	Round         int
	ExecutionPath liveCacheExecutionPath
	Downstream    string
	Upstream      string
	ProviderID    string
	Model         string
}

type liveCacheDirectReuseKey struct {
	Round      int
	Upstream   string
	ProviderID string
	Model      string
}

type liveCacheDirectReuseResult struct {
	Observations []liveCacheObservation
	Err          error
}

func TestLiveCacheMatrix(t *testing.T) {
	if os.Getenv(liveCacheMatrixEnabledEnv) != "1" {
		t.Skipf("set %s=1 to run the live cache matrix test", liveCacheMatrixEnabledEnv)
	}

	cfg := mustLoadLiveCacheRuntimeConfig(t)
	client := &http.Client{Timeout: cfg.Timeout}
	sampleScenario := buildLiveCacheScenarioSpec(1, liveCacheUpstreams[0])

	observations := make([]liveCacheObservation, 0, cfg.Rounds*len(liveCacheDownstreams)*len(liveCacheUpstreams)*len(sampleScenario.Turns)*2)
	comboFailures := make([]string, 0)
	totalCombos := cfg.Rounds * len(liveCacheDownstreams) * len(liveCacheUpstreams) * 2
	comboIndex := 0
	directReuse := map[liveCacheDirectReuseKey]liveCacheDirectReuseResult{}

	for round := 1; round <= cfg.Rounds; round++ {
		for _, downstream := range liveCacheDownstreams {
			for _, upstream := range liveCacheUpstreams {
				spec := cfg.Providers[upstream]
				for _, path := range []liveCacheExecutionPath{liveCacheExecutionPathProxy, liveCacheExecutionPathDirect} {
					comboIndex++
					combo := liveCacheCombo{Round: round, ExecutionPath: path, Downstream: downstream, Upstream: upstream, ProviderID: spec.ProviderID, Model: spec.Model}
					t.Logf("[live-cache] combo %d/%d start %s", comboIndex, totalCombos, combo)
					comboObservations, err := runLiveCacheScenarioForExecutionPathWithDirectReuse(client, cfg, round, downstream, spec, path, directReuse)
					observations = append(observations, comboObservations...)
					if err != nil {
						t.Logf("[live-cache] combo %d/%d fail %s observations=%d err=%v", comboIndex, totalCombos, combo, len(comboObservations), err)
						comboFailures = append(comboFailures, err.Error())
						continue
					}
					t.Logf("[live-cache] combo %d/%d done %s observations=%d", comboIndex, totalCombos, combo, len(comboObservations))
				}
			}
		}
	}

	t.Log("\n" + renderLiveCacheAverageTable(observations) + "\n\n" + renderLiveCacheObservationTable(observations))

	if len(comboFailures) > 0 {
		t.Fatalf("live cache matrix had %d failing combination(s):\n%s", len(comboFailures), strings.Join(comboFailures, "\n"))
	}
}

func TestNormalizedCacheMetrics(t *testing.T) {
	cases := []struct {
		name                string
		upstream            string
		usage               map[string]any
		wantRawInput        int64
		wantRawCached       int64
		wantRawCreated      int64
		wantNormalizedInput int64
		wantNormalizedCache int64
		wantRatio           float64
	}{
		{
			name:     "openai-style usage keeps total input as normalized denominator",
			upstream: config.UpstreamEndpointTypeResponses,
			usage: map[string]any{
				"input_tokens":  100,
				"output_tokens": 20,
				"total_tokens":  120,
				"input_tokens_details": map[string]any{
					"cached_tokens":         40,
					"cache_creation_tokens": 10,
				},
			},
			wantRawInput:        100,
			wantRawCached:       40,
			wantRawCreated:      10,
			wantNormalizedInput: 100,
			wantNormalizedCache: 40,
			wantRatio:           40,
		},
		{
			name:     "anthropic-style usage adds cache read and creation into normalized denominator",
			upstream: config.UpstreamEndpointTypeAnthropic,
			usage: map[string]any{
				"input_tokens":                50,
				"output_tokens":               20,
				"cache_read_input_tokens":     30,
				"cache_creation_input_tokens": 20,
			},
			wantRawInput:        50,
			wantRawCached:       30,
			wantRawCreated:      20,
			wantNormalizedInput: 100,
			wantNormalizedCache: 30,
			wantRatio:           30,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, normalized, ok := liveCacheMetricsFromUsage(tc.usage, tc.upstream)
			if !ok {
				t.Fatalf("liveCacheMetricsFromUsage(%s) reported no metrics", tc.upstream)
			}
			if raw.InputTokens != tc.wantRawInput || raw.CachedTokens != tc.wantRawCached || raw.CreatedTokens != tc.wantRawCreated {
				t.Fatalf("unexpected raw metrics: got=%#v want input=%d cached=%d created=%d", raw, tc.wantRawInput, tc.wantRawCached, tc.wantRawCreated)
			}
			if normalized.InputTokens != tc.wantNormalizedInput || normalized.CachedTokens != tc.wantNormalizedCache {
				t.Fatalf("unexpected normalized metrics: got=%#v want input=%d cached=%d", normalized, tc.wantNormalizedInput, tc.wantNormalizedCache)
			}
			if math.Abs(normalized.CacheRatio-tc.wantRatio) > 0.0001 {
				t.Fatalf("unexpected normalized ratio: got=%.4f want=%.4f", normalized.CacheRatio, tc.wantRatio)
			}
		})
	}

	observations := []liveCacheObservation{
		{
			Round:                 1,
			ExecutionPath:         liveCacheExecutionPathProxy,
			Downstream:            "responses",
			Upstream:              config.UpstreamEndpointTypeResponses,
			Turn:                  1,
			ProviderID:            "openai",
			Model:                 "gpt-test",
			RawUsageInputTokens:   100,
			RawUsageCachedTokens:  40,
			RawUsageCreatedTokens: 10,
			RawUsageOutputTokens:  20,
			RawUsageTotalTokens:   120,
			UsageInputTokens:      100,
			UsageCachedTokens:     40,
			CacheRatio:            40,
		},
		{
			Round:                 1,
			ExecutionPath:         liveCacheExecutionPathDirect,
			Downstream:            "responses",
			Upstream:              config.UpstreamEndpointTypeResponses,
			Turn:                  1,
			ProviderID:            "openai",
			Model:                 "gpt-test",
			RawUsageInputTokens:   120,
			RawUsageCachedTokens:  60,
			RawUsageCreatedTokens: 20,
			RawUsageOutputTokens:  20,
			RawUsageTotalTokens:   140,
			UsageInputTokens:      120,
			UsageCachedTokens:     60,
			CacheRatio:            50,
		},
		{
			Round:                1,
			ExecutionPath:        liveCacheExecutionPathProxy,
			Downstream:           "responses",
			Upstream:             config.UpstreamEndpointTypeResponses,
			Turn:                 2,
			ProviderID:           "openai",
			Model:                "gpt-test",
			RawUsageInputTokens:  100,
			RawUsageCachedTokens: 25,
			RawUsageOutputTokens: 20,
			RawUsageTotalTokens:  120,
			UsageInputTokens:     100,
			UsageCachedTokens:    25,
			CacheRatio:           25,
		},
		{
			Round:                1,
			ExecutionPath:        liveCacheExecutionPathDirect,
			Downstream:           "responses",
			Upstream:             config.UpstreamEndpointTypeResponses,
			Turn:                 2,
			ProviderID:           "openai",
			Model:                "gpt-test",
			RawUsageInputTokens:  100,
			RawUsageCachedTokens: 50,
			RawUsageOutputTokens: 20,
			RawUsageTotalTokens:  120,
			UsageInputTokens:     100,
			UsageCachedTokens:    50,
			CacheRatio:           50,
		},
	}

	rendered := renderLiveCacheAverageTable(observations)
	for _, needle := range []string{"## Raw Vendor Metrics", "## Direct Baseline", "## Proxy Preservation / Loss", "| turn1 |", "| turn2+ |", "| 50.00% |", "| 0.5000 |", "| 0.5000 |"} {
		if !strings.Contains(rendered, needle) {
			t.Fatalf("renderLiveCacheAverageTable missing %q\n%s", needle, rendered)
		}
	}
}

func TestProxyLossNAWhenDirectBaselineZero(t *testing.T) {
	observations := []liveCacheObservation{
		{
			Round:             1,
			ExecutionPath:     liveCacheExecutionPathProxy,
			Downstream:        "responses",
			Upstream:          config.UpstreamEndpointTypeResponses,
			Turn:              1,
			ProviderID:        "openai",
			Model:             "gpt-test",
			UsageInputTokens:  100,
			UsageCachedTokens: 90,
			CacheRatio:        90,
		},
		{
			Round:             1,
			ExecutionPath:     liveCacheExecutionPathDirect,
			Downstream:        "responses",
			Upstream:          config.UpstreamEndpointTypeResponses,
			Turn:              1,
			ProviderID:        "openai",
			Model:             "gpt-test",
			UsageInputTokens:  100,
			UsageCachedTokens: 100,
			CacheRatio:        100,
		},
		{
			Round:             1,
			ExecutionPath:     liveCacheExecutionPathProxy,
			Downstream:        "responses",
			Upstream:          config.UpstreamEndpointTypeResponses,
			Turn:              2,
			ProviderID:        "openai",
			Model:             "gpt-test",
			UsageInputTokens:  100,
			UsageCachedTokens: 25,
			CacheRatio:        25,
		},
		{
			Round:             1,
			ExecutionPath:     liveCacheExecutionPathDirect,
			Downstream:        "responses",
			Upstream:          config.UpstreamEndpointTypeResponses,
			Turn:              2,
			ProviderID:        "openai",
			Model:             "gpt-test",
			UsageInputTokens:  100,
			UsageCachedTokens: 0,
			CacheRatio:        0,
		},
	}

	rows := buildLiveCachePreservationLossRows(observations)
	if len(rows) != 1 {
		t.Fatalf("expected exactly one turn2+ preservation row, got %d: %#v", len(rows), rows)
	}
	if rows[0].TurnClass != "turn2+" {
		t.Fatalf("expected preservation row to exclude turn1 and stay in turn2+ bucket, got %#v", rows[0])
	}
	if rows[0].Preservation != nil || rows[0].Loss != nil {
		t.Fatalf("expected baseline-zero preservation/loss to be N/A, got %#v", rows[0])
	}

	rendered := renderLiveCachePreservationLossTable(observations)
	if !strings.Contains(rendered, "| turn2+ |") {
		t.Fatalf("expected rendered preservation table to keep turn2+ row\n%s", rendered)
	}
	if strings.Contains(rendered, "| turn1 |") {
		t.Fatalf("turn1 must be excluded from preservation table\n%s", rendered)
	}
	if !strings.Contains(rendered, "| N/A | N/A |") {
		t.Fatalf("expected rendered preservation table to show N/A for baseline-zero row\n%s", rendered)
	}
}

func TestLiveCacheMarksNonIsomorphicSamples(t *testing.T) {
	observations := []liveCacheObservation{
		{
			Round:             1,
			ExecutionPath:     liveCacheExecutionPathProxy,
			Downstream:        "chat",
			Upstream:          config.UpstreamEndpointTypeAnthropic,
			Turn:              2,
			ProviderID:        "anthropic",
			Model:             "claude-test",
			CanonicalDigest:   "canon-shared",
			InputDigest:       "pair-a",
			UsageInputTokens:  100,
			UsageCachedTokens: 50,
			CacheRatio:        50,
		},
		{
			Round:             1,
			ExecutionPath:     liveCacheExecutionPathDirect,
			Downstream:        "chat",
			Upstream:          config.UpstreamEndpointTypeAnthropic,
			Turn:              2,
			ProviderID:        "anthropic",
			Model:             "claude-test",
			CanonicalDigest:   "canon-shared",
			InputDigest:       "pair-a",
			UsageInputTokens:  100,
			UsageCachedTokens: 100,
			CacheRatio:        100,
		},
		{
			Round:             1,
			ExecutionPath:     liveCacheExecutionPathProxy,
			Downstream:        "chat",
			Upstream:          config.UpstreamEndpointTypeAnthropic,
			Turn:              3,
			ProviderID:        "anthropic",
			Model:             "claude-test",
			CanonicalDigest:   "canon-shared",
			InputDigest:       "pair-b-proxy",
			UsageInputTokens:  100,
			UsageCachedTokens: 90,
			CacheRatio:        90,
		},
		{
			Round:             1,
			ExecutionPath:     liveCacheExecutionPathDirect,
			Downstream:        "chat",
			Upstream:          config.UpstreamEndpointTypeAnthropic,
			Turn:              3,
			ProviderID:        "anthropic",
			Model:             "claude-test",
			CanonicalDigest:   "canon-shared",
			InputDigest:       "pair-b-direct",
			UsageInputTokens:  100,
			UsageCachedTokens: 90,
			CacheRatio:        90,
		},
	}

	rows := buildLiveCachePreservationLossRows(observations)
	if len(rows) != 1 {
		t.Fatalf("expected exactly one turn2+ preservation row, got %d: %#v", len(rows), rows)
	}
	if rows[0].Preservation == nil {
		t.Fatalf("expected at least one attributable pair to keep preservation, got %#v", rows[0])
	}
	if got, want := *rows[0].Preservation, 0.5; math.Abs(got-want) > 0.0001 {
		t.Fatalf("expected non-isomorphic sample to be excluded from loss attribution, got preservation=%.4f want=%.4f row=%#v", got, want, rows[0])
	}

	rendered := renderLiveCacheAverageTable(observations)
	if !strings.Contains(rendered, "not attributable") {
		t.Fatalf("expected rendered output to mark non-isomorphic sample as not attributable\n%s", rendered)
	}
	if !strings.Contains(rendered, "input digest mismatch") {
		t.Fatalf("expected rendered output to explain the comparability failure\n%s", rendered)
	}
}

func TestLiveCacheAllowsCrossProtocolShapeDrift(t *testing.T) {
	observations := []liveCacheObservation{
		{
			Round:             1,
			ExecutionPath:     liveCacheExecutionPathProxy,
			Downstream:        "chat",
			Upstream:          config.UpstreamEndpointTypeAnthropic,
			Turn:              2,
			ProviderID:        "anthropic",
			Model:             "claude-test",
			CanonicalDigest:   "canon-shared",
			InputDigest:       "input-shared",
			PayloadDigest:     "proxy-payload",
			MessageCount:      9,
			ToolCount:         1,
			ToolShape:         "chat-tool-shape",
			UsageInputTokens:  100,
			UsageCachedTokens: 40,
			CacheRatio:        40,
		},
		{
			Round:             1,
			ExecutionPath:     liveCacheExecutionPathDirect,
			Downstream:        "chat",
			Upstream:          config.UpstreamEndpointTypeAnthropic,
			Turn:              2,
			ProviderID:        "anthropic",
			Model:             "claude-test",
			CanonicalDigest:   "canon-shared",
			InputDigest:       "input-shared",
			PayloadDigest:     "direct-payload",
			MessageCount:      7,
			ToolCount:         1,
			ToolShape:         "anthropic-tool-shape",
			UsageInputTokens:  100,
			UsageCachedTokens: 80,
			CacheRatio:        80,
		},
	}

	rows := buildLiveCachePreservationLossRows(observations)
	if len(rows) != 1 {
		t.Fatalf("expected exactly one preservation row, got %d: %#v", len(rows), rows)
	}
	if rows[0].Preservation == nil {
		t.Fatalf("expected cross-protocol shape drift sample to stay attributable, got %#v", rows[0])
	}
	if got, want := *rows[0].Preservation, 0.5; math.Abs(got-want) > 0.0001 {
		t.Fatalf("unexpected preservation for cross-protocol drift: got %.4f want %.4f row=%#v", got, want, rows[0])
	}
	if rows[0].ExcludedTurns != 0 {
		t.Fatalf("expected zero excluded turns for cross-protocol shape drift, got %#v", rows[0])
	}
}

func TestLiveCacheStillRejectsToolCountMismatch(t *testing.T) {
	observations := []liveCacheObservation{
		{
			Round:             1,
			ExecutionPath:     liveCacheExecutionPathProxy,
			Downstream:        "chat",
			Upstream:          config.UpstreamEndpointTypeAnthropic,
			Turn:              2,
			ProviderID:        "anthropic",
			Model:             "claude-test",
			CanonicalDigest:   "canon-shared",
			InputDigest:       "input-shared",
			ToolCount:         2,
			UsageInputTokens:  100,
			UsageCachedTokens: 40,
			CacheRatio:        40,
		},
		{
			Round:             1,
			ExecutionPath:     liveCacheExecutionPathDirect,
			Downstream:        "chat",
			Upstream:          config.UpstreamEndpointTypeAnthropic,
			Turn:              2,
			ProviderID:        "anthropic",
			Model:             "claude-test",
			CanonicalDigest:   "canon-shared",
			InputDigest:       "input-shared",
			ToolCount:         1,
			UsageInputTokens:  100,
			UsageCachedTokens: 80,
			CacheRatio:        80,
		},
	}

	rendered := renderLiveCacheAverageTable(observations)
	if !strings.Contains(rendered, "tool count mismatch") {
		t.Fatalf("expected tool count mismatch to remain a hard exclusion\n%s", rendered)
	}
}

func TestLiveCacheDeferredIssuesOutput(t *testing.T) {
	rendered := renderLiveCacheAverageTable(nil)
	for _, needle := range []string{
		"## Deferred Issues",
		"recorded caveats, not fixes",
		"route/provider resolution differences",
		"internal/httpapi/routes.go:83-205",
		"responses IncludeUsage/history replay special handling",
		"internal/httpapi/handlers_responses.go:365-373",
		"chat vs anthropic tool replay shape differences",
		"internal/upstream/protocol.go:904-907",
		"internal/upstream/protocol.go:1062-1083",
		"cache metric normalization risk",
		"internal/httpapi/cacheinfo_usage.go:76-86",
		"internal/cacheinfo/render.go:68-72",
	} {
		if !strings.Contains(rendered, needle) {
			t.Fatalf("renderLiveCacheAverageTable missing deferred issue marker %q\n%s", needle, rendered)
		}
	}
}

func TestLiveCacheSendRequestUsesScopedTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadline := time.NewTimer(250 * time.Millisecond)
		defer deadline.Stop()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-deadline.C:
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true}`))
				return
			case <-ticker.C:
			}
		}
	}))
	defer server.Close()

	cfg := liveCacheRuntimeConfig{Timeout: 50 * time.Millisecond}
	spec := liveCacheProviderSpec{
		Label:         config.UpstreamEndpointTypeResponses,
		ProviderID:    "timeout-provider",
		Model:         "timeout-model",
		DirectBaseURL: server.URL,
		DirectAPIKey:  "timeout-direct-key",
	}
	binding := liveCacheScenarioBinding{ExecutionPath: liveCacheExecutionPathDirect, CanonicalDigest: "timeout-canonical"}

	start := time.Now()
	_, err := sendLiveCacheJSONRequest(&http.Client{}, cfg, spec, binding, "responses", map[string]any{"model": spec.Model}, nil)
	if err == nil {
		t.Fatalf("expected sendLiveCacheJSONRequest to fail fast on scoped timeout")
	}
	if !strings.Contains(err.Error(), "request_timeout=50ms") {
		t.Fatalf("expected timeout error to retain scoped timeout value, got %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 200*time.Millisecond {
		t.Fatalf("expected scoped timeout to fail well before server fallback, took %s err=%v", elapsed, err)
	}
}

func TestLiveCacheScenarioTimeoutErrorIncludesComboAttribution(t *testing.T) {
	var mu sync.Mutex
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestCount++
		current := requestCount
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch current {
		case 1:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"content": []map[string]any{{
					"type":  "tool_use",
					"id":    "tool-1",
					"name":  "lookup_project_facts",
					"input": map[string]any{"project": "atlas", "focus": "cache"},
				}},
				"usage": map[string]any{
					"input_tokens":                50,
					"cache_read_input_tokens":     0,
					"cache_creation_input_tokens": 0,
					"output_tokens":               20,
				},
			})
		case 2:
			select {
			case <-r.Context().Done():
				return
			case <-time.After(250 * time.Millisecond):
				_ = json.NewEncoder(w).Encode(map[string]any{
					"content": []map[string]any{{"type": "text", "text": "late follow-up"}},
					"usage": map[string]any{
						"input_tokens":                50,
						"cache_read_input_tokens":     10,
						"cache_creation_input_tokens": 0,
						"output_tokens":               20,
					},
				})
			}
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"content": []map[string]any{{"type": "text", "text": "follow-up ok"}},
				"usage": map[string]any{
					"input_tokens":                50,
					"cache_read_input_tokens":     10,
					"cache_creation_input_tokens": 0,
					"output_tokens":               20,
				},
			})
		}
	}))
	defer server.Close()

	cfg := liveCacheRuntimeConfig{
		BaseURL: "http://proxy.invalid",
		APIKey:  "proxy-key",
		Timeout: 50 * time.Millisecond,
	}
	spec := liveCacheProviderSpec{
		Label:         config.UpstreamEndpointTypeAnthropic,
		ProviderID:    "timeout-provider",
		Model:         "timeout-model",
		DirectBaseURL: server.URL,
		DirectAPIKey:  "timeout-direct-key",
	}

	observations, err := runLiveCacheScenarioForExecutionPath(&http.Client{}, cfg, 1, "chat", spec, liveCacheExecutionPathDirect)
	if err == nil {
		t.Fatalf("expected direct anthropic follow-up request to fail fast with attribution")
	}
	if len(observations) != 1 {
		t.Fatalf("expected timeout after first successful turn, got %d observations", len(observations))
	}
	for _, needle := range []string{
		"round=1",
		"execution_path=direct-control",
		"downstream=chat",
		"upstream=anthropic",
		"provider=timeout-provider",
		"model=timeout-model",
		"route=messages",
		"turn=2",
		"stage=follow-up",
		"request_timeout=50ms",
	} {
		if !strings.Contains(err.Error(), needle) {
			t.Fatalf("expected attributed timeout error to contain %q, got %v", needle, err)
		}
	}
}

func TestRunLiveCacheChatScenarioDirectRetriesStructuredToolChoiceWhenRequiredDoesNotToolCall(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if requestCount == 1 {
			if got, _ := payload["tool_choice"].(string); got != "required" {
				t.Fatalf("expected first direct chat request to use tool_choice=required, got %#v", payload["tool_choice"])
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{
						"role":    "assistant",
						"content": "我先直接回答，不走工具。",
					},
				}},
				"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 3, "total_tokens": 13},
			})
			return
		}
		toolChoice, _ := payload["tool_choice"].(map[string]any)
		if toolChoice == nil {
			t.Fatalf("expected retry to send structured tool_choice, got %#v", payload["tool_choice"])
		}
		if got, _ := toolChoice["type"].(string); got != "function" {
			t.Fatalf("expected retry tool_choice.type=function, got %#v", payload["tool_choice"])
		}
		function, _ := toolChoice["function"].(map[string]any)
		if got, _ := function["name"].(string); got != "lookup_project_facts" {
			t.Fatalf("expected retry tool_choice.function.name=lookup_project_facts, got %#v", payload["tool_choice"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{{
						"id":   "call-1",
						"type": "function",
						"function": map[string]any{
							"name":      "lookup_project_facts",
							"arguments": `{"project":"atlas","focus":"cache"}`,
						},
					}},
				},
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 3, "total_tokens": 13},
		})
	}))
	defer server.Close()

	cfg := liveCacheRuntimeConfig{Timeout: 5 * time.Second}
	spec := liveCacheProviderSpec{Label: config.UpstreamEndpointTypeChat, ProviderID: "chat", Model: "MiniMax-M2.7", DirectBaseURL: server.URL, DirectAPIKey: "direct-key"}
	binding := liveCacheScenarioBinding{
		ExecutionPath:   liveCacheExecutionPathDirect,
		CanonicalDigest: "canon-chat-retry",
		Scenario: liveCacheScenarioSpec{
			Round:    1,
			Upstream: config.UpstreamEndpointTypeChat,
			ToolName: "lookup_project_facts",
			Turns: []liveCacheScenarioTurn{{
				Number:         1,
				InputText:      strings.Repeat("a", liveCacheTurnOneMinInputChars),
				MinInputChars:  liveCacheTurnOneMinInputChars,
				ExpectToolCall: true,
			}},
		},
	}

	observations, err := runLiveCacheChatScenario(server.Client(), cfg, "chat", spec, binding)
	if err != nil {
		t.Fatalf("runLiveCacheChatScenario returned error: %v", err)
	}
	if requestCount != 2 {
		t.Fatalf("expected retry path to issue two requests, got %d", requestCount)
	}
	if len(observations) != 1 {
		t.Fatalf("expected one observation, got %d", len(observations))
	}
	if !observations[0].HasToolCall {
		t.Fatalf("expected retried direct chat turn to capture tool call, got %#v", observations[0])
	}
}

func TestRunLiveCacheChatScenarioDirectSanitizesAssistantToolArgumentsBeforeFollowUp(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		switch requestCount {
		case 1:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{
						"role": "assistant",
						"tool_calls": []map[string]any{{
							"id":   "call-1",
							"type": "function",
							"function": map[string]any{
								"name":      "lookup_project_facts",
								"arguments": `请使用 {"project":"atlas","focus":"cache"}`,
							},
						}},
					},
				}},
				"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 3, "total_tokens": 13},
			})
		case 2:
			rawMessages, _ := payload["messages"].([]any)
			if len(rawMessages) < 2 {
				t.Fatalf("expected follow-up history with assistant message, got %#v", payload["messages"])
			}
			assistant, _ := rawMessages[1].(map[string]any)
			toolCalls, _ := assistant["tool_calls"].([]any)
			if len(toolCalls) == 0 {
				t.Fatalf("expected assistant tool_calls in follow-up payload, got %#v", assistant)
			}
			toolCall, _ := toolCalls[0].(map[string]any)
			function, _ := toolCall["function"].(map[string]any)
			arguments, _ := function["arguments"].(string)
			if arguments != `{"project":"atlas","focus":"cache"}` {
				t.Fatalf("expected sanitized tool arguments in follow-up payload, got %q", arguments)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{
						"role":    "assistant",
						"content": "follow-up ok",
					},
				}},
				"usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 4, "total_tokens": 16},
			})
		default:
			t.Fatalf("unexpected request count %d", requestCount)
		}
	}))
	defer server.Close()

	cfg := liveCacheRuntimeConfig{Timeout: 5 * time.Second}
	spec := liveCacheProviderSpec{Label: config.UpstreamEndpointTypeChat, ProviderID: "chat", Model: "MiniMax-M2.7", DirectBaseURL: server.URL, DirectAPIKey: "direct-key"}
	binding := liveCacheScenarioBinding{
		ExecutionPath:   liveCacheExecutionPathDirect,
		CanonicalDigest: "canon-chat-sanitize",
		Scenario: liveCacheScenarioSpec{
			Round:    1,
			Upstream: config.UpstreamEndpointTypeChat,
			ToolName: "lookup_project_facts",
			Turns: []liveCacheScenarioTurn{
				{
					Number:         1,
					InputText:      strings.Repeat("a", liveCacheTurnOneMinInputChars),
					MinInputChars:  liveCacheTurnOneMinInputChars,
					ExpectToolCall: true,
				},
				{
					Number:            2,
					InputText:         strings.Repeat("b", liveCacheFollowUpMinInputChars),
					MinInputChars:     liveCacheFollowUpMinInputChars,
					IncludeToolResult: true,
					ToolResultJSON:    `{"cache_state":"warm"}`,
				},
			},
		},
	}

	observations, err := runLiveCacheChatScenario(server.Client(), cfg, "chat", spec, binding)
	if err != nil {
		t.Fatalf("runLiveCacheChatScenario returned error: %v", err)
	}
	if requestCount != 2 {
		t.Fatalf("expected two chat requests, got %d", requestCount)
	}
	if len(observations) != 2 {
		t.Fatalf("expected two observations, got %d", len(observations))
	}
}

func TestRunLiveCacheResponsesScenarioProxyUsesExpandedTurnOneBudgetForChatUpstream(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if r.URL.Path != "/chat/v1/responses" {
			t.Fatalf("unexpected request path %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		maxOutputTokens, _ := payload["max_output_tokens"].(float64)
		if requestCount == 1 && int(maxOutputTokens) < 256 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":                 "resp-incomplete",
				"object":             "response",
				"status":             "incomplete",
				"incomplete_details": map[string]any{"reason": "length"},
				"output": []map[string]any{{
					"id":     "msg-output",
					"type":   "message",
					"role":   "assistant",
					"status": "completed",
					"content": []map[string]any{{
						"type": "output_text",
						"text": "",
					}},
				}},
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 128, "total_tokens": 138},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "resp-ok",
			"output": []map[string]any{{
				"type":      "function_call",
				"call_id":   "call-1",
				"name":      "lookup_project_facts",
				"arguments": `{"project":"atlas","focus":"cache"}`,
			}},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 32, "total_tokens": 42},
		})
	}))
	defer server.Close()

	cfg := liveCacheRuntimeConfig{BaseURL: server.URL, APIKey: "proxy-key", Timeout: 5 * time.Second}
	spec := liveCacheProviderSpec{Label: config.UpstreamEndpointTypeChat, ProviderID: "chat", Model: "MiniMax-M2.7"}
	binding := liveCacheScenarioBinding{
		ExecutionPath:   liveCacheExecutionPathProxy,
		CanonicalDigest: "canon-responses-chat-budget",
		Scenario: liveCacheScenarioSpec{
			Round:    1,
			Upstream: config.UpstreamEndpointTypeChat,
			ToolName: "lookup_project_facts",
			Turns: []liveCacheScenarioTurn{{
				Number:         1,
				InputText:      strings.Repeat("a", liveCacheTurnOneMinInputChars),
				MinInputChars:  liveCacheTurnOneMinInputChars,
				ExpectToolCall: true,
			}},
		},
	}

	observations, err := runLiveCacheResponsesScenario(server.Client(), cfg, "responses", spec, binding)
	if err != nil {
		t.Fatalf("runLiveCacheResponsesScenario returned error: %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("expected expanded budget to avoid incomplete retry loop, got %d request(s)", requestCount)
	}
	if len(observations) != 1 {
		t.Fatalf("expected one observation, got %d", len(observations))
	}
	if !observations[0].HasToolCall {
		t.Fatalf("expected proxy responses/chat turn1 to capture tool call, got %#v", observations[0])
	}
}

func TestLiveCacheChatTurnOneMaxTokensForExecution(t *testing.T) {
	tests := []struct {
		name       string
		downstream string
		spec       liveCacheProviderSpec
		binding    liveCacheScenarioBinding
		want       int
	}{
		{
			name:       "proxy chat upstream expands chat downstream budget",
			downstream: "chat",
			spec:       liveCacheProviderSpec{Label: config.UpstreamEndpointTypeChat},
			binding:    liveCacheScenarioBinding{ExecutionPath: liveCacheExecutionPathProxy},
			want:       liveCacheTurnOneChatResponsesMaxOutputTokens,
		},
		{
			name:       "proxy chat upstream expands messages downstream budget",
			downstream: "messages",
			spec:       liveCacheProviderSpec{Label: config.UpstreamEndpointTypeChat},
			binding:    liveCacheScenarioBinding{ExecutionPath: liveCacheExecutionPathProxy},
			want:       liveCacheTurnOneChatResponsesMaxOutputTokens,
		},
		{
			name:       "direct chat upstream keeps default budget",
			downstream: "chat",
			spec:       liveCacheProviderSpec{Label: config.UpstreamEndpointTypeChat},
			binding:    liveCacheScenarioBinding{ExecutionPath: liveCacheExecutionPathDirect},
			want:       liveCacheMaxOutputTokens,
		},
		{
			name:       "proxy non chat upstream keeps default budget",
			downstream: "chat",
			spec:       liveCacheProviderSpec{Label: config.UpstreamEndpointTypeResponses},
			binding:    liveCacheScenarioBinding{ExecutionPath: liveCacheExecutionPathProxy},
			want:       liveCacheMaxOutputTokens,
		},
		{
			name:       "proxy anthropic upstream expands chat downstream budget",
			downstream: "chat",
			spec:       liveCacheProviderSpec{Label: config.UpstreamEndpointTypeAnthropic},
			binding:    liveCacheScenarioBinding{ExecutionPath: liveCacheExecutionPathProxy},
			want:       liveCacheAnthropicMessagesTurnOneMaxTokens,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := liveCacheChatTurnOneMaxTokensForExecution(tc.downstream, tc.spec, tc.binding); got != tc.want {
				t.Fatalf("liveCacheChatTurnOneMaxTokensForExecution(%s, %#v, %#v) = %d, want %d", tc.downstream, tc.spec, tc.binding, got, tc.want)
			}
		})
	}
}

func TestLiveCacheSendRequestNormalizesDirectResponsesSSE(t *testing.T) {
	testLiveCacheDirectSSENormalization(t, config.UpstreamEndpointTypeResponses, "responses", responsesToolCallSSEFixture(), func(body []byte) error {
		_, toolCall, _, _, _, err := parseResponsesTurnOne(body)
		if err != nil {
			return err
		}
		if toolCall.Name != "lookup_project_facts" {
			return fmt.Errorf("unexpected tool name %q", toolCall.Name)
		}
		return nil
	})
}

func TestLiveCacheSendRequestNormalizesDirectChatSSE(t *testing.T) {
	testLiveCacheDirectSSENormalization(t, config.UpstreamEndpointTypeChat, "chat/completions", chatToolCallSSEFixture(), func(body []byte) error {
		_, toolCall, _, _, err := parseChatTurnOne(body)
		if err != nil {
			return err
		}
		if toolCall.Name != "lookup_project_facts" {
			return fmt.Errorf("unexpected tool name %q", toolCall.Name)
		}
		return nil
	})
}

func TestLiveCacheSendRequestNormalizesDirectAnthropicSSE(t *testing.T) {
	testLiveCacheDirectSSENormalization(t, config.UpstreamEndpointTypeAnthropic, "messages", anthropicTextSSEFixture(), func(body []byte) error {
		content, preview, usage, err := parseAnthropicTurnFollowUp(body)
		if err != nil {
			return err
		}
		if len(content) == 0 {
			return fmt.Errorf("anthropic content missing")
		}
		if preview == "" {
			return fmt.Errorf("anthropic preview missing")
		}
		if usage == nil {
			return fmt.Errorf("anthropic usage missing")
		}
		return nil
	})
}

func TestSendLiveCacheJSONRequestRetriesTransientUpstreamStatus(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount == 1 {
			w.WriteHeader(529)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-ok","output":[{"type":"function_call","call_id":"call-1","name":"lookup_project_facts","arguments":"{\"project\":\"atlas\",\"focus\":\"cache\"}"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`))
	}))
	defer server.Close()

	cfg := liveCacheRuntimeConfig{Timeout: 5 * time.Second}
	spec := liveCacheProviderSpec{Label: config.UpstreamEndpointTypeResponses, ProviderID: "test-provider", Model: "test-model", DirectBaseURL: server.URL, DirectAPIKey: "direct-key"}
	binding := liveCacheScenarioBinding{ExecutionPath: liveCacheExecutionPathDirect, CanonicalDigest: "retry-canonical"}
	payload := map[string]any{
		"model": "test-model",
		"input": []map[string]any{{
			"role": "user",
			"content": []map[string]any{{
				"type": "input_text",
				"text": strings.Repeat("a", liveCacheTurnOneMinInputChars),
			}},
		}},
	}

	body, err := sendLiveCacheJSONRequest(server.Client(), cfg, spec, binding, "responses", payload, nil)
	if err != nil {
		t.Fatalf("sendLiveCacheJSONRequest returned error: %v", err)
	}
	if requestCount != 2 {
		t.Fatalf("expected one retry after transient upstream status, got %d requests", requestCount)
	}
	if _, toolCall, _, _, _, err := parseResponsesTurnOne(body); err != nil || toolCall.Name != "lookup_project_facts" {
		t.Fatalf("expected retried response body to parse, toolCall=%#v err=%v", toolCall, err)
	}
}

func testLiveCacheDirectSSENormalization(t *testing.T, label, route, responseBody string, verify func([]byte) error) {
	t.Helper()

	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(responseBody))
	}))
	defer server.Close()

	cfg := liveCacheRuntimeConfig{Timeout: time.Second}
	spec := liveCacheProviderSpec{
		Label:         label,
		ProviderID:    "test-provider",
		Model:         "test-model",
		DirectBaseURL: server.URL,
		DirectAPIKey:  "test-direct-api-key",
	}
	binding := liveCacheScenarioBinding{ExecutionPath: liveCacheExecutionPathDirect, CanonicalDigest: "test-canonical"}

	body, err := sendLiveCacheJSONRequest(&http.Client{}, cfg, spec, binding, route, map[string]any{"model": spec.Model}, map[string]string{"anthropic-version": "2023-06-01"})
	if err != nil {
		t.Fatalf("sendLiveCacheJSONRequest(%s): %v", route, err)
	}
	if stream, _ := received["stream"].(bool); !stream {
		t.Fatalf("expected direct %s request to force stream=true, got payload=%v", route, received)
	}
	if err := verify(body); err != nil {
		t.Fatalf("verify normalized %s body: %v\nbody=%s", route, err, string(body))
	}
}

func responsesToolCallSSEFixture() string {
	return strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_test","usage":null}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_test","type":"function_call","status":"in_progress","arguments":"","call_id":"call_test","name":"lookup_project_facts"}}`,
		"",
		"event: response.function_call_arguments.delta",
		`data: {"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_test","delta":"{\"project\":\"atlas\"}"}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"fc_test","type":"function_call","status":"completed","arguments":"{\"project\":\"atlas\"}","call_id":"call_test","name":"lookup_project_facts"}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_test","usage":{"input_tokens":47,"input_tokens_details":{"cached_tokens":0},"output_tokens":27,"total_tokens":74},"output":[{"id":"fc_test","type":"function_call","status":"completed","arguments":"{\"project\":\"atlas\"}","call_id":"call_test","name":"lookup_project_facts"}]}}`,
		"",
	}, "\n")
}

func chatToolCallSSEFixture() string {
	return strings.Join([]string{
		`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_test","type":"function","function":{"name":"lookup_project_facts","arguments":""}}]},"finish_reason":null}],"usage":null}`,
		"",
		`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"project\":\"atlas\"}"}}]},"finish_reason":null}],"usage":null}`,
		"",
		`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":null},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":47,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens":27,"total_tokens":74}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
}

func anthropicTextSSEFixture() string {
	return strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","content":[],"usage":{"input_tokens":0,"output_tokens":0}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":47,"output_tokens":11}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")
}

func TestLiveCacheScenarioSpec(t *testing.T) {
	for _, upstream := range liveCacheUpstreams {
		t.Run(upstream, func(t *testing.T) {
			for _, downstream := range liveCacheDownstreams {
				t.Run(downstream, func(t *testing.T) {
					probe := newLiveCacheScenarioProbe(t)
					defer probe.Close()

					spec := liveCacheProviderSpec{
						Label:         upstream,
						ProviderID:    "test-provider",
						Model:         "test-model",
						DirectBaseURL: probe.URL(),
						DirectAPIKey:  "test-direct-api-key",
					}
					cfg := liveCacheRuntimeConfig{
						BaseURL: probe.URL(),
						APIKey:  "test-api-key",
						Timeout: time.Minute,
					}

					observations, err := runLiveCacheScenario(probe.Client(), cfg, 1, downstream, spec)
					if err != nil {
						t.Fatalf("runLiveCacheScenario(%s, %s): %v", upstream, downstream, err)
					}

					inputs := probe.Inputs(downstream)
					if len(observations) != len(inputs) {
						t.Fatalf("observation/request count mismatch for %s/%s: got observations=%d requests=%d", upstream, downstream, len(observations), len(inputs))
					}
					if len(inputs) != liveCacheCanonicalTurnCount {
						t.Fatalf("scenario for %s/%s must produce exactly %d turns, got %d", upstream, downstream, liveCacheCanonicalTurnCount, len(inputs))
					}
					if got := len(inputs[0]); got <= 10000 {
						t.Fatalf("turn 1 input for %s/%s must be >10000 chars, got %d", upstream, downstream, got)
					}
					for turnIndex := 1; turnIndex < len(inputs); turnIndex++ {
						if got := len(inputs[turnIndex]); got <= 5000 {
							t.Fatalf("turn %d input for %s/%s must be >5000 chars, got %d", turnIndex+1, upstream, downstream, got)
						}
					}
				})
			}
		})
	}
}

func TestLiveCacheScenarioIsomorphicDigest(t *testing.T) {
	for _, upstream := range liveCacheUpstreams {
		t.Run(upstream, func(t *testing.T) {
			probe := newLiveCacheScenarioProbe(t)
			defer probe.Close()

			spec := liveCacheProviderSpec{
				Label:      upstream,
				ProviderID: "test-provider",
				Model:      "test-model",
			}
			cfg := liveCacheRuntimeConfig{
				BaseURL: probe.URL(),
				APIKey:  "test-api-key",
				Timeout: time.Minute,
			}

			for _, downstream := range liveCacheDownstreams {
				if _, err := runLiveCacheScenario(probe.Client(), cfg, 1, downstream, spec); err != nil {
					t.Fatalf("runLiveCacheScenario(%s, %s): %v", upstream, downstream, err)
				}
			}

			responsesInputs := probe.Inputs("responses")
			chatInputs := probe.Inputs("chat")
			messageInputs := probe.Inputs("messages")
			if len(responsesInputs) == 0 || len(chatInputs) == 0 || len(messageInputs) == 0 {
				t.Fatalf("all downstreams must emit captured inputs, got responses=%d chat=%d messages=%d", len(responsesInputs), len(chatInputs), len(messageInputs))
			}
			if len(responsesInputs) != len(chatInputs) || len(responsesInputs) != len(messageInputs) {
				t.Fatalf("isomorphic scenario requires equal turn counts, got responses=%d chat=%d messages=%d", len(responsesInputs), len(chatInputs), len(messageInputs))
			}

			for turnIndex := range responsesInputs {
				responsesDigest := digestLiveCacheInput(responsesInputs[turnIndex])
				chatDigest := digestLiveCacheInput(chatInputs[turnIndex])
				messagesDigest := digestLiveCacheInput(messageInputs[turnIndex])
				if responsesDigest != chatDigest || responsesDigest != messagesDigest {
					t.Fatalf("turn %d scenario digest drifted across downstreams for upstream=%s: responses=%s chat=%s messages=%s", turnIndex+1, upstream, responsesDigest, chatDigest, messagesDigest)
				}
			}
		})
	}
}

func TestLiveCacheDirectControlPairing(t *testing.T) {
	for _, upstream := range liveCacheUpstreams {
		t.Run(upstream, func(t *testing.T) {
			probe := newLiveCacheScenarioProbe(t)
			defer probe.Close()

			spec := liveCacheProviderSpec{
				Label:         upstream,
				ProviderID:    "test-provider",
				Model:         "test-model",
				DirectBaseURL: probe.URL(),
				DirectAPIKey:  "test-direct-api-key",
			}
			cfg := liveCacheRuntimeConfig{
				BaseURL: probe.URL(),
				APIKey:  "test-api-key",
				Timeout: time.Minute,
			}
			directReuse := map[liveCacheDirectReuseKey]liveCacheDirectReuseResult{}

			for _, downstream := range liveCacheDownstreams {
				if _, err := runLiveCacheScenarioForExecutionPathWithDirectReuse(probe.Client(), cfg, 1, downstream, spec, liveCacheExecutionPathDirect, directReuse); err != nil {
					t.Fatalf("direct round 1 runLiveCacheScenarioForExecutionPath(%s, %s): %v", upstream, downstream, err)
				}
				if _, err := runLiveCacheScenarioForExecutionPathWithDirectReuse(probe.Client(), cfg, 2, downstream, spec, liveCacheExecutionPathDirect, directReuse); err != nil {
					t.Fatalf("direct round 2 runLiveCacheScenarioForExecutionPath(%s, %s): %v", upstream, downstream, err)
				}
			}

			route, err := liveCacheTransportDownstreamForExecutionPath(liveCacheExecutionPathDirect, liveCacheDownstreams[0], upstream)
			if err != nil {
				t.Fatalf("liveCacheTransportDownstreamForExecutionPath(%s, %s, %s): %v", liveCacheExecutionPathDirect, liveCacheDownstreams[0], upstream, err)
			}
			requests := probe.Requests(liveCacheExecutionPathDirect, route)
			if len(requests) != liveCacheCanonicalTurnCount*2 {
				t.Fatalf("direct requests for %s should be shared across downstreams per round, got %d want %d", upstream, len(requests), liveCacheCanonicalTurnCount*2)
			}
			for turnIndex := 0; turnIndex < liveCacheCanonicalTurnCount; turnIndex++ {
				first := requests[turnIndex]
				second := requests[turnIndex+liveCacheCanonicalTurnCount]
				if first.InputDigest == second.InputDigest {
					t.Fatalf("direct round pairing reused turn %d for %s: digest=%s", turnIndex+1, upstream, first.InputDigest)
				}
			}
		})
	}
}

func TestLiveCacheProxyAndDirectIsomorphic(t *testing.T) {
	for _, upstream := range liveCacheUpstreams {
		t.Run(upstream, func(t *testing.T) {
			for _, downstream := range liveCacheDownstreams {
				t.Run(downstream, func(t *testing.T) {
					probe := newLiveCacheScenarioProbe(t)
					defer probe.Close()

					spec := liveCacheProviderSpec{
						Label:         upstream,
						ProviderID:    "test-provider",
						Model:         "test-model",
						DirectBaseURL: probe.URL(),
						DirectAPIKey:  "test-direct-api-key",
					}
					cfg := liveCacheRuntimeConfig{
						BaseURL: probe.URL(),
						APIKey:  "test-api-key",
						Timeout: time.Minute,
					}

					proxyBinding := bindLiveCacheScenarioForExecutionPath(liveCacheExecutionPathProxy, buildLiveCacheScenarioSpec(1, upstream))
					directBinding := bindLiveCacheScenarioForExecutionPath(liveCacheExecutionPathDirect, buildLiveCacheScenarioSpec(1, upstream))

					if _, err := runLiveCacheScenarioForExecutionPath(probe.Client(), cfg, 1, downstream, spec, liveCacheExecutionPathProxy); err != nil {
						t.Fatalf("proxy runLiveCacheScenarioForExecutionPath(%s, %s): %v", upstream, downstream, err)
					}
					if _, err := runLiveCacheScenarioForExecutionPath(probe.Client(), cfg, 1, downstream, spec, liveCacheExecutionPathDirect); err != nil {
						t.Fatalf("direct runLiveCacheScenarioForExecutionPath(%s, %s): %v", upstream, downstream, err)
					}

					proxyRoute, err := liveCacheTransportDownstreamForExecutionPath(liveCacheExecutionPathProxy, downstream, upstream)
					if err != nil {
						t.Fatalf("proxy liveCacheTransportDownstreamForExecutionPath(%s, %s, %s): %v", liveCacheExecutionPathProxy, downstream, upstream, err)
					}
					directRoute, err := liveCacheTransportDownstreamForExecutionPath(liveCacheExecutionPathDirect, downstream, upstream)
					if err != nil {
						t.Fatalf("direct liveCacheTransportDownstreamForExecutionPath(%s, %s, %s): %v", liveCacheExecutionPathDirect, downstream, upstream, err)
					}

					proxyRequests := probe.Requests(liveCacheExecutionPathProxy, proxyRoute)
					directRequests := probe.Requests(liveCacheExecutionPathDirect, directRoute)
					if len(proxyRequests) != liveCacheCanonicalTurnCount || len(directRequests) != liveCacheCanonicalTurnCount {
						t.Fatalf("proxy/direct requests for %s/%s must both cover %d turns, got proxy=%d direct=%d", upstream, downstream, liveCacheCanonicalTurnCount, len(proxyRequests), len(directRequests))
					}

					for turnIndex := range proxyRequests {
						proxyReq := proxyRequests[turnIndex]
						directReq := directRequests[turnIndex]
						if proxyReq.CanonicalDigest == "" || directReq.CanonicalDigest == "" {
							t.Fatalf("proxy/direct canonical digest must be explicit for %s/%s turn %d, got proxy=%q direct=%q", upstream, downstream, turnIndex+1, proxyReq.CanonicalDigest, directReq.CanonicalDigest)
						}
						if proxyReq.CanonicalDigest != proxyBinding.CanonicalDigest || directReq.CanonicalDigest != directBinding.CanonicalDigest {
							t.Fatalf("proxy/direct canonical digest drifted for %s/%s turn %d: proxy=%s direct=%s want=%s", upstream, downstream, turnIndex+1, proxyReq.CanonicalDigest, directReq.CanonicalDigest, proxyBinding.CanonicalDigest)
						}
						if proxyReq.InputDigest != directReq.InputDigest {
							t.Fatalf("proxy/direct input digest drifted for %s/%s turn %d: proxy=%s direct=%s", upstream, downstream, turnIndex+1, proxyReq.InputDigest, directReq.InputDigest)
						}
						if proxyReq.Model != directReq.Model {
							t.Fatalf("proxy/direct model drifted for %s/%s turn %d: proxy=%s direct=%s", upstream, downstream, turnIndex+1, proxyReq.Model, directReq.Model)
						}
						if proxyReq.ToolName != directReq.ToolName {
							t.Fatalf("proxy/direct tool name drifted for %s/%s turn %d: proxy=%q direct=%q", upstream, downstream, turnIndex+1, proxyReq.ToolName, directReq.ToolName)
						}
						if proxyReq.ToolSchemaDigest != directReq.ToolSchemaDigest {
							t.Fatalf("proxy/direct tool schema drifted for %s/%s turn %d: proxy=%s direct=%s", upstream, downstream, turnIndex+1, proxyReq.ToolSchemaDigest, directReq.ToolSchemaDigest)
						}
						if proxyReq.ToolResultJSON != directReq.ToolResultJSON {
							t.Fatalf("proxy/direct tool result drifted for %s/%s turn %d: proxy=%q direct=%q", upstream, downstream, turnIndex+1, proxyReq.ToolResultJSON, directReq.ToolResultJSON)
						}
						if proxyReq.MaxTokens != directReq.MaxTokens {
							if upstream == config.UpstreamEndpointTypeChat && turnIndex == 0 && proxyReq.MaxTokens == liveCacheTurnOneChatResponsesMaxOutputTokens && directReq.MaxTokens == liveCacheMaxOutputTokens {
								continue
							}
							if upstream == config.UpstreamEndpointTypeAnthropic && downstream == "chat" && turnIndex == 0 && proxyReq.MaxTokens == liveCacheAnthropicMessagesTurnOneMaxTokens && directReq.MaxTokens == liveCacheTurnOneAnthropicMaxOutputTokens {
								continue
							}
							t.Fatalf("proxy/direct token budget drifted for %s/%s turn %d: proxy=%d direct=%d", upstream, downstream, turnIndex+1, proxyReq.MaxTokens, directReq.MaxTokens)
						}
					}
				})
			}
		})
	}
}

func runLiveCacheScenarioForExecutionPath(client *http.Client, cfg liveCacheRuntimeConfig, round int, downstream string, spec liveCacheProviderSpec, path liveCacheExecutionPath) ([]liveCacheObservation, error) {
	binding := bindLiveCacheScenarioForExecutionPath(path, buildLiveCacheScenarioSpec(round, spec.Label))
	return runLiveCacheScenarioBinding(client, cfg, downstream, spec, binding)
}

func runLiveCacheScenarioForExecutionPathWithDirectReuse(client *http.Client, cfg liveCacheRuntimeConfig, round int, downstream string, spec liveCacheProviderSpec, path liveCacheExecutionPath, directReuse map[liveCacheDirectReuseKey]liveCacheDirectReuseResult) ([]liveCacheObservation, error) {
	if path != liveCacheExecutionPathDirect {
		return runLiveCacheScenarioForExecutionPath(client, cfg, round, downstream, spec, path)
	}
	key := liveCacheDirectReuseKey{Round: round, Upstream: spec.Label, ProviderID: spec.ProviderID, Model: spec.Model}
	if cached, ok := directReuse[key]; ok {
		return cloneLiveCacheObservationsForDownstream(cached.Observations, downstream), cached.Err
	}
	observations, err := runLiveCacheScenarioForExecutionPath(client, cfg, round, downstream, spec, path)
	directReuse[key] = liveCacheDirectReuseResult{Observations: cloneLiveCacheObservations(observations), Err: err}
	return observations, err
}

func cloneLiveCacheObservations(observations []liveCacheObservation) []liveCacheObservation {
	if len(observations) == 0 {
		return nil
	}
	cloned := make([]liveCacheObservation, len(observations))
	copy(cloned, observations)
	return cloned
}

func cloneLiveCacheObservationsForDownstream(observations []liveCacheObservation, downstream string) []liveCacheObservation {
	cloned := cloneLiveCacheObservations(observations)
	for i := range cloned {
		cloned[i].Downstream = downstream
	}
	return cloned
}

func liveCacheTransportDownstreamForExecutionPath(path liveCacheExecutionPath, downstream, upstream string) (string, error) {
	switch path {
	case liveCacheExecutionPathProxy:
		return downstream, nil
	case liveCacheExecutionPathDirect:
		switch upstream {
		case config.UpstreamEndpointTypeResponses:
			return "responses", nil
		case config.UpstreamEndpointTypeChat:
			return "chat", nil
		case config.UpstreamEndpointTypeAnthropic:
			return "messages", nil
		default:
			return "", fmt.Errorf("unsupported upstream family %q", upstream)
		}
	default:
		return "", fmt.Errorf("unsupported execution path %q", path)
	}
}

func runLiveCacheScenarioBinding(client *http.Client, cfg liveCacheRuntimeConfig, downstream string, spec liveCacheProviderSpec, binding liveCacheScenarioBinding) ([]liveCacheObservation, error) {
	transportDownstream, err := liveCacheTransportDownstreamForExecutionPath(binding.ExecutionPath, downstream, spec.Label)
	if err != nil {
		return nil, err
	}
	switch transportDownstream {
	case "responses":
		return runLiveCacheResponsesScenario(client, cfg, downstream, spec, binding)
	case "chat":
		return runLiveCacheChatScenario(client, cfg, downstream, spec, binding)
	case "messages":
		return runLiveCacheAnthropicScenario(client, cfg, downstream, spec, binding)
	default:
		return nil, fmt.Errorf("unsupported downstream route %q", transportDownstream)
	}
}

type liveCacheScenarioProbeRequest struct {
	ExecutionPath    liveCacheExecutionPath
	TransportRoute   string
	RequestPath      string
	CanonicalDigest  string
	Model            string
	InputText        string
	InputDigest      string
	MaxTokens        int
	ToolName         string
	ToolSchemaDigest string
	ToolResultJSON   string
}

type liveCacheScenarioProbe struct {
	t *testing.T

	server *httptest.Server

	mu          sync.Mutex
	requestTurn map[string]int
	inputs      map[string][]string
	requests    []liveCacheScenarioProbeRequest
}

func newLiveCacheScenarioProbe(t *testing.T) *liveCacheScenarioProbe {
	t.Helper()

	probe := &liveCacheScenarioProbe{
		t:           t,
		requestTurn: make(map[string]int),
		inputs:      make(map[string][]string),
	}
	probe.server = httptest.NewServer(http.HandlerFunc(probe.handle))
	return probe
}

func (p *liveCacheScenarioProbe) Close() {
	p.server.Close()
}

func (p *liveCacheScenarioProbe) URL() string {
	return p.server.URL
}

func (p *liveCacheScenarioProbe) Client() *http.Client {
	return p.server.Client()
}

func (p *liveCacheScenarioProbe) Inputs(downstream string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	raw := p.inputs[downstream]
	return append([]string(nil), raw...)
}

func (p *liveCacheScenarioProbe) Requests(path liveCacheExecutionPath, route string) []liveCacheScenarioProbeRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	matched := make([]liveCacheScenarioProbeRequest, 0)
	for _, request := range p.requests {
		if request.ExecutionPath != path {
			continue
		}
		if route != "" && request.TransportRoute != route {
			continue
		}
		matched = append(matched, request)
	}
	return append([]liveCacheScenarioProbeRequest(nil), matched...)
}

func (p *liveCacheScenarioProbe) handle(w http.ResponseWriter, r *http.Request) {
	p.t.Helper()

	downstream, ok := liveCacheScenarioProbeDownstream(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		p.t.Fatalf("read %s probe body: %v", downstream, err)
	}
	_ = r.Body.Close()

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		p.t.Fatalf("decode %s probe body: %v", downstream, err)
	}
	userInput, err := liveCacheScenarioProbeLatestUserInput(downstream, payload)
	if err != nil {
		p.t.Fatalf("extract %s latest user input: %v", downstream, err)
	}
	request := liveCacheScenarioProbeRequest{
		ExecutionPath:    liveCacheExecutionPath(strings.TrimSpace(r.Header.Get("X-Live-Cache-Execution-Path"))),
		TransportRoute:   downstream,
		RequestPath:      r.URL.Path,
		CanonicalDigest:  strings.TrimSpace(r.Header.Get("X-Live-Cache-Canonical-Digest")),
		Model:            strings.TrimSpace(liveCacheScenarioProbeStringField(payload, "model")),
		InputText:        userInput,
		InputDigest:      digestLiveCacheInput(userInput),
		MaxTokens:        liveCacheScenarioProbeMaxTokens(payload),
		ToolName:         liveCacheScenarioProbeToolName(payload),
		ToolSchemaDigest: liveCacheScenarioProbeToolSchemaDigest(payload),
		ToolResultJSON:   liveCacheScenarioProbeToolResultJSON(downstream, payload),
	}

	p.mu.Lock()
	requestKey := string(request.ExecutionPath) + "|" + downstream + "|" + request.CanonicalDigest
	p.requestTurn[requestKey]++
	turn := p.requestTurn[requestKey]
	p.inputs[downstream] = append(p.inputs[downstream], userInput)
	p.requests = append(p.requests, request)
	p.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(liveCacheScenarioProbeResponse(downstream, turn)); err != nil {
		p.t.Fatalf("encode %s probe response: %v", downstream, err)
	}
}

func liveCacheScenarioProbeDownstream(path string) (string, bool) {
	switch {
	case strings.HasSuffix(path, "/v1/responses"):
		return "responses", true
	case strings.HasSuffix(path, "/v1/chat/completions"):
		return "chat", true
	case strings.HasSuffix(path, "/v1/messages"):
		return "messages", true
	default:
		return "", false
	}
}

func liveCacheScenarioProbeLatestUserInput(downstream string, payload map[string]any) (string, error) {
	switch downstream {
	case "responses":
		return liveCacheScenarioProbeLatestResponsesInput(payload)
	case "chat":
		return liveCacheScenarioProbeLatestChatInput(payload)
	case "messages":
		return liveCacheScenarioProbeLatestAnthropicInput(payload)
	default:
		return "", fmt.Errorf("unsupported probe downstream %q", downstream)
	}
}

func liveCacheScenarioProbeLatestResponsesInput(payload map[string]any) (string, error) {
	rawItems, _ := payload["input"].([]any)
	for i := len(rawItems) - 1; i >= 0; i-- {
		item, _ := rawItems[i].(map[string]any)
		if item == nil {
			continue
		}
		if role, _ := item["role"].(string); role != "user" {
			continue
		}
		if text := liveCacheScenarioProbeTextBlock(item["content"], "input_text", "text"); text != "" {
			return text, nil
		}
	}
	return "", fmt.Errorf("responses payload missing user input")
}

func liveCacheScenarioProbeLatestChatInput(payload map[string]any) (string, error) {
	rawMessages, _ := payload["messages"].([]any)
	for i := len(rawMessages) - 1; i >= 0; i-- {
		message, _ := rawMessages[i].(map[string]any)
		if message == nil {
			continue
		}
		if role, _ := message["role"].(string); role != "user" {
			continue
		}
		if text, _ := message["content"].(string); text != "" {
			return text, nil
		}
	}
	return "", fmt.Errorf("chat payload missing user input")
}

func liveCacheScenarioProbeLatestAnthropicInput(payload map[string]any) (string, error) {
	rawMessages, _ := payload["messages"].([]any)
	for i := len(rawMessages) - 1; i >= 0; i-- {
		message, _ := rawMessages[i].(map[string]any)
		if message == nil {
			continue
		}
		if role, _ := message["role"].(string); role != "user" {
			continue
		}
		if text := liveCacheScenarioProbeTextBlock(message["content"], "text"); text != "" {
			return text, nil
		}
	}
	return "", fmt.Errorf("messages payload missing user input")
}

func liveCacheScenarioProbeTextBlock(raw any, allowedTypes ...string) string {
	typeSet := make(map[string]struct{}, len(allowedTypes))
	for _, blockType := range allowedTypes {
		typeSet[blockType] = struct{}{}
	}
	blocks, _ := raw.([]any)
	for i := len(blocks) - 1; i >= 0; i-- {
		block, _ := blocks[i].(map[string]any)
		if block == nil {
			continue
		}
		blockType, _ := block["type"].(string)
		if _, ok := typeSet[blockType]; !ok {
			continue
		}
		if text, _ := block["text"].(string); text != "" {
			return text
		}
	}
	return ""
}

func liveCacheScenarioProbeStringField(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func liveCacheScenarioProbeMaxTokens(payload map[string]any) int {
	for _, key := range []string{"max_output_tokens", "max_tokens"} {
		switch value := payload[key].(type) {
		case float64:
			return int(value)
		case int:
			return value
		case int64:
			return int(value)
		case json.Number:
			parsed, _ := value.Int64()
			return int(parsed)
		}
	}
	return 0
}

func liveCacheScenarioProbeToolName(payload map[string]any) string {
	normalized := liveCacheScenarioProbeNormalizedTool(payload)
	name, _ := normalized["name"].(string)
	return name
}

func liveCacheScenarioProbeToolSchemaDigest(payload map[string]any) string {
	normalized := liveCacheScenarioProbeNormalizedTool(payload)
	if len(normalized) == 0 {
		return ""
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return ""
	}
	return digestLiveCacheInput(string(encoded))
}

func liveCacheScenarioProbeNormalizedTool(payload map[string]any) map[string]any {
	rawTools, _ := payload["tools"].([]any)
	if len(rawTools) == 0 {
		return nil
	}
	tool, _ := rawTools[0].(map[string]any)
	if tool == nil {
		return nil
	}
	name, description, schema := "", "", any(nil)
	if function, _ := tool["function"].(map[string]any); function != nil {
		name, _ = function["name"].(string)
		description, _ = function["description"].(string)
		schema = function["parameters"]
	} else {
		name, _ = tool["name"].(string)
		description, _ = tool["description"].(string)
		schema = tool["parameters"]
		if schema == nil {
			schema = tool["input_schema"]
		}
	}
	if name == "" {
		return nil
	}
	normalized := map[string]any{"name": name}
	if description != "" {
		normalized["description"] = description
	}
	if schema != nil {
		normalized["schema"] = schema
	}
	return normalized
}

func liveCacheScenarioProbeToolResultJSON(downstream string, payload map[string]any) string {
	switch downstream {
	case "responses":
		rawItems, _ := payload["input"].([]any)
		for i := len(rawItems) - 1; i >= 0; i-- {
			item, _ := rawItems[i].(map[string]any)
			if item == nil {
				continue
			}
			if itemType, _ := item["type"].(string); itemType == "function_call_output" {
				output, _ := item["output"].(string)
				return output
			}
		}
	case "chat":
		rawMessages, _ := payload["messages"].([]any)
		for i := len(rawMessages) - 1; i >= 0; i-- {
			message, _ := rawMessages[i].(map[string]any)
			if message == nil {
				continue
			}
			if role, _ := message["role"].(string); role == "tool" {
				content, _ := message["content"].(string)
				return content
			}
		}
	case "messages":
		rawMessages, _ := payload["messages"].([]any)
		for i := len(rawMessages) - 1; i >= 0; i-- {
			message, _ := rawMessages[i].(map[string]any)
			if message == nil {
				continue
			}
			content, _ := message["content"].([]any)
			for j := len(content) - 1; j >= 0; j-- {
				block, _ := content[j].(map[string]any)
				if block == nil {
					continue
				}
				if blockType, _ := block["type"].(string); blockType == "tool_result" {
					toolResult, _ := block["content"].(string)
					return toolResult
				}
			}
		}
	}
	return ""
}

func liveCacheScenarioProbeResponse(downstream string, turn int) map[string]any {
	switch downstream {
	case "responses":
		if turn == 1 {
			return map[string]any{
				"id":    fmt.Sprintf("resp-%d", turn),
				"usage": map[string]any{},
				"output": []map[string]any{
					{
						"type":      "function_call",
						"call_id":   "call-1",
						"name":      "lookup_project_facts",
						"arguments": `{"project":"atlas","focus":"cache"}`,
					},
				},
			}
		}
		return map[string]any{
			"id":    fmt.Sprintf("resp-%d", turn),
			"usage": map[string]any{},
			"output": []map[string]any{{
				"type": "message",
				"role": "assistant",
				"content": []map[string]any{{
					"type": "output_text",
					"text": fmt.Sprintf("responses turn %d complete", turn),
				}},
			}},
		}
	case "chat":
		message := map[string]any{"role": "assistant", "content": fmt.Sprintf("chat turn %d complete", turn)}
		if turn == 1 {
			message["tool_calls"] = []map[string]any{{
				"id":   "call-1",
				"type": "function",
				"function": map[string]any{
					"name":      "lookup_project_facts",
					"arguments": `{"project":"atlas","focus":"cache"}`,
				},
			}}
		}
		return map[string]any{
			"usage": map[string]any{},
			"choices": []map[string]any{{
				"message": message,
			}},
		}
	case "messages":
		content := []map[string]any{{
			"type": "text",
			"text": fmt.Sprintf("messages turn %d complete", turn),
		}}
		if turn == 1 {
			content = []map[string]any{{
				"type":  "tool_use",
				"id":    "call-1",
				"name":  "lookup_project_facts",
				"input": map[string]any{"project": "atlas", "focus": "cache"},
			}}
		}
		return map[string]any{
			"usage":   map[string]any{},
			"content": content,
		}
	default:
		return map[string]any{"error": fmt.Sprintf("unsupported downstream %s", downstream)}
	}
}

func mustLoadLiveCacheRuntimeConfig(t *testing.T) liveCacheRuntimeConfig {
	t.Helper()

	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv(liveCacheBaseURLEnv)), "/")
	if baseURL == "" {
		t.Fatalf("%s is required when %s=1", liveCacheBaseURLEnv, liveCacheMatrixEnabledEnv)
	}
	apiKey := strings.TrimSpace(os.Getenv(liveCacheAPIKeyEnv))
	if apiKey == "" {
		t.Fatalf("%s is required when %s=1", liveCacheAPIKeyEnv, liveCacheMatrixEnabledEnv)
	}

	rounds := 3
	if raw := strings.TrimSpace(os.Getenv(liveCacheRoundsEnv)); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			t.Fatalf("invalid %s=%q", liveCacheRoundsEnv, raw)
		}
		rounds = parsed
	}

	timeout := 5 * time.Minute
	if raw := strings.TrimSpace(os.Getenv(liveCacheRequestTimeoutEnv)); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			t.Fatalf("invalid %s=%q: %v", liveCacheRequestTimeoutEnv, raw, err)
		}
		timeout = parsed
	}

	rawProviders := strings.TrimSpace(os.Getenv(liveCacheProvidersJSONEnv))
	if rawProviders == "" {
		t.Fatalf("%s is required when %s=1", liveCacheProvidersJSONEnv, liveCacheMatrixEnabledEnv)
	}
	var providerList []liveCacheProviderSpec
	if err := json.Unmarshal([]byte(rawProviders), &providerList); err != nil {
		t.Fatalf("decode %s: %v", liveCacheProvidersJSONEnv, err)
	}
	providers := make(map[string]liveCacheProviderSpec, len(providerList))
	for _, spec := range providerList {
		label := strings.TrimSpace(spec.Label)
		spec.Label = label
		spec.ProviderID = strings.TrimSpace(spec.ProviderID)
		spec.Model = strings.TrimSpace(spec.Model)
		spec.DirectBaseURL = strings.TrimRight(strings.TrimSpace(spec.DirectBaseURL), "/")
		spec.DirectAPIKey = strings.TrimSpace(spec.DirectAPIKey)
		if label == "" || spec.ProviderID == "" || spec.Model == "" || spec.DirectBaseURL == "" || spec.DirectAPIKey == "" {
			t.Fatalf("each %s entry must include label, provider_id, model, direct_base_url, and direct_api_key: %#v", liveCacheProvidersJSONEnv, spec)
		}
		providers[label] = spec
	}
	for _, label := range liveCacheUpstreams {
		if _, ok := providers[label]; !ok {
			t.Fatalf("%s must include label=%q", liveCacheProvidersJSONEnv, label)
		}
	}

	return liveCacheRuntimeConfig{
		BaseURL:   baseURL,
		APIKey:    apiKey,
		Rounds:    rounds,
		Timeout:   timeout,
		Providers: providers,
	}
}

func runLiveCacheScenario(client *http.Client, cfg liveCacheRuntimeConfig, round int, downstream string, spec liveCacheProviderSpec) ([]liveCacheObservation, error) {
	return runLiveCacheScenarioForExecutionPath(client, cfg, round, downstream, spec, liveCacheExecutionPathProxy)
}

func runLiveCacheResponsesScenario(client *http.Client, cfg liveCacheRuntimeConfig, downstream string, spec liveCacheProviderSpec, binding liveCacheScenarioBinding) ([]liveCacheObservation, error) {
	scenario := binding.Scenario
	state := liveCacheResponsesState{}
	observations := make([]liveCacheObservation, 0, len(scenario.Turns))

	for _, turn := range scenario.Turns {
		if turn.ExpectToolCall {
			user := map[string]any{
				"role": "user",
				"content": []map[string]any{{
					"type": "input_text",
					"text": turn.InputText,
				}},
			}
			state.History = []map[string]any{user}
			payload := map[string]any{
				"model":               spec.Model,
				"parallel_tool_calls": false,
				"tool_choice":         "required",
				"max_output_tokens":   liveCacheResponsesTurnOneMaxOutputTokens(spec, binding),
				"input":               state.History,
				"tools":               []map[string]any{responsesToolDefinition()},
			}
			fingerprint := liveCacheFingerprintPayload("responses", payload)
			body, err := sendLiveCacheJSONRequest(client, cfg, spec, binding, "responses", payload, nil)
			if err != nil {
				return observations, wrapLiveCacheTurnError(scenario.Round, binding.ExecutionPath, downstream, spec, "responses", turn.Number, "tool-call", "request", err)
			}
			responseID, toolCall, output, preview, usage, err := parseResponsesTurnOne(body)
			observations = append(observations, attachLiveCacheRequestFingerprint(buildLiveCacheObservation(binding.ExecutionPath, binding.CanonicalDigest, scenario.Round, downstream, spec, turn.Number, turn.InputText, preview, responseID, usage, toolCall.ID != "", ""), fingerprint))
			if err != nil {
				return observations, wrapLiveCacheTurnError(scenario.Round, binding.ExecutionPath, downstream, spec, "responses", turn.Number, "tool-call", "parse", err)
			}
			state.ToolCall = toolCall
			state.History = append(state.History, cloneLiveCacheMapSlice(output)...)
			continue
		}

		history := cloneLiveCacheMapSlice(state.History)
		if turn.IncludeToolResult {
			history = append(history, map[string]any{
				"type":    "function_call_output",
				"call_id": state.ToolCall.ID,
				"output":  turn.ToolResultJSON,
			})
		}
		history = append(history, map[string]any{
			"role": "user",
			"content": []map[string]any{{
				"type": "input_text",
				"text": turn.InputText,
			}},
		})
		payload := map[string]any{
			"model":             spec.Model,
			"max_output_tokens": liveCacheMaxOutputTokens,
			"input":             history,
		}
		fingerprint := liveCacheFingerprintPayload("responses", payload)
		body, err := sendLiveCacheJSONRequest(client, cfg, spec, binding, "responses", payload, nil)
		if err != nil {
			return observations, wrapLiveCacheTurnError(scenario.Round, binding.ExecutionPath, downstream, spec, "responses", turn.Number, "follow-up", "request", err)
		}
		responseID, output, preview, usage, err := parseResponsesTurnFollowUp(body)
		observations = append(observations, attachLiveCacheRequestFingerprint(buildLiveCacheObservation(binding.ExecutionPath, binding.CanonicalDigest, scenario.Round, downstream, spec, turn.Number, turn.InputText, preview, responseID, usage, false, ""), fingerprint))
		if err != nil {
			return observations, wrapLiveCacheTurnError(scenario.Round, binding.ExecutionPath, downstream, spec, "responses", turn.Number, "follow-up", "parse", err)
		}
		state.History = append(history, cloneLiveCacheMapSlice(output)...)
	}

	return observations, nil
}

func runLiveCacheChatScenario(client *http.Client, cfg liveCacheRuntimeConfig, downstream string, spec liveCacheProviderSpec, binding liveCacheScenarioBinding) ([]liveCacheObservation, error) {
	scenario := binding.Scenario
	state := liveCacheChatState{}
	observations := make([]liveCacheObservation, 0, len(scenario.Turns))

	for _, turn := range scenario.Turns {
		if turn.ExpectToolCall {
			user := map[string]any{"role": "user", "content": turn.InputText}
			state.Messages = []map[string]any{user}
			payload := map[string]any{
				"model":       spec.Model,
				"max_tokens":  liveCacheChatTurnOneMaxTokensForExecution(downstream, spec, binding),
				"messages":    state.Messages,
				"tools":       []map[string]any{chatToolDefinition()},
				"tool_choice": "required",
			}
			payload, body, err := requestLiveCacheChatTurnOne(client, cfg, spec, binding, scenario, payload)
			if err != nil {
				return observations, wrapLiveCacheTurnError(scenario.Round, binding.ExecutionPath, downstream, spec, "chat/completions", turn.Number, "tool-call", "request", err)
			}
			fingerprint := liveCacheFingerprintPayload("chat", payload)
			assistantMessage, toolCall, preview, usage, err := parseChatTurnOne(body)
			observations = append(observations, attachLiveCacheRequestFingerprint(buildLiveCacheObservation(binding.ExecutionPath, binding.CanonicalDigest, scenario.Round, downstream, spec, turn.Number, turn.InputText, preview, "", usage, toolCall.ID != "", ""), fingerprint))
			if err != nil {
				return observations, wrapLiveCacheTurnError(scenario.Round, binding.ExecutionPath, downstream, spec, "chat/completions", turn.Number, "tool-call", "parse", err)
			}
			sanitizeLiveCacheChatAssistantToolCalls(assistantMessage)
			state.ToolCall = toolCall
			state.Messages = append(state.Messages, assistantMessage)
			continue
		}

		history := cloneLiveCacheMapSlice(state.Messages)
		sanitizeLiveCacheChatToolCallsInMessages(history)
		if turn.IncludeToolResult {
			history = append(history, map[string]any{
				"role":         "tool",
				"tool_call_id": state.ToolCall.ID,
				"content":      turn.ToolResultJSON,
			})
		}
		history = append(history, map[string]any{"role": "user", "content": turn.InputText})
		payload := map[string]any{
			"model":      spec.Model,
			"max_tokens": liveCacheMaxOutputTokens,
			"messages":   history,
		}
		fingerprint := liveCacheFingerprintPayload("chat", payload)
		body, err := sendLiveCacheJSONRequest(client, cfg, spec, binding, "chat/completions", payload, nil)
		if err != nil {
			return observations, wrapLiveCacheTurnError(scenario.Round, binding.ExecutionPath, downstream, spec, "chat/completions", turn.Number, "follow-up", "request", err)
		}
		assistantMessage, preview, usage, err := parseChatTurnFollowUp(body)
		observations = append(observations, attachLiveCacheRequestFingerprint(buildLiveCacheObservation(binding.ExecutionPath, binding.CanonicalDigest, scenario.Round, downstream, spec, turn.Number, turn.InputText, preview, "", usage, false, ""), fingerprint))
		if err != nil {
			return observations, wrapLiveCacheTurnError(scenario.Round, binding.ExecutionPath, downstream, spec, "chat/completions", turn.Number, "follow-up", "parse", err)
		}
		state.Messages = append(history, assistantMessage)
	}

	return observations, nil
}

func requestLiveCacheChatTurnOne(client *http.Client, cfg liveCacheRuntimeConfig, spec liveCacheProviderSpec, binding liveCacheScenarioBinding, scenario liveCacheScenarioSpec, payload map[string]any) (map[string]any, []byte, error) {
	body, err := sendLiveCacheJSONRequest(client, cfg, spec, binding, "chat/completions", payload, nil)
	if err != nil {
		return payload, nil, err
	}
	if !shouldRetryLiveCacheDirectChatTurnOne(binding, spec, body) {
		return payload, body, nil
	}
	retryPayload := cloneLiveCacheMap(payload)
	retryPayload["tool_choice"] = map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": scenario.ToolName,
		},
	}
	retryBody, err := sendLiveCacheJSONRequest(client, cfg, spec, binding, "chat/completions", retryPayload, nil)
	if err != nil {
		return retryPayload, nil, err
	}
	return retryPayload, retryBody, nil
}

func shouldRetryLiveCacheDirectChatTurnOne(binding liveCacheScenarioBinding, spec liveCacheProviderSpec, body []byte) bool {
	if binding.ExecutionPath != liveCacheExecutionPathDirect || spec.Label != config.UpstreamEndpointTypeChat {
		return false
	}
	_, _, _, _, err := parseChatTurnOne(body)
	return err != nil && strings.Contains(err.Error(), "did not return tool_calls")
}

func sanitizeLiveCacheChatToolCallsInMessages(messages []map[string]any) {
	for _, message := range messages {
		sanitizeLiveCacheChatAssistantToolCalls(message)
	}
}

func sanitizeLiveCacheChatAssistantToolCalls(message map[string]any) {
	toolCalls, _ := message["tool_calls"].([]any)
	for _, rawToolCall := range toolCalls {
		toolCall, _ := rawToolCall.(map[string]any)
		if toolCall == nil {
			continue
		}
		function, _ := toolCall["function"].(map[string]any)
		if function == nil {
			continue
		}
		arguments, _ := function["arguments"].(string)
		if sanitized := sanitizeLiveCacheChatToolArguments(arguments); sanitized != "" {
			function["arguments"] = sanitized
		}
	}
}

func sanitizeLiveCacheChatToolArguments(arguments string) string {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return arguments
	}
	if normalized, ok := syntaxrepair.RepairJSON(trimmed); ok {
		return normalized
	}
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] != '{' && trimmed[i] != '[' {
			continue
		}
		if normalized, ok := syntaxrepair.RepairJSON(trimmed[i:]); ok {
			return normalized
		}
	}
	if json.Valid([]byte(trimmed)) {
		return arguments
	}
	return arguments
}

func runLiveCacheAnthropicScenario(client *http.Client, cfg liveCacheRuntimeConfig, downstream string, spec liveCacheProviderSpec, binding liveCacheScenarioBinding) ([]liveCacheObservation, error) {
	scenario := binding.Scenario
	state := liveCacheAnthropicState{}
	observations := make([]liveCacheObservation, 0, len(scenario.Turns))
	headers := map[string]string{"anthropic-version": "2023-06-01"}

	for _, turn := range scenario.Turns {
		if turn.ExpectToolCall {
			user := map[string]any{"role": "user", "content": []map[string]any{anthropicTextBlock(turn.InputText)}}
			state.Messages = []map[string]any{user}
			payload := map[string]any{
				"model":      spec.Model,
				"max_tokens": liveCacheAnthropicTurnOneMaxTokensForExecution(downstream, spec, binding),
				"messages":   state.Messages,
				"tools":      []map[string]any{anthropicToolDefinition()},
			}
			if spec.Label == config.UpstreamEndpointTypeAnthropic {
				payload["tool_choice"] = map[string]any{"type": "tool", "name": scenario.ToolName}
			}
			fingerprint := liveCacheFingerprintPayload("messages", payload)
			body, err := sendLiveCacheJSONRequest(client, cfg, spec, binding, "messages", payload, headers)
			if err != nil {
				return observations, wrapLiveCacheTurnError(scenario.Round, binding.ExecutionPath, downstream, spec, "messages", turn.Number, "tool-call", "request", err)
			}
			assistantContent, toolUse, preview, usage, err := parseAnthropicTurnOne(body)
			observations = append(observations, attachLiveCacheRequestFingerprint(buildLiveCacheObservation(binding.ExecutionPath, binding.CanonicalDigest, scenario.Round, downstream, spec, turn.Number, turn.InputText, preview, "", usage, toolUse.ID != "", ""), fingerprint))
			if err != nil {
				return observations, wrapLiveCacheTurnError(scenario.Round, binding.ExecutionPath, downstream, spec, "messages", turn.Number, "tool-call", "parse", err)
			}
			state.ToolUse = toolUse
			state.AssistantContent = assistantContent
			state.Messages = append(state.Messages, map[string]any{"role": "assistant", "content": cloneLiveCacheMapSlice(assistantContent)})
			continue
		}

		userContent := []map[string]any{anthropicTextBlock(turn.InputText)}
		if turn.IncludeToolResult {
			userContent = append([]map[string]any{{
				"type":        "tool_result",
				"tool_use_id": state.ToolUse.ID,
				"content":     turn.ToolResultJSON,
			}}, userContent...)
		}
		history := cloneLiveCacheMapSlice(state.Messages)
		history = append(history, map[string]any{"role": "user", "content": userContent})
		payload := map[string]any{
			"model":      spec.Model,
			"max_tokens": liveCacheMaxOutputTokens,
			"messages":   history,
		}
		fingerprint := liveCacheFingerprintPayload("messages", payload)
		body, err := sendLiveCacheJSONRequest(client, cfg, spec, binding, "messages", payload, headers)
		if err != nil {
			return observations, wrapLiveCacheTurnError(scenario.Round, binding.ExecutionPath, downstream, spec, "messages", turn.Number, "follow-up", "request", err)
		}
		assistantContent, preview, usage, err := parseAnthropicTurnFollowUp(body)
		observations = append(observations, attachLiveCacheRequestFingerprint(buildLiveCacheObservation(binding.ExecutionPath, binding.CanonicalDigest, scenario.Round, downstream, spec, turn.Number, turn.InputText, preview, "", usage, false, ""), fingerprint))
		if err != nil {
			return observations, wrapLiveCacheTurnError(scenario.Round, binding.ExecutionPath, downstream, spec, "messages", turn.Number, "follow-up", "parse", err)
		}
		state.AssistantContent = assistantContent
		state.Messages = append(history, map[string]any{"role": "assistant", "content": cloneLiveCacheMapSlice(assistantContent)})
	}

	return observations, nil
}

func sendLiveCacheJSONRequest(client *http.Client, cfg liveCacheRuntimeConfig, spec liveCacheProviderSpec, binding liveCacheScenarioBinding, route string, payload map[string]any, extraHeaders map[string]string) ([]byte, error) {
	if binding.ExecutionPath == liveCacheExecutionPathDirect {
		payload = cloneLiveCacheMap(payload)
		payload["stream"] = true
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal %s request: %w", route, err)
	}
	url, authHeaders, err := liveCacheRequestTarget(cfg, spec, binding, route)
	if err != nil {
		return nil, err
	}
	reqCtx := context.Background()
	cancel := func() {}
	if cfg.Timeout > 0 {
		reqCtx, cancel = context.WithTimeout(reqCtx, cfg.Timeout)
	}
	defer cancel()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("build %s request: %w", route, err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Live-Cache-Execution-Path", string(binding.ExecutionPath))
		req.Header.Set("X-Live-Cache-Canonical-Digest", binding.CanonicalDigest)
		for key, value := range authHeaders {
			req.Header.Set(key, value)
		}
		for key, value := range extraHeaders {
			req.Header.Set(key, value)
		}
		resp, err := client.Do(req)
		if err != nil {
			if cfg.Timeout > 0 {
				return nil, fmt.Errorf("send %s request (request_timeout=%s): %w", route, cfg.Timeout, err)
			}
			return nil, fmt.Errorf("send %s request: %w", route, err)
		}
		respBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			if cfg.Timeout > 0 {
				return nil, fmt.Errorf("read %s response (request_timeout=%s): %w", route, cfg.Timeout, readErr)
			}
			return nil, fmt.Errorf("read %s response: %w", route, readErr)
		}
		if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
			respBody, err = normalizeLiveCacheSSEBody(route, respBody)
			if err != nil {
				return nil, fmt.Errorf("normalize %s sse response: %w", route, err)
			}
		}
		if resp.StatusCode == http.StatusOK {
			return respBody, nil
		}
		lastErr = fmt.Errorf("%s returned status=%d body=%s", route, resp.StatusCode, string(respBody))
		if !liveCacheShouldRetryStatus(resp.StatusCode) || attempt == 2 {
			return nil, lastErr
		}
	}
	return nil, lastErr
}

func liveCacheShouldRetryStatus(statusCode int) bool {
	if statusCode == http.StatusTooManyRequests || statusCode == 529 {
		return true
	}
	return statusCode >= http.StatusInternalServerError && statusCode < 600
}

type liveCacheSSEEvent struct {
	Event string
	Data  []byte
}

func normalizeLiveCacheSSEBody(route string, body []byte) ([]byte, error) {
	events := parseLiveCacheSSEEvents(body)
	switch route {
	case "responses":
		return normalizeLiveCacheResponsesSSE(events)
	case "chat/completions":
		return normalizeLiveCacheChatSSE(events)
	case "messages":
		return normalizeLiveCacheAnthropicSSE(events)
	default:
		return body, nil
	}
}

func parseLiveCacheSSEEvents(body []byte) []liveCacheSSEEvent {
	lines := strings.Split(string(body), "\n")
	events := make([]liveCacheSSEEvent, 0)
	current := liveCacheSSEEvent{}
	dataLines := make([]string, 0)
	flush := func() {
		if len(dataLines) == 0 {
			current = liveCacheSSEEvent{}
			return
		}
		current.Data = []byte(strings.Join(dataLines, "\n"))
		events = append(events, current)
		current = liveCacheSSEEvent{}
		dataLines = dataLines[:0]
	}
	for _, rawLine := range lines {
		line := strings.TrimRight(rawLine, "\r")
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "event:") {
			current.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()
	return events
}

func normalizeLiveCacheResponsesSSE(events []liveCacheSSEEvent) ([]byte, error) {
	var responseID string
	var usage map[string]any
	responseOutput := make([]map[string]any, 0)
	outputByIndex := map[int]map[string]any{}
	for _, event := range events {
		if len(event.Data) == 0 || bytes.Equal(event.Data, []byte("[DONE]")) {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return nil, err
		}
		eventType, _ := payload["type"].(string)
		switch eventType {
		case "response.created", "response.in_progress", "response.completed":
			response, _ := payload["response"].(map[string]any)
			if response == nil {
				continue
			}
			if id, _ := response["id"].(string); id != "" {
				responseID = id
			}
			if eventType == "response.completed" {
				usage, _ = response["usage"].(map[string]any)
				if rawOutput, _ := response["output"].([]any); len(rawOutput) > 0 {
					responseOutput = cloneResponsesOutputItems(rawOutput)
				}
			}
		case "response.output_item.added", "response.output_item.done":
			index := liveCacheSSEOutputIndex(payload)
			item, _ := payload["item"].(map[string]any)
			if item != nil {
				outputByIndex[index] = cloneLiveCacheMap(item)
			}
		case "response.function_call_arguments.delta":
			index := liveCacheSSEOutputIndex(payload)
			item := outputByIndex[index]
			if item == nil {
				item = map[string]any{"type": "function_call"}
				outputByIndex[index] = item
			}
			delta, _ := payload["delta"].(string)
			arguments, _ := item["arguments"].(string)
			item["arguments"] = arguments + delta
		}
	}
	if len(responseOutput) == 0 && len(outputByIndex) > 0 {
		responseOutput = orderedLiveCacheItems(outputByIndex)
	}
	return json.Marshal(map[string]any{"id": responseID, "output": responseOutput, "usage": usage})
}

func normalizeLiveCacheChatSSE(events []liveCacheSSEEvent) ([]byte, error) {
	message := map[string]any{"role": "assistant"}
	var usage map[string]any
	var content strings.Builder
	toolCalls := map[int]map[string]any{}
	for _, event := range events {
		if len(event.Data) == 0 || bytes.Equal(event.Data, []byte("[DONE]")) {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return nil, err
		}
		choices, _ := payload["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		if choice == nil {
			continue
		}
		delta, _ := choice["delta"].(map[string]any)
		if delta != nil {
			if role, _ := delta["role"].(string); role != "" {
				message["role"] = role
			}
			if text, _ := delta["content"].(string); text != "" {
				content.WriteString(text)
			}
			if rawToolCalls, _ := delta["tool_calls"].([]any); len(rawToolCalls) > 0 {
				for _, rawToolCall := range rawToolCalls {
					toolCall, _ := rawToolCall.(map[string]any)
					if toolCall == nil {
						continue
					}
					index := liveCacheSSEOutputIndex(toolCall)
					aggregated := toolCalls[index]
					if aggregated == nil {
						aggregated = map[string]any{"type": "function", "function": map[string]any{}}
						toolCalls[index] = aggregated
					}
					if id, _ := toolCall["id"].(string); id != "" {
						aggregated["id"] = id
					}
					if kind, _ := toolCall["type"].(string); kind != "" {
						aggregated["type"] = kind
					}
					function, _ := toolCall["function"].(map[string]any)
					aggFunction, _ := aggregated["function"].(map[string]any)
					if aggFunction == nil {
						aggFunction = map[string]any{}
						aggregated["function"] = aggFunction
					}
					if function != nil {
						if name, _ := function["name"].(string); name != "" {
							aggFunction["name"] = name
						}
						if args, _ := function["arguments"].(string); args != "" {
							current, _ := aggFunction["arguments"].(string)
							aggFunction["arguments"] = current + args
						}
					}
				}
			}
		}
		if eventUsage, _ := payload["usage"].(map[string]any); eventUsage != nil {
			usage = eventUsage
		}
	}
	if content.Len() > 0 {
		message["content"] = content.String()
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = orderedLiveCacheItems(toolCalls)
	}
	return json.Marshal(map[string]any{"choices": []map[string]any{{"message": message}}, "usage": usage})
}

func normalizeLiveCacheAnthropicSSE(events []liveCacheSSEEvent) ([]byte, error) {
	blocks := map[int]map[string]any{}
	var usage map[string]any
	for _, event := range events {
		if len(event.Data) == 0 || bytes.Equal(event.Data, []byte("[DONE]")) {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return nil, err
		}
		switch event.Event {
		case "content_block_start":
			index := liveCacheSSEOutputIndex(payload)
			block, _ := payload["content_block"].(map[string]any)
			if block != nil {
				blocks[index] = cloneLiveCacheMap(block)
			}
		case "content_block_delta":
			index := liveCacheSSEOutputIndex(payload)
			block := blocks[index]
			if block == nil {
				block = map[string]any{"type": "text", "text": ""}
				blocks[index] = block
			}
			delta, _ := payload["delta"].(map[string]any)
			if delta == nil {
				continue
			}
			switch deltaType, _ := delta["type"].(string); deltaType {
			case "text_delta":
				current, _ := block["text"].(string)
				text, _ := delta["text"].(string)
				block["text"] = current + text
			case "input_json_delta":
				current, _ := block["input_json"].(string)
				partial, _ := delta["partial_json"].(string)
				block["input_json"] = current + partial
			}
		case "message_delta":
			usage, _ = payload["usage"].(map[string]any)
		}
	}
	content := orderedLiveCacheItems(blocks)
	for _, block := range content {
		if inputJSON, ok := block["input_json"].(string); ok && inputJSON != "" {
			var decoded map[string]any
			if json.Unmarshal([]byte(inputJSON), &decoded) == nil {
				block["input"] = decoded
			}
			delete(block, "input_json")
		}
	}
	return json.Marshal(map[string]any{"content": content, "usage": usage})
}

func liveCacheSSEOutputIndex(payload map[string]any) int {
	if index, ok := payload["output_index"].(float64); ok {
		return int(index)
	}
	if index, ok := payload["index"].(float64); ok {
		return int(index)
	}
	return 0
}

func orderedLiveCacheItems(items map[int]map[string]any) []map[string]any {
	if len(items) == 0 {
		return nil
	}
	ordered := make([]map[string]any, 0, len(items))
	for index := 0; index < len(items)+4; index++ {
		if item, ok := items[index]; ok {
			ordered = append(ordered, cloneLiveCacheMap(item))
		}
	}
	if len(ordered) == len(items) {
		return ordered
	}
	for _, item := range items {
		ordered = append(ordered, cloneLiveCacheMap(item))
	}
	return ordered
}

func (combo liveCacheCombo) String() string {
	return fmt.Sprintf("round=%d execution_path=%s downstream=%s upstream=%s provider=%s model=%s", combo.Round, combo.ExecutionPath, combo.Downstream, combo.Upstream, combo.ProviderID, combo.Model)
}

func wrapLiveCacheTurnError(round int, path liveCacheExecutionPath, downstream string, spec liveCacheProviderSpec, route string, turn int, stage, step string, err error) error {
	combo := liveCacheCombo{
		Round:         round,
		ExecutionPath: path,
		Downstream:    downstream,
		Upstream:      spec.Label,
		ProviderID:    spec.ProviderID,
		Model:         spec.Model,
	}
	return fmt.Errorf("%s route=%s turn=%d stage=%s step=%s: %w", combo, route, turn, stage, step, err)
}

func liveCacheRequestTarget(cfg liveCacheRuntimeConfig, spec liveCacheProviderSpec, binding liveCacheScenarioBinding, route string) (string, map[string]string, error) {
	switch binding.ExecutionPath {
	case liveCacheExecutionPathProxy:
		return fmt.Sprintf("%s/%s/v1/%s", cfg.BaseURL, spec.ProviderID, route), map[string]string{"Authorization": "Bearer " + cfg.APIKey}, nil
	case liveCacheExecutionPathDirect:
		if spec.DirectBaseURL == "" || spec.DirectAPIKey == "" {
			return "", nil, fmt.Errorf("upstream=%s provider=%s missing direct target config", spec.Label, spec.ProviderID)
		}
		url := liveCacheDirectRouteURL(spec.DirectBaseURL, route)
		headers := map[string]string{}
		if spec.Label == config.UpstreamEndpointTypeAnthropic {
			headers["x-api-key"] = spec.DirectAPIKey
		} else {
			headers["Authorization"] = "Bearer " + spec.DirectAPIKey
		}
		return url, headers, nil
	default:
		return "", nil, fmt.Errorf("unsupported execution path %q", binding.ExecutionPath)
	}
}

func liveCacheDirectRouteURL(baseURL, route string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL + "/" + route
	}
	return baseURL + "/v1/" + route
}

func parseResponsesTurnOne(body []byte) (string, liveCacheToolCall, []map[string]any, string, map[string]any, error) {
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", liveCacheToolCall{}, nil, "", nil, fmt.Errorf("decode responses turn1 body: %w", err)
	}
	responseID, _ := decoded["id"].(string)
	usage, _ := decoded["usage"].(map[string]any)
	output, _ := decoded["output"].([]any)
	preview := previewResponsesOutput(output)
	outputItems := cloneResponsesOutputItems(output)
	for _, rawItem := range output {
		item, _ := rawItem.(map[string]any)
		if item == nil {
			continue
		}
		if itemType, _ := item["type"].(string); itemType == "function_call" {
			callID, _ := item["call_id"].(string)
			name, _ := item["name"].(string)
			arguments, _ := item["arguments"].(string)
			if callID == "" || name == "" {
				continue
			}
			return responseID, liveCacheToolCall{ID: callID, Name: name, Arguments: arguments}, outputItems, preview, usage, nil
		}
	}
	return responseID, liveCacheToolCall{}, outputItems, preview, usage, fmt.Errorf("responses turn1 did not return function_call: %s", string(body))
}

func parseResponsesTurnFollowUp(body []byte) (string, []map[string]any, string, map[string]any, error) {
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", nil, "", nil, fmt.Errorf("decode responses follow-up body: %w", err)
	}
	responseID, _ := decoded["id"].(string)
	usage, _ := decoded["usage"].(map[string]any)
	output, _ := decoded["output"].([]any)
	return responseID, cloneResponsesOutputItems(output), previewResponsesOutput(output), usage, nil
}

func cloneResponsesOutputItems(output []any) []map[string]any {
	if len(output) == 0 {
		return nil
	}
	items := make([]map[string]any, 0, len(output))
	for _, rawItem := range output {
		item, _ := rawItem.(map[string]any)
		if item == nil {
			continue
		}
		switch itemType, _ := item["type"].(string); itemType {
		case "function_call":
			sanitized := map[string]any{"type": "function_call"}
			if callID, _ := item["call_id"].(string); callID != "" {
				sanitized["call_id"] = callID
			}
			if name, _ := item["name"].(string); name != "" {
				sanitized["name"] = name
			}
			if arguments, _ := item["arguments"].(string); arguments != "" {
				sanitized["arguments"] = arguments
			}
			items = append(items, sanitized)
		case "message":
			sanitized := map[string]any{"role": "assistant"}
			if role, _ := item["role"].(string); role != "" {
				sanitized["role"] = role
			}
			if content, ok := item["content"].([]any); ok && len(content) > 0 {
				sanitizedContent := make([]map[string]any, 0, len(content))
				for _, rawContent := range content {
					contentItem, _ := rawContent.(map[string]any)
					if contentItem == nil {
						continue
					}
					sanitizedEntry := map[string]any{}
					if contentType, _ := contentItem["type"].(string); contentType != "" {
						sanitizedEntry["type"] = contentType
					}
					if text, _ := contentItem["text"].(string); text != "" {
						sanitizedEntry["text"] = text
					}
					if len(sanitizedEntry) > 0 {
						sanitizedContent = append(sanitizedContent, sanitizedEntry)
					}
				}
				if len(sanitizedContent) > 0 {
					sanitized["content"] = sanitizedContent
				}
			}
			items = append(items, sanitized)
		default:
			items = append(items, cloneLiveCacheMap(item))
		}
	}
	return items
}

func parseChatTurnOne(body []byte) (map[string]any, liveCacheToolCall, string, map[string]any, error) {
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, liveCacheToolCall{}, "", nil, fmt.Errorf("decode chat turn1 body: %w", err)
	}
	usage, _ := decoded["usage"].(map[string]any)
	assistantMessage, preview, err := extractChatAssistantMessage(decoded)
	if err != nil {
		return nil, liveCacheToolCall{}, "", usage, err
	}
	toolCalls, _ := assistantMessage["tool_calls"].([]any)
	for _, rawToolCall := range toolCalls {
		toolCall, _ := rawToolCall.(map[string]any)
		if toolCall == nil {
			continue
		}
		callID, _ := toolCall["id"].(string)
		function, _ := toolCall["function"].(map[string]any)
		name, _ := function["name"].(string)
		arguments, _ := function["arguments"].(string)
		if callID == "" || name == "" {
			continue
		}
		return assistantMessage, liveCacheToolCall{ID: callID, Name: name, Arguments: arguments}, preview, usage, nil
	}
	return assistantMessage, liveCacheToolCall{}, preview, usage, fmt.Errorf("chat turn1 did not return tool_calls: %s", string(body))
}

func parseChatTurnFollowUp(body []byte) (map[string]any, string, map[string]any, error) {
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, "", nil, fmt.Errorf("decode chat follow-up body: %w", err)
	}
	usage, _ := decoded["usage"].(map[string]any)
	assistantMessage, preview, err := extractChatAssistantMessage(decoded)
	if err != nil {
		return nil, preview, usage, err
	}
	return assistantMessage, preview, usage, nil
}

func extractChatAssistantMessage(decoded map[string]any) (map[string]any, string, error) {
	choices, _ := decoded["choices"].([]any)
	if len(choices) == 0 {
		return nil, "", fmt.Errorf("chat response missing choices")
	}
	choice, _ := choices[0].(map[string]any)
	if choice == nil {
		return nil, "", fmt.Errorf("chat response choice is not an object")
	}
	message, _ := choice["message"].(map[string]any)
	if message == nil {
		return nil, "", fmt.Errorf("chat response missing assistant message")
	}
	preview := previewChatMessage(message)
	return cloneLiveCacheMap(message), preview, nil
}

func parseAnthropicTurnOne(body []byte) ([]map[string]any, liveCacheToolCall, string, map[string]any, error) {
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, liveCacheToolCall{}, "", nil, fmt.Errorf("decode anthropic turn1 body: %w", err)
	}
	usage, _ := decoded["usage"].(map[string]any)
	content, err := extractAnthropicContent(decoded)
	if err != nil {
		return nil, liveCacheToolCall{}, "", usage, err
	}
	preview := previewAnthropicContent(content)
	for _, block := range content {
		if blockType, _ := block["type"].(string); blockType == "tool_use" {
			callID, _ := block["id"].(string)
			name, _ := block["name"].(string)
			input, _ := block["input"].(map[string]any)
			if callID == "" || name == "" {
				continue
			}
			arguments := "{}"
			if input != nil {
				encoded, _ := json.Marshal(input)
				arguments = string(encoded)
			}
			return content, liveCacheToolCall{ID: callID, Name: name, Arguments: arguments, Input: input}, preview, usage, nil
		}
	}
	return content, liveCacheToolCall{}, preview, usage, fmt.Errorf("anthropic turn1 did not return tool_use: %s", string(body))
}

func parseAnthropicTurnFollowUp(body []byte) ([]map[string]any, string, map[string]any, error) {
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, "", nil, fmt.Errorf("decode anthropic follow-up body: %w", err)
	}
	usage, _ := decoded["usage"].(map[string]any)
	content, err := extractAnthropicContent(decoded)
	if err != nil {
		return nil, "", usage, err
	}
	return content, previewAnthropicContent(content), usage, nil
}

func extractAnthropicContent(decoded map[string]any) ([]map[string]any, error) {
	rawContent, _ := decoded["content"].([]any)
	if len(rawContent) == 0 {
		return nil, fmt.Errorf("anthropic response missing content")
	}
	content := make([]map[string]any, 0, len(rawContent))
	for _, rawBlock := range rawContent {
		block, _ := rawBlock.(map[string]any)
		if block == nil {
			return nil, fmt.Errorf("anthropic content block is not an object")
		}
		content = append(content, cloneLiveCacheMap(block))
	}
	return content, nil
}

func buildLiveCacheObservation(path liveCacheExecutionPath, canonicalDigest string, round int, downstream string, spec liveCacheProviderSpec, turn int, inputText, outputPreview, responseID string, usage map[string]any, hasToolCall bool, note string) liveCacheObservation {
	obs := liveCacheObservation{
		Round:           round,
		ExecutionPath:   path,
		Downstream:      downstream,
		Upstream:        spec.Label,
		Turn:            turn,
		ProviderID:      spec.ProviderID,
		Model:           spec.Model,
		CanonicalDigest: canonicalDigest,
		InputDigest:     digestLiveCacheInput(inputText),
		InputChars:      len(inputText),
		OutputPreview:   collapsePreview(outputPreview),
		ResponseID:      responseID,
		HasToolCall:     hasToolCall,
		Note:            note,
	}
	rawMetrics, normalizedMetrics, ok := liveCacheMetricsFromUsage(usage, spec.Label)
	if !ok {
		obs.MissingUsage = true
		if obs.Note == "" {
			obs.Note = "missing usage"
		} else {
			obs.Note += "; missing usage"
		}
		return obs
	}
	obs.RawUsageInputTokens = rawMetrics.InputTokens
	obs.RawUsageCachedTokens = rawMetrics.CachedTokens
	obs.RawUsageCreatedTokens = rawMetrics.CreatedTokens
	obs.RawUsageOutputTokens = rawMetrics.OutputTokens
	obs.RawUsageTotalTokens = rawMetrics.TotalTokens
	obs.UsageInputTokens = normalizedMetrics.InputTokens
	obs.UsageCachedTokens = normalizedMetrics.CachedTokens
	obs.UsageCreatedTokens = rawMetrics.CreatedTokens
	obs.UsageOutputTokens = rawMetrics.OutputTokens
	obs.UsageTotalTokens = normalizedMetrics.InputTokens + rawMetrics.OutputTokens
	obs.CacheRatio = normalizedMetrics.CacheRatio
	minInputChars := liveCacheMinInputCharsForTurn(turn)
	if obs.InputChars < minInputChars {
		obs.BelowMinInputChars = true
		if obs.Note == "" {
			obs.Note = fmt.Sprintf("input_chars<%d", minInputChars)
		} else {
			obs.Note += fmt.Sprintf("; input_chars<%d", minInputChars)
		}
	}
	return obs
}

func attachLiveCacheRequestFingerprint(obs liveCacheObservation, fingerprint liveCacheRequestFingerprint) liveCacheObservation {
	obs.PayloadDigest = fingerprint.PayloadDigest
	obs.MessageCount = fingerprint.MessageCount
	obs.ToolCount = fingerprint.ToolCount
	obs.ToolShape = fingerprint.ToolShape
	return obs
}

func liveCacheFingerprintPayload(downstream string, payload map[string]any) liveCacheRequestFingerprint {
	fingerprint := liveCacheRequestFingerprint{
		PayloadDigest: digestLiveCachePayload(payload),
		ToolCount:     liveCachePayloadToolCount(payload),
	}
	switch downstream {
	case "responses":
		fingerprint.MessageCount = liveCachePayloadArrayCount(payload, "input")
		fingerprint.ToolShape = fmt.Sprintf("responses:function_call_output=%d", liveCacheResponsesToolResultCount(payload))
	case "chat":
		fingerprint.MessageCount = liveCachePayloadArrayCount(payload, "messages")
		fingerprint.ToolShape = fmt.Sprintf("chat:tool_role_messages=%d", liveCacheChatToolMessageCount(payload))
	case "messages":
		fingerprint.MessageCount = liveCachePayloadArrayCount(payload, "messages")
		fingerprint.ToolShape = fmt.Sprintf("messages:tool_result_blocks=%d", liveCacheAnthropicToolResultBlockCount(payload))
	default:
		fingerprint.ToolShape = downstream
	}
	return fingerprint
}

func digestLiveCachePayload(payload map[string]any) string {
	if len(payload) == 0 {
		return ""
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return digestLiveCacheInput(string(encoded))
}

func liveCachePayloadArrayCount(payload map[string]any, key string) int {
	raw, _ := payload[key].([]any)
	if len(raw) > 0 {
		return len(raw)
	}
	if typed, _ := payload[key].([]map[string]any); len(typed) > 0 {
		return len(typed)
	}
	return 0
}

func liveCachePayloadToolCount(payload map[string]any) int {
	raw, _ := payload["tools"].([]any)
	if len(raw) > 0 {
		return len(raw)
	}
	if typed, _ := payload["tools"].([]map[string]any); len(typed) > 0 {
		return len(typed)
	}
	return 0
}

func liveCacheResponsesToolResultCount(payload map[string]any) int {
	rawItems, _ := payload["input"].([]any)
	count := 0
	for _, rawItem := range rawItems {
		item, _ := rawItem.(map[string]any)
		if item == nil {
			continue
		}
		if itemType, _ := item["type"].(string); itemType == "function_call_output" {
			count++
		}
	}
	return count
}

func liveCacheChatToolMessageCount(payload map[string]any) int {
	rawMessages, _ := payload["messages"].([]any)
	count := 0
	for _, rawMessage := range rawMessages {
		message, _ := rawMessage.(map[string]any)
		if message == nil {
			continue
		}
		if role, _ := message["role"].(string); role == "tool" {
			count++
		}
	}
	return count
}

func liveCacheAnthropicToolResultBlockCount(payload map[string]any) int {
	rawMessages, _ := payload["messages"].([]any)
	count := 0
	for _, rawMessage := range rawMessages {
		message, _ := rawMessage.(map[string]any)
		if message == nil {
			continue
		}
		content, _ := message["content"].([]any)
		for _, rawBlock := range content {
			block, _ := rawBlock.(map[string]any)
			if block == nil {
				continue
			}
			if blockType, _ := block["type"].(string); blockType == "tool_result" {
				count++
			}
		}
	}
	return count
}

func liveCacheMetricsFromUsage(usage map[string]any, upstream string) (liveCacheRawMetrics, liveCacheNormalizedMetrics, bool) {
	rawMetrics, rawOK := liveCacheRawMetricsFromUsage(usage)
	parsed, normalizedOK := cacheInfoUsageFromMap(usage, upstream)
	if !normalizedOK || parsed == nil {
		return liveCacheRawMetrics{}, liveCacheNormalizedMetrics{}, false
	}
	normalized := liveCacheNormalizedMetrics{
		InputTokens:  parsed.InputTokens,
		CachedTokens: parsed.CachedTokens,
	}
	if normalized.InputTokens > 0 {
		normalized.CacheRatio = float64(normalized.CachedTokens) / float64(normalized.InputTokens) * 100
	}
	if !rawOK {
		rawMetrics = liveCacheRawMetrics{
			InputTokens:   parsed.InputTokens,
			CachedTokens:  parsed.CachedTokens,
			CreatedTokens: parsed.CacheCreationTokens,
			OutputTokens:  parsed.OutputTokens,
			TotalTokens:   parsed.TotalTokens,
		}
	}
	return rawMetrics, normalized, true
}

func liveCacheRawMetricsFromUsage(usage map[string]any) (liveCacheRawMetrics, bool) {
	if len(usage) == 0 {
		return liveCacheRawMetrics{}, false
	}
	metrics := liveCacheRawMetrics{}
	var hasValues bool
	if n, ok := usageNumberAsInt64(usage["input_tokens"]); ok {
		metrics.InputTokens = n
		hasValues = true
	} else if n, ok := usageNumberAsInt64(usage["prompt_tokens"]); ok {
		metrics.InputTokens = n
		hasValues = true
	}
	if n, ok := usageNumberAsInt64(usage["output_tokens"]); ok {
		metrics.OutputTokens = n
		hasValues = true
	} else if n, ok := usageNumberAsInt64(usage["completion_tokens"]); ok {
		metrics.OutputTokens = n
		hasValues = true
	}
	if n, ok := usageNumberAsInt64(usage["total_tokens"]); ok {
		metrics.TotalTokens = n
		hasValues = true
	}
	if n, ok := cachedTokensFromUsage(usage); ok {
		metrics.CachedTokens = n
		hasValues = true
	}
	if n, ok := cacheCreationTokensFromUsage(usage); ok {
		metrics.CreatedTokens = n
		hasValues = true
	}
	if !hasValues {
		return liveCacheRawMetrics{}, false
	}
	if metrics.TotalTokens == 0 && (metrics.InputTokens > 0 || metrics.OutputTokens > 0) {
		metrics.TotalTokens = metrics.InputTokens + metrics.OutputTokens
	}
	return metrics, true
}

func liveCacheTurnOneMaxOutputTokens(spec liveCacheProviderSpec) int {
	if spec.Label == config.UpstreamEndpointTypeAnthropic {
		return liveCacheTurnOneAnthropicMaxOutputTokens
	}
	return liveCacheMaxOutputTokens
}

func liveCacheChatTurnOneMaxTokensForExecution(downstream string, spec liveCacheProviderSpec, binding liveCacheScenarioBinding) int {
	if binding.ExecutionPath == liveCacheExecutionPathProxy && spec.Label == config.UpstreamEndpointTypeChat {
		switch downstream {
		case "chat", "messages":
			return liveCacheTurnOneChatResponsesMaxOutputTokens
		}
	}
	if binding.ExecutionPath == liveCacheExecutionPathProxy && spec.Label == config.UpstreamEndpointTypeAnthropic && downstream == "chat" {
		return liveCacheAnthropicMessagesTurnOneMaxTokens
	}
	return liveCacheTurnOneMaxOutputTokens(spec)
}

func liveCacheResponsesTurnOneMaxOutputTokens(spec liveCacheProviderSpec, binding liveCacheScenarioBinding) int {
	if binding.ExecutionPath == liveCacheExecutionPathProxy && spec.Label == config.UpstreamEndpointTypeChat {
		return liveCacheTurnOneChatResponsesMaxOutputTokens
	}
	return liveCacheTurnOneMaxOutputTokens(spec)
}

func liveCacheAnthropicTurnOneMaxTokens(spec liveCacheProviderSpec) int {
	if spec.Label == config.UpstreamEndpointTypeAnthropic {
		return liveCacheAnthropicMessagesTurnOneMaxTokens
	}
	return liveCacheTurnOneMaxOutputTokens(spec)
}

func liveCacheAnthropicTurnOneMaxTokensForExecution(downstream string, spec liveCacheProviderSpec, binding liveCacheScenarioBinding) int {
	if binding.ExecutionPath == liveCacheExecutionPathProxy && spec.Label == config.UpstreamEndpointTypeChat {
		return liveCacheTurnOneChatResponsesMaxOutputTokens
	}
	if binding.ExecutionPath == liveCacheExecutionPathDirect && downstream != "messages" {
		return liveCacheTurnOneMaxOutputTokens(spec)
	}
	return liveCacheAnthropicTurnOneMaxTokens(spec)
}

func renderLiveCacheAverageTable(observations []liveCacheObservation) string {
	var b strings.Builder
	b.WriteString("## Raw Vendor Metrics\n")
	b.WriteString(renderLiveCacheRawMetricsTable(observations))
	b.WriteString("\n\n## Direct Baseline\n")
	b.WriteString(renderLiveCacheDirectBaselineTable(observations))
	b.WriteString("\n\n## Proxy Preservation / Loss\n")
	b.WriteString(renderLiveCachePreservationLossTable(observations))
	b.WriteString("\n\n## Deferred Issues\n")
	b.WriteString(renderLiveCacheDeferredIssuesTable())
	return b.String()
}

func renderLiveCacheRawMetricsTable(observations []liveCacheObservation) string {
	rows := buildLiveCacheRawMetricsRows(observations)
	if len(rows) == 0 {
		return "| round | execution_path | downstream | upstream | provider | model | turn_class | turns | avg_raw_input_tokens | avg_raw_cache_read_tokens | avg_raw_cache_creation_tokens | avg_raw_output_tokens | avg_raw_total_tokens | avg_normalized_cache_ratio |\n|---|---|---|---|---|---|---|---:|---:|---:|---:|---:|---:|---:|"
	}
	var b strings.Builder
	b.WriteString("| round | execution_path | downstream | upstream | provider | model | turn_class | turns | avg_raw_input_tokens | avg_raw_cache_read_tokens | avg_raw_cache_creation_tokens | avg_raw_output_tokens | avg_raw_total_tokens | avg_normalized_cache_ratio |\n")
	b.WriteString("|---|---|---|---|---|---|---|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, row := range rows {
		b.WriteString(fmt.Sprintf("| %d | %s | %s | %s | %s | %s | %s | %d | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f%% |\n",
			row.Round,
			escapeLiveCacheTableValue(string(row.ExecutionPath)),
			escapeLiveCacheTableValue(row.Downstream),
			escapeLiveCacheTableValue(row.Upstream),
			escapeLiveCacheTableValue(row.ProviderID),
			escapeLiveCacheTableValue(row.Model),
			escapeLiveCacheTableValue(row.TurnClass),
			row.Turns,
			row.AvgRawInputTokens,
			row.AvgRawCachedTokens,
			row.AvgRawCreatedTokens,
			row.AvgRawOutputTokens,
			row.AvgRawTotalTokens,
			row.AvgNormalizedCacheRatio,
		))
	}
	return b.String()
}

func renderLiveCacheDirectBaselineTable(observations []liveCacheObservation) string {
	rows := buildLiveCacheDirectBaselineRows(observations)
	if len(rows) == 0 {
		return "| round | downstream | upstream | provider | model | turn_class | turns | direct_normalized_cache_ratio |\n|---|---|---|---|---|---|---:|---:|"
	}
	var b strings.Builder
	b.WriteString("| round | downstream | upstream | provider | model | turn_class | turns | direct_normalized_cache_ratio |\n")
	b.WriteString("|---|---|---|---|---|---|---:|---:|\n")
	for _, row := range rows {
		b.WriteString(fmt.Sprintf("| %d | %s | %s | %s | %s | %s | %d | %.2f%% |\n",
			row.Round,
			escapeLiveCacheTableValue(row.Downstream),
			escapeLiveCacheTableValue(row.Upstream),
			escapeLiveCacheTableValue(row.ProviderID),
			escapeLiveCacheTableValue(row.Model),
			escapeLiveCacheTableValue(row.TurnClass),
			row.Turns,
			row.AvgDirectNormalizedRatio,
		))
	}
	return b.String()
}

func renderLiveCachePreservationLossTable(observations []liveCacheObservation) string {
	rows := buildLiveCachePreservationLossRows(observations)
	if len(rows) == 0 {
		return "| round | downstream | upstream | provider | model | turn_class | turns | excluded_turns | baseline_missing | proxy_normalized_cache_ratio | direct_normalized_cache_ratio | preservation | loss | attribution | caveat |\n|---|---|---|---|---|---|---:|---:|---|---:|---:|---:|---:|---|---|"
	}
	var b strings.Builder
	b.WriteString("| round | downstream | upstream | provider | model | turn_class | turns | excluded_turns | baseline_missing | proxy_normalized_cache_ratio | direct_normalized_cache_ratio | preservation | loss | attribution | caveat |\n")
	b.WriteString("|---|---|---|---|---|---|---:|---:|---|---:|---:|---:|---:|---|---|\n")
	for _, row := range rows {
		preservation := "N/A"
		loss := "N/A"
		if row.Preservation != nil {
			preservation = fmt.Sprintf("%.4f", *row.Preservation)
		}
		if row.Loss != nil {
			loss = fmt.Sprintf("%.4f", *row.Loss)
		}
		b.WriteString(fmt.Sprintf("| %d | %s | %s | %s | %s | %s | %d | %d | %t | %.2f%% | %.2f%% | %s | %s | %s | %s |\n",
			row.Round,
			escapeLiveCacheTableValue(row.Downstream),
			escapeLiveCacheTableValue(row.Upstream),
			escapeLiveCacheTableValue(row.ProviderID),
			escapeLiveCacheTableValue(row.Model),
			escapeLiveCacheTableValue(row.TurnClass),
			row.Turns,
			row.ExcludedTurns,
			row.BaselineMissing,
			row.AvgProxyNormalizedRatio,
			row.AvgDirectNormalizedRatio,
			preservation,
			loss,
			escapeLiveCacheTableValue(row.Attribution),
			escapeLiveCacheTableValue(row.Caveat),
		))
	}
	return b.String()
}

func buildLiveCacheRawMetricsRows(observations []liveCacheObservation) []liveCacheRawMetricsRow {
	type aggregate struct {
		row                     liveCacheRawMetricsRow
		sumRawInput             float64
		sumRawCached            float64
		sumRawCreated           float64
		sumRawOutput            float64
		sumRawTotal             float64
		sumNormalizedCacheRatio float64
	}
	orderedKeys := make([]string, 0)
	groups := make(map[string]*aggregate)
	for _, obs := range observations {
		turnClass := liveCacheTurnClass(obs.Turn)
		key := fmt.Sprintf("%03d|%s|%s|%s|%s|%s|%s", obs.Round, obs.ExecutionPath, obs.Downstream, obs.Upstream, obs.ProviderID, obs.Model, turnClass)
		group := groups[key]
		if group == nil {
			group = &aggregate{row: liveCacheRawMetricsRow{Round: obs.Round, ExecutionPath: obs.ExecutionPath, Downstream: obs.Downstream, Upstream: obs.Upstream, ProviderID: obs.ProviderID, Model: obs.Model, TurnClass: turnClass}}
			groups[key] = group
			orderedKeys = append(orderedKeys, key)
		}
		group.row.Turns++
		group.sumRawInput += float64(obs.RawUsageInputTokens)
		group.sumRawCached += float64(obs.RawUsageCachedTokens)
		group.sumRawCreated += float64(obs.RawUsageCreatedTokens)
		group.sumRawOutput += float64(obs.RawUsageOutputTokens)
		group.sumRawTotal += float64(obs.RawUsageTotalTokens)
		group.sumNormalizedCacheRatio += obs.CacheRatio
	}
	rows := make([]liveCacheRawMetricsRow, 0, len(orderedKeys))
	for _, key := range orderedKeys {
		group := groups[key]
		if group.row.Turns > 0 {
			count := float64(group.row.Turns)
			group.row.AvgRawInputTokens = group.sumRawInput / count
			group.row.AvgRawCachedTokens = group.sumRawCached / count
			group.row.AvgRawCreatedTokens = group.sumRawCreated / count
			group.row.AvgRawOutputTokens = group.sumRawOutput / count
			group.row.AvgRawTotalTokens = group.sumRawTotal / count
			group.row.AvgNormalizedCacheRatio = group.sumNormalizedCacheRatio / count
		}
		rows = append(rows, group.row)
	}
	return rows
}

func buildLiveCacheDirectBaselineRows(observations []liveCacheObservation) []liveCacheBaselineRow {
	type aggregate struct {
		row      liveCacheBaselineRow
		sumRatio float64
	}
	orderedKeys := make([]string, 0)
	groups := make(map[string]*aggregate)
	for _, obs := range observations {
		if obs.ExecutionPath != liveCacheExecutionPathDirect {
			continue
		}
		turnClass := liveCacheTurnClass(obs.Turn)
		key := fmt.Sprintf("%03d|%s|%s|%s|%s|%s", obs.Round, obs.Downstream, obs.Upstream, obs.ProviderID, obs.Model, turnClass)
		group := groups[key]
		if group == nil {
			group = &aggregate{row: liveCacheBaselineRow{Round: obs.Round, Downstream: obs.Downstream, Upstream: obs.Upstream, ProviderID: obs.ProviderID, Model: obs.Model, TurnClass: turnClass}}
			groups[key] = group
			orderedKeys = append(orderedKeys, key)
		}
		group.row.Turns++
		group.sumRatio += obs.CacheRatio
	}
	rows := make([]liveCacheBaselineRow, 0, len(orderedKeys))
	for _, key := range orderedKeys {
		group := groups[key]
		if group.row.Turns > 0 {
			group.row.AvgDirectNormalizedRatio = group.sumRatio / float64(group.row.Turns)
		}
		rows = append(rows, group.row)
	}
	return rows
}

func buildLiveCachePreservationLossRows(observations []liveCacheObservation) []liveCachePreservationLossRow {
	type aggregate struct {
		row            liveCachePreservationLossRow
		sumProxyRatio  float64
		sumDirectRatio float64
		reasons        []string
	}
	proxyByTurn := make(map[string]liveCacheObservation)
	directByTurn := make(map[string]liveCacheObservation)
	orderedRows := make([]string, 0)
	groups := make(map[string]*aggregate)
	for _, obs := range observations {
		if obs.Turn == 1 {
			continue
		}
		turnKey := liveCacheObservationTurnKey(obs)
		switch obs.ExecutionPath {
		case liveCacheExecutionPathProxy:
			proxyByTurn[turnKey] = obs
		case liveCacheExecutionPathDirect:
			directByTurn[turnKey] = obs
		}
	}
	for _, obs := range observations {
		if obs.Turn == 1 || obs.ExecutionPath != liveCacheExecutionPathProxy {
			continue
		}
		rowKey := liveCachePreservationGroupKey(obs)
		group := groups[rowKey]
		if group == nil {
			group = &aggregate{row: liveCachePreservationLossRow{Round: obs.Round, Downstream: obs.Downstream, Upstream: obs.Upstream, ProviderID: obs.ProviderID, Model: obs.Model, TurnClass: "turn2+"}}
			groups[rowKey] = group
			orderedRows = append(orderedRows, rowKey)
		}
		direct, hasDirect := directByTurn[liveCacheObservationTurnKey(obs)]
		baselineMissing, comparable, reason := liveCacheComparablePair(obs, direct, hasDirect)
		group.row.BaselineMissing = group.row.BaselineMissing || baselineMissing
		if !comparable {
			group.row.ExcludedTurns++
			group.reasons = appendLiveCacheUniqueReason(group.reasons, reason)
			continue
		}
		group.row.Turns++
		group.sumProxyRatio += obs.CacheRatio
		group.sumDirectRatio += direct.CacheRatio
	}
	rows := make([]liveCachePreservationLossRow, 0, len(orderedRows))
	for _, key := range orderedRows {
		group := groups[key]
		row := group.row
		if row.Turns > 0 {
			row.AvgProxyNormalizedRatio = group.sumProxyRatio / float64(row.Turns)
			row.AvgDirectNormalizedRatio = group.sumDirectRatio / float64(row.Turns)
		}
		switch {
		case row.Turns == 0:
			row.Attribution = "not attributable"
		case row.AvgDirectNormalizedRatio <= 0:
			row.Attribution = "baseline unavailable"
			group.reasons = appendLiveCacheUniqueReason(group.reasons, "baseline ratio unavailable")
		default:
			preservation := row.AvgProxyNormalizedRatio / row.AvgDirectNormalizedRatio
			loss := 1 - preservation
			row.Preservation = &preservation
			row.Loss = &loss
			if row.ExcludedTurns > 0 {
				row.Attribution = "partially attributable"
			} else {
				row.Attribution = "attributable"
			}
		}
		if row.ExcludedTurns > 0 {
			prefix := fmt.Sprintf("%d sample(s) not attributable", row.ExcludedTurns)
			if len(group.reasons) > 0 {
				row.Caveat = prefix + ": " + strings.Join(group.reasons, "; ")
			} else {
				row.Caveat = prefix
			}
		} else if len(group.reasons) > 0 {
			row.Caveat = strings.Join(group.reasons, "; ")
		}
		rows = append(rows, row)
	}
	return rows
}

func liveCacheObservationTurnKey(obs liveCacheObservation) string {
	return fmt.Sprintf("%03d|%s|%s|%s|%s|%03d", obs.Round, obs.Downstream, obs.Upstream, obs.ProviderID, obs.Model, obs.Turn)
}

func liveCachePreservationGroupKey(obs liveCacheObservation) string {
	return fmt.Sprintf("%03d|%s|%s|%s|%s|turn2+", obs.Round, obs.Downstream, obs.Upstream, obs.ProviderID, obs.Model)
}

func liveCacheComparablePair(proxy, direct liveCacheObservation, hasDirect bool) (bool, bool, string) {
	if !hasDirect {
		return true, false, "baseline missing"
	}
	if direct.MissingUsage {
		return true, false, "baseline usage missing"
	}
	if proxy.MissingUsage {
		return false, false, "proxy usage missing"
	}
	if proxy.CanonicalDigest != "" && direct.CanonicalDigest != "" && proxy.CanonicalDigest != direct.CanonicalDigest {
		return false, false, "canonical digest mismatch"
	}
	if proxy.InputDigest != "" && direct.InputDigest != "" && proxy.InputDigest != direct.InputDigest {
		return false, false, "input digest mismatch"
	}
	if proxy.ToolCount != direct.ToolCount {
		return false, false, "tool count mismatch"
	}
	return false, true, ""
}

func appendLiveCacheUniqueReason(reasons []string, reason string) []string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return reasons
	}
	for _, existing := range reasons {
		if existing == reason {
			return reasons
		}
	}
	return append(reasons, reason)
}

func renderLiveCacheDeferredIssuesTable() string {
	type deferredIssue struct {
		RiskClass string
		Evidence  string
		Caveat    string
	}
	issues := []deferredIssue{
		{
			RiskClass: "route/provider resolution differences",
			Evidence:  "internal/httpapi/routes.go:83-205",
			Caveat:    "bare legacy route overlay and explicit provider selection can hit different provider resolution paths, so record this as a deferred caveat instead of treating route parity as fixed.",
		},
		{
			RiskClass: "responses IncludeUsage/history replay special handling",
			Evidence:  "internal/httpapi/handlers_responses.go:365-373",
			Caveat:    "responses requests can inject IncludeUsage and replay stored history on restore, so loss attribution must keep this as a recorded caveat, not a resolved difference.",
		},
		{
			RiskClass: "chat vs anthropic tool replay shape differences",
			Evidence:  "internal/upstream/protocol.go:904-907; internal/upstream/protocol.go:1062-1083",
			Caveat:    "chat replays tool results as role=tool messages while anthropic can merge tool_result blocks with user text, so any observed mismatch stays deferred and not silently normalized away.",
		},
		{
			RiskClass: "cache metric normalization risk",
			Evidence:  "internal/httpapi/cacheinfo_usage.go:76-86; internal/cacheinfo/render.go:68-72",
			Caveat:    "anthropic normalization expands input tokens before cache rate rendering, so the harness records this denominator risk as deferred rather than claiming protocol parity is already fixed.",
		},
	}
	var b strings.Builder
	b.WriteString("recorded caveats, not fixes.\n\n")
	b.WriteString("| risk_class | evidence | caveat |\n")
	b.WriteString("|---|---|---|\n")
	for _, issue := range issues {
		b.WriteString(fmt.Sprintf("| %s | %s | %s |\n",
			escapeLiveCacheTableValue(issue.RiskClass),
			escapeLiveCacheTableValue(issue.Evidence),
			escapeLiveCacheTableValue(issue.Caveat),
		))
	}
	return b.String()
}

func liveCacheTurnClass(turn int) string {
	if turn == 1 {
		return "turn1"
	}
	return "turn2+"
}

func renderLiveCacheObservationTable(observations []liveCacheObservation) string {
	if len(observations) == 0 {
		return "| round | execution_path | downstream | upstream | turn | turn_class | provider | model | canonical_digest | payload_digest | input_digest | input_chars | message_count | tool_count | tool_shape | raw_input_tokens | raw_cache_read_tokens | raw_cache_creation_tokens | raw_output_tokens | raw_total_tokens | normalized_input_tokens | normalized_cached_tokens | normalized_cache_ratio | tool | preview | note |\n|---|---|---|---|---|---|---|---|---|---|---|---:|---:|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---|---|---|"
	}
	var b strings.Builder
	b.WriteString("| round | execution_path | downstream | upstream | turn | turn_class | provider | model | canonical_digest | payload_digest | input_digest | input_chars | message_count | tool_count | tool_shape | raw_input_tokens | raw_cache_read_tokens | raw_cache_creation_tokens | raw_output_tokens | raw_total_tokens | normalized_input_tokens | normalized_cached_tokens | normalized_cache_ratio | tool | preview | note |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---:|---:|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---|---|---|\n")
	for _, obs := range observations {
		b.WriteString(fmt.Sprintf("| %d | %s | %s | %s | %d | %s | %s | %s | %s | %s | %s | %d | %d | %d | %s | %d | %d | %d | %d | %d | %d | %d | %.2f%% | %t | %s | %s |\n",
			obs.Round,
			escapeLiveCacheTableValue(string(obs.ExecutionPath)),
			escapeLiveCacheTableValue(obs.Downstream),
			escapeLiveCacheTableValue(obs.Upstream),
			obs.Turn,
			escapeLiveCacheTableValue(liveCacheTurnClass(obs.Turn)),
			escapeLiveCacheTableValue(obs.ProviderID),
			escapeLiveCacheTableValue(obs.Model),
			escapeLiveCacheTableValue(obs.CanonicalDigest),
			escapeLiveCacheTableValue(obs.PayloadDigest),
			escapeLiveCacheTableValue(obs.InputDigest),
			obs.InputChars,
			obs.MessageCount,
			obs.ToolCount,
			escapeLiveCacheTableValue(obs.ToolShape),
			obs.RawUsageInputTokens,
			obs.RawUsageCachedTokens,
			obs.RawUsageCreatedTokens,
			obs.RawUsageOutputTokens,
			obs.RawUsageTotalTokens,
			obs.UsageInputTokens,
			obs.UsageCachedTokens,
			obs.CacheRatio,
			obs.HasToolCall,
			escapeLiveCacheTableValue(obs.OutputPreview),
			escapeLiveCacheTableValue(obs.Note),
		))
	}
	return b.String()
}

func bindLiveCacheScenarioForExecutionPath(path liveCacheExecutionPath, scenario liveCacheScenarioSpec) liveCacheScenarioBinding {
	return liveCacheScenarioBinding{
		ExecutionPath:   path,
		Scenario:        scenario,
		CanonicalDigest: digestLiveCacheScenario(scenario),
	}
}

func buildLiveCacheScenarioSpec(round int, upstream string) liveCacheScenarioSpec {
	sharedCorpus := buildLiveCacheSharedCorpus(round, upstream)
	turns := make([]liveCacheScenarioTurn, 0, liveCacheCanonicalTurnCount)
	for turn := 1; turn <= liveCacheCanonicalTurnCount; turn++ {
		minInputChars := liveCacheMinInputCharsForTurn(turn)
		input := buildLiveCacheTurnInput(sharedCorpus, round, upstream, turn)
		input = ensureLiveCacheTurnInputMinChars(input, minInputChars, round, upstream, turn)
		turnSpec := liveCacheScenarioTurn{
			Number:         turn,
			InputText:      input,
			MinInputChars:  minInputChars,
			ExpectToolCall: turn == 1,
		}
		if turn == 2 {
			turnSpec.IncludeToolResult = true
			turnSpec.ToolResultJSON = buildLiveCacheToolResultJSON(round, upstream, turn)
		}
		turns = append(turns, turnSpec)
	}
	return liveCacheScenarioSpec{
		Round:        round,
		Upstream:     upstream,
		SharedCorpus: sharedCorpus,
		ToolName:     "lookup_project_facts",
		Turns:        turns,
	}
}

func liveCacheMinInputCharsForTurn(turn int) int {
	if turn == 1 {
		return liveCacheTurnOneMinInputChars
	}
	return liveCacheFollowUpMinInputChars
}

func ensureLiveCacheTurnInputMinChars(input string, minInputChars, round int, upstream string, turn int) string {
	if len(input) > minInputChars {
		return input
	}
	var b strings.Builder
	b.Grow(minInputChars + 512)
	b.WriteString(input)
	padding := fmt.Sprintf("\n[Turn %d Padding] round=%d upstream=%s 继续对 atlas_cache_window_%03d、atlas_component_%03d、atlas_release_note_%03d 做缓存复盘，强调长前缀复用、工具结果吸收和多轮上下文延续。", turn, round, upstream, turn*11, turn*13, turn*17)
	for b.Len() <= minInputChars {
		b.WriteString(padding)
	}
	return b.String()
}

func digestLiveCacheScenario(scenario liveCacheScenarioSpec) string {
	encoded, err := json.Marshal(scenario)
	if err != nil {
		return ""
	}
	return digestLiveCacheInput(string(encoded))
}

func buildLiveCacheSharedCorpus(round int, upstream string) string {
	var b strings.Builder
	b.Grow(72000)
	b.WriteString("你正在读取一份超长业务资料，用于验证代理层在多轮对话下是否会影响上游缓存命中。\n")
	b.WriteString(fmt.Sprintf("ROUND=%d UPSTREAM=%s\n", round, upstream))
	for section := 1; section <= 140; section++ {
		b.WriteString(fmt.Sprintf("[Section %03d] Atlas 资料段 %03d：记录缓存窗口 atlas_cache_window_%03d、组件 atlas_component_%03d、发布项 atlas_release_note_%03d。所有回答都必须围绕缓存命中率、输入 token、输出 token、多轮上下文保留和工具调用展开。第一轮先调工具，第二轮吸收工具结果，后续轮次持续复核缓存是否稳定并补充细节。\n", section, section, section, section, section))
	}
	return b.String()
}

func buildLiveCacheTurnInput(sharedCorpus string, round int, upstream string, turn int) string {
	return fmt.Sprintf("%s\n\n[Turn %d Request]\n请基于上面的 Atlas 资料继续工作。当前 round=%d upstream=%s turn=%d。\n要求一：不要忽略上面的长文档，并继续沿用同一批资料。\n要求二：如果本轮允许工具，请先调用 lookup_project_facts，并传入 project=atlas、focus=cache。\n要求三：作答时顺带提及 atlas_component_%03d 和 atlas_release_note_%03d。\n要求四：请用一句简短结论收尾，但不要把整份资料原样复述。",
		sharedCorpus,
		turn,
		round,
		upstream,
		turn,
		turn*37,
		turn*41,
	)
}

func responsesToolDefinition() map[string]any {
	return map[string]any{
		"type":        "function",
		"name":        "lookup_project_facts",
		"description": "Read the prepared project facts and return a compact cache-focused summary.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"project": map[string]any{"type": "string"},
				"focus":   map[string]any{"type": "string"},
			},
			"required": []string{"project", "focus"},
		},
	}
}

func chatToolDefinition() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "lookup_project_facts",
			"description": "Read the prepared project facts and return a compact cache-focused summary.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project": map[string]any{"type": "string"},
					"focus":   map[string]any{"type": "string"},
				},
				"required": []string{"project", "focus"},
			},
		},
	}
}

func anthropicToolDefinition() map[string]any {
	return map[string]any{
		"name":        "lookup_project_facts",
		"description": "Read the prepared project facts and return a compact cache-focused summary.",
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"project": map[string]any{"type": "string"},
				"focus":   map[string]any{"type": "string"},
			},
			"required": []string{"project", "focus"},
		},
	}
}

func anthropicTextBlock(text string) map[string]any {
	return map[string]any{
		"type": "text",
		"text": text,
		"cache_control": map[string]any{
			"type": "ephemeral",
		},
	}
}

func buildLiveCacheToolResultJSON(round int, upstream string, turn int) string {
	return fmt.Sprintf(`{"project":"atlas","focus":"cache","round":%d,"upstream":"%s","turn":%d,"summary":"The repeated prefix should stay cacheable while later turns only append short deltas."}`,
		round,
		upstream,
		turn,
	)
}

func previewResponsesOutput(output []any) string {
	parts := make([]string, 0, len(output))
	for _, rawItem := range output {
		item, _ := rawItem.(map[string]any)
		if item == nil {
			continue
		}
		switch itemType, _ := item["type"].(string); itemType {
		case "message":
			content, _ := item["content"].([]any)
			for _, rawContent := range content {
				contentItem, _ := rawContent.(map[string]any)
				if contentItem == nil {
					continue
				}
				if text, _ := contentItem["text"].(string); text != "" {
					parts = append(parts, text)
				}
			}
		case "function_call":
			name, _ := item["name"].(string)
			arguments, _ := item["arguments"].(string)
			parts = append(parts, fmt.Sprintf("function_call:%s %s", name, arguments))
		}
	}
	return strings.Join(parts, " | ")
}

func previewChatMessage(message map[string]any) string {
	parts := make([]string, 0, 2)
	if content, _ := message["content"].(string); content != "" {
		parts = append(parts, content)
	}
	if toolCalls, _ := message["tool_calls"].([]any); len(toolCalls) > 0 {
		for _, rawToolCall := range toolCalls {
			toolCall, _ := rawToolCall.(map[string]any)
			function, _ := toolCall["function"].(map[string]any)
			name, _ := function["name"].(string)
			arguments, _ := function["arguments"].(string)
			parts = append(parts, fmt.Sprintf("tool_call:%s %s", name, arguments))
		}
	}
	return strings.Join(parts, " | ")
}

func previewAnthropicContent(content []map[string]any) string {
	parts := make([]string, 0, len(content))
	for _, block := range content {
		switch blockType, _ := block["type"].(string); blockType {
		case "text", "thinking":
			if text, _ := block[blockType].(string); text != "" {
				parts = append(parts, text)
				continue
			}
			if text, _ := block["text"].(string); text != "" {
				parts = append(parts, text)
			}
		case "tool_use":
			name, _ := block["name"].(string)
			encodedInput, _ := json.Marshal(block["input"])
			parts = append(parts, fmt.Sprintf("tool_use:%s %s", name, string(encodedInput)))
		}
	}
	return strings.Join(parts, " | ")
}

func digestLiveCacheInput(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:6])
}

func collapsePreview(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= 96 {
		return text
	}
	return text[:93] + "..."
}

func escapeLiveCacheTableValue(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

func cloneLiveCacheMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func cloneLiveCacheMapSlice(input []map[string]any) []map[string]any {
	if len(input) == 0 {
		return nil
	}
	cloned := make([]map[string]any, 0, len(input))
	for _, item := range input {
		cloned = append(cloned, cloneLiveCacheMap(item))
	}
	return cloned
}
