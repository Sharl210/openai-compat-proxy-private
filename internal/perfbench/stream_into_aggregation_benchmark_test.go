package perfbench

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"openai-compat-proxy/internal/aggregate"
	"openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/upstream"
)

const (
	proxyBufferAggregationEventCount = 128
	proxyBufferAggregationChunkBytes = 8 << 10
)

type proxyBufferAggregationFixture struct {
	client  *upstream.Client
	request model.CanonicalRequest
}

func TestProxyBufferAggregation_preserves_result_when_streaming_incrementally(t *testing.T) {
	// Given
	fixture := newProxyBufferAggregationFixture(t)

	// When
	legacy, err := fixture.collectLegacy()
	if err != nil {
		t.Fatalf("collect legacy: %v", err)
	}
	incremental, err := fixture.collectIncrementally()
	if err != nil {
		t.Fatalf("collect incrementally: %v", err)
	}

	// Then
	if !reflect.DeepEqual(legacy, incremental) {
		t.Fatalf("proxy-buffer result differs: legacy=%+v incremental=%+v", legacy, incremental)
	}
}

func TestProxyBufferAggregation_retains_less_heap_when_streaming_incrementally(t *testing.T) {
	// Given
	fixture := newProxyBufferAggregationFixture(t)

	// When
	legacyHeap, err := fixture.measureLegacyRetainedHeap()
	if err != nil {
		t.Fatalf("measure legacy retained heap: %v", err)
	}
	incrementalHeap, err := fixture.measureIncrementalRetainedHeap()
	if err != nil {
		t.Fatalf("measure incremental retained heap: %v", err)
	}

	// Then
	if legacyHeap <= incrementalHeap {
		t.Fatalf("expected legacy heap to exceed incremental heap: legacy=%d incremental=%d", legacyHeap, incrementalHeap)
	}
	difference := legacyHeap - incrementalHeap
	if difference < proxyBufferAggregationEventCount*proxyBufferAggregationChunkBytes/2 {
		t.Fatalf("expected retained-heap reduction of at least half the response payload, got %d bytes", difference)
	}
	t.Logf("retained heap: legacy=%d incremental=%d difference=%d", legacyHeap, incrementalHeap, difference)
}

func BenchmarkProxyBufferAggregation_collects_large_response(b *testing.B) {
	fixture := newProxyBufferAggregationFixture(b)
	cases := []struct {
		name    string
		collect func() (aggregate.Result, error)
	}{
		{name: "legacy_event_slice", collect: fixture.collectLegacy},
		{name: "stream_into", collect: fixture.collectIncrementally},
	}
	for _, benchmark := range cases {
		b.Run(benchmark.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				result, err := benchmark.collect()
				if err != nil {
					b.Fatal(err)
				}
				if len(result.Text) != proxyBufferAggregationEventCount*proxyBufferAggregationChunkBytes {
					b.Fatalf("unexpected aggregated text size %d", len(result.Text))
				}
			}
		})
	}
}

