package perfbench

import "time"

const (
	capacityReportSchemaVersion = "capacity.v1"
	capacityTransportScope      = "httptest_loopback_fake_upstream"
)

type capacityReportCohort struct {
	WorkloadCount          int    `json:"workload_count"`
	RepetitionsPerWorkload int    `json:"repetitions_per_workload"`
	ScenarioCatalogVersion string `json:"scenario_catalog_version"`
	ScenarioCatalogDigest  string `json:"scenario_catalog_digest"`
}

type capacityReportIdentity struct {
	SchemaVersion    string                       `json:"schema_version"`
	Cohort           capacityReportCohort         `json:"cohort"`
	TransportScope   string                       `json:"transport_scope"`
	MeasurementBasis string                       `json:"measurement_basis"`
	Environment      performanceReportEnvironment `json:"environment"`
}

type capacityReportFailure struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

type capacityHeapSnapshot struct {
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	Attribution string `json:"attribution"`
	HeapAlloc   uint64 `json:"heap_alloc,omitempty"`
	HeapInuse   uint64 `json:"heap_inuse,omitempty"`
}

type capacityProcessMemory struct {
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	Attribution string `json:"attribution"`
	VmRSS       uint64 `json:"vm_rss,omitempty"`
	RssAnon     uint64 `json:"rss_anon,omitempty"`
}

type capacityCPUTime struct {
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	Attribution string `json:"attribution"`
	UserNS      int64  `json:"user_ns,omitempty"`
	SystemNS    int64  `json:"system_ns,omitempty"`
	TotalNS     int64  `json:"total_ns,omitempty"`
}

type capacityChildResources struct {
	PID               int                   `json:"pid"`
	AttributionStatus string                `json:"attribution_status"`
	Heap              capacityHeapSnapshot  `json:"heap"`
	AtFullGateRSS     capacityProcessMemory `json:"at_full_gate_rss"`
	CPUDelta          capacityCPUTime       `json:"cpu_delta"`
}

type capacityMeasurement struct {
	RequestedRequests    int                    `json:"requested_requests"`
	PeakInFlight         int                    `json:"peak_in_flight"`
	GateValid            bool                   `json:"gate_valid"`
	TTFB                 []time.Duration        `json:"request_ttfb_ns,omitempty"`
	TotalLatency         []time.Duration        `json:"request_total_latency_ns,omitempty"`
	SuccessfulRequests   int                    `json:"successful_requests"`
	SuccessfulThroughput float64                `json:"successful_throughput_per_second"`
	ErrorCount           int                    `json:"error_count"`
	RequestErrorRate     float64                `json:"request_error_rate"`
	Elapsed              time.Duration          `json:"elapsed_ns"`
	Child                capacityChildResources `json:"child"`
	Failure              *capacityReportFailure `json:"-"`
}

type capacityReportSample struct {
	capacityReportIdentity
	Workload    capacityWorkload       `json:"workload"`
	Repetition  int                    `json:"repetition"`
	Measurement capacityMeasurement    `json:"measurement"`
	Failure     *capacityReportFailure `json:"failure,omitempty"`
}

type capacityReportDistribution = performanceReportDistribution

type capacityReportFloatDistribution struct {
	Count int     `json:"count"`
	Min   float64 `json:"min"`
	P50   float64 `json:"p50"`
	P95   float64 `json:"p95"`
	Max   float64 `json:"max"`
}

type capacityUnavailableMetric struct {
	Metric string `json:"metric"`
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

type capacityTierSummary struct {
	Workload                      capacityWorkload                 `json:"workload"`
	RepetitionCount               int                              `json:"repetition_count"`
	ValidSampleCount              int                              `json:"valid_sample_count"`
	FailureCount                  int                              `json:"failure_count"`
	RequestTTFB                   *capacityReportDistribution      `json:"request_ttfb_ns,omitempty"`
	RequestTotalLatency           *capacityReportDistribution      `json:"request_total_latency_ns,omitempty"`
	SuccessfulThroughput          *capacityReportFloatDistribution `json:"successful_throughput_per_second,omitempty"`
	ErrorCount                    *capacityReportDistribution      `json:"error_count,omitempty"`
	AllRepetitionRequestErrorRate *capacityReportFloatDistribution `json:"all_repetition_request_error_rate,omitempty"`
	ChildHeapAlloc                *capacityReportDistribution      `json:"child_heap_alloc,omitempty"`
	ChildHeapInuse                *capacityReportDistribution      `json:"child_heap_inuse,omitempty"`
	ChildAtFullGateVmRSS          *capacityReportDistribution      `json:"child_at_full_gate_vm_rss,omitempty"`
	ChildAtFullGateRssAnon        *capacityReportDistribution      `json:"child_at_full_gate_rss_anon,omitempty"`
	ChildCPUUser                  *capacityReportDistribution      `json:"child_cpu_user_ns,omitempty"`
	ChildCPUSystem                *capacityReportDistribution      `json:"child_cpu_system_ns,omitempty"`
	ChildCPUTotal                 *capacityReportDistribution      `json:"child_cpu_total_ns,omitempty"`
	Unavailable                   []capacityUnavailableMetric      `json:"unavailable,omitempty"`
}

type capacityReportSummary struct {
	capacityReportIdentity
	SampleCount  int                   `json:"sample_count"`
	FailureCount int                   `json:"failure_count"`
	Tiers        []capacityTierSummary `json:"tiers"`
}

type capacityReportResult struct {
	RawPath      string                `json:"raw_path"`
	SummaryPath  string                `json:"summary_path"`
	TextPath     string                `json:"text_path"`
	Summary      capacityReportSummary `json:"summary"`
	HumanSummary string                `json:"human_summary"`
}
