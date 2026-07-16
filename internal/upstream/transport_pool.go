package upstream

import (
	"strings"
	"sync"
	"time"

	"openai-compat-proxy/internal/config"
)

const transportGenerationsPerProvider = 2

type TransportSet struct {
	Regular    *managedTransport
	StreamOpen *managedTransport
}

func (t *TransportSet) retire() {
	t.Regular.retire()
	t.StreamOpen.retire()
}

func (t *TransportSet) markRetired() {
	t.Regular.markRetired()
	t.StreamOpen.markRetired()
}

type transportKey struct {
	providerID        string
	baseURL           string
	connectTimeout    time.Duration
	firstByteTimeout  time.Duration
	streamOpenTimeout time.Duration
	idleTimeout       time.Duration
}

type transportGeneration struct {
	key        transportKey
	transports *TransportSet
}

type TransportPool struct {
	mu                sync.Mutex
	byProvider        map[string][]transportGeneration
	activeProviderIDs map[string]struct{}
	reconciled        bool
	closed            bool
}

func NewTransportPool() *TransportPool {
	return &TransportPool{byProvider: make(map[string][]transportGeneration)}
}

func (p *TransportPool) ReconcileProviderIDs(active []string) {
	if p == nil {
		return
	}
	activeIDs := make(map[string]struct{}, len(active))
	for _, providerID := range active {
		providerID = strings.TrimSpace(providerID)
		if providerID != "" {
			activeIDs[providerID] = struct{}{}
		}
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.activeProviderIDs = activeIDs
	p.reconciled = true
	retired := make([]*TransportSet, 0)
	for providerID, generations := range p.byProvider {
		if _, ok := activeIDs[providerID]; ok {
			continue
		}
		delete(p.byProvider, providerID)
		for _, generation := range generations {
			retired = append(retired, generation.transports)
		}
	}
	p.mu.Unlock()

	for _, transports := range retired {
		transports.retire()
	}
}

func (p *TransportPool) Get(providerID string, baseURL string, cfg config.Config) *TransportSet {
	key := transportKey{
		providerID:        strings.TrimSpace(providerID),
		baseURL:           strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		connectTimeout:    cfg.ConnectTimeout,
		firstByteTimeout:  cfg.FirstByteTimeout,
		streamOpenTimeout: cfg.StreamOpenTimeout,
		idleTimeout:       cfg.IdleTimeout,
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		transports := newTransportSet(cfg)
		transports.retire()
		return transports
	}
	if p.reconciled {
		if _, active := p.activeProviderIDs[key.providerID]; !active {
			p.mu.Unlock()
			transports := newTransportSet(cfg)
			transports.retire()
			return transports
		}
	}
	generations := p.byProvider[key.providerID]
	for index, generation := range generations {
		if generation.key == key {
			if index != len(generations)-1 {
				copy(generations[index:], generations[index+1:])
				generations[len(generations)-1] = generation
				p.byProvider[key.providerID] = generations
			}
			p.mu.Unlock()
			return generation.transports
		}
	}
	transports := newTransportSet(cfg)
	for _, generation := range generations {
		generation.transports.markRetired()
	}
	generations = append(generations, transportGeneration{key: key, transports: transports})
	var evicted *TransportSet
	if len(generations) > transportGenerationsPerProvider {
		evicted = generations[0].transports
		generations = generations[1:]
	}
	p.byProvider[key.providerID] = generations
	p.mu.Unlock()

	if evicted != nil {
		evicted.retire()
	}
	return transports
}

func (p *TransportPool) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	retired := make([]*TransportSet, 0)
	for providerID, generations := range p.byProvider {
		delete(p.byProvider, providerID)
		for _, generation := range generations {
			retired = append(retired, generation.transports)
		}
	}
	p.mu.Unlock()

	for _, transports := range retired {
		transports.retire()
	}
}

func newTransportSet(cfg config.Config) *TransportSet {
	return &TransportSet{
		Regular:    newManagedTransport(newTransport(cfg)),
		StreamOpen: newManagedTransport(newStreamOpenTransport(cfg)),
	}
}
