package httpapi

import (
	"context"
	"errors"
	"net/http"

	chatadapter "openai-compat-proxy/internal/adapter/chat"
	"openai-compat-proxy/internal/aggregate"
	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/errorsx"
	"openai-compat-proxy/internal/logging"
	"openai-compat-proxy/internal/upstream"
)

func handleChat() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setNormalizationVersionHeader(w)
		requestID := w.Header().Get("X-Request-Id")

		canon, err := chatadapter.DecodeRequest(r.Body)
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
		if !provider.SupportsChat {
			errorsx.WriteJSON(w, http.StatusBadRequest, "unsupported_provider_contract", "provider does not support chat completions")
			return
		}
		canon.Model = resolvedModel
		if snapshot, ok := runtimeSnapshotFromRequest(r); ok {
			setConfigVersionHeaders(w, snapshot, providerID)
		}
		authorization, err := authHeaderForResolvedProviderUpstream(r, providerCfg, providerID)
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
		clientServiceTier := serviceTierFromTopLevelFields(canon.PreservedTopLevelFields)
		clientReasoningParameters := clientToProxyReasoningParameters(clientReasoningProtocolChat, clientModel, canon.Reasoning, provider.EnableReasoningEffortSuffix, canon.MaxOutputTokens)
		clientReasoningEffort := clientToProxyReasoningEffort(clientModel, canon.Reasoning, provider.EnableReasoningEffortSuffix)
		canon.Messages = prepareCanonicalMessages(canon.Messages)
		applyProviderSystemPrompt(&canon, provider)
		if ok {
			normalizeCanonicalModelAndReasoningForProvider(&canon, provider, providerCfg)
			applyProviderOpenAIServiceTierOverride(&canon, provider, providerCfg)
		}
		if err := setDirectionalObservabilityHeaders(w, providerCfg, canon, clientModel, clientServiceTier, clientReasoningParameters, clientReasoningEffort); err != nil {
			errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
			return
		}
		canon.RequestID = requestID
		usageRecorder := cacheInfoUsageRecorder(r, canon.RequestID, providerID, providerCfg.UpstreamEndpointType)
		canon.AuthMode = authModeForResolvedProviderUpstream(r, providerCfg, providerID)
		attrs := map[string]any{
			"request_id":    canon.RequestID,
			"route":         "/v1/chat/completions",
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
			stream, err := client.OpenEventStreamLazy(ctx, canon, authorization)
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
			if err := writeChatSSELive(ctx, stream, w, flusher, canon, providerCfg.UpstreamEndpointType, providerCfg.UpstreamThinkingTagStyle, usageRecorder); err != nil {
				var terminalFailure *aggregate.TerminalFailureError
				if errors.As(err, &terminalFailure) {
					return
				}
				if isUpstreamTimeout(err, ctx) {
					_ = writeChatTerminalFailure(w, flusher, "upstream_timeout", "upstream request timed out")
					return
				}
				_ = writeChatTerminalFailure(w, flusher, "upstreamStreamBroken", err.Error())
				return
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
			if len(result.UnsupportedContentTypes) > 0 {
				errorsx.WriteJSON(w, http.StatusBadGateway, "unsupported_output_mapping", "upstream returned unsupported chat output content")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if err := writeJSON(w, chatadapter.BuildResponse(result)); err != nil {
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
			errorsx.WriteJSON(w, http.StatusBadGateway, "invalid_upstream_stream", err.Error())
			return
		}
		if len(result.UnsupportedContentTypes) > 0 {
			errorsx.WriteJSON(w, http.StatusBadGateway, "unsupported_output_mapping", "upstream returned unsupported chat output content")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := writeJSON(w, chatadapter.BuildResponse(result)); err != nil {
			errorsx.WriteJSON(w, http.StatusInternalServerError, "encode_error", err.Error())
			return
		}
		if usageRecorder != nil {
			usageRecorder(result.Usage)
		}
		logging.Event("downstream_chat_usage_mapped", map[string]any{
			"request_id":    canon.RequestID,
			"cached_tokens": nestedCachedTokens(result.Usage),
			"usage_present": len(result.Usage) > 0,
		})
	}
}
