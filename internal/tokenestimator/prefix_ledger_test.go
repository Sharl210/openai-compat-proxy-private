package tokenestimator

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPrefixLedgerReloadsMeasuredNumbersWithoutRawRequest(t *testing.T) {
	root := t.TempDir()
	key := BucketKey{ProviderID: "openai", EndpointType: "responses", Model: "gpt-5.6"}
	measurement := PrefixMeasurement{
		Version:                PrefixMeasurementVersion,
		WireContextFingerprint: "wire-context-hash",
		PrefixFingerprint:      "prefix-content-hash",
		PrefixUnits:            1200,
		StructuralUnits:        7,
		LocalEstimate:          310,
		InputTokens:            212000,
		CachedTokens:           180000,
		RecordedAt:             time.Unix(42, 0).UTC(),
	}

	manager := NewManager(root, time.UTC, func() []string { return []string{"openai"} })
	if err := manager.RecordPrefixMeasurement(key, measurement); err != nil {
		t.Fatalf("record prefix measurement: %v", err)
	}
	path := PrefixLedgerPath(root, key)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read prefix ledger: %v", err)
	}
	if strings.Contains(string(data), "raw request") || strings.Contains(string(data), "secret credential") || strings.Contains(string(data), "previous_response_id") {
		t.Fatalf("prefix ledger contains raw request material: %s", data)
	}
	if strings.Contains(string(data), ".tmp") {
		t.Fatalf("prefix ledger persisted temporary file state: %s", data)
	}

	reloaded := NewManager(root, time.UTC, func() []string { return []string{"openai"} })
	got, ok := reloaded.LookupPrefixMeasurement(key, measurement.WireContextFingerprint, measurement.PrefixFingerprint)
	if !ok {
		t.Fatal("expected persisted prefix measurement after manager reload")
	}
	if got.InputTokens != measurement.InputTokens || got.CachedTokens != measurement.CachedTokens || got.Version != PrefixMeasurementVersion {
		t.Fatalf("reloaded measurement changed normalized usage: got=%#v want=%#v", got, measurement)
	}
}

func TestPrefixLedgerEvictsOldestEntriesByFixedLimit(t *testing.T) {
	root := t.TempDir()
	key := BucketKey{ProviderID: "openai", EndpointType: "responses", Model: "gpt-5.6"}
	manager := NewManager(root, time.UTC, func() []string { return []string{"openai"} })
	if err := manager.SetPrefixLedgerLimits(2, 1<<20); err != nil {
		t.Fatalf("set prefix ledger limits: %v", err)
	}
	for index := 0; index < 3; index++ {
		measurement := PrefixMeasurement{
			Version:                PrefixMeasurementVersion,
			WireContextFingerprint: "wire",
			PrefixFingerprint:      "prefix-" + string(rune('a'+index)),
			PrefixUnits:            int64(index + 1),
			LocalEstimate:          int64(index + 1),
			InputTokens:            int64(index + 10),
			RecordedAt:             time.Unix(int64(index), 0).UTC(),
		}
		if err := manager.RecordPrefixMeasurement(key, measurement); err != nil {
			t.Fatalf("record prefix %d: %v", index, err)
		}
	}
	if got := manager.PrefixLedgerEntryCount(key); got != 2 {
		t.Fatalf("expected two bounded entries, got %d", got)
	}
	if _, ok := manager.LookupPrefixMeasurement(key, "wire", "prefix-a"); ok {
		t.Fatal("expected oldest prefix entry to be evicted")
	}
	if _, ok := manager.LookupPrefixMeasurement(key, "wire", "prefix-b"); !ok {
		t.Fatal("expected second prefix entry to remain")
	}
	if _, ok := manager.LookupPrefixMeasurement(key, "wire", "prefix-c"); !ok {
		t.Fatal("expected newest prefix entry to remain")
	}
}

