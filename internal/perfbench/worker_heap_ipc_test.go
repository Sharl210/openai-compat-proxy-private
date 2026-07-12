package perfbench

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

const (
	heapSnapshotSignal         = "PERFBENCH_HEAP_SNAPSHOT_V1\n"
	heapSnapshotFrameMarker    = "PERFBENCH_HEAP_SNAPSHOT_V1 "
	maxHeapSnapshotFrameBytes  = 4 << 10
	maxHeapSnapshotHeaderBytes = 64
)

type workerHeapSnapshot struct {
	HeapAlloc uint64 `json:"heap_alloc"`
	HeapInuse uint64 `json:"heap_inuse"`
}

func encodeWorkerHeapSnapshotFrame(snapshot workerHeapSnapshot) ([]byte, error) {
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("marshal worker heap snapshot: %w", err)
	}
	if len(payload) > maxHeapSnapshotFrameBytes {
		return nil, fmt.Errorf("worker heap snapshot is %d bytes, limit %d", len(payload), maxHeapSnapshotFrameBytes)
	}
	return append([]byte(heapSnapshotFrameMarker+strconv.Itoa(len(payload))+"\n"), payload...), nil
}

func decodeWorkerHeapSnapshotFrame(reader io.Reader) (workerHeapSnapshot, error) {
	payload, err := readFramedPayload(reader, heapSnapshotFrameMarker, maxHeapSnapshotHeaderBytes, maxHeapSnapshotFrameBytes)
	if err != nil {
		return workerHeapSnapshot{}, err
	}
	var snapshot workerHeapSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return workerHeapSnapshot{}, fmt.Errorf("decode worker heap snapshot payload: %w", err)
	}
	canonical, err := json.Marshal(snapshot)
	if err != nil {
		return workerHeapSnapshot{}, fmt.Errorf("re-encode worker heap snapshot payload: %w", err)
	}
	if !bytes.Equal(payload, canonical) {
		return workerHeapSnapshot{}, errors.New("worker heap snapshot payload is not canonical")
	}
	return snapshot, nil
}
