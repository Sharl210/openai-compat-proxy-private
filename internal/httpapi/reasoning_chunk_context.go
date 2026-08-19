package httpapi

import (
	"strings"
	"sync"
	"unicode/utf8"
)

const reasoningChunkTailLimit = 10

type reasoningChunkContextStore struct {
	mu    sync.Mutex
	tails map[string]string
}

var requestReasoningChunkContexts = reasoningChunkContextStore{tails: make(map[string]string)}

func (s *reasoningChunkContextStore) appendAndFormat(requestID, chunk string, preserve bool) string {
	if s == nil || requestID == "" || chunk == "" || preserve {
		return chunk
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.tails[requestID]
	formatted := chunk
	if !strings.HasPrefix(chunk, "\n\n") && previous != "" && strings.HasSuffix(previous, "**") && startsWithCompleteBoldSpan(chunk) {
		formatted = "\n\n" + chunk
	}
	s.tails[requestID] = lastReasoningRunes(formatted, reasoningChunkTailLimit)
	return formatted
}

func (s *reasoningChunkContextStore) record(requestID, chunk string, preserve bool) {
	if s == nil || requestID == "" || chunk == "" || preserve {
		return
	}
	s.mu.Lock()
	s.tails[requestID] = lastReasoningRunes(chunk, reasoningChunkTailLimit)
	s.mu.Unlock()
}

func (s *reasoningChunkContextStore) delete(requestID string) {
	if s == nil || requestID == "" {
		return
	}
	s.mu.Lock()
	delete(s.tails, requestID)
	s.mu.Unlock()
}

func (s *reasoningChunkContextStore) tail(requestID string) (string, bool) {
	if s == nil || requestID == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tail, ok := s.tails[requestID]
	return tail, ok
}

func lastReasoningRunes(text string, limit int) string {
	if limit <= 0 || text == "" {
		return ""
	}
	count := 0
	start := len(text)
	for start > 0 && count < limit {
		_, size := utf8.DecodeLastRuneInString(text[:start])
		if size == 0 {
			break
		}
		start -= size
		count++
	}
	return text[start:]
}
