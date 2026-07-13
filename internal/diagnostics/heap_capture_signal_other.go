//go:build !linux

package diagnostics

func StartHeapCaptureSignalHandler(heapCaptureLogFunc) func() {
	return func() {}
}
