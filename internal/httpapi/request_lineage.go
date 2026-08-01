package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	modelpkg "openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/tokenestimator"
)

const (
	defaultRequestLineageConversationLimit    = 512
	defaultRequestLineageNodesPerConversation = 256
	requestLineageRootModeRoot                = "root"
	requestLineageRootModeAnchored            = "anchored"
	requestLineageRootModeUnanchored          = "unanchored"
)

type requestLineageContextKey struct{}

type requestLineage struct {
	ConversationID         string `json:"conversation_id,omitempty"`
	ConversationRequestSeq uint64 `json:"conversation_request_seq,omitempty"`
	ConversationRequestID  string `json:"conversation_request_id,omitempty"`
	RequestUID             string `json:"request_uid,omitempty"`
	NodeID                 string `json:"lineage_node_id,omitempty"`
	ParentNodeID           string `json:"lineage_parent_node_id,omitempty"`
	ParentRequestUID       string `json:"lineage_parent_request_uid,omitempty"`
	ParentResponseID       string `json:"lineage_parent_response_id,omitempty"`
	RootMode               string `json:"lineage_root_mode,omitempty"`
}

type requestLineageEstimatorFact struct {
	Bucket                 tokenestimator.BucketKey
	WireContextFingerprint string
	LineageWireFingerprint string
	PrefixFingerprint      string
	PrefixUnits            int64
	StructuralUnits        int64
	LocalEstimate          int64
	InputTokens            int64
}

type requestLineageNode struct {
	Meta          requestLineage
	ChildNodeIDs  []string
	ResponseID    string
	FinalizedFact *requestLineageEstimatorFact
	Completed     bool
}

type requestLineageConversation struct {
	ConversationID string
	NextSeq        uint64
	Nodes          map[string]*requestLineageNode
	RequestIndex   map[string]string
	ResponseIndex  map[string]string
	Order          []string
}

type requestLineageStore struct {
	mu                   sync.Mutex
	conversations        map[string]*requestLineageConversation
	conversationOrder    []string
	conversationLimit    int
	nodesPerConversation int
}

// requestLineageCarrier carries the final request lineage metadata through the
// request lifecycle. Allocation is lazy so that handlers can rebind the
// session/conversation identity after request decoding without forcing the
// middleware to parse or infer parentage up front.
type requestLineageCarrier struct {
	mu         sync.Mutex
	requestUID string
	sessionID  string
	lineage    *requestLineage
}

func (c *requestLineageCarrier) requestUIDValue() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.TrimSpace(c.requestUID)
}

func newRequestLineageStore() *requestLineageStore {
	return &requestLineageStore{
		conversations:        map[string]*requestLineageConversation{},
		conversationLimit:    defaultRequestLineageConversationLimit,
		nodesPerConversation: defaultRequestLineageNodesPerConversation,
	}
}

func newRequestLineageCarrier(requestUID, sessionID string) *requestLineageCarrier {
	return &requestLineageCarrier{
		requestUID: strings.TrimSpace(requestUID),
		sessionID:  normalizeProxySessionID(sessionID),
	}
}

func withRequestLineageCarrier(ctx context.Context, carrier *requestLineageCarrier) context.Context {
	if carrier == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestLineageContextKey{}, carrier)
}

func requestLineageCarrierFromContext(ctx context.Context) *requestLineageCarrier {
	if ctx == nil {
		return nil
	}
	carrier, _ := ctx.Value(requestLineageContextKey{}).(*requestLineageCarrier)
	return carrier
}

func requestLineageFromContext(ctx context.Context) (requestLineage, bool) {
	carrier := requestLineageCarrierFromContext(ctx)
	if carrier == nil {
		return requestLineage{}, false
	}
	return carrier.lineageSnapshot()
}

func requestLineageFromRequest(r *http.Request) (requestLineage, bool) {
	if r == nil {
		return requestLineage{}, false
	}
	return requestLineageFromContext(r.Context())
}

func (c *requestLineageCarrier) sessionIDValue() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return normalizeProxySessionID(c.sessionID)
}

func (c *requestLineageCarrier) setSessionID(sessionID string) {
	if c == nil {
		return
	}
	sessionID = normalizeProxySessionID(sessionID)
	if sessionID == "" {
		return
	}
	c.mu.Lock()
	c.sessionID = sessionID
	c.mu.Unlock()
}

