package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"openai-compat-proxy/internal/aggregate"
	"openai-compat-proxy/internal/model"
)

type responsesHistoryStore struct {
	mu           sync.RWMutex
	entries      map[string]responsesConversationSnapshot
	byResponseID map[string]string
	toolCalls    map[string]responsesHistoryToolCallEntry
	order        []string
	maxSize      int

	toolCallRecoveryIndexPath   string
	toolCallRecoveryIndexLoaded bool
}

type responsesConversationSnapshot struct {
	Messages []model.CanonicalMessage
}

type responsesHistoryToolCallEntry struct {
	SnapshotKey     string
	Call            model.CanonicalToolCall
	ReasoningBlocks []map[string]any
}

const defaultResponsesHistoryMaxSize = 512

const responsesHistoryToolCallRecoveryIndexVersion = 1

type responsesHistoryToolCallRecoveryIndexFile struct {
	Version   int                                      `json:"version"`
	Order     []string                                 `json:"order,omitempty"`
	ToolCalls map[string]responsesHistoryToolCallEntry `json:"tool_calls"`
}

var globalResponsesHistory = &responsesHistoryStore{entries: map[string]responsesConversationSnapshot{}, byResponseID: map[string]string{}, toolCalls: map[string]responsesHistoryToolCallEntry{}, maxSize: defaultResponsesHistoryMaxSize}

func newResponsesHistoryStore(maxSize int, toolCallRecoveryIndexPath string) *responsesHistoryStore {
	if maxSize <= 0 {
		maxSize = defaultResponsesHistoryMaxSize
	}
	return &responsesHistoryStore{
		entries:                   map[string]responsesConversationSnapshot{},
		byResponseID:              map[string]string{},
		toolCalls:                 map[string]responsesHistoryToolCallEntry{},
		maxSize:                   maxSize,
		toolCallRecoveryIndexPath: strings.TrimSpace(toolCallRecoveryIndexPath),
	}
}

func ConfigureResponsesHistoryPersistence(providersDir string) {
	globalResponsesHistory = newResponsesHistoryStore(defaultResponsesHistoryMaxSize, responsesHistoryToolCallRecoveryIndexPath(providersDir))
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
	parts := []string{strings.TrimSpace(upstreamBaseURL), strings.TrimSpace(modelName), strings.TrimSpace(authMode), authorizationFingerprint(authorization)}
	return strings.Join(parts, "|")
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
			clone.ReasoningBlocks = cloneReasoningBlocks(msg.ReasoningBlocks)
		}
		cloned = append(cloned, clone)
	}
	return cloned
}

