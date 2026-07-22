package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"openai-compat-proxy/internal/aggregate"
	"openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/syntaxrepair"
)

type responsesHistoryStore struct {
	mu             sync.RWMutex
	entries        map[string]responsesConversationSnapshot
	byResponseID   map[string]string
	toolCalls      map[string]responsesHistoryToolCallEntry
	opaqueThinking map[string]responsesHistoryOpaqueThinkingEntry
	order          []string
	maxSize        int
	maxBytes       int64
	retainedBytes  int64

	toolCallRecoveryIndexPath     string
	toolCallRecoveryIndexMaxBytes int64
	toolCallRecoveryIndexLoaded   bool
}

type responsesConversationSnapshot struct {
	Messages         []model.CanonicalMessage
	CompressedFields []responsesHistoryCompressedField
	Bytes            int64
	ResponseID       string
	Scope            string
	PortableScope    string
}

type responsesHistoryToolCallEntry struct {
	SnapshotKey           string
	Call                  model.CanonicalToolCall
	ReasoningBlocks       []map[string]any
	ToolCallSequenceHash  string `json:"tool_call_sequence_hash,omitempty"`
	ArgumentsCompressed   []byte `json:"arguments_compressed,omitempty"`
	ArgumentsOriginalSize int    `json:"arguments_original_size,omitempty"`
}

type responsesHistoryOpaqueThinkingEntry struct {
	SnapshotKey  string
	MessageIndex int
	BlockIndex   int
}

type responsesHistoryReplayProvenance struct {
	ProviderID                string `json:"provider_id"`
	DownstreamEndpoint        string `json:"downstream_endpoint"`
	UpstreamEndpointType      string `json:"upstream_endpoint_type"`
	NormalizedUpstreamBaseURL string `json:"normalized_upstream_base_url"`
	FinalUpstreamModel        string `json:"final_upstream_model"`
	CredentialFingerprint     string `json:"credential_fingerprint"`
	InboundCallerFingerprint  string `json:"inbound_caller_fingerprint"`
}

type responsesHistoryPortableProvenance struct {
	Purpose                  string `json:"purpose"`
	InboundCallerFingerprint string `json:"inbound_caller_fingerprint"`
}

const defaultResponsesHistoryMaxSize = 512

const defaultResponsesHistoryMaxBytes int64 = 256 << 20

const defaultResponsesHistoryToolCallRecoveryIndexMaxBytes int64 = defaultResponsesHistoryMaxBytes

const responsesHistoryToolCallRecoveryIndexVersion = 1

type responsesHistoryToolCallRecoveryIndexFile struct {
	Version   int                                      `json:"version"`
	Order     []string                                 `json:"order,omitempty"`
	ToolCalls map[string]responsesHistoryToolCallEntry `json:"tool_calls"`
}

func newResponsesHistoryStore(maxSize int, toolCallRecoveryIndexPath string) *responsesHistoryStore {
	if maxSize <= 0 {
		maxSize = defaultResponsesHistoryMaxSize
	}
	return &responsesHistoryStore{
		entries:                       map[string]responsesConversationSnapshot{},
		byResponseID:                  map[string]string{},
		toolCalls:                     map[string]responsesHistoryToolCallEntry{},
		opaqueThinking:                map[string]responsesHistoryOpaqueThinkingEntry{},
		maxSize:                       maxSize,
		maxBytes:                      defaultResponsesHistoryMaxBytes,
		toolCallRecoveryIndexPath:     strings.TrimSpace(toolCallRecoveryIndexPath),
		toolCallRecoveryIndexMaxBytes: defaultResponsesHistoryToolCallRecoveryIndexMaxBytes,
	}
}

func responsesHistoryToolCallRecoveryIndexPath(providersDir string) string {
	providersDir = strings.TrimSpace(providersDir)
	if providersDir == "" {
		return ""
	}
	return filepath.Join(providersDir, "Responses_History", "tool_call_recovery_index.json")
}

func responsesHistoryKey(providerID, responseID string) string {
	return providerID + "::" + responseID
}

func responsesHistoryToolCallKey(providerID, callID string) string {
	return providerID + "::" + callID
}

func responsesHistoryScopedToolCallKey(providerID, callID, scope string) string {
	if scope == "" {
		return responsesHistoryToolCallKey(providerID, callID)
	}
	return providerID + "::" + scope + "::" + callID
}

func responsesHistoryToolCallScope(upstreamBaseURL, modelName, authMode, authorization string) string {
	return responsesHistoryReplayScope(responsesHistoryReplayProvenance{
		NormalizedUpstreamBaseURL: normalizeResponsesHistoryUpstreamBaseURL(upstreamBaseURL),
		FinalUpstreamModel:        strings.TrimSpace(modelName),
		CredentialFingerprint:     authorizationFingerprint(authorization),
	})
}

