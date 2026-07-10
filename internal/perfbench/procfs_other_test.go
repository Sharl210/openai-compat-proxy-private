//go:build !linux

package perfbench

func readProcessMemory() (processMemory, bool, error) {
	return processMemory{}, false, errProcessMemoryUnsupported
}
