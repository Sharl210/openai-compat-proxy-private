package upstream

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
)

func TestTransportPoolReconcileAbsentProviderGenerationsRetired(t *testing.T) {
	pool := NewTransportPool()
	removed := pool.Get("provider-a", "https://a.example/v1", transportPoolTestConfig())
	pool.Get("provider-b", "https://b.example/v1", transportPoolTestConfig())

	pool.ReconcileProviderIDs([]string{"provider-b"})

	if !removed.Regular.retired.Load() || !removed.StreamOpen.retired.Load() {
		t.Fatal("expected every absent provider transport to be retired")
	}
}

func TestTransportPoolReconcilePresentProviderPreserved(t *testing.T) {
	pool := NewTransportPool()
	preserved := pool.Get("provider-a", "https://a.example/v1", transportPoolTestConfig())

	pool.ReconcileProviderIDs([]string{" provider-a "})

	if got := pool.Get("provider-a", "https://a.example/v1", transportPoolTestConfig()); got != preserved {
		t.Fatal("expected normalized active provider generation to be preserved")
	}
}

func TestTransportPoolReconcileDuplicateCallIsIdempotent(t *testing.T) {
	pool := NewTransportPool()
	var retireCalls atomic.Int64
	pool.byProvider["provider-a"] = []transportGeneration{{transports: countingTransportSet(&retireCalls)}}

	pool.ReconcileProviderIDs(nil)
	pool.ReconcileProviderIDs(nil)

	if got := retireCalls.Load(); got != 2 {
		t.Fatalf("retire close calls=%d, want one per transport", got)
	}
}

func TestTransportPoolReconcileRetiresOutsidePoolLock(t *testing.T) {
	pool := NewTransportPool()
	reconcileReturned := make(chan struct{})
	transport := newManagedTestTransport(func(*http.Request) (*http.Response, error) { return nil, nil })
	transport.closeIdle = func() {
		pool.Get("provider-b", "https://b.example/v1", transportPoolTestConfig())
	}
	pool.byProvider["provider-a"] = []transportGeneration{{transports: &TransportSet{Regular: transport, StreamOpen: newManagedTestTransport(func(*http.Request) (*http.Response, error) { return nil, nil })}}}

	go func() {
		pool.ReconcileProviderIDs(nil)
		close(reconcileReturned)
	}()

	select {
	case <-reconcileReturned:
	case <-time.After(time.Second):
		t.Fatal("retirement callback blocked on the pool lock")
	}
}

func countingTransportSet(closeCalls *atomic.Int64) *TransportSet {
	newTransport := func() *managedTransport {
		transport := newManagedTestTransport(func(*http.Request) (*http.Response, error) { return nil, nil })
		transport.closeIdle = func() { closeCalls.Add(1) }
		return transport
	}
	return &TransportSet{Regular: newTransport(), StreamOpen: newTransport()}
}

func TestTransportPoolReusesNormalizedEquivalentKey(t *testing.T) {
	pool := NewTransportPool()
	cfg := transportPoolTestConfig()

	first := pool.Get(" provider-a ", " https://example.test/v1/// ", cfg)
	second := pool.Get("provider-a", "https://example.test/v1", cfg)

	if first != second {
		t.Fatal("expected normalized equivalent keys to reuse transports")
	}
}

func TestTransportPoolSeparatesTransportKeyFields(t *testing.T) {
	base := transportPoolTestConfig()
	tests := []struct {
		name       string
		providerID string
		baseURL    string
		mutate     func(*config.Config)
	}{
		{name: "provider", providerID: "provider-b", baseURL: "https://example.test/v1"},
		{name: "base URL", providerID: "provider-a", baseURL: "https://other.test/v1"},
		{name: "connect timeout", providerID: "provider-a", baseURL: "https://example.test/v1", mutate: func(cfg *config.Config) { cfg.ConnectTimeout++ }},
		{name: "first byte timeout", providerID: "provider-a", baseURL: "https://example.test/v1", mutate: func(cfg *config.Config) { cfg.FirstByteTimeout++ }},
		{name: "stream open timeout", providerID: "provider-a", baseURL: "https://example.test/v1", mutate: func(cfg *config.Config) { cfg.StreamOpenTimeout++ }},
		{name: "idle timeout", providerID: "provider-a", baseURL: "https://example.test/v1", mutate: func(cfg *config.Config) { cfg.IdleTimeout++ }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool := NewTransportPool()
			first := pool.Get("provider-a", "https://example.test/v1", base)
			changed := base
			if test.mutate != nil {
				test.mutate(&changed)
			}
			second := pool.Get(test.providerID, test.baseURL, changed)
			if first == second {
				t.Fatalf("expected %s to select distinct transports", test.name)
			}
		})
	}
}

func TestTransportPoolPromotesHitBeforeEviction(t *testing.T) {
	pool := NewTransportPool()
	cfgA := transportPoolTestConfig()
	cfgB := cfgA
	cfgB.FirstByteTimeout++
	cfgC := cfgB
	cfgC.FirstByteTimeout++

	a := pool.Get("provider-a", "https://example.test/v1", cfgA)
	b := pool.Get("provider-a", "https://example.test/v1", cfgB)
	if got := pool.Get("provider-a", "https://example.test/v1", cfgA); got != a {
		t.Fatal("expected A hit to remain reusable after A-B-A")
	}
	c := pool.Get("provider-a", "https://example.test/v1", cfgC)
	if got := pool.Get("provider-a", "https://example.test/v1", cfgA); got != a {
		t.Fatal("expected A generation to remain retained after C")
	}
	if got := pool.Get("provider-a", "https://example.test/v1", cfgC); got != c {
		t.Fatal("expected C generation to remain retained after A")
	}
	if got := pool.Get("provider-a", "https://example.test/v1", cfgB); got == b {
		t.Fatal("expected B generation to be evicted after A-B-A-C")
	}
}

func TestTransportPoolDoesNotRetainProviderRemovedByReconcile(t *testing.T) {
	pool := NewTransportPool()
	cfg := transportPoolTestConfig()
	pool.ReconcileProviderIDs([]string{"provider-a"})
	original := pool.Get("provider-a", "https://example.test/v1", cfg)
	pool.ReconcileProviderIDs([]string{"provider-b"})

	stale := pool.Get("provider-a", "https://example.test/v1", cfg)

	if stale == original {
		t.Fatal("expected stale request to receive a private retired transport set")
	}
	if _, retained := pool.byProvider["provider-a"]; retained {
		t.Fatal("expected stale request not to restore removed provider in pool")
	}
	if !stale.Regular.retired.Load() || !stale.StreamOpen.retired.Load() {
		t.Fatal("expected stale request transports to retire after their response completes")
	}
}

func transportPoolTestConfig() config.Config {
	return config.Config{
		ConnectTimeout:    time.Second,
		FirstByteTimeout:  2 * time.Second,
		StreamOpenTimeout: 3 * time.Second,
		IdleTimeout:       4 * time.Second,
	}
}
