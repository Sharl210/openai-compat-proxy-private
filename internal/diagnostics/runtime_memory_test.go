package diagnostics

import "testing"

func TestSnapshot_reports_runtime_memory_metrics(t *testing.T) {
	// Given
	reader := func(string) ([]byte, error) {
		return []byte("VmRSS:\t  1146880 kB\nRssAnon:\t  1140000 kB\n"), nil
	}

	// When
	snapshot := snapshotWithStatusReader(reader)

	// Then
	if snapshot.HeapAlloc == 0 || snapshot.HeapInuse == 0 || snapshot.Sys == 0 {
		t.Fatalf("expected runtime memory metrics, got %#v", snapshot)
	}
	if snapshot.Goroutines == 0 {
		t.Fatalf("expected goroutine count, got %#v", snapshot)
	}
	if snapshot.VmRSS != 1146880*1024 || snapshot.RssAnon != 1140000*1024 {
		t.Fatalf("expected Linux RSS metrics in bytes, got %#v", snapshot)
	}
}

func TestParseLinuxStatus_extracts_memory_metrics(t *testing.T) {
	// Given
	status := []byte("Name:\tproxy\nVmRSS:\t  42 kB\nRssAnon:\t17 kB\nThreads:\t3\n")

	// When
	vmRSS, rssAnon := parseLinuxStatus(status)

	// Then
	if vmRSS != 42*1024 || rssAnon != 17*1024 {
		t.Fatalf("expected byte values, got VmRSS=%d RssAnon=%d", vmRSS, rssAnon)
	}
}
