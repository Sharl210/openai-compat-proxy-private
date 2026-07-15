package upstream

import (
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
)

var errManagedTransportTest = errors.New("managed transport test error")

type managedRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f managedRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type managedTestBody struct {
	reads      []managedTestRead
	readIndex  int
	closeCalls atomic.Int64
	closeErr   error
}

type managedTestRead struct {
	data string
	err  error
}

func (b *managedTestBody) Read(buffer []byte) (int, error) {
	if b.readIndex >= len(b.reads) {
		return 0, io.EOF
	}
	read := b.reads[b.readIndex]
	b.readIndex++
	return copy(buffer, read.data), read.err
}

func (b *managedTestBody) Close() error {
	b.closeCalls.Add(1)
	return b.closeErr
}

func TestManagedTransport_preserves_round_trip_error_and_nil_response(t *testing.T) {
	request := &http.Request{}
	transport := newManagedTestTransport(func(got *http.Request) (*http.Response, error) {
		if got != request {
			t.Fatal("RoundTrip changed the request pointer")
		}
		return nil, errManagedTransportTest
	})

	response, err := transport.RoundTrip(request)

	if response != nil || err != errManagedTransportTest {
		t.Fatalf("got response=%p error=%v", response, err)
	}
}

func TestManagedTransport_preserves_nil_body(t *testing.T) {
	want := &http.Response{StatusCode: http.StatusNoContent, Header: http.Header{"X-Test": {"value"}}}
	transport := newManagedTestTransport(func(*http.Request) (*http.Response, error) { return want, nil })

	got, err := transport.RoundTrip(&http.Request{})

	if err != nil || got != want || got.Body != nil {
		t.Fatalf("got response=%p body=%v error=%v", got, got.Body, err)
	}
}

func TestManagedTransport_wraps_non_nil_body_and_preserves_response(t *testing.T) {
	body := &managedTestBody{}
	want := &http.Response{StatusCode: http.StatusAccepted, Body: body, Header: http.Header{"X-Test": {"value"}}}
	transport := newManagedTestTransport(func(*http.Request) (*http.Response, error) { return want, nil })

	got, err := transport.RoundTrip(&http.Request{})

	if err != nil || got != want || got.Body == body || got.StatusCode != want.StatusCode || got.Header.Get("X-Test") != "value" {
		t.Fatalf("response fields were not preserved: response=%p body=%T error=%v", got, got.Body, err)
	}
}

func TestManagedTransport_retire_is_immediate_and_idempotent(t *testing.T) {
	transport, closeCalls := newManagedCountingTransport(nil)

	transport.retire()
	transport.retire()

	if got := closeCalls.Load(); got != 1 {
		t.Fatalf("CloseIdleConnections calls=%d, want 1", got)
	}
}

func TestManagedTransport_retired_body_terminal_closes_idle_once(t *testing.T) {
	tests := []struct {
		name   string
		reads  []managedTestRead
		finish func(t *testing.T, body io.ReadCloser)
	}{
		{name: "EOF", reads: []managedTestRead{{err: io.EOF}}, finish: readManagedBody},
		{name: "read error", reads: []managedTestRead{{err: errManagedTransportTest}}, finish: readManagedBody},
		{name: "Close", finish: closeManagedBody},
		{name: "duplicate Close", finish: func(t *testing.T, body io.ReadCloser) { closeManagedBody(t, body); closeManagedBody(t, body) }},
		{name: "EOF then Close", reads: []managedTestRead{{err: io.EOF}}, finish: func(t *testing.T, body io.ReadCloser) { readManagedBody(t, body); closeManagedBody(t, body) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := &managedTestBody{reads: test.reads}
			transport, closeCalls := newManagedCountingTransport(body)
			response, err := transport.RoundTrip(&http.Request{})
			if err != nil {
				t.Fatal(err)
			}
			transport.retire()

			test.finish(t, response.Body)

			if got := closeCalls.Load(); got != 2 {
				t.Fatalf("CloseIdleConnections calls=%d, want retirement plus one terminal close", got)
			}
			if test.name == "duplicate Close" && body.closeCalls.Load() != 1 {
				t.Fatalf("underlying Close calls=%d, want 1", body.closeCalls.Load())
			}
		})
	}
}

func TestManagedTransport_terminal_before_retirement_only_closes_on_retirement(t *testing.T) {
	body := &managedTestBody{reads: []managedTestRead{{err: io.EOF}}}
	transport, closeCalls := newManagedCountingTransport(body)
	response, err := transport.RoundTrip(&http.Request{})
	if err != nil {
		t.Fatal(err)
	}

	readManagedBody(t, response.Body)
	transport.retire()

	if got := closeCalls.Load(); got != 1 {
		t.Fatalf("CloseIdleConnections calls=%d, want retirement only", got)
	}
}

func TestManagedTransport_concurrent_round_trip_and_retire_closes_after_terminal(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	body := &managedTestBody{reads: []managedTestRead{{err: io.EOF}}}
	transport, closeCalls := newManagedCountingTransport(nil)
	transport.roundTripper = managedRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		close(entered)
		<-release
		return &http.Response{Body: body}, nil
	})
	result := make(chan *http.Response, 1)
	go func() {
		response, err := transport.RoundTrip(&http.Request{})
		if err != nil {
			result <- nil
			return
		}
		result <- response
	}()
	<-entered

	transport.retire()
	close(release)
	response := <-result
	if response == nil {
		t.Fatal("RoundTrip returned an unexpected error")
	}
	readManagedBody(t, response.Body)

	if got := closeCalls.Load(); got != 2 {
		t.Fatalf("CloseIdleConnections calls=%d, want retirement plus terminal close", got)
	}
}

func newManagedTestTransport(roundTrip func(*http.Request) (*http.Response, error)) *managedTransport {
	return &managedTransport{roundTripper: managedRoundTripperFunc(roundTrip), closeIdle: func() {}}
}

func newManagedCountingTransport(body io.ReadCloser) (*managedTransport, *atomic.Int64) {
	var closeCalls atomic.Int64
	return &managedTransport{
		roundTripper: managedRoundTripperFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{Body: body}, nil
		}),
		closeIdle: func() { closeCalls.Add(1) },
	}, &closeCalls
}

func readManagedBody(t *testing.T, body io.ReadCloser) {
	t.Helper()
	_, _ = body.Read(make([]byte, 8))
}

func closeManagedBody(t *testing.T, body io.ReadCloser) {
	t.Helper()
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedTransport_preserves_body_read_and_close_errors(t *testing.T) {
	wantCloseErr := errors.New("close error")
	body := &managedTestBody{
		reads:    []managedTestRead{{data: "payload", err: errManagedTransportTest}},
		closeErr: wantCloseErr,
	}
	transport, _ := newManagedCountingTransport(body)
	response, err := transport.RoundTrip(&http.Request{})
	if err != nil {
		t.Fatal(err)
	}
	transport.retire()
	buffer := make([]byte, 16)

	n, readErr := response.Body.Read(buffer)
	closeErr := response.Body.Close()
	duplicateCloseErr := response.Body.Close()

	if n != len("payload") || string(buffer[:n]) != "payload" || readErr != errManagedTransportTest {
		t.Fatalf("read result changed: n=%d data=%q error=%v", n, buffer[:n], readErr)
	}
	if closeErr != wantCloseErr || duplicateCloseErr != wantCloseErr {
		t.Fatalf("close errors changed: first=%v duplicate=%v", closeErr, duplicateCloseErr)
	}
}
