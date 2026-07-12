package perfbench

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"
)

const performanceReportSchemaVersion = "perfbench.v1"

type performanceReportEnvironment struct {
	GoVersion  string `json:"go_version"`
	GOOS       string `json:"os"`
	GOARCH     string `json:"arch"`
	GOMAXPROCS int    `json:"gomaxprocs"`
}

type performanceReportCohort struct {
	ScenarioCatalogVersion string `json:"scenario_catalog_version"`
	ScenarioCatalogDigest  string `json:"scenario_catalog_digest"`
}

type performanceReportIdentity struct {
	SchemaVersion  string                       `json:"schema_version"`
	Cohort         performanceReportCohort      `json:"cohort"`
	TransportScope string                       `json:"transport_scope"`
	Environment    performanceReportEnvironment `json:"environment"`
}

type performanceReportFailure struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

type performanceReportSample struct {
	performanceReportIdentity
	Scenario        scenario                  `json:"scenario"`
	MeasurementMode measurementMode           `json:"measurement_mode"`
	Iteration       int                       `json:"iteration"`
	Metrics         workerMetrics             `json:"-"`
	Failure         *performanceReportFailure `json:"failure,omitempty"`
}

type performanceReportRawSample struct {
	performanceReportIdentity
	Scenario        scenario                  `json:"scenario"`
	MeasurementMode measurementMode           `json:"measurement_mode"`
	Iteration       int                       `json:"iteration"`
	Metrics         performanceReportMetrics  `json:"metrics"`
	Failure         *performanceReportFailure `json:"failure,omitempty"`
}

type performanceReportProcessMemory struct {
	VmRSS   uint64 `json:"vm_rss"`
	RssAnon uint64 `json:"rss_anon"`
}

type performanceReportSampledPeak struct {
	WorkerHeap             sampledHeapPeak                `json:"worker_heap"`
	ParentProcess          performanceReportProcessMemory `json:"parent_process"`
	ParentProcessSupported bool                           `json:"parent_process_supported"`
	ParentSampleInterval   time.Duration                  `json:"parent_sample_interval_ns"`
	ParentSampleCount      uint64                         `json:"parent_sample_count"`
}

type performanceReportMetrics struct {
	Mode                       measurementMode               `json:"mode"`
	Latency                    *latencyMetrics               `json:"latency,omitempty"`
	AllocationRetained         *allocationRetainedMetrics    `json:"allocation_retained,omitempty"`
	SampledPeakDuringOperation *performanceReportSampledPeak `json:"sampled_peak_during_operation,omitempty"`
	ObservedRequestBytes       int64                         `json:"observed_request_bytes"`
	ResponseBytes              int64                         `json:"response_bytes"`
	ObservedRequestBodySHA256  string                        `json:"observed_request_body_sha256"`
	ResponseBodySHA256         string                        `json:"response_body_sha256"`
	Connections                connectionMetrics             `json:"connections"`
}

type performanceReportDistribution struct {
	Count int   `json:"count"`
	Min   int64 `json:"min"`
	P50   int64 `json:"p50"`
	P95   int64 `json:"p95"`
	Max   int64 `json:"max"`
}

type performanceReportMetricSummary struct {
	TTFB                 *performanceReportDistribution `json:"ttfb_ns,omitempty"`
	TotalDuration        *performanceReportDistribution `json:"total_duration_ns,omitempty"`
	AllocationBytes      *performanceReportDistribution `json:"allocation_bytes,omitempty"`
	RetainedHeapAlloc    *performanceReportDistribution `json:"retained_heap_alloc,omitempty"`
	RetainedHeapInuse    *performanceReportDistribution `json:"retained_heap_inuse,omitempty"`
	SampledWorkerHeap    *performanceReportDistribution `json:"sampled_worker_heap_alloc,omitempty"`
	SampledWorkerInuse   *performanceReportDistribution `json:"sampled_worker_heap_inuse,omitempty"`
	ParentVmRSS          *performanceReportDistribution `json:"parent_vm_rss,omitempty"`
	ParentRssAnon        *performanceReportDistribution `json:"parent_rss_anon,omitempty"`
	ObservedRequestBytes *performanceReportDistribution `json:"observed_request_bytes,omitempty"`
	ResponseBytes        *performanceReportDistribution `json:"response_bytes,omitempty"`
	ConnectionNew        *performanceReportDistribution `json:"connection_new,omitempty"`
	ConnectionActive     *performanceReportDistribution `json:"connection_active,omitempty"`
	ConnectionIdle       *performanceReportDistribution `json:"connection_idle,omitempty"`
	ConnectionClosed     *performanceReportDistribution `json:"connection_closed,omitempty"`
}

