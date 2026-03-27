package httpapi

import (
	"fmt"
	"testing"
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
