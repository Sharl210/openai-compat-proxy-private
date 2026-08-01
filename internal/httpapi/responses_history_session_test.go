package httpapi

import (
	"testing"

	"openai-compat-proxy/internal/model"
)

func TestResponsesHistoryStoresSessionIDForScopedAndPortableReplay(t *testing.T) {
	store := newResponsesHistoryStore(8, "")
	messages := []model.CanonicalMessage{{
		Role:  "user",
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "session history"}},
	}}
	sessionID := "session-history-123"
	scope := "native-scope"
	portableScope := responsesHistoryPortableScope("caller-a")

	store.SaveWithPortableScopeAndSession("provider-a", "resp-1", messages, scope, portableScope, sessionID)

	if got := store.LoadSessionIDScoped("provider-a", "resp-1", scope); got != sessionID {
		t.Fatalf("expected scoped session ID %q, got %q", sessionID, got)
	}
	if got := store.LoadSessionIDScoped("provider-a", "resp-1", "other-scope"); got != "" {
		t.Fatalf("expected scope mismatch to hide session ID, got %q", got)
	}
	if got := store.LoadSessionIDPortable("resp-1", portableScope); got != sessionID {
		t.Fatalf("expected portable session ID %q, got %q", sessionID, got)
	}
}
