//go:build linux

package diagnostics

import (
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestRunHeapCaptureSignalLoop_capturesUSR1AndStops(t *testing.T) {
	signals := make(chan os.Signal, 2)
	done := make(chan struct{})
	captured := make(chan struct{}, 1)
	var captureCount atomic.Int32

	go runHeapCaptureSignalLoop(signals, done, func() (HeapProfileCapture, error) {
		captureCount.Add(1)
		captured <- struct{}{}
		return HeapProfileCapture{Path: "/tmp/heap.pb.gz", Size: 1}, nil
	}, nil)

	signals <- syscall.SIGUSR1
	select {
	case <-captured:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for heap capture")
	}

	close(done)
	signals <- syscall.SIGUSR1
	time.Sleep(20 * time.Millisecond)
	if captureCount.Load() != 1 {
		t.Fatalf("expected signal loop to stop after one capture, got %d", captureCount.Load())
	}
}
