package httpapi

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/upstream"
)

func TestUpstreamTransportPoolRealServerReusesConnectionForExplicitAndBareRoutes(t *testing.T) {
	upstreamServer, connections, _ := newTransportPoolTestServer(t, nil)
	fixture := newTransportPoolRuntimeFixture(t, upstreamServer.URL)

	fixture.requestModels(t, "/provider-a/v1/models")
	fixture.requestModels(t, "/v1/models")

	if got := connections.Load(); got != 1 {
		t.Fatalf("expected explicit and bare routes to share provider-a connection, got %d", got)
	}
}

func TestUpstreamTransportPoolRealHotReloadRetainsPreviousGeneration(t *testing.T) {
	serverA, connectionsA, _ := newTransportPoolTestServer(t, nil)
	serverB, _, _ := newTransportPoolTestServer(t, nil)
	fixture := newTransportPoolRuntimeFixture(t, serverA.URL)

	fixture.requestModels(t, "/provider-a/v1/models")
	fixture.refreshProvider(t, serverB.URL)
	fixture.requestModels(t, "/provider-a/v1/models")
	fixture.refreshProvider(t, serverA.URL)
	fixture.requestModels(t, "/provider-a/v1/models")

	if got := connectionsA.Load(); got != 1 {
		t.Fatalf("expected A connection retained across A-B-A hot reload, got %d", got)
	}
}

func TestUpstreamTransportPoolRealHotReloadPromotesHitsBeforeEviction(t *testing.T) {
	serverA, connectionsA, _ := newTransportPoolTestServer(t, nil)
	serverB, connectionsB, _ := newTransportPoolTestServer(t, nil)
	serverC, _, _ := newTransportPoolTestServer(t, nil)
	fixture := newTransportPoolRuntimeFixture(t, serverA.URL)

	fixture.requestModels(t, "/provider-a/v1/models")
	fixture.refreshProvider(t, serverB.URL)
	fixture.requestModels(t, "/provider-a/v1/models")
	fixture.refreshProvider(t, serverA.URL)
	fixture.requestModels(t, "/provider-a/v1/models")
	fixture.refreshProvider(t, serverC.URL)
	fixture.requestModels(t, "/provider-a/v1/models")
	fixture.refreshProvider(t, serverA.URL)
	fixture.requestModels(t, "/provider-a/v1/models")
	fixture.refreshProvider(t, serverB.URL)
	fixture.requestModels(t, "/provider-a/v1/models")

	if got := connectionsA.Load(); got != 1 {
		t.Fatalf("expected promoted A to reuse one connection across A-B-A-C-A, got %d connections", got)
	}
	if got := connectionsB.Load(); got != 2 {
		t.Fatalf("expected evicted B to reopen after A-B-A-C-A-B, got %d connections", got)
	}
}

func TestUpstreamTransportPoolActiveSSESurvivesGenerationEviction(t *testing.T) {
	streamStarted := make(chan struct{})
	releaseStream := make(chan struct{})
	serverA, _, _ := newTransportPoolTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: response.created\ndata: {}\n\n")
		w.(http.Flusher).Flush()
		close(streamStarted)
		<-releaseStream
		_, _ = io.WriteString(w, "event: response.completed\ndata: {}\n\n")
	})
	serverB, _, _ := newTransportPoolTestServer(t, nil)
	serverC, _, _ := newTransportPoolTestServer(t, nil)
	pool := upstream.NewTransportPool()
	requestA := requestWithSpecificTransportPool(transportPoolHTTPConfig(serverA.URL), pool)

	result := make(chan error, 1)
	go func() {
		client := upstreamClientForProvider(requestA, "provider-a", transportPoolHTTPConfig(serverA.URL))
		events, err := client.Stream(requestA.Context(), model.CanonicalRequest{Model: "test-model"}, "Bearer test")
		if err == nil && (len(events) == 0 || events[len(events)-1].Event != "response.completed") {
			err = errors.New("active SSE did not receive terminal event")
		}
		result <- err
	}()
	select {
	case <-streamStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for active SSE to start")
	}
	requestModelsDirect(t, requestWithSpecificTransportPool(transportPoolHTTPConfig(serverB.URL), pool))
	requestModelsDirect(t, requestWithSpecificTransportPool(transportPoolHTTPConfig(serverC.URL), pool))
	close(releaseStream)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("active SSE failed after eviction: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for active SSE to finish")
	}
}

