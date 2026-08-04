package httpapi

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"openai-compat-proxy/internal/model"
)

const implicitSessionStateLimit = defaultRequestLineageConversationLimit * 4

type implicitSessionHistoryKind uint8

const (
	implicitSessionHistoryMessages implicitSessionHistoryKind = iota + 1
	implicitSessionHistoryResponsesInputItems
)

type implicitSessionHistory struct {
	kind         implicitSessionHistoryKind
	prefixHashes [][32]byte
	anchorSuffix []bool
}

type implicitSessionState struct {
	requestUID   string
	sessionID    string
	route        string
	caller       string
	historyKind  implicitSessionHistoryKind
	messageCount int
	prefixHash   [32]byte
	anchored     bool
	reusable     bool
	completed    bool
	order        uint64
}

type implicitSessionResolution struct {
	SessionID  string
	RequestUID string
}

type implicitSessionStore struct {
	mu        sync.Mutex
	states    map[string]implicitSessionState
	nextOrder uint64
}

func newImplicitSessionStore() *implicitSessionStore {
	return &implicitSessionStore{states: map[string]implicitSessionState{}}
}

func (s *implicitSessionStore) resolve(route, caller string, messages []model.CanonicalMessage) string {
	return s.resolveDetailed(route, caller, messages).SessionID
}

func (s *implicitSessionStore) resolveDetailed(route, caller string, messages []model.CanonicalMessage) implicitSessionResolution {
	return s.resolveHistoryDetailed(route, caller, newImplicitSessionHistory(messages, nil))
}

func (s *implicitSessionStore) resolveHistory(route, caller string, history implicitSessionHistory) string {
	return s.resolveHistoryDetailed(route, caller, history).SessionID
}

