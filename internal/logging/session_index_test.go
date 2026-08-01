package logging_test

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"openai-compat-proxy/internal/logging"
)

func TestSessionRequestIndexPersistsRequiredFieldsAndUsesSafeSessionPath(t *testing.T) {
	root := t.TempDir()
	sessionID := "../../session/with?unsafe"
	index := logging.NewSessionRequestIndex(root)

	seq, err := index.Reserve(sessionID, "req-1", "req-1")
	if err != nil {
		t.Fatalf("reserve session request: %v", err)
	}
	if seq != 1 {
		t.Fatalf("expected first sequence 1, got %d", seq)
	}
	if err := index.Append(logging.SessionRequestRecord{
		Event:                  "completed",
		SessionID:              sessionID,
		ConversationRequestSeq: seq,
		ConversationRequestID:  "r000001",
		RequestUID:             "req-1",
		XRequestID:             "req-1",
		Status:                 400,
		Route:                  "/v1/chat/completions",
	}); err != nil {
		t.Fatalf("append session request completion: %v", err)
	}

	path := logging.SessionRequestIndexPath(root, sessionID)
	if strings.Contains(filepath.Base(path), "..") || strings.Contains(filepath.Base(path), "unsafe") {
		t.Fatalf("expected hashed session directory, got %q", path)
	}
	if _, err := os.Stat(filepath.Join(path, "index.ndjson")); err != nil {
		t.Fatalf("expected direct session index path: %v", err)
	}

	records, err := index.Lookup(sessionID)
	if err != nil {
		t.Fatalf("lookup session records: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected allocation and completion records, got %d", len(records))
	}
	completed := records[len(records)-1]
	if completed.SessionID != sessionID || completed.ConversationRequestSeq != 1 || completed.ConversationRequestID != "r000001" || completed.RequestUID != "req-1" || completed.XRequestID != "req-1" {
		t.Fatalf("required identity fields missing: %#v", completed)
	}
	if completed.Timestamp == "" || completed.Status != 400 || completed.Route != "/v1/chat/completions" {
		t.Fatalf("required lifecycle fields missing: %#v", completed)
	}
}

func TestSessionRequestIndexContinuesSequenceAfterRestart(t *testing.T) {
	root := t.TempDir()
	first := logging.NewSessionRequestIndex(root)
	if got, err := first.Reserve("restart-session", "req-1", "req-1"); err != nil || got != 1 {
		t.Fatalf("first reserve got seq=%d err=%v", got, err)
	}
	if got, err := first.Reserve("restart-session", "req-1", "req-1"); err != nil || got != 1 {
		t.Fatalf("duplicate reserve should be idempotent, got seq=%d err=%v", got, err)
	}

	second := logging.NewSessionRequestIndex(root)
	got, err := second.Reserve("restart-session", "req-2", "req-2")
	if err != nil {
		t.Fatalf("reserve after restart: %v", err)
	}
	if got != 2 {
		t.Fatalf("expected sequence 2 after restart, got %d", got)
	}
}

func TestSessionRequestIndexConcurrentSameSessionAllocationIsUnique(t *testing.T) {
	index := logging.NewSessionRequestIndex(t.TempDir())
	const requestCount = 24
	sequences := make(chan uint64, requestCount)
	errors := make(chan error, requestCount)
	var group sync.WaitGroup
	for i := 0; i < requestCount; i++ {
		group.Add(1)
		go func(requestIndex int) {
			defer group.Done()
			sequence, err := index.Reserve("concurrent-session", "req-"+strconv.Itoa(requestIndex), "")
			if err != nil {
				errors <- err
				return
			}
			sequences <- sequence
		}(i)
	}
	group.Wait()
	close(sequences)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}

	got := make([]int, 0, requestCount)
	for sequence := range sequences {
		got = append(got, int(sequence))
	}
	if len(got) != requestCount {
		t.Fatalf("expected %d sequences, got %d", requestCount, len(got))
	}
	sort.Ints(got)
	for index, sequence := range got {
		if sequence != index+1 {
			t.Fatalf("expected contiguous sequence at position %d, got %d in %v", index, sequence, got)
		}
	}
}
