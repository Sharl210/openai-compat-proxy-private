package upstream

import (
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
)

var errManagedTransportRetired = errors.New("upstream transport retired")

type managedTransport struct {
	roundTripper    http.RoundTripper
	closeIdle       func()
	roundTripGate   sync.Mutex
	retired         atomic.Bool
	closed          atomic.Bool
	closeOnTerminal atomic.Bool
	idleClosed      atomic.Bool
}

func newManagedTransport(transport *http.Transport) *managedTransport {
	return &managedTransport{
		roundTripper: transport,
		closeIdle:    transport.CloseIdleConnections,
	}
}

func (t *managedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.roundTripGate.Lock()
	if t.closed.Load() {
		t.roundTripGate.Unlock()
		return nil, errManagedTransportRetired
	}
	t.roundTripGate.Unlock()
	response, err := t.roundTripper.RoundTrip(request)
	if response != nil && response.Body != nil {
		response.Body = &managedResponseBody{
			ReadCloser: response.Body,
			onTerminal: t.closeIdleIfRetired,
		}
	}
	return response, err
}

func (t *managedTransport) retire() {
	t.roundTripGate.Lock()
	t.retired.Store(true)
	t.closed.Store(true)
	t.closeOnTerminal.Store(true)
	t.roundTripGate.Unlock()
	if t.idleClosed.CompareAndSwap(false, true) {
		t.closeIdle()
	}
}

func (t *managedTransport) markRetired() {
	t.retired.Store(true)
}

func (t *managedTransport) closeIdleIfRetired() {
	if t.closeOnTerminal.Load() {
		t.closeIdle()
	}
}

type managedResponseBody struct {
	io.ReadCloser
	onTerminal func()
	terminal   sync.Once
	close      sync.Once
	closeErr   error
}

func (b *managedResponseBody) Read(buffer []byte) (int, error) {
	n, err := b.ReadCloser.Read(buffer)
	if err != nil {
		b.terminal.Do(b.onTerminal)
	}
	return n, err
}

func (b *managedResponseBody) Close() error {
	b.close.Do(func() {
		b.closeErr = b.ReadCloser.Close()
		b.terminal.Do(b.onTerminal)
	})
	return b.closeErr
}
