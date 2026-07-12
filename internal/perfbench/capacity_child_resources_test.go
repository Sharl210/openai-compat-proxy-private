package perfbench

import "fmt"

type capacityCPUCheckpoint struct {
	start  processCPUTime
	reason string
}

func beginCapacityChildResources(pid int) (capacityChildResources, capacityCPUCheckpoint) {
	resources := capacityChildResources{
		PID:               pid,
		AttributionStatus: "child_pid",
		Heap:              capacityHeapSnapshot{Status: "unavailable", Reason: "not_at_full_gate_snapshot", Attribution: "child_worker_ipc_frame"},
		AtFullGateRSS:     capacityProcessMemory{Status: "unavailable", Reason: "not_at_full_gate_snapshot", Attribution: "child_pid_procfs_at_full_gate_snapshot"},
		CPUDelta:          capacityCPUTime{Status: "unavailable", Attribution: "child_pid_delta"},
	}
	start, err := readProcessCPUTimePID(pid)
	if err != nil || !start.Supported {
		return resources, capacityCPUCheckpoint{reason: capacityResourceReason(err, start.Supported)}
	}
	return resources, capacityCPUCheckpoint{start: start}
}

func captureCapacityChildResourcesAtFullGate(resources *capacityChildResources, fixture *capacityProxyFixture) {
	snapshot, err := fixture.requestHeapSnapshot()
	if err != nil {
		resources.Heap = capacityHeapSnapshot{Status: "unavailable", Reason: fmt.Sprintf("worker_ipc: %v", err), Attribution: "child_worker_ipc_frame"}
	} else {
		resources.Heap = capacityHeapSnapshot{Status: "available", Attribution: "child_worker_ipc_frame", HeapAlloc: snapshot.HeapAlloc, HeapInuse: snapshot.HeapInuse}
	}
	memory, supported, err := readProcessMemoryPID(resources.PID)
	if err != nil || !supported {
		resources.AtFullGateRSS = capacityProcessMemory{Status: "unavailable", Reason: capacityResourceReason(err, supported), Attribution: "child_pid_procfs_at_full_gate_snapshot"}
		return
	}
	resources.AtFullGateRSS = capacityProcessMemory{Status: "available", Attribution: "child_pid_procfs_at_full_gate_snapshot", VmRSS: memory.VmRSS, RssAnon: memory.RssAnon}
}

func finishCapacityChildCPU(resources *capacityChildResources, checkpoint capacityCPUCheckpoint) {
	if checkpoint.reason != "" {
		resources.CPUDelta = capacityCPUTime{Status: "unavailable", Reason: checkpoint.reason, Attribution: "child_pid_delta"}
		return
	}
	end, err := readProcessCPUTimePID(resources.PID)
	if err != nil || !end.Supported {
		resources.CPUDelta = capacityCPUTime{Status: "unavailable", Reason: capacityResourceReason(err, end.Supported), Attribution: "child_pid_delta"}
		return
	}
	delta := processCPUTimeDelta(checkpoint.start, end)
	if !delta.Supported {
		resources.CPUDelta = capacityCPUTime{Status: "unavailable", Reason: "non_monotonic_cpu_samples", Attribution: "child_pid_delta"}
		return
	}
	resources.CPUDelta = capacityCPUTime{Status: "available", Attribution: "child_pid_delta", UserNS: delta.User.Nanoseconds(), SystemNS: delta.System.Nanoseconds(), TotalNS: delta.Total.Nanoseconds()}
}

func capacityResourceReason(err error, supported bool) string {
	if !supported {
		return "platform_unsupported"
	}
	if err != nil {
		return err.Error()
	}
	return "unavailable"
}