func (s *implicitSessionStore) resolveHistoryDetailed(route, caller string, history implicitSessionHistory) implicitSessionResolution {
	if s == nil || len(history.prefixHashes) == 0 {
		return implicitSessionResolution{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for count := len(history.prefixHashes); count > 0; count-- {
		matches := map[string]map[string]struct{}{}
		for _, state := range s.states {
			if !state.reusable || state.route != route || state.caller != caller || state.historyKind != history.kind || state.messageCount != count || state.prefixHash != history.prefixHashes[count-1] {
				continue
			}
			if !state.anchored && !history.hasAnchorAfter(count) {
				continue
			}
			requestUIDs := matches[state.sessionID]
			if requestUIDs == nil {
				requestUIDs = map[string]struct{}{}
				matches[state.sessionID] = requestUIDs
			}
			requestUIDs[state.requestUID] = struct{}{}
		}
		if len(matches) == 1 {
			for sessionID, requestUIDs := range matches {
				resolution := implicitSessionResolution{SessionID: sessionID}
				if len(requestUIDs) == 1 {
					for requestUID := range requestUIDs {
						resolution.RequestUID = requestUID
					}
				}
				return resolution
			}
		}
		if len(matches) > 1 {
			return implicitSessionResolution{}
		}
	}
	return implicitSessionResolution{}
}

func (s *implicitSessionStore) observe(requestUID, sessionID, route, caller string, messages []model.CanonicalMessage) {
	s.observeHistory(requestUID, sessionID, route, caller, newImplicitSessionHistory(messages, nil))
}

func (s *implicitSessionStore) observeHistory(requestUID, sessionID, route, caller string, history implicitSessionHistory) {
	if s == nil || strings.TrimSpace(requestUID) == "" || strings.TrimSpace(sessionID) == "" || len(history.prefixHashes) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.states == nil {
		s.states = map[string]implicitSessionState{}
	}
	s.nextOrder++
	if _, exists := s.states[requestUID]; !exists && len(s.states) >= implicitSessionStateLimit {
		oldestID := ""
		var oldestOrder uint64
		for candidateID, state := range s.states {
			if oldestID == "" || state.order < oldestOrder {
				oldestID = candidateID
				oldestOrder = state.order
			}
		}
		if oldestID != "" {
			delete(s.states, oldestID)
		}
	}
	s.states[requestUID] = implicitSessionState{
		requestUID:   requestUID,
		sessionID:    sessionID,
		route:        route,
		caller:       caller,
		historyKind:  history.kind,
		messageCount: len(history.prefixHashes),
		prefixHash:   history.prefixHashes[len(history.prefixHashes)-1],
		anchored:     history.hasAnchorAtOrBeforeEnd(),
		order:        s.nextOrder,
	}
}

func (s *implicitSessionStore) markCompleted(requestUID string) {
	s.markFinished(requestUID, true)
}

func (s *implicitSessionStore) markReusable(requestUID string) {
	if s == nil || strings.TrimSpace(requestUID) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[requestUID]
	if !ok {
		return
	}
	state.reusable = true
	s.states[requestUID] = state
}

func (s *implicitSessionStore) markFinished(requestUID string, reusable bool) {
	if s == nil || strings.TrimSpace(requestUID) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[requestUID]
	if !ok {
		return
	}
	state.reusable = reusable
	state.completed = true
	s.states[requestUID] = state
}

func resolveImplicitProxySessionID(r *http.Request, w http.ResponseWriter, history implicitSessionHistory) (*http.Request, string) {
	r, resolution := resolveImplicitProxySessionIDWithResolution(r, w, history)
	return r, resolution.SessionID
}

func resolveImplicitProxySessionIDWithResolution(r *http.Request, w http.ResponseWriter, history implicitSessionHistory) (*http.Request, implicitSessionResolution) {
	if r == nil {
		return r, implicitSessionResolution{}
	}
	sessionID := proxySessionIDFromRequest(r)
	resolution := implicitSessionResolution{SessionID: sessionID}
	store := requestLineageStoreFromRequest(r)
	if store == nil || store.implicitSessions == nil || len(history.prefixHashes) == 0 {
		return r, resolution
	}
	route := implicitSessionRouteKey(r)
	caller := inboundCallerIdentityFromRequest(r)
	if explicitProxySessionIDFromRequest(r) == "" {
		if resolved := store.implicitSessions.resolveHistoryDetailed(route, caller, history); resolved.SessionID != "" {
			r, sessionID = withProxySessionID(r, w, resolved.SessionID)
			resolution = implicitSessionResolution{SessionID: sessionID, RequestUID: resolved.RequestUID}
			if carrier := requestLineageCarrierFromContext(r.Context()); carrier != nil {
				carrier.setImplicitParentRequestUID(sessionID, resolved.RequestUID)
			}
		}
	}
	store.implicitSessions.observeHistory(requestUIDFromRequest(r), sessionID, route, caller, history)
	return r, resolution
}

func implicitSessionRouteKey(r *http.Request) string {
	if r == nil {
		return ""
	}
	if info, ok := routeInfoFromRequest(r); ok && strings.TrimSpace(info.CanonicalPath) != "" {
		return info.CanonicalPath
	}
	if canonicalPath, ok := canonicalPublicRoutePath(r.URL.Path); ok {
		return canonicalPath
	}
	return normalizePublicRoutePath(r.URL.Path)
}

func requestUIDFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if carrier := requestLineageCarrierFromContext(r.Context()); carrier != nil {
		return carrier.requestUIDValue()
	}
	return ""
}

func newImplicitSessionHistory(messages []model.CanonicalMessage, responseInputItems []map[string]any) implicitSessionHistory {
	responseInputItems = filterImplicitSessionResponsesInputItems(responseInputItems)
	if len(responseInputItems) > 0 {
		if hashes, ok := canonicalResponsesInputPrefixHashes(responseInputItems); ok {
			return implicitSessionHistory{
				kind:         implicitSessionHistoryResponsesInputItems,
				prefixHashes: hashes,
				anchorSuffix: buildImplicitSessionAnchorSuffix(responseInputItemsHaveAnchors(responseInputItems)),
			}
		}
	}
	hashes, ok := canonicalMessagePrefixHashes(messages)
	if !ok {
		return implicitSessionHistory{}
	}
	return implicitSessionHistory{
		kind:         implicitSessionHistoryMessages,
		prefixHashes: hashes,
		anchorSuffix: buildImplicitSessionAnchorSuffix(messagesHaveAnchors(messages)),
	}
}

func (h implicitSessionHistory) hasAnchorAfter(count int) bool {
	if count < 0 || count >= len(h.anchorSuffix) {
		return false
	}
	return h.anchorSuffix[count]
}

func (h implicitSessionHistory) hasAnchorAtOrBeforeEnd() bool {
	return len(h.anchorSuffix) > 0 && h.anchorSuffix[0]
}

func canonicalMessagePrefixHashes(messages []model.CanonicalMessage) ([][32]byte, bool) {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte("openai-compat-proxy/implicit-session/v1\x00"))
	hashes := make([][32]byte, 0, len(messages))
	for _, message := range messages {
		encoded, err := json.Marshal(message)
		if err != nil {
			return nil, false
		}
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(encoded)))
		_, _ = hasher.Write(length[:])
		_, _ = hasher.Write(encoded)
		var prefix [32]byte
		copy(prefix[:], hasher.Sum(nil))
		hashes = append(hashes, prefix)
	}
	return hashes, true
}

