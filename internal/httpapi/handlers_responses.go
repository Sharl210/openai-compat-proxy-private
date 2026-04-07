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
		setNormalizationVersionHeader(w)
		requestID := w.Header().Get("X-Request-Id")

		canon, err := responsesadapter.DecodeRequest(r.Body)
		if err != nil {
			clearTransparencyHeaders(w)
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		clientModel := canon.Model
		provider, providerCfg, providerID, resolvedModel, ok := providerSelectionForModelRequest(r, canon.Model)
		if !ok {
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_model", "requested model is not in models list")
			return
		}
		if !provider.SupportsResponses {
			errorsx.WriteJSON(w, http.StatusBadRequest, "unsupported_provider_contract", "provider does not support responses")
			return
		}
		if !ensureRequestAuthorizedForProvider(w, r, provider) {
			clearTransparencyHeaders(w)
			return
		}
		canon.Model = resolvedModel
		if snapshot, ok := runtimeSnapshotFromRequest(r); ok {
			setConfigVersionHeaders(w, snapshot, providerID)
		}
		usageRecorder := cacheInfoUsageRecorder(r, requestID, providerID, providerCfg.UpstreamEndpointType)
		authorization, err := authHeaderForUpstream(r, providerCfg)
		if err != nil {
			clearTransparencyHeaders(w)
			errorsx.WriteJSON(w, http.StatusUnauthorized, "missing_upstream_auth", err.Error())
			return
		}
		if err := ensureProviderModelAllowed(r.Context(), r, provider, providerCfg, clientModel, authorization); err != nil {
			writeModelAllowanceError(w, err)
			return
		}
		client := upstream.NewClient(providerCfg.UpstreamBaseURL, providerCfg)
		clientReasoningParameters := clientToProxyReasoningParameters(clientReasoningProtocolResponses, clientModel, canon.Reasoning, provider.EnableReasoningEffortSuffix, canon.MaxOutputTokens)
		clientReasoningEffort := clientToProxyReasoningEffort(clientModel, canon.Reasoning, provider.EnableReasoningEffortSuffix)
		if providerCfg.UpstreamEndpointType != config.UpstreamEndpointTypeResponses {
			canon.IncludeUsage = true
		}
		if providerCfg.UpstreamEndpointType != config.UpstreamEndpointTypeResponses && shouldRestorePreviousConversation(canon.Messages) {
			if previousResponseID := previousResponseIDFromItems(canon.ResponseInputItems); previousResponseID != "" {
				if history := globalResponsesHistory.Load(providerID, previousResponseID); len(history) > 0 {
					canon.Messages = append(history, canon.Messages...)
				}
			}
		}
		canon.Messages = prepareCanonicalMessages(canon.Messages)
		applyProviderSystemPrompt(&canon, provider)
		if ok {
			normalizeCanonicalModelAndReasoningForProvider(&canon, provider, providerCfg)
		}
		if err := setDirectionalObservabilityHeaders(w, providerCfg, canon, clientModel, clientReasoningParameters, clientReasoningEffort); err != nil {
			errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
			return
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
			var flusher http.Flusher
			var initialState *responsesStreamState
			if shouldInjectSyntheticResponsesReasoning(providerCfg.UpstreamEndpointType, providerCfg.UpstreamThinkingTagStyle) {
				flusher = startSSE(w)
				initialState, err = startResponsesSyntheticPrelude(w, flusher, canon, providerCfg.UpstreamEndpointType, providerCfg.UpstreamThinkingTagStyle)
				if err != nil {
					_ = writeResponsesTerminalFailure(w, flusher, canon.RequestID, "stream_setup_error", err.Error())
					return
				}
			}
			stream, err := client.OpenEventStreamLazy(ctx, canon, authorization)
			if err != nil {
				if flusher != nil {
					if isUpstreamTimeout(err, ctx) {
						_ = writeResponsesTerminalFailure(w, flusher, canon.RequestID, "upstream_timeout", "upstream request timed out")
						return
					}
					_ = writeResponsesTerminalFailure(w, flusher, canon.RequestID, "upstream_error", err.Error())
					return
				}
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
			if flusher == nil {
				flusher = startSSE(w)
			}
			result, err := writeResponsesSSELive(ctx, stream, w, flusher, canon, providerCfg.UpstreamEndpointType, providerCfg.UpstreamThinkingTagStyle, usageRecorder, initialState)
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
			logNonStreamResponsesOutput(canon.RequestID, normalized)
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
		logNonStreamResponsesOutput(canon.RequestID, normalized)
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

func logNonStreamResponsesOutput(requestID string, normalized map[string]any) {
	if requestID == "" || len(normalized) == 0 {
		return
	}
	output, _ := normalized["output"].([]any)
	for _, raw := range output {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		if itemType, _ := item["type"].(string); itemType != "function_call" {
			continue
		}
		arguments, _ := item["arguments"].(string)
		parameters, _ := item["parameters"].(map[string]any)
		logging.Event("downstreamNonStreamToolOutput", map[string]any{
			"request_id":        requestID,
			"item_id":           stringValue(item["id"]),
			"call_id":           stringValue(item["call_id"]),
			"name":              stringValue(item["name"]),
			"arguments_len":     len(arguments),
			"arguments_preview": truncateForLog(arguments, 120),
			"has_parameters":    len(parameters) > 0,
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
