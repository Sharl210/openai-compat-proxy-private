package tokenestimator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultPrefixLedgerMaxEntries = 1024
	defaultPrefixLedgerMaxBytes   = 256 << 10
)

type prefixLedger struct {
	Version int
	Entries map[string]PrefixMeasurement
	Order   []string
}

type persistedPrefixLedger struct {
	Version int                 `json:"version"`
	Order   []string            `json:"order,omitempty"`
	Entries []PrefixMeasurement `json:"entries"`
}

func PrefixLedgerPath(providersDir string, key BucketKey) string {
	jsonPath, _ := BucketPaths(providersDir, key)
	return jsonPath + ".prefix_ledger.json"
}

func prefixLedgerKey(wireContextFingerprint, prefixFingerprint string) string {
	return strings.TrimSpace(wireContextFingerprint) + "\x00" + strings.TrimSpace(prefixFingerprint)
}

func validPrefixMeasurement(measurement PrefixMeasurement) bool {
	return measurement.Version == PrefixMeasurementVersion &&
		strings.TrimSpace(measurement.WireContextFingerprint) != "" &&
		strings.TrimSpace(measurement.PrefixFingerprint) != "" &&
		measurement.PrefixUnits >= 0 &&
		measurement.StructuralUnits >= 0 &&
		measurement.LocalEstimate >= 0 &&
		measurement.InputTokens > 0 &&
		measurement.CachedTokens >= 0 &&
		measurement.CachedTokens <= measurement.InputTokens
}

