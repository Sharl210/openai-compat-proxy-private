//go:build linux

package perfbench

import (
	"fmt"
	"os"
)

func readProcessMemory() (processMemory, bool, error) {
	return readProcessMemoryPID(os.Getpid())
}

func readProcessMemoryPID(pid int) (processMemory, bool, error) {
	status, err := os.Open(processStatusPath(pid))
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

func processStatusPath(pid int) string {
	return fmt.Sprintf("/proc/%d/status", pid)
}
