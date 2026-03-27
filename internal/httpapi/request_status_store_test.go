package httpapi

import (
	"fmt"
	"testing"
	"time"
)

func TestRequestStatusStoreEvictsOldestEntryWhenCapacityExceeded(t *testing.T) {
	store := newRequestStatusStore()
	for i := 0; i < requestStatusStoreMaxItems+1; i++ {
		store.start(fmt.Sprintf("req-%d", i), "openai", "/v1/responses")
	}
	if len(store.items) != requestStatusStoreMaxItems {
		t.Fatalf("expected store size capped at %d, got %d", requestStatusStoreMaxItems, len(store.items))
	}
	if _, ok := store.get("req-0"); ok {
		t.Fatalf("expected oldest request to be evicted")
	}
	if _, ok := store.get(fmt.Sprintf("req-%d", requestStatusStoreMaxItems)); !ok {
		t.Fatalf("expected newest request to remain")
	}
}

func TestRequestStatusStoreMarkFailedKeepsFailedRequestAsCompletedTerminalState(t *testing.T) {
	store := newRequestStatusStore()
	store.start("req-failed", "openai", "/v1/responses")

	store.markFailed("req-failed", "upstream_timeout", "upstream_timeout", "upstream request timed out")

	status, ok := store.get("req-failed")
	if !ok {
		t.Fatalf("expected failed request status to remain in store")
	}
	if status.Status != "failed" {
		t.Fatalf("expected failed terminal status, got %#v", status)
	}
	if !status.Completed {
		t.Fatalf("expected failed terminal status to report completed=true, got %#v", status)
	}
	if status.Stage != "failed" || status.HealthFlag != "upstream_timeout" || status.ErrorCode != "upstream_timeout" {
		t.Fatalf("unexpected failed terminal status payload: %#v", status)
	}
}

func TestRequestStatusStoreEvictsOldestCompletedEntryBeforeActiveEntry(t *testing.T) {
	store := newRequestStatusStore()
	for i := 0; i < requestStatusStoreMaxItems; i++ {
		store.start(fmt.Sprintf("req-%d", i), "openai", "/v1/responses")
	}

	store.markCompleted("req-1")

	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	store.mu.Lock()
	activeOldest := store.items["req-0"]
	activeOldest.UpdatedAt = base
	store.items["req-0"] = activeOldest

	completedOldest := store.items["req-1"]
	completedOldest.UpdatedAt = base.Add(time.Second)
	store.items["req-1"] = completedOldest

	for i := 2; i < requestStatusStoreMaxItems; i++ {
		status := store.items[fmt.Sprintf("req-%d", i)]
		status.UpdatedAt = base.Add(time.Duration(i) * time.Minute)
		store.items[status.RequestID] = status
	}
	store.mu.Unlock()

	store.start("req-overflow", "openai", "/v1/responses")

	if _, ok := store.get("req-0"); !ok {
		t.Fatalf("expected oldest active request to remain when completed entries exist")
	}
	if _, ok := store.get("req-1"); ok {
		t.Fatalf("expected oldest completed request to be evicted before active request")
	}
	if _, ok := store.get("req-overflow"); !ok {
		t.Fatalf("expected overflow request to be inserted")
	}
}
