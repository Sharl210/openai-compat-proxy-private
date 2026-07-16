package httpapi

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
)

func TestUpstreamTransportPoolHotReloadPrunesDeletedProvider(t *testing.T) {
	upstreamServer, connectionClosed := newReconcileTestServer(t, nil)
	fixture := newReconcileRuntimeFixture(t, upstreamServer.URL)
	fixture.requestModels(t, "/provider-a/v1/models")

	if err := os.Remove(fixture.providerEnvPath); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Refresh(); err != nil {
		t.Fatal(err)
	}
	awaitConnectionClosed(t, connectionClosed)
	assertRouteStatus(t, fixture.server, "/provider-a/v1/models", http.StatusNotFound)
}

func TestUpstreamTransportPoolHotReloadPrunesRenamedProvider(t *testing.T) {
	upstreamServer, connectionClosed := newReconcileTestServer(t, nil)
	fixture := newReconcileRuntimeFixture(t, upstreamServer.URL)
	fixture.requestModels(t, "/provider-a/v1/models")

	writeReconcileProviderEnv(t, fixture.providerEnvPath, "provider-c", true, upstreamServer.URL)
	if err := fixture.store.Refresh(); err != nil {
		t.Fatal(err)
	}
	awaitConnectionClosed(t, connectionClosed)
	fixture.requestModels(t, "/provider-c/v1/models")
	assertRouteStatus(t, fixture.server, "/provider-a/v1/models", http.StatusNotFound)
}

func TestUpstreamTransportPoolHotReloadPrunesDisabledProvider(t *testing.T) {
	upstreamServer, connectionClosed := newReconcileTestServer(t, nil)
	fixture := newReconcileRuntimeFixture(t, upstreamServer.URL)
	fixture.requestModels(t, "/provider-a/v1/models")

	writeReconcileProviderEnv(t, fixture.providerEnvPath, "provider-a", false, upstreamServer.URL)
	if err := fixture.store.Refresh(); err != nil {
		t.Fatal(err)
	}
	awaitConnectionClosed(t, connectionClosed)
	assertRouteStatus(t, fixture.server, "/provider-a/v1/models", http.StatusNotFound)
}

func TestUpstreamTransportPoolStaleSnapshotCannotRestoreRemovedProvider(t *testing.T) {
	upstreamServer, _ := newReconcileTestServer(t, nil)
	fixture := newReconcileRuntimeFixture(t, upstreamServer.URL)
	staleSnapshot := fixture.store.Active()
	fixture.requestModels(t, "/provider-a/v1/models")

	if err := os.Remove(fixture.providerEnvPath); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Refresh(); err != nil {
		t.Fatal(err)
	}
	assertRouteStatus(t, fixture.server, "/provider-a/v1/models", http.StatusNotFound)

	fixture.server.reconcileUpstreamTransports(staleSnapshot)
	provider, err := staleSnapshot.Config.ProviderByID("provider-a")
	if err != nil {
		t.Fatal(err)
	}
	providerCfg := providerConfigForID(staleSnapshot, provider.ID)
	first := fixture.server.upstreamTransports.Get(provider.ID, providerCfg.UpstreamBaseURL, providerCfg)
	second := fixture.server.upstreamTransports.Get(provider.ID, providerCfg.UpstreamBaseURL, providerCfg)
	if first == second {
		t.Fatal("expected stale snapshot requests not to restore a reusable removed-provider generation")
	}
}

func TestUpstreamTransportPoolRetiredActiveSSEReceivesTerminalThenConnectionCloses(t *testing.T) {
	streamStarted := make(chan struct{})
	releaseStream := make(chan struct{})
	upstreamServer, connectionClosed := newReconcileTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"test-model","object":"model"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.created\ndata: {}\n\n")
		w.(http.Flusher).Flush()
		close(streamStarted)
		<-releaseStream
		_, _ = io.WriteString(w, "event: response.completed\ndata: {}\n\n")
	})
	fixture := newReconcileRuntimeFixture(t, upstreamServer.URL)
	result := make(chan string, 1)
	go func() {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/provider-a/v1/responses", strings.NewReader(`{"model":"test-model","input":"hi","stream":true}`))
		request.Header.Set("Content-Type", "application/json")
		fixture.server.ServeHTTP(recorder, request)
		result <- recorder.Body.String()
	}()
	select {
	case <-streamStarted:
	case body := <-result:
		t.Fatalf("SSE request ended before upstream stream started: %s", body)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for active SSE start")
	}

	if err := os.Remove(fixture.providerEnvPath); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Refresh(); err != nil {
		t.Fatal(err)
	}
	close(releaseStream)

	select {
	case body := <-result:
		if !strings.Contains(body, "response.completed") {
			t.Fatalf("retired active SSE missed terminal event: %s", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for retired active SSE terminal event")
	}
	awaitConnectionClosed(t, connectionClosed)
	assertRouteStatus(t, fixture.server, "/provider-a/v1/models", http.StatusNotFound)
}

func newReconcileTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	if handler == nil {
		handler = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
		}
	}
	connectionClosed := make(chan struct{}, 4)
	server := httptest.NewUnstartedServer(handler)
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			connectionClosed <- struct{}{}
		}
	}
	server.Start()
	t.Cleanup(server.Close)
	return server, connectionClosed
}

func newReconcileRuntimeFixture(t *testing.T, upstreamBaseURL string) *transportPoolRuntimeFixture {
	t.Helper()
	rootDir := t.TempDir()
	providersDir := filepath.Join(rootDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rootEnvPath := filepath.Join(rootDir, ".env")
	providerAPath := filepath.Join(providersDir, "provider-a.env")
	providerBPath := filepath.Join(providersDir, "provider-b.env")
	rootEnv := "PROVIDERS_DIR=" + providersDir + "\nDEFAULT_PROVIDER=provider-b\nENABLE_LEGACY_V1_ROUTES=true\n"
	if err := os.WriteFile(rootEnvPath, []byte(rootEnv), 0o600); err != nil {
		t.Fatal(err)
	}
	writeReconcileProviderEnv(t, providerAPath, "provider-a", true, upstreamBaseURL)
	writeReconcileProviderEnv(t, providerBPath, "provider-b", true, upstreamBaseURL)
	store, err := config.NewRuntimeStore(rootEnvPath)
	if err != nil {
		t.Fatal(err)
	}
	return &transportPoolRuntimeFixture{server: NewServerWithStore(store, nil, nil), store: store, providerEnvPath: providerAPath}
}

func writeReconcileProviderEnv(t *testing.T, path string, providerID string, enabled bool, upstreamBaseURL string) {
	t.Helper()
	contents := "PROVIDER_ID=" + providerID + "\nPROVIDER_ENABLED=" + strconv.FormatBool(enabled) + "\nUPSTREAM_BASE_URL=" + upstreamBaseURL + "\nUPSTREAM_API_KEY=test-key\nUPSTREAM_ENDPOINT_TYPE=responses\nSUPPORTS_MODELS=true\nSUPPORTS_RESPONSES=true\nMANUAL_MODELS=test-model\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertRouteStatus(t *testing.T, server *Server, path string, want int) {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	if recorder.Code != want {
		t.Fatalf("GET %s status=%d, want %d: %s", path, recorder.Code, want, recorder.Body.String())
	}
}

func awaitConnectionClosed(t *testing.T, closed <-chan struct{}) {
	t.Helper()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream TCP StateClosed")
	}
}