func canonicalResponsesInputPrefixHashes(items []map[string]any) ([][32]byte, bool) {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte("openai-compat-proxy/implicit-session/responses-input/v1\x00"))
	hashes := make([][32]byte, 0, len(items))
	for _, item := range items {
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil, false
		}
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(encoded)))
		_, _ = hasher.Write(length[:])
		_, _ = hasher.Write(encoded)
		var prefix [32]byte
		copy(prefix[:], hasher.Sum(nil))
		hashes = append(hashes, prefix)
	}
	return hashes, true
}

func filterImplicitSessionResponsesInputItems(items []map[string]any) []map[string]any {
	if len(items) == 0 {
		return nil
	}
	filtered := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if _, ok := item["__openai_compat_responses_top_level"]; ok {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func buildImplicitSessionAnchorSuffix(anchors []bool) []bool {
	suffix := make([]bool, len(anchors)+1)
	for index := len(anchors) - 1; index >= 0; index-- {
		suffix[index] = anchors[index] || suffix[index+1]
	}
	return suffix
}

func messagesHaveAnchors(messages []model.CanonicalMessage) []bool {
	anchors := make([]bool, len(messages))
	for index, message := range messages {
		anchors[index] = canonicalMessageHasAnchor(message)
	}
	return anchors
}

func responseInputItemsHaveAnchors(items []map[string]any) []bool {
	anchors := make([]bool, len(items))
	for index, item := range items {
		itemType, _ := item["type"].(string)
		role, _ := item["role"].(string)
		switch itemType {
		case "reasoning", "function_call", "function_call_output", "item_reference", "compaction":
			anchors[index] = true
		case "message":
			anchors[index] = role == "assistant" || role == "tool"
		default:
			anchors[index] = role == "assistant" || role == "tool"
		}
	}
	return anchors
}

func canonicalMessagesHaveAnchor(messages []model.CanonicalMessage) bool {
	for _, anchor := range messagesHaveAnchors(messages) {
		if anchor {
			return true
		}
	}
	return false
}

func canonicalMessageHasAnchor(message model.CanonicalMessage) bool {
	switch strings.ToLower(strings.TrimSpace(message.Role)) {
	case "assistant", "tool":
		return true
	}
	return len(message.ToolCalls) > 0 || message.RecoveredToolCall != nil || len(message.ReasoningBlocks) > 0
}
