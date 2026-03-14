package integration_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/testutil"
)

func TestNonStreamingRequestTimesOutWhenUpstreamNeverCompletes(t *testing.T) {
	stub := testutil.NewHangingUpstream(t)
	defer stub.Close()

	cfg := config.Default()
	cfg.UpstreamBaseURL = stub.URL
	cfg.UpstreamAPIKey = "server-key"
	cfg.TotalTimeout = 200 * time.Millisecond

	server := newTestServerWithConfig(t, cfg)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"x","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("expected 504, got %d", resp.StatusCode)
	}
}
