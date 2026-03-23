package httpapi

import (
	"context"
	"testing"
	"time"

	"openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/upstream"
)

func TestStreamLiveWithSyntheticTicksFiresWhileWaitingForFirstText(t *testing.T) {
	oldInterval := syntheticReasoningTickInterval
	syntheticReasoningTickInterval = 10 * time.Millisecond
	defer func() { syntheticReasoningTickInterval = oldInterval }()

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
