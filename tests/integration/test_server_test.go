package integration_test

import (
	"net/http/httptest"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/httpapi"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return newTestServerWithConfig(t, config.Default())
}

func newTestServerWithConfig(t *testing.T, cfg config.Config) *httptest.Server {
	t.Helper()
	return httptest.NewServer(httpapi.NewServer(cfg))
}

func newServerWithStubbedUpstream(t *testing.T, upstreamURL string) *httptest.Server {
	t.Helper()
	cfg := config.Default()
	cfg.UpstreamBaseURL = upstreamURL
	cfg.UpstreamAPIKey = "server-key"
	return newTestServerWithConfig(t, cfg)
}
