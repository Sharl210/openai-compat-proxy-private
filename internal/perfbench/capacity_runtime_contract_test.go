package perfbench

import (
	"context"
	"testing"
	"time"
)

func TestCapacityWorkloadRunner_collects_valid_child_attributed_measurement_at_gate_peak(t *testing.T) {
	// Given
	item, err := capacityScenario("chat-chat-proxy_buffer-1mib-plain")
	if err != nil {
		t.Fatalf("load capacity scenario: %v", err)
	}
	workload := capacityWorkload{Scenario: item, Traffic: capacityTrafficManyUsers, Concurrency: 2, Repetitions: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// When
	measurement := runCapacityWorkload(ctx, workload, 1)

	// Then
	if measurement.Failure != nil || !measurement.GateValid || measurement.PeakInFlight != workload.Concurrency {
		t.Fatalf("gate measurement = %+v", measurement)
	}
	if measurement.SuccessfulRequests != workload.Concurrency || len(measurement.TTFB) != workload.Concurrency || len(measurement.TotalLatency) != workload.Concurrency {
		t.Fatalf("request measurement = %+v", measurement)
	}
	if measurement.Child.PID <= 0 || measurement.Child.AttributionStatus != "child_pid" {
		t.Fatalf("child attribution = %+v", measurement.Child)
	}
	if measurement.Child.Heap.Status != "available" || measurement.Child.Heap.Attribution != "child_worker_ipc_frame" {
		t.Fatalf("child heap = %+v", measurement.Child.Heap)
	}
	if measurement.Child.AtFullGateRSS.Attribution != "child_pid_procfs_at_full_gate_snapshot" || measurement.Child.CPUDelta.Attribution != "child_pid_delta" {
		t.Fatalf("child process resources = %+v", measurement.Child)
	}
}

func TestCapacityClients_models_many_users_and_one_user_burst_with_distinct_transport_ownership(t *testing.T) {
	// Given / When
	manyUsers, closeManyUsers, err := newCapacityClients(capacityTrafficManyUsers, 2)
	if err != nil {
		t.Fatalf("create many-users clients: %v", err)
	}
	defer closeManyUsers()
	oneUserBurst, closeOneUserBurst, err := newCapacityClients(capacityTrafficOneUserBurst, 2)
	if err != nil {
		t.Fatalf("create one-user-burst clients: %v", err)
	}
	defer closeOneUserBurst()

	// Then
	if manyUsers[0] == manyUsers[1] || manyUsers[0].Transport == manyUsers[1].Transport {
		t.Fatalf("many-users clients share ownership: %+v", manyUsers)
	}
	if oneUserBurst[0] != oneUserBurst[1] || oneUserBurst[0].Transport != oneUserBurst[1].Transport {
		t.Fatalf("one-user-burst clients do not share ownership: %+v", oneUserBurst)
	}
}
