package aggregate

import (
	"errors"
	"testing"

	"openai-compat-proxy/internal/upstream"
)

func TestCollectorPreservesFirstFailure_whenLaterFailureDiffers(t *testing.T) {
	// Given
	collector := NewCollector()
	collector.Accept(upstream.Event{Event: "error", Data: map[string]any{
		"health_flag": "context_length_exceeded",
		"message":     "prompt is too long: first classified failure",
	}})

	// When
	collector.Accept(upstream.Event{Event: "response.failed", Data: map[string]any{
		"health_flag": "upstream_error",
		"message":     "later generic failure",
	}})
	_, err := collector.Result()

	// Then
	var terminalFailure *TerminalFailureError
	if !errors.As(err, &terminalFailure) {
		t.Fatalf("expected TerminalFailureError, got %v", err)
	}
	if terminalFailure.HealthFlag != "context_length_exceeded" {
		t.Fatalf("expected first health flag to win, got %q", terminalFailure.HealthFlag)
	}
	if terminalFailure.Message != "prompt is too long: first classified failure" {
		t.Fatalf("expected first failure message to win, got %q", terminalFailure.Message)
	}
}

func TestCollectorPreservesSuccess_whenFailureArrivesAfterCompletion(t *testing.T) {
	// Given
	collector := NewCollector()
	collector.Accept(upstream.Event{Event: "response.completed", Data: map[string]any{
		"response": map[string]any{"finish_reason": "stop"},
	}})

	// When
	collector.Accept(upstream.Event{Event: "error", Data: map[string]any{
		"health_flag": "upstream_error",
		"message":     "late failure",
	}})
	result, err := collector.Result()

	// Then
	if err != nil {
		t.Fatalf("expected first successful terminal to win, got %v", err)
	}
	if result.FinishReason != "stop" {
		t.Fatalf("expected successful finish reason to be preserved, got %q", result.FinishReason)
	}
}