func TestPrefixLedgerReloadAppliesCurrentEntryLimit(t *testing.T) {
	root := t.TempDir()
	key := BucketKey{ProviderID: "openai", EndpointType: "responses", Model: "gpt-5.6"}
	writer := NewManager(root, time.UTC, func() []string { return []string{"openai"} })
	if err := writer.SetPrefixLedgerLimits(4, 1<<20); err != nil {
		t.Fatalf("set writer prefix ledger limits: %v", err)
	}
	for index := 0; index < 4; index++ {
		measurement := PrefixMeasurement{
			Version:                PrefixMeasurementVersion,
			WireContextFingerprint: "wire",
			PrefixFingerprint:      "prefix-" + string(rune('a'+index)),
			PrefixUnits:            int64(index + 1),
			StructuralUnits:        1,
			LocalEstimate:          int64(index + 1),
			InputTokens:            int64(index + 10),
			RecordedAt:             time.Unix(int64(index), 0).UTC(),
		}
		if err := writer.RecordPrefixMeasurement(key, measurement); err != nil {
			t.Fatalf("record prefix %d: %v", index, err)
		}
	}

	reloaded := NewManager(root, time.UTC, func() []string { return []string{"openai"} })
	if err := reloaded.SetPrefixLedgerLimits(2, 1<<20); err != nil {
		t.Fatalf("set reloaded prefix ledger limits: %v", err)
	}
	if _, ok := reloaded.LookupPrefixMeasurement(key, "wire", "prefix-a"); ok {
		t.Fatal("expected reload to evict the oldest entry under the current limit")
	}
	if got := reloaded.PrefixLedgerEntryCount(key); got != 2 {
		t.Fatalf("expected reloaded ledger to contain two entries, got %d", got)
	}
}

func TestCorruptPrefixLedgerFallsBackCold(t *testing.T) {
	root := t.TempDir()
	key := BucketKey{ProviderID: "openai", EndpointType: "responses", Model: "gpt-5.6"}
	path := PrefixLedgerPath(root, key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create ledger directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("not-json"), 0o644); err != nil {
		t.Fatalf("write corrupt ledger: %v", err)
	}
	manager := NewManager(root, time.UTC, func() []string { return []string{"openai"} })
	if _, ok := manager.LookupPrefixMeasurement(key, "wire", "prefix"); ok {
		t.Fatal("expected corrupt ledger to fall back cold")
	}
}

func TestPrefixLedgerConcurrentRecordsReloadAllBoundedEntries(t *testing.T) {
	root := t.TempDir()
	key := BucketKey{ProviderID: "openai", EndpointType: "responses", Model: "gpt-5.6"}
	manager := NewManager(root, time.UTC, func() []string { return []string{"openai"} })
	const entryCount = 32
	if err := manager.SetPrefixLedgerLimits(entryCount, 1<<20); err != nil {
		t.Fatalf("set prefix ledger limits: %v", err)
	}

	var waitGroup sync.WaitGroup
	errors := make(chan error, entryCount)
	for index := 0; index < entryCount; index++ {
		index := index
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			errors <- manager.RecordPrefixMeasurement(key, PrefixMeasurement{
				Version:                PrefixMeasurementVersion,
				WireContextFingerprint: "wire",
				PrefixFingerprint:      "prefix-" + strconv.Itoa(index),
				PrefixUnits:            int64(index + 1),
				StructuralUnits:        1,
				LocalEstimate:          int64(index + 1),
				InputTokens:            int64(index + 100),
				CachedTokens:           int64(index),
				RecordedAt:             time.Unix(int64(index), 0).UTC(),
			})
		}()
	}
	waitGroup.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("record concurrent prefix measurement: %v", err)
		}
	}

	reloaded := NewManager(root, time.UTC, func() []string { return []string{"openai"} })
	if got := reloaded.PrefixLedgerEntryCount(key); got != entryCount {
		t.Fatalf("expected all bounded concurrent entries after reload, got %d want %d", got, entryCount)
	}
	for index := 0; index < entryCount; index++ {
		if _, ok := reloaded.LookupPrefixMeasurement(key, "wire", "prefix-"+strconv.Itoa(index)); !ok {
			t.Fatalf("expected concurrent prefix entry %d after reload", index)
		}
	}
}
