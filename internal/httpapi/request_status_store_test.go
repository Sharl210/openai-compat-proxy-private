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
