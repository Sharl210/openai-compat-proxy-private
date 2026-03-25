package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/testutil"
	"openai-compat-proxy/internal/upstream"
)

func TestStreamLiveWithSyntheticTicksFiresWhileWaitingForFirstText(t *testing.T) {
	oldInterval := syntheticReasoningTickInterval
	oldHeartbeatInterval := sseHeartbeatInterval
	syntheticReasoningTickInterval = 10 * time.Millisecond
	sseHeartbeatInterval = time.Hour
	defer func() {
		syntheticReasoningTickInterval = oldInterval
		sseHeartbeatInterval = oldHeartbeatInterval
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	tickCount := 0
	eventCount := 0
	err := streamLiveWithSyntheticTicks(
		ctx,
		model.CanonicalRequest{},
		"",
		func(ctx context.Context, req model.CanonicalRequest, authorization string, onEvent func(upstream.Event) error) error {
			time.Sleep(35 * time.Millisecond)
			return onEvent(upstream.Event{Event: "response.output_text.delta", Data: map[string]any{"delta": "hello"}})
		},
		func() bool { return eventCount > 0 },
		func() error {
			tickCount++
			return nil
		},
		func() error { return nil },
		func(evt upstream.Event) error {
			eventCount++
			return nil
		},
	)
	if err != nil {
		t.Fatalf("streamLiveWithSyntheticTicks error: %v", err)
	}
	if tickCount < 2 {
		t.Fatalf("expected at least 2 synthetic ticks before first text, got %d", tickCount)
	}
	if eventCount != 1 {
		t.Fatalf("expected one upstream event, got %d", eventCount)
	}
}

func TestStreamLiveWithSyntheticTicksSendsHeartbeatWhileWaiting(t *testing.T) {
	oldTickInterval := syntheticReasoningTickInterval
	oldHeartbeatInterval := sseHeartbeatInterval
	syntheticReasoningTickInterval = time.Hour
	sseHeartbeatInterval = 10 * time.Millisecond
	defer func() {
		syntheticReasoningTickInterval = oldTickInterval
		sseHeartbeatInterval = oldHeartbeatInterval
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	heartbeatCount := 0
	eventCount := 0
	err := streamLiveWithSyntheticTicks(
		ctx,
		model.CanonicalRequest{},
		"",
		func(ctx context.Context, req model.CanonicalRequest, authorization string, onEvent func(upstream.Event) error) error {
			time.Sleep(35 * time.Millisecond)
			return onEvent(upstream.Event{Event: "response.output_text.delta", Data: map[string]any{"delta": "hello"}})
		},
		func() bool { return true },
		nil,
		func() error {
			heartbeatCount++
			return nil
		},
		func(evt upstream.Event) error {
			eventCount++
			return nil
		},
	)
	if err != nil {
		t.Fatalf("streamLiveWithSyntheticTicks error: %v", err)
	}
	if heartbeatCount < 2 {
		t.Fatalf("expected at least 2 heartbeat frames before first event, got %d", heartbeatCount)
	}
	if eventCount != 1 {
		t.Fatalf("expected one upstream event, got %d", eventCount)
	}
}

func TestMessagesStreamStopsSyntheticTicksAfterRealReasoningStarts(t *testing.T) {
	oldInterval := syntheticReasoningTickInterval
	oldHeartbeatInterval := sseHeartbeatInterval
	syntheticReasoningTickInterval = 10 * time.Millisecond
	sseHeartbeatInterval = time.Hour
	defer func() {
		syntheticReasoningTickInterval = oldInterval
		sseHeartbeatInterval = oldHeartbeatInterval
	}()

	upstream := testutil.NewDelayedStreamingUpstream(t, []string{
		"event: response.reasoning.delta\n" +
			"data: {\"summary\":\"alpha\"}\n\n",
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	}, 35*time.Millisecond)
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "anthropic",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                        "anthropic",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			SupportsAnthropicMessages: true,
			SupportsResponses:         true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"gpt-5.4",
		"stream":true,
		"max_tokens":64,
		"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	alphaIdx := strings.Index(body, `"thinking":"alpha"`)
	helloIdx := strings.Index(body, `"text":"hello"`)
	if alphaIdx == -1 || helloIdx == -1 || helloIdx <= alphaIdx {
		t.Fatalf("expected real reasoning before output text, got %s", body)
	}
	if strings.Contains(body[alphaIdx:helloIdx], "\u200b") {
		t.Fatalf("expected no synthetic zero-width ticks after real reasoning started, got %s", body)
	}
}
