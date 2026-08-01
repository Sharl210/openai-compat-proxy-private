package httpapi

import (
	"net/http/httptest"
	"testing"

	"openai-compat-proxy/internal/upstream"
)

func TestEnsureProxySessionIDBindsProvidedIDToRequestAndUpstreamContext(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/responses", nil)
	req.Header.Set(headerProxySessionID, "client-session-123")
	recorder := httptest.NewRecorder()

	updated, sessionID := ensureProxySessionID(req, recorder)

	if sessionID != "client-session-123" {
		t.Fatalf("expected provided session ID to be preserved, got %q", sessionID)
	}
	if got := recorder.Header().Get(headerProxySessionID); got != sessionID {
		t.Fatalf("expected response session header %q, got %q", sessionID, got)
	}
	if got := proxySessionIDFromRequest(updated); got != sessionID {
		t.Fatalf("expected request session ID %q, got %q", sessionID, got)
	}
	if got := upstream.SessionIDFromContext(updated.Context()); got != sessionID {
		t.Fatalf("expected upstream context session ID %q, got %q", sessionID, got)
	}
}

func TestEnsureProxySessionIDGeneratesAndBindsMissingID(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	recorder := httptest.NewRecorder()

	updated, sessionID := ensureProxySessionID(req, recorder)

	if sessionID == "" {
		t.Fatal("expected generated session ID")
	}
	if got := recorder.Header().Get(headerProxySessionID); got != sessionID {
		t.Fatalf("expected response session header %q, got %q", sessionID, got)
	}
	if got := upstream.SessionIDFromContext(updated.Context()); got != sessionID {
		t.Fatalf("expected upstream context session ID %q, got %q", sessionID, got)
	}
}
