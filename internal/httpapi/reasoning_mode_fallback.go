package httpapi

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/upstream"
)

type unsupportedReasoningModeError struct{}

func (unsupportedReasoningModeError) Error() string {
	return "provider does not support reasoning mode pro"
}

type reasoningModeFallbackCoordinator struct {
	fallback        model.CanonicalRequest
	eligible        bool
	retried         bool
	modeUnsupported bool
	cacheKey        reasoningModeNegativeCacheKey
}

const (
	reasoningModeNegativeCacheTTL        = 30 * time.Minute
	reasoningModeNegativeCacheMaxEntries = 256
)

type reasoningModeNegativeCacheKey struct {
	providerID      string
	rootVersion     string
	providerVersion string
	endpointType    string
	baseURL         string
	model           string
	authScope       [sha256.Size]byte
}

type reasoningModeNegativeCache struct {
	mu      sync.Mutex
	entries map[reasoningModeNegativeCacheKey]time.Time
}

var proReasoningModeNegativeCache = reasoningModeNegativeCache{entries: map[reasoningModeNegativeCacheKey]time.Time{}}

func reasoningModeFallbackKeyForRequest(r *http.Request, providerID string, providerCfg config.Config, finalModel string, authorization string) reasoningModeNegativeCacheKey {
	key := reasoningModeNegativeCacheKey{
		providerID:   providerID,
		endpointType: providerCfg.UpstreamEndpointType,
		baseURL:      providerCfg.UpstreamBaseURL,
		model:        finalModel,
		authScope:    sha256.Sum256([]byte(authorization)),
	}
	if snapshot, ok := runtimeSnapshotFromRequest(r); ok {
		key.rootVersion = snapshot.RootEnvVersion
		key.providerVersion = snapshot.ProviderVersionByID[providerID]
	}
	return key
}

func (c *reasoningModeNegativeCache) has(key reasoningModeNegativeCacheKey, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	expiresAt, ok := c.entries[key]
	if !ok {
		return false
	}
	if now.Before(expiresAt) {
		return true
	}
	delete(c.entries, key)
	return false
}

func (c *reasoningModeNegativeCache) add(key reasoningModeNegativeCacheKey, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for cachedKey, expiresAt := range c.entries {
		if !now.Before(expiresAt) {
			delete(c.entries, cachedKey)
		}
	}
	if _, exists := c.entries[key]; !exists && len(c.entries) >= reasoningModeNegativeCacheMaxEntries {
		var oldestKey reasoningModeNegativeCacheKey
		var oldestExpiration time.Time
		for cachedKey, expiresAt := range c.entries {
			if oldestExpiration.IsZero() || expiresAt.Before(oldestExpiration) {
				oldestKey = cachedKey
				oldestExpiration = expiresAt
			}
		}
		delete(c.entries, oldestKey)
	}
	c.entries[key] = now.Add(reasoningModeNegativeCacheTTL)
}

func prepareReasoningModeFallback(req model.CanonicalRequest, provider config.ProviderConfig, providerCfg config.Config, cacheKey reasoningModeNegativeCacheKey) (model.CanonicalRequest, *reasoningModeFallbackCoordinator, error) {
	if req.Reasoning == nil || req.Reasoning.Mode != model.ReasoningModePro {
		return req, nil, nil
	}

	capability := provider.ResolveReasoningModeProCapability(req.Model)
	if providerCfg.UpstreamEndpointType != config.UpstreamEndpointTypeResponses {
		capability = config.ReasoningModeProCapabilityUnsupported
	}
	if capability == config.ReasoningModeProCapabilityUnsupported {
		if req.ReasoningModeOrigin == model.ReasoningModeOriginBody {
			return req, nil, unsupportedReasoningModeError{}
		}
		if supportsAutomaticReasoningModeFallback(req.ReasoningModeOrigin) {
			removeReasoningMode(&req)
			return req, &reasoningModeFallbackCoordinator{modeUnsupported: true}, nil
		}
		return req, nil, nil
	}
	if capability == config.ReasoningModeProCapabilityProbe &&
		supportsAutomaticReasoningModeFallback(req.ReasoningModeOrigin) &&
		proReasoningModeNegativeCache.has(cacheKey, time.Now()) {
		removeReasoningMode(&req)
		return req, &reasoningModeFallbackCoordinator{modeUnsupported: true}, nil
	}

	coordinator := &reasoningModeFallbackCoordinator{
		fallback: cloneRequestWithoutReasoningMode(req),
		eligible: capability == config.ReasoningModeProCapabilityProbe &&
			supportsAutomaticReasoningModeFallback(req.ReasoningModeOrigin) &&
			isReasoningModeFallbackEligible(req),
		cacheKey: cacheKey,
	}
	return req, coordinator, nil
}