type performanceReportSummary struct {
	performanceReportIdentity
	SampleCount    int                            `json:"sample_count"`
	FailureCount   int                            `json:"failure_count"`
	Metrics        performanceReportMetricSummary `json:"metrics"`
	RequestHashes  []string                       `json:"request_hashes"`
	ResponseHashes []string                       `json:"response_hashes"`
}

func currentPerformanceReportIdentity() performanceReportIdentity {
	version, digest := scenarioCatalogCanonicalDigest(scenarioCatalog())
	return performanceReportIdentity{
		SchemaVersion:  performanceReportSchemaVersion,
		Cohort:         performanceReportCohort{ScenarioCatalogVersion: version, ScenarioCatalogDigest: digest},
		TransportScope: "httptest_loopback_fake_upstream",
		Environment:    performanceReportEnvironment{GoVersion: runtime.Version(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GOMAXPROCS: runtime.GOMAXPROCS(0)},
	}
}

func writePerformanceReportJSONL(path string, samples []performanceReportSample) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("performance report path must be absolute: %q", path)
	}
	var payload bytes.Buffer
	encoder := json.NewEncoder(&payload)
	for _, sample := range samples {
		if err := encoder.Encode(sample.raw()); err != nil {
			return fmt.Errorf("encode performance report sample: %w", err)
		}
	}
	if err := os.WriteFile(path, payload.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write performance report JSONL: %w", err)
	}
	return nil
}

func (sample performanceReportSample) raw() performanceReportRawSample {
	metrics := performanceReportMetrics{
		Mode: sample.Metrics.Mode, Latency: sample.Metrics.Latency, AllocationRetained: sample.Metrics.AllocationRetained,
		ObservedRequestBytes: sample.Metrics.ObservedRequestBytes, ResponseBytes: sample.Metrics.ResponseBytes,
		ObservedRequestBodySHA256: sample.Metrics.ObservedRequestBodySHA256, ResponseBodySHA256: sample.Metrics.ResponseBodySHA256,
		Connections: sample.Metrics.Connections,
	}
	if peak := sample.Metrics.SampledPeakDuringOperation; peak != nil {
		metrics.SampledPeakDuringOperation = &performanceReportSampledPeak{
			WorkerHeap: peak.WorkerHeap, ParentProcess: performanceReportProcessMemory{VmRSS: peak.ParentProcess.VmRSS, RssAnon: peak.ParentProcess.RssAnon},
			ParentProcessSupported: peak.ParentProcessSupported, ParentSampleInterval: peak.ParentSampleInterval, ParentSampleCount: peak.ParentSampleCount,
		}
	}
	return performanceReportRawSample{performanceReportIdentity: sample.performanceReportIdentity, Scenario: sample.Scenario, MeasurementMode: sample.MeasurementMode, Iteration: sample.Iteration, Metrics: metrics, Failure: sample.Failure}
}

func summarizePerformanceReport(samples []performanceReportSample) performanceReportSummary {
	summary := performanceReportSummary{}
	if len(samples) == 0 {
		return summary
	}
	summary.performanceReportIdentity = samples[0].performanceReportIdentity
	summary.SampleCount = len(samples)
	values := performanceReportSummaryValues{}
	requestHashes := make(map[string]struct{})
	responseHashes := make(map[string]struct{})
	for _, sample := range samples {
		if sample.Failure != nil {
			summary.FailureCount++
			continue
		}
		values.add(sample)
		if sample.Metrics.ObservedRequestBodySHA256 != "" {
			requestHashes[sample.Metrics.ObservedRequestBodySHA256] = struct{}{}
		}
		if sample.Metrics.ResponseBodySHA256 != "" {
			responseHashes[sample.Metrics.ResponseBodySHA256] = struct{}{}
		}
	}
	summary.Metrics = values.summary()
	summary.RequestHashes = sortedKeys(requestHashes)
	summary.ResponseHashes = sortedKeys(responseHashes)
	return summary
}

