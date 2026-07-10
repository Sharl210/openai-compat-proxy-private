//go:build linux

package perfbench

import (
	"fmt"
	"os"
)

func readProcessMemory() (processMemory, bool, error) {
	status, err := os.Open("/proc/self/status")
	if err != nil {
		return processMemory{}, true, fmt.Errorf("open process status: %w", err)
	}
	defer status.Close()
	memory, err := parseProcessStatus(status)
	if err != nil {
		return processMemory{}, true, err
	}
	return memory, true, nil
}
