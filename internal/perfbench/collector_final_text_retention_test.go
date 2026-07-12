package perfbench

import (
	"runtime"
	"strings"
	"testing"

	"openai-compat-proxy/internal/aggregate"
	"openai-compat-proxy/internal/upstream"
)

func TestProxyBufferAggregation_avoids_duplicate_final_text_when_delta_matches_output_item(t *testing.T) {
	// Given
	deltaOnlyHeap, err := measureCollectorRetainedHeap(false)
	if err != nil {
		t.Fatalf("measure delta-only retained heap: %v", err)
	}

	// When
	finalItemHeap, err := measureCollectorRetainedHeap(true)
	if err != nil {
		t.Fatalf("measure final-item retained heap: %v", err)
	}

	// Then
	if difference := finalItemHeap - deltaOnlyHeap; difference > 128<<10 {
		t.Fatalf("final item retained %d duplicate bytes beyond the delta-only baseline", difference)
	}
}

func measureCollectorRetainedHeap(includeFinalItem bool) (uint64, error) {
	runtime.GC()
	result, err := collectProxyBufferAggregationResult(includeFinalItem)
	if err != nil {
		return 0, err
	}
	runtime.GC()
	runtime.KeepAlive(result)
	var snapshot runtime.MemStats
	runtime.ReadMemStats(&snapshot)
	return snapshot.HeapAlloc, nil
}

func collectProxyBufferAggregationResult(includeFinalItem bool) (aggregate.Result, error) {
	collector := aggregate.NewCollector()
	chunk := strings.Repeat("x", proxyBufferAggregationChunkBytes)
	for range proxyBufferAggregationEventCount {
		collector.Accept(upstream.Event{Event: "response.output_text.delta", Data: map[string]any{"delta": chunk}})
	}
	if includeFinalItem {
		collector.Accept(upstream.Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{
			"type":    "message",
			"content": []any{map[string]any{"type": "output_text", "text": strings.Repeat(chunk, proxyBufferAggregationEventCount)}},
		}}})
	}
	collector.Accept(upstream.Event{Event: "response.completed"})
	return collector.Result()
}