func (s *responsesHistoryStore) Save(providerID, responseID string, messages []model.CanonicalMessage, scopes ...string) {
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
	if err := s.loadToolCallRecoveryIndexLocked(); err != nil {
		log.Printf("warning: failed to load responses tool-call recovery index %q: %v", s.toolCallRecoveryIndexPath, err)
	}
	if _, exists := s.entries[key]; exists {
		s.removeKeyLocked(key)
		s.deleteToolCallsForKeyLocked(key)
	}
	storedMessages := cloneCanonicalMessages(messages)
	s.entries[key] = responsesConversationSnapshot{Messages: storedMessages}
	s.byResponseID[responseID] = key
	s.indexToolCallsLocked(providerID, key, storedMessages, firstString(scopes...))
	s.order = append(s.order, key)
	for len(s.order) > s.maxSize {
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
	if s == nil || providerID == "" || responseID == "" {
		return nil
	}
	s.mu.RLock()
	stored, ok := s.entries[responsesHistoryKey(providerID, responseID)]
	s.mu.RUnlock()
	if !ok || len(stored.Messages) == 0 {
		return nil
	}
	return cloneCanonicalMessages(stored.Messages)
}

func (s *responsesHistoryStore) LoadToolCall(providerID, callID string, scopes ...string) (model.CanonicalToolCall, []map[string]any, bool) {
	if s == nil || providerID == "" || callID == "" {
		return model.CanonicalToolCall{}, nil, false
	}
	s.mu.Lock()
	if err := s.loadToolCallRecoveryIndexLocked(); err != nil {
		log.Printf("warning: failed to load responses tool-call recovery index %q: %v", s.toolCallRecoveryIndexPath, err)
	}
	entry, ok := s.toolCalls[responsesHistoryScopedToolCallKey(providerID, callID, firstString(scopes...))]
	s.mu.Unlock()
	if !ok || entry.Call.ID == "" || entry.Call.Name == "" {
		return model.CanonicalToolCall{}, nil, false
	}
	return entry.Call, cloneReasoningBlocks(entry.ReasoningBlocks), true
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
	return cloneCanonicalMessages(stored.Messages)
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
	delete(s.entries, key)
	s.deleteToolCallsForKeyLocked(key)
	for responseID, indexedKey := range s.byResponseID {
		if indexedKey == key {
			delete(s.byResponseID, responseID)
			return
		}
	}
}

func (s *responsesHistoryStore) indexToolCallsLocked(providerID, snapshotKey string, messages []model.CanonicalMessage, scope string) {
	if providerID == "" || snapshotKey == "" || len(messages) == 0 {
		return
	}
	for _, msg := range messages {
		if msg.RecoveredToolCall != nil && msg.RecoveredToolCall.ID != "" && msg.RecoveredToolCall.Name != "" {
			call := *msg.RecoveredToolCall
			s.toolCalls[responsesHistoryScopedToolCallKey(providerID, call.ID, scope)] = responsesHistoryToolCallEntry{SnapshotKey: snapshotKey, Call: call, ReasoningBlocks: cloneReasoningBlocks(msg.ReasoningBlocks)}
		}
		if len(msg.ToolCalls) == 0 {
			continue
		}
		for _, call := range msg.ToolCalls {
			if call.ID == "" || call.Name == "" {
				continue
			}
			s.toolCalls[responsesHistoryScopedToolCallKey(providerID, call.ID, scope)] = responsesHistoryToolCallEntry{SnapshotKey: snapshotKey, Call: call, ReasoningBlocks: cloneReasoningBlocks(msg.ReasoningBlocks)}
		}
	}
}

func (s *responsesHistoryStore) loadToolCallRecoveryIndexLocked() error {
	if s == nil || s.toolCallRecoveryIndexLoaded || s.toolCallRecoveryIndexPath == "" {
		return nil
	}
	data, err := os.ReadFile(s.toolCallRecoveryIndexPath)
	if err != nil {
		if os.IsNotExist(err) {
			s.toolCallRecoveryIndexLoaded = true
			return nil
		}
		return err
	}
	var file responsesHistoryToolCallRecoveryIndexFile
	if err := json.Unmarshal(data, &file); err != nil {
		return err
	}
	s.toolCallRecoveryIndexLoaded = true
	if file.Version != 0 && file.Version != responsesHistoryToolCallRecoveryIndexVersion {
		return nil
	}
	if s.toolCalls == nil {
		s.toolCalls = map[string]responsesHistoryToolCallEntry{}
	}
	for key, entry := range file.ToolCalls {
		if key == "" || entry.Call.ID == "" || entry.Call.Name == "" {
			continue
		}
		s.toolCalls[key] = responsesHistoryToolCallEntry{SnapshotKey: entry.SnapshotKey, Call: entry.Call, ReasoningBlocks: cloneReasoningBlocks(entry.ReasoningBlocks)}
	}
	if len(s.order) == 0 && len(file.Order) > 0 {
		s.order = append([]string(nil), file.Order...)
	}
	return nil
}

func (s *responsesHistoryStore) saveToolCallRecoveryIndexLocked() error {
	if s == nil || s.toolCallRecoveryIndexPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.toolCallRecoveryIndexPath), 0o755); err != nil {
		return err
	}
	toolCalls := make(map[string]responsesHistoryToolCallEntry, len(s.toolCalls))
	for key, entry := range s.toolCalls {
		if key == "" || entry.Call.ID == "" || entry.Call.Name == "" {
			continue
		}
		toolCalls[key] = responsesHistoryToolCallEntry{SnapshotKey: entry.SnapshotKey, Call: entry.Call, ReasoningBlocks: cloneReasoningBlocks(entry.ReasoningBlocks)}
	}
	if len(toolCalls) == 0 {
		if err := os.Remove(s.toolCallRecoveryIndexPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(responsesHistoryToolCallRecoveryIndexFile{Version: responsesHistoryToolCallRecoveryIndexVersion, Order: append([]string(nil), s.order...), ToolCalls: toolCalls}, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteResponsesHistoryFile(s.toolCallRecoveryIndexPath, data)
}

func atomicWriteResponsesHistoryFile(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
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

func recoverToolCallsForMessages(messages []model.CanonicalMessage, providerID string, scopes ...string) []model.CanonicalMessage {
	if len(messages) == 0 || providerID == "" || globalResponsesHistory == nil {
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
		call, reasoningBlocks, ok := globalResponsesHistory.LoadToolCall(providerID, msg.ToolCallID, firstString(scopes...))
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

func recoverResponseItemReferencesForMessages(messages []model.CanonicalMessage, providerID string, scopes ...string) map[string]string {
	if len(messages) == 0 || providerID == "" || globalResponsesHistory == nil {
		return nil
	}
	references := map[string]string{}
	for _, msg := range messages {
		if msg.Role != "tool" || msg.ToolCallID == "" {
			continue
		}
		call, _, ok := globalResponsesHistory.LoadToolCall(providerID, msg.ToolCallID, firstString(scopes...))
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
	reasoningBlocks := cloneReasoningBlocks(result.ReasoningBlocks)
	if len(parts) == 0 && len(toolCalls) == 0 && len(reasoningBlocks) == 0 {
		return nil
	}
	msg := model.CanonicalMessage{Role: "assistant", Parts: parts, ToolCalls: toolCalls, ReasoningBlocks: reasoningBlocks}
	if summary, _ := result.Reasoning["summary"].(string); summary != "" {
		if !isSyntheticReasoningSummary(summary) {
			msg.ReasoningContent = summary
		}
	}
	return []model.CanonicalMessage{msg}
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

func filterSyntheticReasoningBlocks(blocks []map[string]any) []map[string]any {
	return cloneReasoningBlocks(blocks)
}

func isSyntheticResponsesReasoningBlock(block map[string]any) bool {
	if stringValue(block["id"]) != "rs_proxy" {
		return false
	}
	if stringValue(block["type"]) != "reasoning" {
		return false
	}
	if isSyntheticReasoningSummary(stringValue(block["thinking"])) || isSyntheticReasoningSummary(stringValue(block["text"])) {
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
	if len(summaries) == 0 {
		return true
	}
	for _, raw := range summaries {
		item, _ := raw.(map[string]any)
		if len(item) == 0 {
			continue
		}
		if !isSyntheticReasoningSummary(stringValue(item["text"])) && !isSyntheticReasoningSummary(stringValue(item["summary_text"])) {
			return false
		}
		if nested, _ := item["summary_text"].(map[string]any); len(nested) > 0 && !isSyntheticReasoningSummary(stringValue(nested["text"])) {
			return false
		}
	}
	return true
}

func isSyntheticReasoningBlock(block map[string]any) bool {
	if isSyntheticResponsesReasoningBlock(block) {
		return true
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
	snapshot := make([]model.CanonicalMessage, 0, len(base)+len(assistant))
	for _, msg := range base {
		switch msg.Role {
		case "user", "tool":
			if msg.Role == "tool" && shouldDropToolMessageFromHistory(msg) {
				continue
			}
			snapshot = append(snapshot, cloneCanonicalMessages([]model.CanonicalMessage{msg})...)
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				snapshot = append(snapshot, cloneCanonicalMessages([]model.CanonicalMessage{msg})...)
			}
		}
	}
	snapshot = append(snapshot, cloneCanonicalMessages(assistant)...)
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