func responsesHistoryReplayScope(provenance responsesHistoryReplayProvenance) string {
	provenance.ProviderID = strings.TrimSpace(provenance.ProviderID)
	provenance.DownstreamEndpoint = strings.TrimSpace(provenance.DownstreamEndpoint)
	provenance.UpstreamEndpointType = strings.TrimSpace(provenance.UpstreamEndpointType)
	provenance.NormalizedUpstreamBaseURL = normalizeResponsesHistoryUpstreamBaseURL(provenance.NormalizedUpstreamBaseURL)
	provenance.FinalUpstreamModel = strings.TrimSpace(provenance.FinalUpstreamModel)
	provenance.CredentialFingerprint = strings.TrimSpace(provenance.CredentialFingerprint)
	provenance.InboundCallerFingerprint = strings.TrimSpace(provenance.InboundCallerFingerprint)
	encoded, err := json.Marshal(provenance)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func responsesHistoryPortableScope(inboundCallerFingerprint string) string {
	provenance := responsesHistoryPortableProvenance{
		Purpose:                  "responses_portable_history_v1",
		InboundCallerFingerprint: strings.TrimSpace(inboundCallerFingerprint),
	}
	if provenance.InboundCallerFingerprint == "" {
		return ""
	}
	encoded, err := json.Marshal(provenance)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func normalizeResponsesHistoryUpstreamBaseURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return strings.TrimRight(strings.TrimSpace(raw), "/")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	if (parsed.Scheme == "http" && strings.HasSuffix(parsed.Host, ":80")) || (parsed.Scheme == "https" && strings.HasSuffix(parsed.Host, ":443")) {
		parsed.Host = strings.TrimSuffix(parsed.Host, ":80")
		parsed.Host = strings.TrimSuffix(parsed.Host, ":443")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func authorizationFingerprint(authorization string) string {
	trimmed := strings.TrimSpace(authorization)
	if trimmed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(trimmed))
	return hex.EncodeToString(sum[:16])
}

func firstString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func cloneCanonicalMessages(messages []model.CanonicalMessage) []model.CanonicalMessage {
	if len(messages) == 0 {
		return nil
	}
	cloned := make([]model.CanonicalMessage, 0, len(messages))
	for _, msg := range messages {
		reasoningContent := msg.ReasoningContent
		if isSyntheticReasoningSummary(reasoningContent) {
			reasoningContent = ""
		}
		clone := model.CanonicalMessage{Role: msg.Role, ToolCallID: msg.ToolCallID, ReasoningContent: reasoningContent}
		if msg.RecoveredToolCall != nil {
			recovered := *msg.RecoveredToolCall
			clone.RecoveredToolCall = &recovered
		}
		if len(msg.Parts) > 0 {
			clone.Parts = append([]model.CanonicalContentPart(nil), msg.Parts...)
		}
		if len(msg.ToolCalls) > 0 {
			clone.ToolCalls = append([]model.CanonicalToolCall(nil), msg.ToolCalls...)
		}
		if len(msg.ReasoningBlocks) > 0 {
			clone.ReasoningBlocks = cloneReasoningBlocksForHistory(msg.ReasoningBlocks)
		}
		cloned = append(cloned, clone)
	}
	return cloned
}

func (s *responsesHistoryStore) Save(providerID, responseID string, messages []model.CanonicalMessage, scopes ...string) {
	s.save(providerID, responseID, messages, firstString(scopes...), "")
}

func (s *responsesHistoryStore) SaveWithPortableScope(providerID, responseID string, messages []model.CanonicalMessage, scope, portableScope string) {
	s.save(providerID, responseID, messages, scope, portableScope)
}

func (s *responsesHistoryStore) save(providerID, responseID string, messages []model.CanonicalMessage, scope, portableScope string) {
	if s == nil || providerID == "" || responseID == "" || len(messages) == 0 {
		return
	}
	if s.maxSize <= 0 {
		s.maxSize = defaultResponsesHistoryMaxSize
	}
	key := responsesHistoryKey(providerID, responseID)
	s.mu.Lock()
	if s.entries == nil {
		s.entries = map[string]responsesConversationSnapshot{}
	}
	if s.byResponseID == nil {
		s.byResponseID = map[string]string{}
	}
	if s.toolCalls == nil {
		s.toolCalls = map[string]responsesHistoryToolCallEntry{}
	}
	if s.opaqueThinking == nil {
		s.opaqueThinking = map[string]responsesHistoryOpaqueThinkingEntry{}
	}
	if err := s.loadToolCallRecoveryIndexLocked(); err != nil {
		log.Printf("warning: failed to load responses tool-call recovery index %q: %v", s.toolCallRecoveryIndexPath, err)
	}
	s.removeKeyLocked(key)
	s.deleteKeyLocked(key)
	snapshotBytes := estimateCanonicalMessagesBytes(messages)
	recoveryBytes := estimateToolRecoveryBytes(messages)
	if s.maxBytes > 0 && snapshotBytes+recoveryBytes > s.maxBytes {
		if recoveryBytes == 0 || recoveryBytes > s.maxBytes {
			if err := s.saveToolCallRecoveryIndexLocked(); err != nil {
				log.Printf("warning: failed to save responses tool-call recovery index %q: %v", s.toolCallRecoveryIndexPath, err)
			}
			s.mu.Unlock()
			return
		}
		s.entries[key] = responsesConversationSnapshot{Bytes: recoveryBytes, ResponseID: responseID, Scope: scope, PortableScope: portableScope}
		s.retainedBytes += recoveryBytes
		s.indexToolCallsLocked(providerID, key, messages, scope, nil)
	} else {
		storedBytes := snapshotBytes + recoveryBytes
		snapshot, storedMessages := newResponsesConversationSnapshot(messages, storedBytes)
		snapshot.ResponseID = responseID
		snapshot.Scope = scope
		snapshot.PortableScope = portableScope
		s.entries[key] = snapshot
		s.retainedBytes += storedBytes
		s.byResponseID[responseID] = key
		s.indexToolCallsLocked(providerID, key, storedMessages, scope, &snapshot)
		s.indexOpaqueThinkingLocked(providerID, key, storedMessages, scope)
	}
	s.order = append(s.order, key)
	for len(s.order) > s.maxSize || (s.maxBytes > 0 && s.retainedBytes > s.maxBytes) {
		oldest := s.order[0]
		s.order = s.order[1:]
		s.deleteKeyLocked(oldest)
	}
	if err := s.saveToolCallRecoveryIndexLocked(); err != nil {
		log.Printf("warning: failed to save responses tool-call recovery index %q: %v", s.toolCallRecoveryIndexPath, err)
	}
	s.mu.Unlock()
}

func (s *responsesHistoryStore) Load(providerID, responseID string) []model.CanonicalMessage {
	return s.LoadScoped(providerID, responseID, "")
}

func (s *responsesHistoryStore) LoadScoped(providerID, responseID, scope string) []model.CanonicalMessage {
	if s == nil || providerID == "" || responseID == "" {
		return nil
	}
	s.mu.RLock()
	stored, ok := s.entries[responsesHistoryKey(providerID, responseID)]
	s.mu.RUnlock()
	if !ok || (scope != "" && stored.Scope != scope) || len(stored.Messages) == 0 {
		return nil
	}
	return loadResponsesConversationSnapshot(stored)
}

func (s *responsesHistoryStore) LoadPortable(responseID, portableScope string) []model.CanonicalMessage {
	if s == nil || responseID == "" || portableScope == "" {
		return nil
	}
	s.mu.RLock()
	var matched responsesConversationSnapshot
	matchCount := 0
	for _, snapshot := range s.entries {
		if snapshot.ResponseID != responseID || snapshot.PortableScope != portableScope || len(snapshot.Messages) == 0 {
			continue
		}
		matched = snapshot
		matchCount++
	}
	s.mu.RUnlock()
	if matchCount != 1 {
		return nil
	}
	return loadResponsesConversationSnapshot(matched)
}

func (s *responsesHistoryStore) LoadOpaqueThinking(providerID, scope string, publicBlock map[string]any) (map[string]any, bool) {
	if s == nil || providerID == "" || scope == "" {
		return nil, false
	}
	fingerprint, ok := responsesOpaqueThinkingPublicFingerprint(publicBlock)
	if !ok {
		return nil, false
	}
	s.mu.RLock()
	entry, found := s.opaqueThinking[responsesHistoryScopedToolCallKey(providerID, fingerprint, scope)]
	snapshot, snapshotFound := s.entries[entry.SnapshotKey]
	s.mu.RUnlock()
	if !found || !snapshotFound || entry.MessageIndex < 0 || entry.MessageIndex >= len(snapshot.Messages) {
		return nil, false
	}
	blocks := snapshot.Messages[entry.MessageIndex].ReasoningBlocks
	if entry.BlockIndex < 0 || entry.BlockIndex >= len(blocks) {
		return nil, false
	}
	block := cloneReasoningBlocksForHistory([]map[string]any{blocks[entry.BlockIndex]})
	if len(block) != 1 || !hasOpaqueThinkingState(block[0]) {
		return nil, false
	}
	return block[0], true
}

func (s *responsesHistoryStore) HasPortableOpaqueThinking(portableScope string, publicBlock map[string]any) bool {
	if s == nil || portableScope == "" {
		return false
	}
	fingerprint, ok := responsesOpaqueThinkingPublicFingerprint(publicBlock)
	if !ok {
		return false
	}
	s.mu.RLock()
	snapshots := make([]responsesConversationSnapshot, 0, len(s.entries))
	for _, snapshot := range s.entries {
		if snapshot.PortableScope == portableScope && len(snapshot.Messages) > 0 {
			snapshots = append(snapshots, snapshot)
		}
	}
	s.mu.RUnlock()
	matchCount := 0
	for _, snapshot := range snapshots {
		for _, message := range loadResponsesConversationSnapshot(snapshot) {
			for blockIndex, block := range message.ReasoningBlocks {
				if !hasOpaqueThinkingState(block) {
					continue
				}
				candidate, candidateOK := responsesOpaqueThinkingPublicFingerprint(responsesOpaqueThinkingPublicBlock(block, blockIndex))
				if !candidateOK || candidate != fingerprint {
					continue
				}
				matchCount++
				if matchCount > 1 {
					return false
				}
			}
		}
	}
	return matchCount == 1
}

func (s *responsesHistoryStore) LoadToolCall(providerID, callID string, scopes ...string) (model.CanonicalToolCall, []map[string]any, bool) {
	entry, ok := s.loadToolCallEntry(providerID, callID, scopes...)
	if !ok {
		return model.CanonicalToolCall{}, nil, false
	}
	return entry.Call, cloneReasoningBlocksForHistory(entry.ReasoningBlocks), true
}

func (s *responsesHistoryStore) loadToolCallEntry(providerID, callID string, scopes ...string) (responsesHistoryToolCallEntry, bool) {
	if s == nil || providerID == "" || callID == "" {
		return responsesHistoryToolCallEntry{}, false
	}
	s.mu.Lock()
	if err := s.loadToolCallRecoveryIndexLocked(); err != nil {
		log.Printf("warning: failed to load responses tool-call recovery index %q: %v", s.toolCallRecoveryIndexPath, err)
	}
	entry, ok := s.toolCalls[responsesHistoryScopedToolCallKey(providerID, callID, firstString(scopes...))]
	s.mu.Unlock()
	if !ok || entry.Call.ID == "" || entry.Call.Name == "" {
		return responsesHistoryToolCallEntry{}, false
	}
	arguments, ok := loadResponsesHistoryToolCallArguments(entry)
	if !ok {
		return responsesHistoryToolCallEntry{}, false
	}
	entry.Call.Arguments = arguments
	return entry, true
}

func (s *responsesHistoryStore) LoadAny(responseID string) []model.CanonicalMessage {
	if s == nil || responseID == "" {
		return nil
	}
	s.mu.RLock()
	key := s.byResponseID[responseID]
	stored, ok := s.entries[key]
	s.mu.RUnlock()
	if !ok || len(stored.Messages) == 0 {
		return nil
	}
	return loadResponsesConversationSnapshot(stored)
}

func (s *responsesHistoryStore) removeKeyLocked(target string) {
	for idx, key := range s.order {
		if key != target {
			continue
		}
		s.order = append(s.order[:idx], s.order[idx+1:]...)
		return
	}
}

func (s *responsesHistoryStore) deleteKeyLocked(key string) {
	if snapshot, ok := s.entries[key]; ok {
		s.retainedBytes -= snapshot.Bytes
		if s.retainedBytes < 0 {
			s.retainedBytes = 0
		}
	}
	delete(s.entries, key)
	s.deleteToolCallsForKeyLocked(key)
	s.deleteOpaqueThinkingForKeyLocked(key)
	for responseID, indexedKey := range s.byResponseID {
		if indexedKey == key {
			delete(s.byResponseID, responseID)
			return
		}
	}
}

func (s *responsesHistoryStore) deleteOpaqueThinkingForKeyLocked(snapshotKey string) {
	if snapshotKey == "" || len(s.opaqueThinking) == 0 {
		return
	}
	for key, entry := range s.opaqueThinking {
		if entry.SnapshotKey == snapshotKey {
			delete(s.opaqueThinking, key)
		}
	}
}

func (s *responsesHistoryStore) indexToolCallsLocked(providerID, snapshotKey string, messages []model.CanonicalMessage, scope string, snapshot *responsesConversationSnapshot) {
	if providerID == "" || snapshotKey == "" || len(messages) == 0 {
		return
	}
	for messageIndex, msg := range messages {
		if msg.RecoveredToolCall != nil && msg.RecoveredToolCall.ID != "" && msg.RecoveredToolCall.Name != "" {
			call := *msg.RecoveredToolCall
			stored := responsesHistoryToolCallEntry{SnapshotKey: snapshotKey, Call: call, ReasoningBlocks: cloneReasoningBlocksForHistory(msg.ReasoningBlocks)}
			data, originalSize, ok := responsesHistoryCompressedFieldData(snapshot, messageIndex, 0, responsesHistoryCompressedRecoveredToolArguments)
			s.toolCalls[responsesHistoryScopedToolCallKey(providerID, call.ID, scope)] = compressResponsesHistoryToolCallEntryWithSnapshotField(stored, data, originalSize, ok)
		}
		sequenceHash := assistantToolCallSequenceHash(msg.ToolCalls)
		for toolIndex, call := range msg.ToolCalls {
			if call.ID == "" || call.Name == "" {
				continue
			}
			stored := responsesHistoryToolCallEntry{SnapshotKey: snapshotKey, Call: call, ReasoningBlocks: cloneReasoningBlocksForHistory(msg.ReasoningBlocks), ToolCallSequenceHash: sequenceHash}
			data, originalSize, ok := responsesHistoryCompressedFieldData(snapshot, messageIndex, toolIndex, responsesHistoryCompressedToolArguments)
			s.toolCalls[responsesHistoryScopedToolCallKey(providerID, call.ID, scope)] = compressResponsesHistoryToolCallEntryWithSnapshotField(stored, data, originalSize, ok)
		}
	}
}

func (s *responsesHistoryStore) indexOpaqueThinkingLocked(providerID, snapshotKey string, messages []model.CanonicalMessage, scope string) {
	if providerID == "" || snapshotKey == "" || scope == "" || len(messages) == 0 {
		return
	}
	for messageIndex, message := range messages {
		if message.Role != "assistant" {
			continue
		}
		for blockIndex, block := range message.ReasoningBlocks {
			if !hasOpaqueThinkingState(block) {
				continue
			}
			publicBlock := responsesOpaqueThinkingPublicBlock(block, blockIndex)
			fingerprint, ok := responsesOpaqueThinkingPublicFingerprint(publicBlock)
			if !ok {
				continue
			}
			s.opaqueThinking[responsesHistoryScopedToolCallKey(providerID, fingerprint, scope)] = responsesHistoryOpaqueThinkingEntry{SnapshotKey: snapshotKey, MessageIndex: messageIndex, BlockIndex: blockIndex}
		}
	}
}

func hasOpaqueThinkingState(block map[string]any) bool {
	return strings.TrimSpace(stringValue(block["signature"])) != "" || strings.TrimSpace(stringValue(block["encrypted_content"])) != ""
}

func responsesOpaqueThinkingPublicBlock(block map[string]any, index int) map[string]any {
	public := cloneJSONValueForResponse(block).(map[string]any)
	if stringValue(public["type"]) == "thinking" {
		public["type"] = "reasoning"
	}
	if signature := stringValue(public["signature"]); signature != "" && stringValue(public["encrypted_content"]) == "" {
		public["encrypted_content"] = signature
	}
	delete(public, "signature")
	if stringValue(public["id"]) == "" {
		if index == 0 {
			public["id"] = "rs_upstream"
		} else {
			public["id"] = "rs_upstream_" + strconv.Itoa(index)
		}
	}
	return public
}

func responsesOpaqueThinkingPublicFingerprint(block map[string]any) (string, bool) {
	if len(block) == 0 || stringValue(block["type"]) != "reasoning" || strings.TrimSpace(stringValue(block["encrypted_content"])) == "" || strings.TrimSpace(stringValue(block["signature"])) != "" {
		return "", false
	}
	encoded, err := json.Marshal(block)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), true
}

func (s *responsesHistoryStore) loadToolCallRecoveryIndexLocked() error {
	if s == nil || s.toolCallRecoveryIndexLoaded || s.toolCallRecoveryIndexPath == "" {
		return nil
	}
	maxBytes := s.toolCallRecoveryIndexMaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultResponsesHistoryToolCallRecoveryIndexMaxBytes
	}
	fileHandle, err := os.Open(s.toolCallRecoveryIndexPath)
	if err != nil {
		if os.IsNotExist(err) {
			s.toolCallRecoveryIndexLoaded = true
			return nil
		}
		s.toolCallRecoveryIndexLoaded = true
		return err
	}
	defer fileHandle.Close()
	data, err := io.ReadAll(io.LimitReader(fileHandle, maxBytes+1))
	s.toolCallRecoveryIndexLoaded = true
	if err != nil {
		return err
	}
	if int64(len(data)) > maxBytes {
		return fmt.Errorf("responses tool-call recovery index exceeds %d bytes", maxBytes)
	}
	var file responsesHistoryToolCallRecoveryIndexFile
	if err := json.Unmarshal(data, &file); err != nil {
		return err
	}
	if file.Version != 0 && file.Version != responsesHistoryToolCallRecoveryIndexVersion {
		return nil
	}
	if s.toolCalls == nil {
		s.toolCalls = map[string]responsesHistoryToolCallEntry{}
	}
	if s.entries == nil {
		s.entries = map[string]responsesConversationSnapshot{}
	}
	retainedBytesBySnapshot := map[string]int64{}
	for key, entry := range file.ToolCalls {
		if key == "" || entry.SnapshotKey == "" || entry.Call.ID == "" || entry.Call.Name == "" {
			continue
		}
		stored := responsesHistoryToolCallEntry{SnapshotKey: entry.SnapshotKey, Call: entry.Call, ReasoningBlocks: cloneReasoningBlocksForHistory(entry.ReasoningBlocks), ToolCallSequenceHash: entry.ToolCallSequenceHash, ArgumentsCompressed: append([]byte(nil), entry.ArgumentsCompressed...), ArgumentsOriginalSize: entry.ArgumentsOriginalSize}
		var valid bool
		stored, valid = normalizeResponsesHistoryToolCallEntry(stored)
		if !valid {
			continue
		}
		s.toolCalls[key] = stored
		retainedBytesBySnapshot[stored.SnapshotKey] += estimateResponsesHistoryToolCallEntryBytes(stored)
	}
	if len(s.order) == 0 {
		seen := map[string]bool{}
		for _, key := range file.Order {
			if key == "" || seen[key] || retainedBytesBySnapshot[key] == 0 {
				continue
			}
			seen[key] = true
			s.order = append(s.order, key)
		}
		for key := range retainedBytesBySnapshot {
			if seen[key] {
				continue
			}
			s.order = append(s.order, key)
			seen[key] = true
		}
		for _, key := range s.order {
			retainedBytes := retainedBytesBySnapshot[key]
			s.entries[key] = responsesConversationSnapshot{Bytes: retainedBytes}
			s.retainedBytes += retainedBytes
		}
		for len(s.order) > s.maxSize || (s.maxBytes > 0 && s.retainedBytes > s.maxBytes) {
			oldest := s.order[0]
			s.order = s.order[1:]
			s.deleteKeyLocked(oldest)
		}
		if len(s.toolCalls) != len(file.ToolCalls) || len(s.order) != len(file.Order) {
			if err := s.saveToolCallRecoveryIndexLocked(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *responsesHistoryStore) deleteToolCallsForKeyLocked(snapshotKey string) {
	if snapshotKey == "" || len(s.toolCalls) == 0 {
		return
	}
	for key, entry := range s.toolCalls {
		if entry.SnapshotKey == snapshotKey {
			delete(s.toolCalls, key)
		}
	}
}

func previousResponseIDFromItems(items []map[string]any) string {
	for _, item := range items {
		preserved, _ := item["__openai_compat_responses_top_level"].(map[string]any)
		if preserved == nil {
			continue
		}
		if responseID, _ := preserved["previous_response_id"].(string); responseID != "" {
			return responseID
		}
	}
	return ""
}

func shouldRestorePreviousConversation(messages []model.CanonicalMessage) bool {
	if len(messages) == 0 {
		return true
	}
	for _, msg := range messages {
		if msg.Role == "assistant" {
			return false
		}
	}
	return true
}

func recoverToolCallsForMessages(history *responsesHistoryStore, messages []model.CanonicalMessage, providerID string, scopes ...string) []model.CanonicalMessage {
	if len(messages) == 0 || providerID == "" || history == nil {
		return messages
	}
	existingToolCallIDs := currentToolCallIDs(messages)
	recovered := append([]model.CanonicalMessage(nil), messages...)
	for idx := range recovered {
		msg := &recovered[idx]
		if msg.Role != "tool" || msg.ToolCallID == "" || msg.RecoveredToolCall != nil {
			continue
		}
		if existingToolCallIDs[msg.ToolCallID] {
			continue
		}
		call, reasoningBlocks, ok := history.LoadToolCall(providerID, msg.ToolCallID, firstString(scopes...))
		if !ok {
			continue
		}
		msg.RecoveredToolCall = &call
		if len(reasoningBlocks) > 0 && len(msg.ReasoningBlocks) == 0 {
			msg.ReasoningBlocks = reasoningBlocks
		}
	}
	return recovered
}

func recoverAnthropicThinkingForAssistantToolCalls(history *responsesHistoryStore, messages []model.CanonicalMessage, providerID string, scopes ...string) []model.CanonicalMessage {
	if len(messages) == 0 || providerID == "" || history == nil {
		return messages
	}
	scope := firstString(scopes...)
	if scope == "" {
		return messages
	}
	recovered := append([]model.CanonicalMessage(nil), messages...)
	for messageIndex := range recovered {
		message := &recovered[messageIndex]
		if message.Role != "assistant" || len(message.ToolCalls) == 0 || len(message.ReasoningBlocks) > 0 {
			continue
		}

		var serverBlocks []map[string]any
		sequenceHash := assistantToolCallSequenceHash(message.ToolCalls)
		if sequenceHash == "" {
			continue
		}
		matched := true
		seenToolCallIDs := map[string]struct{}{}
		for _, candidate := range message.ToolCalls {
			if strings.TrimSpace(candidate.ID) == "" {
				matched = false
				break
			}
			if _, duplicate := seenToolCallIDs[candidate.ID]; duplicate {
				matched = false
				break
			}
			seenToolCallIDs[candidate.ID] = struct{}{}
			storedEntry, ok := history.loadToolCallEntry(providerID, candidate.ID, scope)
			if !ok || storedEntry.ToolCallSequenceHash != sequenceHash || len(storedEntry.ReasoningBlocks) == 0 || !assistantToolCallsMatchForAnthropicThinkingReplay(candidate, storedEntry.Call) {
				matched = false
				break
			}
			blocks := storedEntry.ReasoningBlocks
			if serverBlocks == nil {
				serverBlocks = blocks
				continue
			}
			if !reasoningBlocksMatchForAnthropicThinkingReplay(serverBlocks, blocks) {
				matched = false
				break
			}
		}
		if matched && len(serverBlocks) > 0 {
			message.ReasoningBlocks = serverBlocks
		}
	}
	return recovered
}

func assistantToolCallSequenceHash(toolCalls []model.CanonicalToolCall) string {
	if len(toolCalls) == 0 {
		return ""
	}
	normalized := make([]map[string]string, 0, len(toolCalls))
	for _, call := range toolCalls {
		if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Name) == "" {
			return ""
		}
		normalized = append(normalized, map[string]string{
			"id":        strings.TrimSpace(call.ID),
			"type":      normalizedAnthropicThinkingReplayToolType(call.Type),
			"name":      strings.TrimSpace(call.Name),
			"arguments": normalizedAnthropicThinkingReplayArguments(call.Arguments),
		})
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func assistantToolCallsMatchForAnthropicThinkingReplay(candidate, stored model.CanonicalToolCall) bool {
	if strings.TrimSpace(candidate.ID) == "" || candidate.ID != stored.ID {
		return false
	}
	if normalizedAnthropicThinkingReplayToolType(candidate.Type) != normalizedAnthropicThinkingReplayToolType(stored.Type) {
		return false
	}
	if strings.TrimSpace(candidate.Name) != strings.TrimSpace(stored.Name) {
		return false
	}
	return normalizedAnthropicThinkingReplayArguments(candidate.Arguments) == normalizedAnthropicThinkingReplayArguments(stored.Arguments)
}

func normalizedAnthropicThinkingReplayToolType(value string) string {
	if normalized := strings.TrimSpace(value); normalized != "" {
		return normalized
	}
	return "function"
}

func normalizedAnthropicThinkingReplayArguments(value string) string {
	trimmed := strings.TrimSpace(value)
	if normalized, ok := syntaxrepair.RepairJSON(trimmed); ok {
		return normalized
	}
	return trimmed
}

func reasoningBlocksMatchForAnthropicThinkingReplay(left, right []map[string]any) bool {
	if len(left) != len(right) {
		return false
	}
	encodedLeft, err := json.Marshal(left)
	if err != nil {
		return false
	}
	encodedRight, err := json.Marshal(right)
	if err != nil {
		return false
	}
	return string(encodedLeft) == string(encodedRight)
}

func saveChatAnthropicThinkingHistory(history *responsesHistoryStore, providerID, scope string, messages []model.CanonicalMessage, result aggregate.Result) {
	if history == nil || providerID == "" || scope == "" || strings.TrimSpace(result.ResponseID) == "" {
		return
	}
	saveResponsesHistorySnapshot(history, providerID, result.ResponseID, messages, assistantHistoryMessagesFromResult(result), scope)
}

func recoverResponseItemReferencesForMessages(history *responsesHistoryStore, messages []model.CanonicalMessage, providerID string, scopes ...string) map[string]string {
	if len(messages) == 0 || providerID == "" || history == nil {
		return nil
	}
	references := map[string]string{}
	for _, msg := range messages {
		if msg.Role != "tool" || msg.ToolCallID == "" {
			continue
		}
		call, _, ok := history.LoadToolCall(providerID, msg.ToolCallID, firstString(scopes...))
		if !ok || strings.TrimSpace(call.ResponseItemID) == "" {
			continue
		}
		references[msg.ToolCallID] = strings.TrimSpace(call.ResponseItemID)
	}
	if len(references) == 0 {
		return nil
	}
	return references
}

func currentToolCallIDs(messages []model.CanonicalMessage) map[string]bool {
	ids := map[string]bool{}
	for _, msg := range messages {
		for _, call := range msg.ToolCalls {
			if call.ID != "" {
				ids[call.ID] = true
			}
		}
		for _, block := range msg.OrderedContent {
			if block.Type == "tool_call" && block.ToolCall.ID != "" {
				ids[block.ToolCall.ID] = true
			}
		}
	}
	return ids
}

func assistantHistoryMessagesFromResult(result aggregate.Result) []model.CanonicalMessage {
	parts := make([]model.CanonicalContentPart, 0, len(result.ResponseMessageContent))
	for _, part := range result.ResponseMessageContent {
		if partType, _ := part["type"].(string); partType == "output_text" {
			if text, _ := part["text"].(string); text != "" {
				parts = append(parts, model.CanonicalContentPart{Type: "text", Text: text})
			}
		}
	}
	toolCalls := make([]model.CanonicalToolCall, 0, len(result.ToolCalls))
	for _, call := range result.ToolCalls {
		callID := call.CallID
		if callID == "" {
			callID = call.ID
		}
		toolCalls = append(toolCalls, model.CanonicalToolCall{ID: callID, ResponseItemID: call.ID, Type: "function", Name: call.Name, Arguments: call.Arguments})
	}
	reasoningBlocks := reasoningBlocksFromResult(result)
	if len(parts) == 0 && len(toolCalls) == 0 && len(reasoningBlocks) == 0 {
		return nil
	}
	msg := model.CanonicalMessage{Role: "assistant", Parts: parts, ToolCalls: toolCalls, ReasoningBlocks: reasoningBlocks}
	if reasoningContent := reasoningContentFromResult(result); reasoningContent != "" && !isSyntheticReasoningSummary(reasoningContent) {
		msg.ReasoningContent = reasoningContent
	}
	return []model.CanonicalMessage{msg}
}

func reasoningContentFromResult(result aggregate.Result) string {
	for _, key := range []string{"reasoning_content", "thinking", "summary", "content", "delta", "text"} {
		if value := stringValue(result.Reasoning[key]); value != "" && !isSyntheticReasoningSummary(value) {
			return value
		}
	}
	return ""
}

func reasoningBlocksFromResult(result aggregate.Result) []map[string]any {
	if len(result.ResponseOutputItems) > 0 {
		outputReasoning := make([]map[string]any, 0, len(result.ResponseOutputItems))
		seenOutputReasoning := make(map[string]int)
		for _, item := range result.ResponseOutputItems {
			if stringValue(item["type"]) != "reasoning" || model.IsSyntheticResponsesReasoningPlaceholder(item) {
				continue
			}
			if itemID := stringValue(item["id"]); itemID != "" {
				if index, exists := seenOutputReasoning[itemID]; exists {
					outputReasoning[index] = item
					continue
				}
				seenOutputReasoning[itemID] = len(outputReasoning)
			}
			outputReasoning = append(outputReasoning, item)
		}
		if len(outputReasoning) > 0 {
			return cloneResponsesOutputReasoningBlocks(outputReasoning)
		}
	}
	return cloneReasoningBlocksForHistory(result.ReasoningBlocks)
}

func cloneResponsesOutputReasoningBlocks(blocks []map[string]any) []map[string]any {
	if len(blocks) == 0 {
		return nil
	}
	cloned := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		if len(block) == 0 || model.IsSyntheticResponsesReasoningPlaceholder(block) {
			continue
		}
		copied := make(map[string]any, len(block))
		for key, value := range block {
			copied[key] = value
		}
		cloned = append(cloned, copied)
	}
	if len(cloned) == 0 {
		return nil
	}
	return cloned
}

func cloneReasoningBlocks(blocks []map[string]any) []map[string]any {
	if len(blocks) == 0 {
		return nil
	}
	cloned := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		if len(block) == 0 {
			continue
		}
		if isSyntheticReasoningBlock(block) {
			continue
		}
		copied := make(map[string]any, len(block))
		for k, v := range block {
			copied[k] = v
		}
		cloned = append(cloned, copied)
	}
	if len(cloned) == 0 {
		return nil
	}
	return cloned
}

func cloneReasoningBlocksForHistory(blocks []map[string]any) []map[string]any {
	if len(blocks) == 0 {
		return nil
	}
	cloned := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		if len(block) == 0 {
			continue
		}
		if isSyntheticReasoningBlock(block) && !isRealResponsesReasoningState(block) {
			continue
		}
		copied := make(map[string]any, len(block))
		for key, value := range block {
			copied[key] = value
		}
		cloned = append(cloned, copied)
	}
	if len(cloned) == 0 {
		return nil
	}
	return cloned
}

func isRealResponsesReasoningState(block map[string]any) bool {
	return stringValue(block["type"]) == "reasoning" && stringValue(block["id"]) != "" && model.HasResponsesReasoningState(block)
}

func filterSyntheticReasoningBlocks(blocks []map[string]any) []map[string]any {
	return cloneReasoningBlocks(blocks)
}

func isSyntheticReasoningBlock(block map[string]any) bool {
	if stringValue(block["type"]) == "reasoning" && stringValue(block["id"]) == "rs_proxy" {
		return model.IsSyntheticResponsesReasoningPlaceholder(block)
	}
	if isSyntheticReasoningSummary(stringValue(block["thinking"])) {
		return true
	}
	if isSyntheticReasoningSummary(stringValue(block["text"])) {
		return true
	}
	var summaries []any
	switch typed := block["summary"].(type) {
	case []any:
		summaries = typed
	case []map[string]any:
		for _, item := range typed {
			summaries = append(summaries, item)
		}
	}
	for _, raw := range summaries {
		item, _ := raw.(map[string]any)
		if len(item) == 0 {
			continue
		}
		if isSyntheticReasoningSummary(stringValue(item["text"])) {
			return true
		}
		if isSyntheticReasoningSummary(stringValue(item["summary_text"])) {
			return true
		}
		if nested, _ := item["summary_text"].(map[string]any); len(nested) > 0 {
			if isSyntheticReasoningSummary(stringValue(nested["text"])) {
				return true
			}
		}
	}
	return false
}

func isSyntheticReasoningSummary(summary string) bool {
	if isInvisibleReasoningResidue(summary) {
		return true
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return false
	}
	return strings.Contains(summary, "代理层占位")
}

func isInvisibleReasoningResidue(summary string) bool {
	if summary == "" {
		return false
	}
	stripped := strings.ReplaceAll(summary, "\u200b", "")
	stripped = strings.ReplaceAll(stripped, "\ufeff", "")
	return stripped != summary && strings.TrimSpace(stripped) == ""
}

func buildResponsesHistorySnapshot(base []model.CanonicalMessage, assistant []model.CanonicalMessage) []model.CanonicalMessage {
	return cloneCanonicalMessages(selectResponsesHistoryMessages(base, assistant))
}

func saveResponsesHistorySnapshot(history *responsesHistoryStore, providerID, responseID string, base []model.CanonicalMessage, assistant []model.CanonicalMessage, scopes ...string) {
	var portableScope string
	if len(scopes) > 1 {
		portableScope = scopes[1]
	}
	history.SaveWithPortableScope(providerID, responseID, selectResponsesHistoryMessages(base, assistant), firstString(scopes...), portableScope)
}

func selectResponsesHistoryMessages(base []model.CanonicalMessage, assistant []model.CanonicalMessage) []model.CanonicalMessage {
	snapshot := make([]model.CanonicalMessage, 0, len(base)+len(assistant))
	for _, msg := range base {
		switch msg.Role {
		case "user", "tool":
			if msg.Role == "tool" && shouldDropToolMessageFromHistory(msg) {
				continue
			}
			snapshot = append(snapshot, msg)
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				snapshot = append(snapshot, msg)
			}
		}
	}
	snapshot = append(snapshot, assistant...)
	return snapshot
}

func mergeConversationHistory(base []model.CanonicalMessage, assistant []model.CanonicalMessage) []model.CanonicalMessage {
	merged := make([]model.CanonicalMessage, 0, len(base)+len(assistant))
	merged = append(merged, cloneCanonicalMessages(base)...)
	merged = append(merged, cloneCanonicalMessages(assistant)...)
	return merged
}

func shouldDropToolMessageFromHistory(msg model.CanonicalMessage) bool {
	if msg.Role != "tool" {
		return false
	}
	text := strings.TrimSpace(toolMessageText(msg))
	if text == "" {
		return false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return false
	}
	errorValue, hasError := payload["error"]
	return hasError && errorValue != nil
}

func toolMessageText(msg model.CanonicalMessage) string {
	var builder strings.Builder
	for _, part := range msg.Parts {
		if part.Type == "text" {
			builder.WriteString(part.Text)
		}
	}
	return builder.String()
}
