package upstream

import (
	"io"
	"net/http"
	"sync"
	"sync/atomic"
)

type managedTransport struct {
	roundTripper http.RoundTripper
	closeIdle    func()
	retired      atomic.Bool
}

func newManagedTransport(transport *http.Transport) *managedTransport {
	return &managedTransport{
		roundTripper: transport,
		closeIdle:    transport.CloseIdleConnections,
	}
}

func (t *managedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
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
	if t.retired.CompareAndSwap(false, true) {
		t.closeIdle()
	}
}

func (t *managedTransport) closeIdleIfRetired() {
	if t.retired.Load() {
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
