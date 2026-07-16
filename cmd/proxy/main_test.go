package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestServeHTTPClosesHandlerOnContextCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closed := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- serveHTTP(ctx, server, listener, func() {
			close(closed)
		})
	}()

	request, err := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String(), nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_ = response.Body.Close()

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("serveHTTP returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveHTTP did not stop after context cancellation")
	}

	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("close callback was not called")
	}
}

func TestServeHTTPForceClosesActiveRequestsAfterShutdownTimeout(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	requestStarted := make(chan struct{})
	handlerDone := make(chan struct{})
	server := &http.Server{
		Handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			close(requestStarted)
			<-r.Context().Done()
			close(handlerDone)
		}),
	}
	requestDone := make(chan error, 1)
	defer func() {
		_ = server.Close()
		select {
		case <-requestDone:
		case <-time.After(time.Second):
			t.Log("client request did not finish during test cleanup")
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- serveHTTPWithShutdownTimeout(ctx, server, listener, nil, 10*time.Millisecond)
	}()

	go func() {
		request, err := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String(), nil)
		if err != nil {
			requestDone <- err
			return
		}
		response, err := http.DefaultClient.Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		requestDone <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for active request")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected shutdown deadline error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("serveHTTP did not return after shutdown timeout")
	}

	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("active request was not force-closed after shutdown timeout")
	}
}
