//go:build linux

package diagnostics

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

type heapCaptureFunc func() (HeapProfileCapture, error)

func StartHeapCaptureSignalHandler(logf heapCaptureLogFunc) func() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGUSR1)
	done := make(chan struct{})

	go func() {
		defer signal.Stop(signals)
		runHeapCaptureSignalLoop(signals, done, CaptureHeapProfile, logf)
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
		})
	}
}

func runHeapCaptureSignalLoop(signals <-chan os.Signal, done <-chan struct{}, capture heapCaptureFunc, logf heapCaptureLogFunc) {
	for {
		select {
		case <-done:
			return
		case received, ok := <-signals:
			if !ok {
				return
			}
			if received != syscall.SIGUSR1 {
				continue
			}
			select {
			case <-done:
				return
			default:
			}

			capture, err := capture()
			if err != nil {
				if logf != nil {
					logf("heap profile capture failed: %v", err)
				}
				continue
			}
			if logf != nil {
				logf("heap profile captured: path=%s size=%d", capture.Path, capture.Size)
			}
		}
	}
}
