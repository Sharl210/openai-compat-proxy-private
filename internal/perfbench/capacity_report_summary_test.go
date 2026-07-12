package perfbench

import (
	"sort"
)

func currentCapacityReportIdentity() capacityReportIdentity {
	version, digest := scenarioCatalogCanonicalDigest(scenarioCatalog())
	return capacityReportIdentity{
		SchemaVersion:    capacityReportSchemaVersion,
		Cohort:           capacityReportCohort{WorkloadCount: 14, RepetitionsPerWorkload: capacityRepetitions, ScenarioCatalogVersion: version, ScenarioCatalogDigest: digest},
		TransportScope:   capacityTransportScope,
		MeasurementBasis: "local loopback fake upstream measurement; not a production capacity guarantee",
		Environment:      currentPerformanceReportIdentity().Environment,
	}
}

func normalizeCapacityMeasurement(measurement capacityMeasurement) capacityMeasurement {
	measurement.GateValid = measurement.PeakInFlight == measurement.RequestedRequests && measurement.Failure == nil
	if measurement.RequestedRequests > 0 {
		measurement.RequestErrorRate = float64(measurement.ErrorCount) / float64(measurement.RequestedRequests)
	}
	if measurement.Elapsed > 0 {
		measurement.SuccessfulThroughput = float64(measurement.SuccessfulRequests) / measurement.Elapsed.Seconds()
	}
	sort.Slice(measurement.TTFB, func(left, right int) bool { return measurement.TTFB[left] < measurement.TTFB[right] })
	sort.Slice(measurement.TotalLatency, func(left, right int) bool { return measurement.TotalLatency[left] < measurement.TotalLatency[right] })
	return measurement
}

func summarizeCapacityReport(samples []capacityReportSample) capacityReportSummary {
	summary := capacityReportSummary{}
	if len(samples) == 0 {
		return summary
	}
	summary.capacityReportIdentity = samples[0].capacityReportIdentity
	summary.SampleCount = len(samples)
	groups := make(map[string]*capacityTierValues)
	for _, sample := range samples {
		key := sample.Workload.Name()
		values := groups[key]
		if values == nil {
			values = &capacityTierValues{workload: sample.Workload, unavailable: make(map[string]int)}
			groups[key] = values
		}
		values.add(sample)
		if sample.Failure != nil {
			summary.FailureCount++
		}
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	summary.Tiers = make([]capacityTierSummary, 0, len(keys))
	for _, key := range keys {
		summary.Tiers = append(summary.Tiers, groups[key].summary())
	}
	return summary
}

type capacityTierValues struct {
	workload                             capacityWorkload
	repetitions, valid, failures         int
	ttfb, total, errorCount              []int64
	throughput, errorRate                []float64
	heapAlloc, heapInuse, vmRSS, rssAnon []int64
	cpuUser, cpuSystem, cpuTotal         []int64
	unavailable                          map[string]int
}

func (values *capacityTierValues) add(sample capacityReportSample) {
	values.repetitions++
	measurement := sample.Measurement
	values.errorCount = append(values.errorCount, int64(measurement.ErrorCount))
	values.errorRate = append(values.errorRate, measurement.RequestErrorRate)
	if sample.Failure != nil || !measurement.GateValid {
		values.failures++
		return
	}
	values.valid++
	for _, duration := range measurement.TTFB {
		values.ttfb = append(values.ttfb, duration.Nanoseconds())
	}
	for _, duration := range measurement.TotalLatency {
		values.total = append(values.total, duration.Nanoseconds())
	}
	values.throughput = append(values.throughput, measurement.SuccessfulThroughput)
	values.addResources(measurement.Child)
}

func (values *capacityTierValues) addResources(resources capacityChildResources) {
	if resources.Heap.Status == "available" {
		values.heapAlloc = append(values.heapAlloc, int64(resources.Heap.HeapAlloc))
		values.heapInuse = append(values.heapInuse, int64(resources.Heap.HeapInuse))
	} else {
		values.unavailable["child_heap:"+resources.Heap.Reason]++
	}
	if resources.AtFullGateRSS.Status == "available" {
		values.vmRSS = append(values.vmRSS, int64(resources.AtFullGateRSS.VmRSS))
		values.rssAnon = append(values.rssAnon, int64(resources.AtFullGateRSS.RssAnon))
	} else {
		values.unavailable["child_at_full_gate_rss:"+resources.AtFullGateRSS.Reason]++
	}
	if resources.CPUDelta.Status == "available" {
		values.cpuUser = append(values.cpuUser, resources.CPUDelta.UserNS)
		values.cpuSystem = append(values.cpuSystem, resources.CPUDelta.SystemNS)
		values.cpuTotal = append(values.cpuTotal, resources.CPUDelta.TotalNS)
	} else {
		values.unavailable["child_cpu_delta:"+resources.CPUDelta.Reason]++
	}
}

func (values capacityTierValues) summary() capacityTierSummary {
	result := capacityTierSummary{
		Workload: values.workload, RepetitionCount: values.repetitions, ValidSampleCount: values.valid, FailureCount: values.failures,
		RequestTTFB: distribution(values.ttfb), RequestTotalLatency: distribution(values.total), SuccessfulThroughput: capacityFloatDistribution(values.throughput),
		ErrorCount: distribution(values.errorCount), AllRepetitionRequestErrorRate: capacityFloatDistribution(values.errorRate), ChildHeapAlloc: distribution(values.heapAlloc),
		ChildHeapInuse: distribution(values.heapInuse), ChildAtFullGateVmRSS: distribution(values.vmRSS), ChildAtFullGateRssAnon: distribution(values.rssAnon),
		ChildCPUUser: distribution(values.cpuUser), ChildCPUSystem: distribution(values.cpuSystem), ChildCPUTotal: distribution(values.cpuTotal),
	}
	keys := make([]string, 0, len(values.unavailable))
	for key := range values.unavailable {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		metric, reason, _ := splitCapacityUnavailableKey(key)
		result.Unavailable = append(result.Unavailable, capacityUnavailableMetric{Metric: metric, Reason: reason, Count: values.unavailable[key]})
	}
	return result
}

func splitCapacityUnavailableKey(value string) (string, string, bool) {
	for index := range value {
		if value[index] == ':' {
			return value[:index], value[index+1:], true
		}
	}
	return value, "", false
}

func capacityFloatDistribution(values []float64) *capacityReportFloatDistribution {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	return &capacityReportFloatDistribution{Count: len(sorted), Min: sorted[0], P50: sorted[(len(sorted)*50+99)/100-1], P95: sorted[(len(sorted)*95+99)/100-1], Max: sorted[len(sorted)-1]}
}
