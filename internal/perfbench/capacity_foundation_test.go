package perfbench

import (
	"context"
	"sort"
	"testing"
	"time"
)

func TestCapacityWorkloads_define_fixed_capacity_cohort(t *testing.T) {
	// Given
	want := []string{
		"chat-chat-proxy_buffer-1mib-plain/many_users/1",
		"chat-chat-proxy_buffer-1mib-plain/many_users/2",
		"chat-chat-proxy_buffer-1mib-plain/many_users/4",
		"chat-chat-proxy_buffer-1mib-plain/many_users/8",
		"chat-chat-proxy_buffer-1mib-plain/one_user_burst/1",
		"chat-chat-proxy_buffer-1mib-plain/one_user_burst/2",
		"chat-chat-proxy_buffer-1mib-plain/one_user_burst/4",
		"chat-chat-proxy_buffer-1mib-plain/one_user_burst/8",
		"responses-responses-stream-8mib-plain/many_users/1",
		"responses-responses-stream-8mib-plain/many_users/2",
		"responses-responses-stream-8mib-plain/many_users/4",
		"responses-responses-stream-8mib-plain/one_user_burst/1",
		"responses-responses-stream-8mib-plain/one_user_burst/2",
		"responses-responses-stream-8mib-plain/one_user_burst/4",
	}

	// When
	workloads, err := capacityWorkloads()

	// Then
	if err != nil {
		t.Fatalf("build capacity workloads: %v", err)
	}
	got := make([]string, 0, len(workloads))
	for _, workload := range workloads {
		if workload.Repetitions != 5 {
			t.Fatalf("%s repetitions = %d, want 5", workload.Name(), workload.Repetitions)
		}
		got = append(got, workload.Name())
	}
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("capacity workload count = %d, want %d: %v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("capacity workload[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}

func TestCapacityUpstreamGate_records_peak_before_release(t *testing.T) {
	// Given
	gate := newCapacityUpstreamGate(2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	completed := make(chan error, 2)

	for range 2 {
		go func() {
			completed <- gate.wait(ctx)
		}()
	}

	// When
	if err := gate.waitForPeak(ctx); err != nil {
		t.Fatalf("wait for capacity peak: %v", err)
	}
	gate.release()

	// Then
	for range 2 {
		if err := <-completed; err != nil {
			t.Fatalf("release gated request: %v", err)
		}
	}
	if peak := gate.peakInFlight(); peak != 2 {
		t.Fatalf("peak in-flight = %d, want 2", peak)
	}
}

func TestCapacityProxyFixture_disables_logging_and_archives(t *testing.T) {
	// Given
	item, err := capacityScenario("chat-chat-proxy_buffer-1mib-plain")
	if err != nil {
		t.Fatalf("load capacity scenario: %v", err)
	}

	// When
	fixture, err := newCapacityProxyFixture(item, nil)
	if err != nil {
		t.Fatalf("create capacity proxy fixture: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := fixture.close(); closeErr != nil {
			t.Errorf("close capacity proxy fixture: %v", closeErr)
		}
	})

	// Then
	if fixture.config.LogEnable {
		t.Fatal("capacity fixture enabled logging")
	}
	if fixture.config.DebugArchiveRootDir != "" {
		t.Fatalf("capacity fixture archive directory = %q, want empty", fixture.config.DebugArchiveRootDir)
	}
}

func TestCapacityProxyFixture_reads_child_owned_heap_snapshot_over_worker_IPC(t *testing.T) {
	// Given
	item, err := capacityScenario("chat-chat-proxy_buffer-1mib-plain")
	if err != nil {
		t.Fatalf("load capacity scenario: %v", err)
	}
	fixture, err := newCapacityProxyFixture(item, nil)
	if err != nil {
		t.Fatalf("create capacity proxy fixture: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := fixture.close(); closeErr != nil {
			t.Errorf("close capacity proxy fixture: %v", closeErr)
		}
	})

	// When
	snapshot, err := fixture.requestHeapSnapshot()

	// Then
	if err != nil {
		t.Fatalf("request child heap snapshot: %v", err)
	}
	if snapshot.HeapAlloc == 0 || snapshot.HeapInuse == 0 {
		t.Fatalf("child heap snapshot = %+v", snapshot)
	}
	if fixture.proxyHeapUnavailable {
		t.Fatal("capacity fixture did not record the valid child heap snapshot")
	}
}

func TestCapacityProxyFixture_records_concurrent_upstream_peak_through_one_proxy(t *testing.T) {
	// Given
	item, err := capacityScenario("chat-chat-proxy_buffer-1mib-plain")
	if err != nil {
		t.Fatalf("load capacity scenario: %v", err)
	}
	gate := newCapacityUpstreamGate(2)
	defer gate.release()
	fixture, err := newCapacityProxyFixture(item, gate)
	if err != nil {
		t.Fatalf("create capacity proxy fixture: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := fixture.close(); closeErr != nil {
			t.Errorf("close capacity proxy fixture: %v", closeErr)
		}
	})
	if fixture.proxyURL == "" || fixture.proxyPID <= 0 {
		t.Fatalf("capacity proxy child identity = url %q pid %d", fixture.proxyURL, fixture.proxyPID)
	}
	if !fixture.proxyHeapUnavailable {
		t.Fatal("capacity fixture exposed parent-process Go heap as proxy heap")
	}
	body, err := semanticScenarioRequestBody(item)
	if err != nil {
		t.Fatalf("build capacity request: %v", err)
	}
	completed := make(chan error, 2)

	for range 2 {
		go func() {
			_, requestErr := performProxyRuntimeRoundTripURL(fixture.proxyClient, fixture.proxyURL, item, body)
			completed <- requestErr
		}()
	}

	// When
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := gate.waitForPeak(ctx); err != nil {
		t.Fatalf("wait for concurrent upstream peak: %v", err)
	}
	gate.release()

	// Then
	for range 2 {
		if err := <-completed; err != nil {
			t.Fatalf("perform concurrent capacity request: %v", err)
		}
	}
	if captures := fixture.fake.capturedRequests(); len(captures) != 2 {
		t.Fatalf("upstream captures = %d, want 2", len(captures))
	}
	if peak := gate.peakInFlight(); peak != 2 {
		t.Fatalf("fixture peak in-flight = %d, want 2", peak)
	}
	if err := fixture.close(); err != nil {
		t.Fatalf("close capacity proxy fixture: %v", err)
	}
	if !fixture.proxyExited {
		t.Fatalf("capacity proxy child %d was not reaped", fixture.proxyPID)
	}
}