func (c *reasoningModeFallbackCoordinator) retryRequest(err error) (model.CanonicalRequest, bool) {
	if c == nil || !c.eligible || c.retried || !isReasoningModeUnsupportedError(err) {
		return model.CanonicalRequest{}, false
	}
	c.retried = true
	proReasoningModeNegativeCache.add(c.cacheKey, time.Now())
	return c.fallback, true
}

func executeWithReasoningModeFallback[T any](req model.CanonicalRequest, coordinator *reasoningModeFallbackCoordinator, execute func(model.CanonicalRequest) (T, error)) (model.CanonicalRequest, T, error) {
	result, err := execute(req)
	if fallback, ok := coordinator.retryRequest(err); ok {
		result, err = execute(fallback)
		return fallback, result, err
	}
	return req, result, err
}

func supportsAutomaticReasoningModeFallback(origin model.ReasoningModeOrigin) bool {
	return origin == model.ReasoningModeOriginSuffix || origin == model.ReasoningModeOriginProxyDefault
}

func cloneRequestWithoutReasoningMode(req model.CanonicalRequest) model.CanonicalRequest {
	cloned := req
	cloned.Reasoning = cloneCanonicalReasoning(req.Reasoning)
	removeReasoningMode(&cloned)
	return cloned
}

func removeReasoningMode(req *model.CanonicalRequest) {
	if req == nil || req.Reasoning == nil {
		return
	}
	req.Reasoning.Mode = ""
	delete(req.Reasoning.Raw, "mode")
	if req.Reasoning.Effort == "" && req.Reasoning.Summary == "" && len(req.Reasoning.Raw) == 0 {
		req.Reasoning = nil
	}
}

func isReasoningModeFallbackEligible(req model.CanonicalRequest) bool {
	if len(req.Tools) > 0 || req.ToolChoice.Mode != "" || req.ResponseStore != nil && *req.ResponseStore {
		return false
	}
	for _, include := range req.ResponseInclude {
		if strings.TrimSpace(include) == "reasoning.encrypted_content" {
			return false
		}
	}
	if len(req.ResponseMultiAgent) > 0 || previousResponseIDFromItems(req.ResponseInputItems) != "" {
		return false
	}
	for _, message := range req.Messages {
		if len(message.ToolCalls) > 0 || message.ToolCallID != "" || message.RecoveredToolCall != nil {
			return false
		}
	}
	for _, item := range req.ResponseInputItems {
		switch stringValue(item["type"]) {
		case "compaction", "function_call", "function_call_output", "item_reference", "program", "program_output", "reasoning":
			return false
		}
	}
	return true
}

func isReasoningModeUnsupportedError(err error) bool {
	var statusErr *upstream.HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != 400 {
		return false
	}
	var response struct {
		Error struct {
			Type        string `json:"type"`
			Param       string `json:"param"`
			Field       string `json:"field"`
			JSONPointer string `json:"json_pointer"`
			Pointer     string `json:"pointer"`
		} `json:"error"`
	}
	if json.Unmarshal(statusErr.BodyBytes, &response) != nil {
		return false
	}
	switch strings.TrimSpace(response.Error.Type) {
	case "invalid_request", "invalid_request_error":
	default:
		return false
	}
	for _, field := range []string{response.Error.Param, response.Error.Field, response.Error.JSONPointer, response.Error.Pointer} {
		normalized := strings.TrimSpace(field)
		if normalized == "reasoning.mode" || normalized == "/reasoning/mode" {
			return true
		}
	}
	return false
}
