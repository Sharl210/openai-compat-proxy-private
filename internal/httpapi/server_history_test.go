package httpapi

import (
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestNewServerUsesLogMaxRequestsForResponsesHistory(t *testing.T) {
	cfg := config.Default()
	cfg.LogMaxRequests = 7

	srv := NewServer(cfg)
	defer srv.Close()

	if srv.history == nil {
		t.Fatal("expected responses history store")
	}
	if srv.history.maxSize != cfg.LogMaxRequests {
		t.Fatalf("expected responses history max size %d, got %d", cfg.LogMaxRequests, srv.history.maxSize)
	}
}