func TestUpstreamTransportReconcileUsesLatestActiveSnapshot(t *testing.T) {
	serverA, connectionsA, _ := newTransportPoolTestServer(t, nil)
	serverB, _, _ := newTransportPoolTestServer(t, nil)
	fixture := newTransportPoolRuntimeFixture(t, serverA.URL)
	oldSnapshot := fixture.store.Active()

	writeTransportPoolProviderEnvWithID(t, fixture.providerEnvPath, "provider-b", serverB.URL)
	providersDir := filepath.Dir(fixture.providerEnvPath)
	rootEnv := []byte("PROVIDERS_DIR=" + providersDir + "\nDEFAULT_PROVIDER=provider-b\nENABLE_LEGACY_V1_ROUTES=true\n")
	if err := os.WriteFile(fixture.rootEnvPath, rootEnv, 0o600); err != nil {
		t.Fatalf("write root env: %v", err)
	}
	if err := fixture.store.Refresh(); err != nil {
		t.Fatalf("refresh runtime store: %v", err)
	}

	// Simulate a stale listener callback selecting the provider set from the old snapshot.
	fixture.server.upstreamTransports.ReconcileProviderIDs([]string{"provider-a"})
	fixture.server.reconcileUpstreamTransports(oldSnapshot)

	request, err := http.NewRequest(http.MethodGet, serverA.URL, nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	transports := fixture.server.upstreamTransports.Get("provider-a", serverA.URL, transportPoolHTTPConfig(serverA.URL))
	response, err := transports.Regular.RoundTrip(request)
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil {
		t.Fatal("stale provider RoundTrip unexpectedly succeeded")
	}
	if got := connectionsA.Load(); got != 0 {
		t.Fatalf("stale provider opened %d connections after reconcile", got)
	}
}

func TestServerCloseRetiresUpstreamTransports(t *testing.T) {
	upstreamServer, _, _ := newTransportPoolTestServer(t, nil)
	fixture := newTransportPoolRuntimeFixture(t, upstreamServer.URL)
	fixture.requestModels(t, "/provider-a/v1/models")

	fixture.server.Close()
	fixture.server.Close()

	request, err := http.NewRequest(http.MethodGet, upstreamServer.URL, nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	transports := fixture.server.upstreamTransports.Get("provider-a", upstreamServer.URL, transportPoolHTTPConfig(upstreamServer.URL))
	response, err := transports.Regular.RoundTrip(request)
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil {
		t.Fatal("RoundTrip succeeded after Server.Close")
	}
}

type transportPoolRuntimeFixture struct {
	server          *Server
	store           *config.RuntimeStore
	rootEnvPath     string
	providerEnvPath string
}

func newTransportPoolRuntimeFixture(t *testing.T, upstreamBaseURL string) *transportPoolRuntimeFixture {
	t.Helper()
	rootDir := t.TempDir()
	providersDir := filepath.Join(rootDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers dir: %v", err)
	}
	rootEnvPath := filepath.Join(rootDir, ".env")
	providerEnvPath := filepath.Join(providersDir, "provider-a.env")
	rootEnv := []byte("PROVIDERS_DIR=" + providersDir + "\nDEFAULT_PROVIDER=provider-a\nENABLE_LEGACY_V1_ROUTES=true\n")
	if err := os.WriteFile(rootEnvPath, rootEnv, 0o600); err != nil {
		t.Fatalf("write root env: %v", err)
	}
	writeTransportPoolProviderEnv(t, providerEnvPath, upstreamBaseURL)
	store, err := config.NewRuntimeStore(rootEnvPath)
	if err != nil {
		t.Fatalf("create runtime store: %v", err)
	}
	return &transportPoolRuntimeFixture{
		server:          NewServerWithStore(store, nil, nil),
		store:           store,
		rootEnvPath:     rootEnvPath,
		providerEnvPath: providerEnvPath,
	}
}

func (f *transportPoolRuntimeFixture) refreshProvider(t *testing.T, upstreamBaseURL string) {
	t.Helper()
	writeTransportPoolProviderEnv(t, f.providerEnvPath, upstreamBaseURL)
	if err := f.store.Refresh(); err != nil {
		t.Fatalf("refresh runtime store: %v", err)
	}
}

func (f *transportPoolRuntimeFixture) requestModels(t *testing.T, path string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	f.server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s returned status %d: %s", path, recorder.Code, recorder.Body.String())
	}
}

func writeTransportPoolProviderEnv(t *testing.T, path string, upstreamBaseURL string) {
	writeTransportPoolProviderEnvWithID(t, path, "provider-a", upstreamBaseURL)
}

func writeTransportPoolProviderEnvWithID(t *testing.T, path string, providerID string, upstreamBaseURL string) {
	t.Helper()
	contents := fmt.Sprintf("PROVIDER_ID=%s\nPROVIDER_ENABLED=true\nUPSTREAM_BASE_URL=%s\nUPSTREAM_API_KEY=test-key\nUPSTREAM_ENDPOINT_TYPE=responses\nSUPPORTS_MODELS=true\n", providerID, upstreamBaseURL)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write provider env: %v", err)
	}
}

func newTransportPoolTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *atomic.Int64, <-chan struct{}) {
	t.Helper()
	connections := &atomic.Int64{}
	newConnection := make(chan struct{}, 8)
	if handler == nil {
		handler = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
		}
	}
	server := httptest.NewUnstartedServer(handler)
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
			newConnection <- struct{}{}
		}
	}
	server.Start()
	t.Cleanup(server.Close)
	return server, connections, newConnection
}

func transportPoolHTTPConfig(baseURL string) config.Config {
	cfg := config.Default()
	cfg.UpstreamBaseURL = baseURL
	cfg.UpstreamEndpointType = config.UpstreamEndpointTypeResponses
	cfg.FirstByteTimeout = time.Minute
	cfg.StreamOpenTimeout = time.Minute
	return cfg
}

func requestWithSpecificTransportPool(cfg config.Config, pool *upstream.TransportPool) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	snapshot := &config.RuntimeSnapshot{Config: cfg}
	ctx := withRuntimeSnapshot(request.Context(), snapshot)
	return request.Clone(withUpstreamTransportPool(ctx, pool))
}

func requestModelsDirect(t *testing.T, request *http.Request) {
	t.Helper()
	snapshot, _ := runtimeSnapshotFromRequest(request)
	client := upstreamClientForProvider(request, "provider-a", snapshot.Config)
	status, _, _, err := client.Models(request.Context(), "Bearer test")
	if err != nil || status != http.StatusOK {
		t.Fatalf("models request failed: status=%d err=%v", status, err)
	}
}
