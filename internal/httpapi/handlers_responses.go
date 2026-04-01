package httpapi

import (
	"context"
	"errors"
	"net/http"

	responsesadapter "openai-compat-proxy/internal/adapter/responses"
	"openai-compat-proxy/internal/aggregate"
	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/errorsx"
	"openai-compat-proxy/internal/logging"
	"openai-compat-proxy/internal/upstream"
)

func handleResponses() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		providerCfg := providerConfigForRequest(r)
		provider, ok := providerForRequest(r)
		if !ok || !provider.SupportsResponses {
			errorsx.WriteJSON(w, http.StatusBadRequest, "unsupported_provider_contract", "provider does not support responses")
			return
		}
		client := upstream.NewClient(providerCfg.UpstreamBaseURL, providerCfg)
		setNormalizationVersionHeader(w)
		requestID := w.Header().Get("X-Request-Id")
		providerID := provider.ID
		usageRecorder := cacheInfoUsageRecorder(r, requestID, providerID)
		authorization, err := authHeaderForUpstream(r, providerCfg)
		if err != nil {
			errorsx.WriteJSON(w, http.StatusUnauthorized, "missing_upstream_auth", err.Error())
			return
		}

		canon, err := responsesadapter.DecodeRequest(r.Body)
		if err != nil {
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if providerCfg.UpstreamEndpointType != config.UpstreamEndpointTypeResponses && shouldRestorePreviousConversation(canon.Messages) {
			if previousResponseID := previousResponseIDFromItems(canon.ResponseInputItems); previousResponseID != "" {
				if history := globalResponsesHistory.Load(providerID, previousResponseID); len(history) > 0 {
					canon.Messages = append(history, canon.Messages...)
				}
			}
		}
		canon.Messages = dedupeCanonicalToolMessages(canon.Messages)
		applyProviderSystemPrompt(&canon, provider)
		if ok {
			mappedModel, effort := provider.ResolveModelAndEffort(canon.Model, provider.EnableReasoningEffortSuffix)
			canon.Model = mappedModel
			canon.Reasoning = applyResolvedReasoningEffort(canon.Reasoning, effort)
			if providerCfg.UpstreamEndpointType == config.UpstreamEndpointTypeAnthropic {
				canon.Reasoning = applyAnthropicThinkingFromResolvedEffort(canon.Reasoning, provider.MapReasoningSuffixToAnthropicThinking, canon.Model, canon.MaxOutputTokens)
			}
		}
		canon.RequestID = requestID
		canon.AuthMode = authModeForUpstream(r, providerCfg)
		attrs := map[string]any{
			"request_id":    canon.RequestID,
			"route":         "/v1/responses",
			"auth_mode":     canon.AuthMode,
			"model":         canon.Model,
			"stream":        canon.Stream,
			"include_usage": canon.IncludeUsage,
			"message_count": len(canon.Messages),
			"tool_count":    len(canon.Tools),
			"has_reasoning": canon.Reasoning != nil,
		}
		for k, v := range canonicalLogAttrs(canon) {
			attrs[k] = v
		}
		logging.Event("proxyBuiltCanonicalRequest", attrs)

		ctx := r.Context()
		var cancel context.CancelFunc
		if providerCfg.TotalTimeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, providerCfg.TotalTimeout)
			defer cancel()
		}

		if canon.Stream {
			stream, err := client.OpenEventStream(ctx, canon, authorization)
			if err != nil {
				if isUpstreamTimeout(err, ctx) {
					errorsx.WriteJSON(w, http.StatusGatewayTimeout, "upstream_timeout", "upstream request timed out")
					return
				}
				if writeUpstreamError(w, err) {
					return
				}
				errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
				return
			}
			defer stream.Close()
			flusher := startSSE(w)
			result, err := writeResponsesSSELive(ctx, stream, w, flusher, canon, providerCfg.UpstreamEndpointType, usageRecorder)
			if err != nil {
				var terminalFailure *aggregate.TerminalFailureError
				if errors.As(err, &terminalFailure) {
					return
				}
				if isUpstreamTimeout(err, ctx) {
					_ = writeResponsesTerminalFailure(w, flusher, canon.RequestID, "upstream_timeout", "upstream request timed out")
					return
				}
				_ = writeResponsesTerminalFailure(w, flusher, canon.RequestID, "upstreamStreamBroken", err.Error())
				return
			}
			if responseID, _ := responsesadapter.BuildResponse(result)["id"].(string); responseID != "" {
				globalResponsesHistory.Save(providerID, responseID, buildResponsesHistorySnapshot(canon.Messages, assistantHistoryMessagesFromResult(result)))
			}
			return
		}

		if providerCfg.DownstreamNonStreamStrategy == config.DownstreamNonStreamStrategyUpstreamNonStream {
			payload, err := client.Response(ctx, canon, authorization)
			if err != nil {
				if isUpstreamTimeout(err, ctx) {
					errorsx.WriteJSON(w, http.StatusGatewayTimeout, "upstream_timeout", "upstream request timed out")
					return
				}
				if writeUpstreamError(w, err) {
					return
				}
				errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
				return
			}
			result, err := aggregate.ResultFromResponsePayload(payload)
			if err != nil {
				errorsx.WriteJSON(w, http.StatusBadGateway, "invalid_upstream_response", err.Error())
				return
			}
			normalized := responsesadapter.BuildResponse(result)
			if responseID, _ := normalized["id"].(string); responseID != "" {
				globalResponsesHistory.Save(providerID, responseID, buildResponsesHistorySnapshot(canon.Messages, assistantHistoryMessagesFromResult(result)))
			}
			mergePreservedResponsesTopLevelFields(normalized, canon.ResponseInputItems)
			w.Header().Set("Content-Type", "application/json")
			if err := writeJSON(w, normalized); err != nil {
				errorsx.WriteJSON(w, http.StatusInternalServerError, "encode_error", err.Error())
				return
			}
			if usageRecorder != nil {
				usageRecorder(result.Usage)
			}
			return
		}

		events, err := client.Stream(ctx, canon, authorization)
		if err != nil {
			if isUpstreamTimeout(err, ctx) {
				errorsx.WriteJSON(w, http.StatusGatewayTimeout, "upstream_timeout", "upstream request timed out")
				return
			}
			if writeUpstreamError(w, err) {
				return
			}
			errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
			return
		}

		collector := aggregate.NewCollector()
		for _, evt := range events {
			collector.Accept(evt)
		}

		result, err := collector.Result()
		if err != nil {
			var terminalFailure *aggregate.TerminalFailureError
			if errors.As(err, &terminalFailure) {
				statusCode := http.StatusBadGateway
				if terminalFailure.HealthFlag == "upstream_timeout" {
					statusCode = http.StatusGatewayTimeout
				}
				errorsx.WriteJSON(w, statusCode, terminalFailure.HealthFlag, terminalFailure.Message)
				return
			}
			errorsx.WriteJSON(w, http.StatusBadGateway, "invalid_upstream_stream", err.Error())
			return
		}

		w.Header().Set("Content-Type", "application/json")
		normalized := responsesadapter.BuildResponse(result)
		if responseID, _ := normalized["id"].(string); responseID != "" {
			globalResponsesHistory.Save(providerID, responseID, buildResponsesHistorySnapshot(canon.Messages, assistantHistoryMessagesFromResult(result)))
		}
		mergePreservedResponsesTopLevelFields(normalized, canon.ResponseInputItems)
		if err := writeJSON(w, normalized); err != nil {
			errorsx.WriteJSON(w, http.StatusInternalServerError, "encode_error", err.Error())
			return
		}
		if usageRecorder != nil {
			usageRecorder(result.Usage)
		}
		logging.Event("downstream_responses_usage_mapped", map[string]any{
			"request_id":    canon.RequestID,
			"cached_tokens": nestedCachedTokens(result.Usage),
			"usage_present": len(result.Usage) > 0,
		})
	}
}

func mergePreservedResponsesTopLevelFields(payload map[string]any, items []map[string]any) {
	if len(payload) == 0 || len(items) == 0 {
		return
	}
	for _, item := range items {
		preserved, ok := item["__openai_compat_responses_top_level"].(map[string]any)
		if !ok {
			continue
		}
		for key, value := range preserved {
			payload[key] = cloneJSONValueForResponse(value)
		}
	}
}

func cloneJSONValueForResponse(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, nested := range typed {
			cloned[key] = cloneJSONValueForResponse(nested)
		}
		return cloned
	case []any:
		cloned := make([]any, 0, len(typed))
		for _, nested := range typed {
			cloned = append(cloned, cloneJSONValueForResponse(nested))
		}
		return cloned
	default:
		return value
	}
}
