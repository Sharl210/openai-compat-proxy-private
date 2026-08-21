package httpapi

import (
	"strings"
	"sync"
	"unicode/utf8"
)

const reasoningChunkTailLimit = 10

type reasoningChunkContext struct {
	tail                     string
	tailStartsAtLineBoundary bool
}

type reasoningChunkContextStore struct {
	mu       sync.Mutex
	contexts map[string]reasoningChunkContext
}

var requestReasoningChunkContexts = reasoningChunkContextStore{contexts: make(map[string]reasoningChunkContext)}

func (s *reasoningChunkContextStore) formatForSend(requestID, chunk string, preserve bool) string {
	if s == nil || requestID == "" {
		return chunk
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if preserve {
		delete(s.contexts, requestID)
		return chunk
	}
	if chunk == "" {
		return chunk
	}
	previous := s.contexts[requestID]
	formatted := chunk
	if !strings.HasPrefix(formatted, "\n\n") && previous.hasTrailingStandaloneTitle() && startsWithCompleteOrEmptyBoldSpan(formatted) {
		formatted = "\n\n" + formatted
	}
	s.setLocked(requestID, formatted)
	return formatted
}
func (s *reasoningChunkContextStore) formatStreamingDelta(requestID string, state *reasoningTextState, delta string, preserve bool) string {
	return s.formatStateDelta(requestID, state, formatStreamingReasoningDelta(state, delta, preserve), preserve)
}

func (s *reasoningChunkContextStore) formatStateDelta(requestID string, state *reasoningTextState, delta string, preserve bool) string {
	formatted := s.formatForSend(requestID, delta, preserve)
	if formatted != delta && state != nil {
		state.replaceFormattedSuffix(delta, formatted)
	}
	return formatted
}

func (s *reasoningChunkContextStore) record(requestID, chunk string, preserve bool) {
	if s == nil || requestID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if preserve || chunk == "" {
		delete(s.contexts, requestID)
		return
	}
	s.setLocked(requestID, chunk)
}

func (s *reasoningChunkContextStore) setLocked(requestID, chunk string) {
	if s.contexts == nil {
		s.contexts = make(map[string]reasoningChunkContext)
	}
	previous, hadPrevious := s.contexts[requestID]
	window := chunk
	windowStartsAtLineBoundary := true
	if hadPrevious {
		window = previous.tail + chunk
		windowStartsAtLineBoundary = previous.tailStartsAtLineBoundary
	}
	tail := lastReasoningRunes(window, reasoningChunkTailLimit)
	if start := len(window) - len(tail); start > 0 {
		windowStartsAtLineBoundary = window[start-1] == '\n' || window[start-1] == '\r'
	}
	s.contexts[requestID] = reasoningChunkContext{
		tail:                     tail,
		tailStartsAtLineBoundary: windowStartsAtLineBoundary,
	}
}

func (context reasoningChunkContext) hasTrailingStandaloneTitle() bool {
	if !reasoningTextHasTrailingBoldSpan(context.tail) {
		return false
	}
	lineStart := strings.LastIndexAny(context.tail, "\r\n") + 1
	return context.tailStartsAtLineBoundary || lineStart > 0
}

func (s *reasoningChunkContextStore) delete(requestID string) {
	if s == nil || requestID == "" {
		return
	}
	s.mu.Lock()
	delete(s.contexts, requestID)
	s.mu.Unlock()
}

func (s *reasoningChunkContextStore) tail(requestID string) (string, bool) {
	if s == nil || requestID == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	context, ok := s.contexts[requestID]
	return context.tail, ok
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
