package contextoverflow

import "testing"

func TestNormalize_returnsClientSignals_whenMessageIsRecognizedOverflow(t *testing.T) {
	// Given
	message := "input tokens exceed maximum"

	// When
	code, normalizedMessage, ok := Normalize("", message)

	// Then
	if !ok {
		t.Fatal("expected overflow signal to be recognized")
	}
	if code != "context_length_exceeded" {
		t.Fatalf("expected normalized code, got %q", code)
	}
	if normalizedMessage != "prompt is too long: context_length_exceeded: input tokens exceed maximum" {
		t.Fatalf("expected client-recognized message, got %q", normalizedMessage)
	}
}

func TestNormalize_preservesInputs_whenMessageIsVagueTooLong(t *testing.T) {
	// Given
	message := "request took too long"

	// When
	code, normalizedMessage, ok := Normalize("", message)

	// Then
	if ok {
		t.Fatal("expected vague too long message not to be normalized")
	}
	if code != "" || normalizedMessage != message {
		t.Fatalf("expected original values to remain unchanged, got code=%q message=%q", code, normalizedMessage)
	}
}

func TestNormalize_preservesInputs_whenTokenLimitIsAccountQuota(t *testing.T) {
	// Given
	message := "token limit exhausted for this API key"

	// When
	code, normalizedMessage, ok := Normalize("insufficient_quota", message)

	// Then
	if ok {
		t.Fatal("expected API-key quota token limit not to be normalized as context overflow")
	}
	if code != "insufficient_quota" || normalizedMessage != message {
		t.Fatalf("expected original quota error to remain unchanged, got code=%q message=%q", code, normalizedMessage)
	}
}

func TestNormalizeCandidates_preservesInputs_whenTypeIsQuotaAndCodeIsUnknown(t *testing.T) {
	// Given
	message := "input tokens exceed maximum for this API key quota"

	// When
	code, normalizedMessage, ok := NormalizeCandidates([]string{"USAGE_LIMIT_EXCEEDED", "insufficient_quota"}, message)

	// Then
	if ok {
		t.Fatal("expected quota type to prevent context overflow normalization")
	}
	if code != "USAGE_LIMIT_EXCEEDED" || normalizedMessage != message {
		t.Fatalf("expected original error to remain unchanged, got code=%q message=%q", code, normalizedMessage)
	}
}

func TestNormalize_returnsClientSignals_whenTokenLimitIsInputContextual(t *testing.T) {
	// Given
	message := "input token limit exceeded"

	// When
	code, normalizedMessage, ok := Normalize("", message)

	// Then
	if !ok {
		t.Fatal("expected input token limit to be recognized as context overflow")
	}
	if code != "context_length_exceeded" {
		t.Fatalf("expected normalized code, got %q", code)
	}
	if normalizedMessage != "prompt is too long: context_length_exceeded: input token limit exceeded" {
		t.Fatalf("expected client-recognized message, got %q", normalizedMessage)
	}
}