type performanceReportSummaryValues struct {
	ttfb, total, allocation, retainedAlloc, retainedInuse, workerHeap, workerInuse, parentVmRSS, parentRssAnon, requestBytes, responseBytes, connectionNew, connectionActive, connectionIdle, connectionClosed []int64
}

func (values *performanceReportSummaryValues) add(sample performanceReportSample) {
	metrics := sample.Metrics
	if latency := metrics.Latency; latency != nil {
		values.ttfb = append(values.ttfb, latency.TTFB.Nanoseconds())
		values.total = append(values.total, latency.TotalDuration.Nanoseconds())
	}
	if allocation := metrics.AllocationRetained; allocation != nil {
		values.allocation = append(values.allocation, int64(allocation.OperationAllocationDelta.TotalAlloc))
		values.retainedAlloc = append(values.retainedAlloc, int64(allocation.PostOperationAfterGC.HeapAlloc))
		values.retainedInuse = append(values.retainedInuse, int64(allocation.PostOperationAfterGC.HeapInuse))
	}
	if peak := metrics.SampledPeakDuringOperation; peak != nil {
		values.workerHeap = append(values.workerHeap, int64(peak.WorkerHeap.HeapAlloc))
		values.workerInuse = append(values.workerInuse, int64(peak.WorkerHeap.HeapInuse))
		if peak.ParentProcessSupported {
			values.parentVmRSS = append(values.parentVmRSS, int64(peak.ParentProcess.VmRSS))
			values.parentRssAnon = append(values.parentRssAnon, int64(peak.ParentProcess.RssAnon))
		}
	}
	if metrics.ObservedRequestBytes != 0 {
		values.requestBytes = append(values.requestBytes, metrics.ObservedRequestBytes)
	}
	if metrics.ResponseBytes != 0 {
		values.responseBytes = append(values.responseBytes, metrics.ResponseBytes)
	}
	if metrics.Connections.New != 0 {
		values.connectionNew = append(values.connectionNew, metrics.Connections.New)
	}
	if metrics.Connections.Active != 0 {
		values.connectionActive = append(values.connectionActive, metrics.Connections.Active)
	}
	if metrics.Connections.Idle != 0 {
		values.connectionIdle = append(values.connectionIdle, metrics.Connections.Idle)
	}
	if metrics.Connections.Closed != 0 {
		values.connectionClosed = append(values.connectionClosed, metrics.Connections.Closed)
	}
}

func (values performanceReportSummaryValues) summary() performanceReportMetricSummary {
	return performanceReportMetricSummary{TTFB: distribution(values.ttfb), TotalDuration: distribution(values.total), AllocationBytes: distribution(values.allocation), RetainedHeapAlloc: distribution(values.retainedAlloc), RetainedHeapInuse: distribution(values.retainedInuse), SampledWorkerHeap: distribution(values.workerHeap), SampledWorkerInuse: distribution(values.workerInuse), ParentVmRSS: distribution(values.parentVmRSS), ParentRssAnon: distribution(values.parentRssAnon), ObservedRequestBytes: distribution(values.requestBytes), ResponseBytes: distribution(values.responseBytes), ConnectionNew: distribution(values.connectionNew), ConnectionActive: distribution(values.connectionActive), ConnectionIdle: distribution(values.connectionIdle), ConnectionClosed: distribution(values.connectionClosed)}
}

func distribution(values []int64) *performanceReportDistribution {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left] < sorted[right] })
	return &performanceReportDistribution{Count: len(sorted), Min: sorted[0], P50: percentile(sorted, 50), P95: percentile(sorted, 95), Max: sorted[len(sorted)-1]}
}

func percentile(sorted []int64, percentile int) int64 {
	return sorted[(len(sorted)*percentile+99)/100-1]
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	sort.Strings(keys)
	return keys
}

func validatePerformanceReportComparison(baseline, candidate performanceReportSummary) error {
	if baseline.SchemaVersion != candidate.SchemaVersion {
		return errors.New("performance report schema versions differ")
	}
	if baseline.Cohort != candidate.Cohort {
		return errors.New("performance report cohorts differ")
	}
	if baseline.Environment != candidate.Environment {
		return errors.New("performance report environments differ")
	}
	if baseline.TransportScope != candidate.TransportScope {
		return errors.New("performance report transport scopes differ")
	}
	return nil
}