func newProxyBufferAggregationFixture(t testing.TB) proxyBufferAggregationFixture {
	t.Helper()
	payload := proxyBufferAggregationPayload(false)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		if _, err := writer.Write(payload); err != nil {
			t.Errorf("write upstream SSE payload: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return proxyBufferAggregationFixture{
		client:  upstream.NewClient(server.URL),
		request: model.CanonicalRequest{RequestID: "req-perf-stream-into", Model: "gpt-5"},
	}
}

func newProxyBufferAggregationComparisonFixtures(t testing.TB) (proxyBufferAggregationFixture, proxyBufferAggregationFixture) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		includeFinalItem := false
		switch request.URL.Path {
		case "/delta/responses":
		case "/final-item/responses":
			includeFinalItem = true
		default:
			http.NotFound(writer, request)
			return
		}
		payload := proxyBufferAggregationPayload(includeFinalItem)
		if _, err := writer.Write(payload); err != nil {
			t.Errorf("write upstream SSE payload: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	request := model.CanonicalRequest{RequestID: "req-perf-stream-into", Model: "gpt-5"}
	deltaOnly := proxyBufferAggregationFixture{client: upstream.NewClient(server.URL + "/delta"), request: request}
	withFinalItem := proxyBufferAggregationFixture{client: upstream.NewClient(server.URL + "/final-item"), request: request}
	return deltaOnly, withFinalItem
}

func (fixture proxyBufferAggregationFixture) collectLegacy() (aggregate.Result, error) {
	events, err := fixture.client.Stream(context.Background(), fixture.request, "")
	if err != nil {
		return aggregate.Result{}, err
	}
	collector := aggregate.NewCollector()
	for _, event := range events {
		collector.Accept(event)
	}
	return collector.Result()
}

func (fixture proxyBufferAggregationFixture) collectIncrementally() (aggregate.Result, error) {
	collector := aggregate.NewCollector()
	err := fixture.client.StreamInto(context.Background(), fixture.request, "", func(event upstream.Event) error {
		collector.Accept(event)
		return nil
	})
	if err != nil {
		return aggregate.Result{}, err
	}
	return collector.Result()
}

func (fixture proxyBufferAggregationFixture) measureLegacyRetainedHeap() (uint64, error) {
	runtime.GC()
	events, err := fixture.client.Stream(context.Background(), fixture.request, "")
	if err != nil {
		return 0, err
	}
	collector := aggregate.NewCollector()
	for _, event := range events {
		collector.Accept(event)
	}
	result, err := collector.Result()
	if err != nil {
		return 0, err
	}
	runtime.GC()
	runtime.KeepAlive(events)
	runtime.KeepAlive(collector)
	runtime.KeepAlive(result)
	var snapshot runtime.MemStats
	runtime.ReadMemStats(&snapshot)
	return snapshot.HeapAlloc, nil
}

func (fixture proxyBufferAggregationFixture) measureIncrementalRetainedHeap() (uint64, error) {
	runtime.GC()
	collector := aggregate.NewCollector()
	err := fixture.client.StreamInto(context.Background(), fixture.request, "", func(event upstream.Event) error {
		collector.Accept(event)
		return nil
	})
	if err != nil {
		return 0, err
	}
	result, err := collector.Result()
	if err != nil {
		return 0, err
	}
	runtime.GC()
	runtime.KeepAlive(collector)
	runtime.KeepAlive(result)
	var snapshot runtime.MemStats
	runtime.ReadMemStats(&snapshot)
	return snapshot.HeapAlloc, nil
}

func proxyBufferAggregationPayload(includeFinalItem bool) []byte {
	var payload strings.Builder
	payloadBytes := proxyBufferAggregationEventCount * (proxyBufferAggregationChunkBytes + 64)
	if includeFinalItem {
		payloadBytes += proxyBufferAggregationEventCount*proxyBufferAggregationChunkBytes + 192
	}
	payload.Grow(payloadBytes)
	payload.WriteString("event: response.created\n")
	payload.WriteString("data: {\"response\":{\"id\":\"resp-perf-stream-into\"}}\n\n")
	chunk := strings.Repeat("x", proxyBufferAggregationChunkBytes)
	for range proxyBufferAggregationEventCount {
		payload.WriteString("event: response.output_text.delta\n")
		payload.WriteString("data: {\"delta\":\"")
		payload.WriteString(chunk)
		payload.WriteString("\"}\n\n")
	}
	if includeFinalItem {
		payload.WriteString("event: response.output_item.done\n")
		payload.WriteString("data: {\"item\":{\"id\":\"msg-perf-stream-into\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"")
		payload.WriteString(strings.Repeat(chunk, proxyBufferAggregationEventCount))
		payload.WriteString("\"}]}}\n\n")
	}
	payload.WriteString("event: response.completed\n")
	payload.WriteString("data: {\"response\":{\"finish_reason\":\"stop\",\"usage\":{\"input_tokens\":1,\"output_tokens\":128,\"total_tokens\":129}}}\n\n")
	return []byte(payload.String())
}
