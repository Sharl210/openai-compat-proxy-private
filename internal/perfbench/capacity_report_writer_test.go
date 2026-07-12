package perfbench

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func formatCapacityReportSummary(summary capacityReportSummary) string {
	lines := []string{
		"capacity.v1 local measurement basis: httptest_loopback_fake_upstream; not a production capacity guarantee",
		fmt.Sprintf("samples=%d failures=%d", summary.SampleCount, summary.FailureCount),
		"scenario | traffic | concurrency | repetitions | valid | failures | ttfb_p50_ns | ttfb_p95_ns | total_p50_ns | throughput_p50 | all_repetition_request_error_rate_p50 | child_at_full_gate_rss_p50 | child_heap_p50 | child_cpu_p50_ns",
	}
	for _, tier := range summary.Tiers {
		lines = append(lines, strings.Join([]string{
			tier.Workload.Scenario.ID,
			string(tier.Workload.Traffic),
			fmt.Sprintf("%d", tier.Workload.Concurrency),
			fmt.Sprintf("%d", tier.RepetitionCount),
			fmt.Sprintf("%d", tier.ValidSampleCount),
			fmt.Sprintf("%d", tier.FailureCount),
			formatCapacityDistributionP50(tier.RequestTTFB),
			formatCapacityDistributionP95(tier.RequestTTFB),
			formatCapacityDistributionP50(tier.RequestTotalLatency),
			formatCapacityFloatP50(tier.SuccessfulThroughput),
			formatCapacityFloatP50(tier.AllRepetitionRequestErrorRate),
			formatCapacityDistributionP50(tier.ChildAtFullGateVmRSS),
			formatCapacityDistributionP50(tier.ChildHeapAlloc),
			formatCapacityDistributionP50(tier.ChildCPUTotal),
		}, " | "))
	}
	return strings.Join(lines, "\n") + "\n"
}

func formatCapacityDistributionP50(value *capacityReportDistribution) string {
	if value == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%d", value.P50)
}

func formatCapacityDistributionP95(value *capacityReportDistribution) string {
	if value == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%d", value.P95)
}

func formatCapacityFloatP50(value *capacityReportFloatDistribution) string {
	if value == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%.6f", value.P50)
}

func TestCapacityReportSetWrite_leavesExistingArtifactsIntactWhenStagingFails(t *testing.T) {
	// Given
	directory := t.TempDir()
	result := capacityReportResult{
		RawPath:      filepath.Join(directory, "capacity.v1.jsonl"),
		SummaryPath:  filepath.Join(directory, "capacity.v1.summary.json"),
		TextPath:     filepath.Join(directory, "capacity.v1.summary.txt"),
		HumanSummary: "new report\n",
	}
	old := map[string][]byte{
		result.RawPath:     []byte("old raw\n"),
		result.SummaryPath: []byte("old summary\n"),
		result.TextPath:    []byte("old text\n"),
	}
	for path, payload := range old {
		if err := os.WriteFile(path, payload, 0o640); err != nil {
			t.Fatalf("write existing report %s: %v", path, err)
		}
	}
	writes := 0
	operations := capacityReportFileOps{
		writeFile: func(path string, payload []byte, mode os.FileMode) error {
			writes++
			if writes == 2 {
				return errors.New("simulated staging write failure")
			}
			return os.WriteFile(path, payload, mode)
		},
	}

	// When
	err := writeCapacityReportSetWithFileOps(result, []capacityReportSample{{}}, operations)

	// Then
	if err == nil || !strings.Contains(err.Error(), "simulated staging write failure") {
		t.Fatalf("write capacity report set error = %v", err)
	}
	for path, want := range old {
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read existing report %s: %v", path, readErr)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("report %s changed after staging failure: got %q want %q", path, got, want)
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read report directory: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("staging failure left report directory entries: %+v", entries)
	}
}

func TestCapacityReportSetWrite_restoresExistingArtifactsWhenReplaceFails(t *testing.T) {
	// Given
	directory := t.TempDir()
	result := capacityReportResult{
		RawPath:      filepath.Join(directory, "capacity.v1.jsonl"),
		SummaryPath:  filepath.Join(directory, "capacity.v1.summary.json"),
		TextPath:     filepath.Join(directory, "capacity.v1.summary.txt"),
		HumanSummary: "new report\n",
	}
	old := map[string][]byte{
		result.RawPath:     []byte("old raw\n"),
		result.SummaryPath: []byte("old summary\n"),
		result.TextPath:    []byte("old text\n"),
	}
	for path, payload := range old {
		if err := os.WriteFile(path, payload, 0o640); err != nil {
			t.Fatalf("write existing report %s: %v", path, err)
		}
	}
	renames := 0
	operations := capacityReportFileOps{
		writeFile: os.WriteFile,
		renameFile: func(oldPath string, newPath string) error {
			renames++
			if renames == 4 {
				return errors.New("simulated replacement failure")
			}
			return os.Rename(oldPath, newPath)
		},
	}

	// When
	err := writeCapacityReportSetWithFileOps(result, []capacityReportSample{{}}, operations)

	// Then
	if err == nil || !strings.Contains(err.Error(), "simulated replacement failure") {
		t.Fatalf("write capacity report set error = %v", err)
	}
	for path, want := range old {
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read existing report %s: %v", path, readErr)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("report %s changed after replacement failure: got %q want %q", path, got, want)
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read report directory: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("replacement failure left report directory entries: %+v", entries)
	}
}

func TestCapacityReportSetWrite_preservesExistingArtifactPermissions(t *testing.T) {
	// Given
	directory := t.TempDir()
	result := capacityReportResult{
		RawPath:      filepath.Join(directory, "capacity.v1.jsonl"),
		SummaryPath:  filepath.Join(directory, "capacity.v1.summary.json"),
		TextPath:     filepath.Join(directory, "capacity.v1.summary.txt"),
		HumanSummary: "new report\n",
	}
	modes := map[string]os.FileMode{
		result.RawPath:     0o640,
		result.SummaryPath: 0o600,
		result.TextPath:    0o644,
	}
	for path, mode := range modes {
		if err := os.WriteFile(path, []byte("old\n"), mode); err != nil {
			t.Fatalf("write existing report %s: %v", path, err)
		}
	}

	// When
	err := writeCapacityReportSetWithFileOps(result, []capacityReportSample{{}}, capacityReportFileOps{writeFile: os.WriteFile})

	// Then
	if err != nil {
		t.Fatalf("write capacity report set: %v", err)
	}
	for path, want := range modes {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat report %s: %v", path, statErr)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("report %s permissions = %04o, want %04o", path, got, want)
		}
	}
}
