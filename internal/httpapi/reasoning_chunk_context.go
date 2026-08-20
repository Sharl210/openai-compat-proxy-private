package httpapi

import (
	"strings"
	"sync"
	"unicode/utf8"
)

const reasoningChunkTailLimit = 10

type reasoningChunkContextStore struct {
	mu         sync.Mutex
	tails      map[string]string
	titleReady map[string]bool
}

var requestReasoningChunkContexts = reasoningChunkContextStore{tails: make(map[string]string)}

func (s *reasoningChunkContextStore) formatForSend(requestID, chunk string, preserve bool) string {
	if s == nil || requestID == "" {
		return chunk
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if preserve {
		delete(s.tails, requestID)
		delete(s.titleReady, requestID)
		return chunk
	}
	if chunk == "" {
		return chunk
	}
	previousTitle := s.titleReady[requestID]
	formatted := chunk
	if !strings.HasPrefix(formatted, "\n\n") && previousTitle && startsWithCompleteBoldSpan(formatted) {
		formatted = "\n\n" + formatted
	}
	s.setLocked(requestID, formatted)
	return formatted
}

func (s *reasoningChunkContextStore) formatStreamingDelta(requestID string, state *reasoningTextState, delta string, preserve bool) string {
	return s.formatForSend(requestID, formatStreamingReasoningDelta(state, delta, preserve), preserve)
}

func (s *reasoningChunkContextStore) record(requestID, chunk string, preserve bool) {
	if s == nil || requestID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if preserve {
		delete(s.tails, requestID)
		delete(s.titleReady, requestID)
		return
	}
	if chunk == "" {
		return
	}
	s.setLocked(requestID, chunk)
}

func (s *reasoningChunkContextStore) setLocked(requestID, chunk string) {
	if s.tails == nil {
		s.tails = make(map[string]string)
	}
	if s.titleReady == nil {
		s.titleReady = make(map[string]bool)
	}
	s.tails[requestID] = lastReasoningRunes(chunk, reasoningChunkTailLimit)
	s.titleReady[requestID] = reasoningTextHasTrailingBoldSpan(chunk)
}

func (s *reasoningChunkContextStore) delete(requestID string) {
	if s == nil || requestID == "" {
		return
	}
	s.mu.Lock()
	delete(s.tails, requestID)
	delete(s.titleReady, requestID)
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