func (m *Manager) LookupPrefixMeasurement(key BucketKey, wireContextFingerprint, prefixFingerprint string) (PrefixMeasurement, bool) {
	if m == nil {
		return PrefixMeasurement{}, false
	}
	lookupKey := prefixLedgerKey(wireContextFingerprint, prefixFingerprint)
	if strings.HasSuffix(lookupKey, "\x00") {
		return PrefixMeasurement{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ledger := m.prefixLedgerLocked(key)
	measurement, ok := ledger.Entries[lookupKey]
	if !ok {
		return PrefixMeasurement{}, false
	}
	for index, existing := range ledger.Order {
		if existing != lookupKey {
			continue
		}
		ledger.Order = append(ledger.Order[:index], ledger.Order[index+1:]...)
		break
	}
	ledger.Order = append(ledger.Order, lookupKey)
	return measurement, true
}

func (m *Manager) RecordPrefixMeasurement(key BucketKey, measurement PrefixMeasurement) error {
	if m == nil || !validPrefixMeasurement(measurement) {
		return ErrInvalidObservation
	}
	if measurement.RecordedAt.IsZero() {
		measurement.RecordedAt = time.Now().In(m.location)
	}
	lookupKey := prefixLedgerKey(measurement.WireContextFingerprint, measurement.PrefixFingerprint)
	m.mu.Lock()
	ledger := m.prefixLedgerLocked(key)
	ledger.Entries[lookupKey] = measurement
	for index, existing := range ledger.Order {
		if existing != lookupKey {
			continue
		}
		ledger.Order = append(ledger.Order[:index], ledger.Order[index+1:]...)
		break
	}
	ledger.Order = append(ledger.Order, lookupKey)
	m.trimPrefixLedgerLocked(ledger)
	m.mu.Unlock()
	return m.persistPrefixLedger(key)
}

func (m *Manager) SetPrefixLedgerLimits(maxEntries int, maxBytes int64) error {
	if m == nil {
		return nil
	}
	if maxEntries <= 0 {
		maxEntries = defaultPrefixLedgerMaxEntries
	}
	if maxBytes <= 0 {
		maxBytes = defaultPrefixLedgerMaxBytes
	}
	m.mu.Lock()
	m.prefixLedgerMaxEntries = maxEntries
	m.prefixLedgerMaxBytes = maxBytes
	keys := make([]BucketKey, 0, len(m.prefixLedgers))
	for key, ledger := range m.prefixLedgers {
		m.trimPrefixLedgerLocked(ledger)
		keys = append(keys, key)
	}
	m.mu.Unlock()
	for _, key := range keys {
		if err := m.persistPrefixLedger(key); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) PrefixLedgerEntryCount(key BucketKey) int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.prefixLedgerLocked(key).Entries)
}

func (m *Manager) prefixLedgerLocked(key BucketKey) *prefixLedger {
	if m.prefixLedgers == nil {
		m.prefixLedgers = map[BucketKey]*prefixLedger{}
	}
	if ledger, ok := m.prefixLedgers[key]; ok {
		return ledger
	}
	ledger := loadPrefixLedger(PrefixLedgerPath(m.providersDir, key), m.prefixLedgerMaxBytes)
	m.prefixLedgers[key] = ledger
	m.trimPrefixLedgerLocked(ledger)
	return ledger
}

func loadPrefixLedger(path string, maxBytes int64) *prefixLedger {
	ledger := &prefixLedger{Version: PrefixMeasurementVersion, Entries: map[string]PrefixMeasurement{}}
	if strings.TrimSpace(path) == "" {
		return ledger
	}
	if maxBytes <= 0 {
		maxBytes = defaultPrefixLedgerMaxBytes
	}
	data, err := os.ReadFile(path)
	if err != nil || int64(len(data)) > maxBytes {
		return ledger
	}
	var persisted persistedPrefixLedger
	if err := json.Unmarshal(data, &persisted); err != nil || persisted.Version != PrefixMeasurementVersion {
		return ledger
	}
	for _, measurement := range persisted.Entries {
		if !validPrefixMeasurement(measurement) {
			continue
		}
		ledger.Entries[prefixLedgerKey(measurement.WireContextFingerprint, measurement.PrefixFingerprint)] = measurement
	}
	seen := map[string]struct{}{}
	for _, key := range persisted.Order {
		if _, ok := ledger.Entries[key]; !ok {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		ledger.Order = append(ledger.Order, key)
	}
	for key := range ledger.Entries {
		if _, ok := seen[key]; ok {
			continue
		}
		ledger.Order = append(ledger.Order, key)
	}
	return ledger
}

func (m *Manager) trimPrefixLedgerLocked(ledger *prefixLedger) {
	if ledger == nil {
		return
	}
	maxEntries := m.prefixLedgerMaxEntries
	if maxEntries <= 0 {
		maxEntries = defaultPrefixLedgerMaxEntries
	}
	for len(ledger.Order) > maxEntries {
		m.evictOldestPrefixEntryLocked(ledger)
	}
	maxBytes := m.prefixLedgerMaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultPrefixLedgerMaxBytes
	}
	for len(ledger.Order) > 0 {
		data, err := marshalPrefixLedger(ledger)
		if err != nil || int64(len(data)) <= maxBytes {
			return
		}
		m.evictOldestPrefixEntryLocked(ledger)
	}
}

func (m *Manager) evictOldestPrefixEntryLocked(ledger *prefixLedger) {
	if ledger == nil || len(ledger.Order) == 0 {
		return
	}
	oldest := ledger.Order[0]
	ledger.Order = ledger.Order[1:]
	delete(ledger.Entries, oldest)
}

func marshalPrefixLedger(ledger *prefixLedger) ([]byte, error) {
	if ledger == nil {
		return nil, fmt.Errorf("nil prefix ledger")
	}
	entries := make([]PrefixMeasurement, 0, len(ledger.Order))
	for _, key := range ledger.Order {
		measurement, ok := ledger.Entries[key]
		if !ok {
			continue
		}
		entries = append(entries, measurement)
	}
	return json.Marshal(persistedPrefixLedger{
		Version: PrefixMeasurementVersion,
		Order:   append([]string(nil), ledger.Order...),
		Entries: entries,
	})
}

func clonePrefixLedger(ledger *prefixLedger) *prefixLedger {
	if ledger == nil {
		return nil
	}
	cloned := &prefixLedger{
		Version: ledger.Version,
		Entries: make(map[string]PrefixMeasurement, len(ledger.Entries)),
		Order:   append([]string(nil), ledger.Order...),
	}
	for key, measurement := range ledger.Entries {
		cloned.Entries[key] = measurement
	}
	return cloned
}

func (m *Manager) persistPrefixLedger(key BucketKey) error {
	if m == nil {
		return nil
	}
	m.prefixPersistMu.Lock()
	defer m.prefixPersistMu.Unlock()

	m.mu.RLock()
	ledger := clonePrefixLedger(m.prefixLedgers[key])
	maxBytes := m.prefixLedgerMaxBytes
	m.mu.RUnlock()
	return m.savePrefixLedger(key, ledger, maxBytes)
}

func (m *Manager) savePrefixLedger(key BucketKey, ledger *prefixLedger, maxBytes int64) error {
	if m == nil || ledger == nil {
		return nil
	}
	path := PrefixLedgerPath(m.providersDir, key)
	data, err := marshalPrefixLedger(ledger)
	if err != nil {
		return err
	}
	if maxBytes <= 0 {
		maxBytes = defaultPrefixLedgerMaxBytes
	}
	if int64(len(data)) > maxBytes {
		return fmt.Errorf("prefix ledger exceeds %d bytes", maxBytes)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicWrite(path, data)
}
