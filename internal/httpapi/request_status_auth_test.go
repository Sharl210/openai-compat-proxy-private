package httpapi

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
)

func TestRequestStatusAuthStoreIssueConsumeRemovesToken(t *testing.T) {
	store := newRequestStatusAuthStore()
	token := store.issueToken("provider", "request")
	if token == "" {
		t.Fatal("issueToken returned empty token")
	}
	if !store.consumeToken(token, "provider", "request") {
		t.Fatal("consumeToken should succeed for matching credentials")
	}
	if store.consumeToken(token, "provider", "request") {
		t.Fatal("consumeToken should fail after token removal")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.grants) != 0 {
		t.Fatalf("expected grants to be empty, got=%d", len(store.grants))
	}
}

func TestRequestStatusAuthStoreGCRemovesExpiredTokens(t *testing.T) {
	store := newRequestStatusAuthStore()
	store.ttl = time.Minute
	store.gcSweepInterval = 1
	fakeNow := time.Unix(1710000000, 0)
	store.now = func() time.Time {
		return fakeNow
	}
	token := store.issueToken("provider", "request")
	if token == "" {
		t.Fatal("issueToken returned empty token")
	}
	store.forceGC()
	store.mu.Lock()
	if _, ok := store.grants[token]; !ok {
		store.mu.Unlock()
		t.Fatal("token should still exist before TTL elapsed")
	}
	store.mu.Unlock()
	fakeNow = fakeNow.Add(2 * time.Minute)
	store.forceGC()
	store.mu.Lock()
	if _, ok := store.grants[token]; ok {
		store.mu.Unlock()
		t.Fatal("token should be removed after TTL elapsed")
	}
	store.mu.Unlock()
}

func TestRequestStatusAuthStoreZeroTTLExpiresImmediately(t *testing.T) {
	store := newRequestStatusAuthStore()
	store.ttl = 0
	store.gcSweepInterval = 1
	token := store.issueToken("provider", "request")
	if token == "" {
		t.Fatal("issueToken returned empty token")
	}
	store.forceGC()
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.grants[token]; ok {
		t.Fatal("token should be removed immediately when TTL <= 0")
	}
}

func TestValidateStatusCheckAuthAcceptsToken(t *testing.T) {
	store := newRequestStatusAuthStore()
	token := store.issueToken("openai", "req-1")
	req := httptest.NewRequest("GET", "/openai/v1/requests/req-1?token="+token, nil)
	ctx := withRequestStatusAuthStore(context.Background(), store)
	ctx = withRequestStatusID(ctx, "req-1")
	req = req.WithContext(ctx)
	provider := config.ProviderConfig{ID: "openai", Enabled: true, ProxyAPIKeyOverrideSet: true, ProxyAPIKeyOverride: "override-key"}
	if err := validateStatusCheckAuth(req, "root-key", provider, "req-1"); err != nil {
		t.Fatalf("token should authorize request: %v", err)
	}
	if store.consumeToken(token, "openai", "req-1") {
		t.Fatal("token should be consumed after validation")
	}
}
