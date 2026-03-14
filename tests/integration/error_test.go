package integration_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestUnsupportedRouteReturnsOpenAIStyleError(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/unknown", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}

	if _, ok := body["error"]; !ok {
		t.Fatal("expected error envelope")
	}
}

func TestRequestIDHeaderIsPresent(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	resp, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.Header.Get("X-Request-Id") == "" {
		t.Fatal("expected X-Request-Id header")
	}
}
