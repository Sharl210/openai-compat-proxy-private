package perfbench

import (
	"bytes"
	"strconv"
	"testing"
)

func TestWorkerHeapSnapshotFrame_round_trips_only_canonical_bounded_payloads(t *testing.T) {
	// Given
	want := workerHeapSnapshot{HeapAlloc: 101, HeapInuse: 202}
	frame, err := encodeWorkerHeapSnapshotFrame(want)
	if err != nil {
		t.Fatalf("encode worker heap snapshot: %v", err)
	}

	// When
	got, err := decodeWorkerHeapSnapshotFrame(bytes.NewReader(frame))

	// Then
	if err != nil {
		t.Fatalf("decode worker heap snapshot: %v", err)
	}
	if got != want {
		t.Fatalf("heap snapshot = %+v, want %+v", got, want)
	}
	for _, payload := range [][]byte{
		[]byte(`{"heap_inuse":202,"heap_alloc":101}`),
		[]byte(`{"heap_alloc":101,"heap_inuse":202,"extra":1}`),
	} {
		t.Run(string(payload), func(t *testing.T) {
			frame := append([]byte(heapSnapshotFrameMarker+""), []byte{}...)
			frame = append(frame, []byte(strconv.Itoa(len(payload))+"\n")...)
			frame = append(frame, payload...)
			if _, err := decodeWorkerHeapSnapshotFrame(bytes.NewReader(frame)); err == nil {
				t.Fatalf("non-canonical heap snapshot was accepted: %s", payload)
			}
		})
	}
	overLimit := []byte(heapSnapshotFrameMarker + strconv.Itoa(maxHeapSnapshotFrameBytes+1) + "\n")
	if _, err := decodeWorkerHeapSnapshotFrame(bytes.NewReader(overLimit)); err == nil {
		t.Fatal("over-limit heap snapshot frame was accepted")
	}
}