func (c *requestLineageCarrier) lineageSnapshot() (requestLineage, bool) {
	if c == nil {
		return requestLineage{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lineage == nil {
		return requestLineage{}, false
	}
	return *c.lineage, true
}

func (c *requestLineageCarrier) ensureResolved(store *requestLineageStore, sessionID, parentResponseID string) (requestLineage, bool) {
	if c == nil || store == nil {
		return requestLineage{}, false
	}
	sessionID = normalizeProxySessionID(firstString(sessionID, c.sessionID))
	if sessionID == "" || strings.TrimSpace(c.requestUID) == "" {
		return requestLineage{}, false
	}
	parentResponseID = strings.TrimSpace(parentResponseID)
	c.mu.Lock()
	if c.lineage != nil {
		meta := *c.lineage
		c.mu.Unlock()
		return meta, true
	}
	c.sessionID = sessionID
	c.mu.Unlock()
	meta := store.allocate(sessionID, c.requestUID, parentResponseID)
	if meta.NodeID == "" {
		return requestLineage{}, false
	}
	c.mu.Lock()
	if c.lineage == nil {
		c.lineage = &meta
	} else {
		meta = *c.lineage
	}
	c.mu.Unlock()
	return meta, true
}

func ensureResolvedRequestLineage(ctx context.Context, sessionID, parentResponseID string) (requestLineage, bool) {
	carrier := requestLineageCarrierFromContext(ctx)
	if carrier == nil {
		return requestLineage{}, false
	}
	store := requestLineageStoreFromContext(ctx)
	return carrier.ensureResolved(store, sessionID, parentResponseID)
}

func finalizeRequestLineage(ctx context.Context, sessionID string) (requestLineage, bool) {
	return ensureResolvedRequestLineage(ctx, sessionID, "")
}

func (s *requestLineageStore) allocate(conversationID, requestUID, parentResponseID string) requestLineage {
	conversationID = normalizeProxySessionID(conversationID)
	requestUID = strings.TrimSpace(requestUID)
	parentResponseID = strings.TrimSpace(parentResponseID)
	if conversationID == "" || requestUID == "" {
		return requestLineage{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation := s.conversations[conversationID]
	if conversation != nil {
		if existingNodeID, ok := conversation.RequestIndex[requestUID]; ok {
			if existing := conversation.Nodes[existingNodeID]; existing != nil {
				return existing.Meta
			}
		}
	}
	if conversation == nil {
		if !s.makeConversationRoomLocked() {
			return requestLineage{}
		}
		conversation = s.ensureConversationLocked(conversationID)
	} else if !s.makeNodeRoomLocked(conversation) {
		return requestLineage{}
	}
	conversation.NextSeq++
	seq := conversation.NextSeq
	meta := requestLineage{
		ConversationID:         conversationID,
		ConversationRequestSeq: seq,
		ConversationRequestID:  fmt.Sprintf("r%06d", seq),
		RequestUID:             requestUID,
		NodeID:                 fmt.Sprintf("n%06d", seq),
		ParentResponseID:       parentResponseID,
		RootMode:               requestLineageRootModeRoot,
	}
	if parentResponseID != "" {
		meta.RootMode = requestLineageRootModeUnanchored
		if parentNodeID, ok := conversation.ResponseIndex[parentResponseID]; ok {
			if parent := conversation.Nodes[parentNodeID]; parent != nil {
				meta.ParentNodeID = parent.Meta.NodeID
				meta.ParentRequestUID = parent.Meta.RequestUID
				meta.RootMode = requestLineageRootModeAnchored
				parent.ChildNodeIDs = append(parent.ChildNodeIDs, meta.NodeID)
			}
		}
	}
	conversation.Nodes[meta.NodeID] = &requestLineageNode{Meta: meta}
	conversation.RequestIndex[requestUID] = meta.NodeID
	conversation.Order = append(conversation.Order, meta.NodeID)
	s.pruneConversationLocked(conversation)
	s.pruneConversationsLocked()
	return meta
}

func (s *requestLineageStore) makeConversationRoomLocked() bool {
	if s == nil || s.conversationLimit <= 0 {
		return true
	}
	for len(s.conversations) >= s.conversationLimit {
		evicted := false
		for _, conversationID := range s.conversationOrder {
			conversation := s.conversations[conversationID]
			if conversation == nil || !conversationCompleted(conversation) {
				continue
			}
			s.removeConversationLocked(conversationID)
			evicted = true
			break
		}
		if !evicted {
			return false
		}
	}
	return true
}

func (s *requestLineageStore) makeNodeRoomLocked(conversation *requestLineageConversation) bool {
	if s == nil || conversation == nil || s.nodesPerConversation <= 0 {
		return true
	}
	s.pruneConversationLocked(conversation)
	return len(conversation.Nodes) < s.nodesPerConversation
}

func (s *requestLineageStore) recordResponseID(meta requestLineage, responseID string) {
	if s == nil || meta.ConversationID == "" || meta.NodeID == "" {
		return
	}
	responseID = strings.TrimSpace(responseID)
	if responseID == "" || responseID == "resp_proxy" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation := s.conversations[meta.ConversationID]
	if conversation == nil {
		return
	}
	node := conversation.Nodes[meta.NodeID]
	if node == nil {
		return
	}
	node.ResponseID = responseID
	conversation.ResponseIndex[responseID] = meta.NodeID
}

func (s *requestLineageStore) recordFinalizedEstimate(meta requestLineage, fact requestLineageEstimatorFact) {
	if s == nil || meta.ConversationID == "" || meta.NodeID == "" || fact.InputTokens <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation := s.conversations[meta.ConversationID]
	if conversation == nil {
		return
	}
	node := conversation.Nodes[meta.NodeID]
	if node == nil {
		return
	}
	cloned := fact
	node.FinalizedFact = &cloned
}

func (s *requestLineageStore) markCompleted(meta requestLineage) {
	if s == nil || meta.ConversationID == "" || meta.NodeID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation := s.conversations[meta.ConversationID]
	if conversation == nil {
		return
	}
	node := conversation.Nodes[meta.NodeID]
	if node == nil {
		return
	}
	node.Completed = true
	if node.ResponseID == "" {
		s.removeNodeLocked(conversation, node.Meta.NodeID)
	}
	s.pruneConversationLocked(conversation)
	if len(conversation.Nodes) == 0 {
		s.removeConversationLocked(meta.ConversationID)
	}
	s.pruneConversationsLocked()
}

func (s *requestLineageStore) parentFinalizedEstimate(meta requestLineage) (requestLineageEstimatorFact, bool) {
	if s == nil || meta.ConversationID == "" || meta.ParentNodeID == "" {
		return requestLineageEstimatorFact{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation := s.conversations[meta.ConversationID]
	if conversation == nil {
		return requestLineageEstimatorFact{}, false
	}
	parent := conversation.Nodes[meta.ParentNodeID]
	if parent == nil || !parent.Completed || parent.FinalizedFact == nil {
		return requestLineageEstimatorFact{}, false
	}
	return *parent.FinalizedFact, true
}

func (s *requestLineageStore) ensureConversationLocked(conversationID string) *requestLineageConversation {
	if s.conversations == nil {
		s.conversations = map[string]*requestLineageConversation{}
	}
	if conversation, ok := s.conversations[conversationID]; ok {
		return conversation
	}
	conversation := &requestLineageConversation{
		ConversationID: conversationID,
		Nodes:          map[string]*requestLineageNode{},
		RequestIndex:   map[string]string{},
		ResponseIndex:  map[string]string{},
	}
	s.conversations[conversationID] = conversation
	s.conversationOrder = append(s.conversationOrder, conversationID)
	return conversation
}

func (s *requestLineageStore) pruneConversationsLocked() {
	if s.conversationLimit <= 0 {
		return
	}
	for len(s.conversations) > s.conversationLimit {
		evicted := false
		for _, conversationID := range s.conversationOrder {
			conversation := s.conversations[conversationID]
			if conversation == nil || !conversationCompleted(conversation) {
				continue
			}
			s.removeConversationLocked(conversationID)
			evicted = true
			break
		}
		if !evicted {
			return
		}
	}
}

func (s *requestLineageStore) pruneConversationLocked(conversation *requestLineageConversation) {
	if conversation == nil || s.nodesPerConversation <= 0 {
		return
	}
	if len(conversation.Nodes) <= s.nodesPerConversation {
		return
	}
	for len(conversation.Nodes) > s.nodesPerConversation {
		evicted := false
		for _, nodeID := range conversation.Order {
			node := conversation.Nodes[nodeID]
			if node == nil || !node.Completed || hasIncompleteChildrenLocked(conversation, node) {
				continue
			}
			s.removeNodeLocked(conversation, nodeID)
			evicted = true
			break
		}
		if !evicted {
			return
		}
	}
}

func (s *requestLineageStore) removeConversationLocked(conversationID string) {
	conversation := s.conversations[conversationID]
	if conversation != nil {
		for _, nodeID := range conversation.Order {
			node := conversation.Nodes[nodeID]
			if node == nil {
				continue
			}
			delete(conversation.RequestIndex, node.Meta.RequestUID)
			if node.ResponseID != "" {
				delete(conversation.ResponseIndex, node.ResponseID)
			}
		}
	}
	delete(s.conversations, conversationID)
	for index, existing := range s.conversationOrder {
		if existing != conversationID {
			continue
		}
		s.conversationOrder = append(s.conversationOrder[:index], s.conversationOrder[index+1:]...)
		break
	}
}

func (s *requestLineageStore) removeNodeLocked(conversation *requestLineageConversation, nodeID string) {
	if conversation == nil {
		return
	}
	node := conversation.Nodes[nodeID]
	if node == nil {
		return
	}
	delete(conversation.Nodes, nodeID)
	delete(conversation.RequestIndex, node.Meta.RequestUID)
	if node.ResponseID != "" {
		delete(conversation.ResponseIndex, node.ResponseID)
	}
	if parentID := node.Meta.ParentNodeID; parentID != "" {
		if parent := conversation.Nodes[parentID]; parent != nil {
			parent.ChildNodeIDs = removeStringValue(parent.ChildNodeIDs, nodeID)
		}
	}
	conversation.Order = removeStringValue(conversation.Order, nodeID)
}

func hasIncompleteChildrenLocked(conversation *requestLineageConversation, node *requestLineageNode) bool {
	if conversation == nil || node == nil {
		return false
	}
	for _, childID := range node.ChildNodeIDs {
		child := conversation.Nodes[childID]
		if child != nil && !child.Completed {
			return true
		}
	}
	return false
}

func conversationCompleted(conversation *requestLineageConversation) bool {
	if conversation == nil {
		return true
	}
	for _, node := range conversation.Nodes {
		if node != nil && !node.Completed {
			return false
		}
	}
	return true
}

func removeStringValue(values []string, target string) []string {
	if len(values) == 0 || target == "" {
		return values
	}
	for index, value := range values {
		if value != target {
			continue
		}
		return append(values[:index], values[index+1:]...)
	}
	return values
}

func appendRequestLineageLogFields(attrs map[string]any, meta requestLineage) {
	if attrs == nil || meta.RequestUID == "" {
		return
	}
	attrs["request_uid"] = meta.RequestUID
	attrs["conversation_id"] = meta.ConversationID
	attrs["conversation_request_seq"] = meta.ConversationRequestSeq
	attrs["conversation_request_id"] = meta.ConversationRequestID
	attrs["lineage_node_id"] = meta.NodeID
	attrs["lineage_root_mode"] = meta.RootMode
	if meta.ParentNodeID != "" {
		attrs["lineage_parent_node_id"] = meta.ParentNodeID
	}
	if meta.ParentRequestUID != "" {
		attrs["lineage_parent_request_uid"] = meta.ParentRequestUID
	}
	if meta.ParentResponseID != "" {
		attrs["lineage_parent_response_id"] = meta.ParentResponseID
	}
}

func applyCanonicalRequestLineage(canon *modelpkg.CanonicalRequest, meta requestLineage) {
	if canon == nil {
		return
	}
	canon.ConversationID = meta.ConversationID
	canon.ConversationRequestSeq = meta.ConversationRequestSeq
	canon.ConversationRequestID = meta.ConversationRequestID
	canon.LineageNodeID = meta.NodeID
	canon.LineageParentNodeID = meta.ParentNodeID
	canon.LineageParentRequestUID = meta.ParentRequestUID
	canon.LineageParentResponseID = meta.ParentResponseID
	canon.LineageRootMode = meta.RootMode
}

func recordResponsesLineageResult(r *http.Request, responseID string) {
	if r == nil || strings.TrimSpace(responseID) == "" {
		return
	}
	store := requestLineageStoreFromRequest(r)
	meta, ok := requestLineageFromRequest(r)
	if store == nil || !ok {
		return
	}
	store.recordResponseID(meta, responseID)
	if input, ok := tokenEstimatorObservationFromContext(r.Context()); ok {
		coordinate := input.Coordinate
		store.recordFinalizedEstimate(meta, requestLineageEstimatorFact{
			Bucket:                 tokenestimator.BucketKey{ProviderID: input.ProviderID, EndpointType: input.EndpointType, Model: input.FinalUpstreamModel},
			WireContextFingerprint: coordinate.WireContextFingerprint,
			LineageWireFingerprint: coordinate.LineageWireFingerprint,
			PrefixFingerprint:      coordinate.PrefixFingerprint,
			PrefixUnits:            coordinate.PrefixUnits,
			StructuralUnits:        coordinate.StructuralUnits,
			LocalEstimate:          coordinate.LocalEstimate,
			InputTokens:            input.Usage.InputTokens,
		})
	}
}
