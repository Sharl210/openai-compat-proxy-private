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
	"openai-compat-proxy/internal/model"
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
		provider, providerCfg, providerID, resolvedModel, ok, selectionErr := providerSelectionForModelRequest(r, canon.Model)
		if !ok {
			if hasNoPromptModelSuffix(canon.Model) {
				w.Header().Set(headerClientToProxyNoPrompt, "false")
			}
			if writeUpstreamErrorForProtocol(w, selectionErr, clientReasoningProtocolChat) {
				return
			}
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_model", "requested model is not in models list")
			return
		}
		if !provider.SupportsChat {
			errorsx.WriteJSON(w, http.StatusBadRequest, "unsupported_provider_contract", "provider does not support chat completions")
			return
		}
		rawClientModel := canon.Model
		clientReasoningMode := clientReasoningModeForRequest(r, rawClientModel, canon, provider, providerCfg)
		clientModel := prepareProviderClientModelForRequest(providerClientModelRequest{req: &canon, httpRequest: r, resolvedModel: sourceModelBeforeProviderMapping(r, rawClientModel, resolvedModel, provider), provider: provider, config: providerCfg})
		resolvedModel = clientModel
		applyProxyModelIntentReasoningMode(r, &canon)
		if hasNoPromptModelSuffix(clientModel) {
			w.Header().Set(headerClientToProxyNoPrompt, "false")
		}
		if info, ok := routeInfoFromRequest(r); ok && info.Legacy {
			*r = *r.Clone(context.WithValue(r.Context(), legacyRoutingModelKey, clientModel))
		}
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
		client := upstreamClientForProvider(r, providerID, providerCfg)
		clientServiceTier := serviceTierFromTopLevelFields(canon.PreservedTopLevelFields)
		clientReasoningParameters := clientToProxyReasoningParameters(clientReasoningProtocolChat, clientModel, canon.Reasoning, provider.EnableReasoningEffortSuffix, canon.MaxOutputTokens)
		clientReasoningEffort := clientToProxyReasoningEffort(clientModel, canon.Reasoning, provider.EnableReasoningEffortSuffix)
		*r = *r.Clone(context.WithValue(r.Context(), routeRequestEffortKey, clientReasoningEffort))
		canon.Messages = prepareCanonicalMessages(canon.Messages)
		applyProviderSystemPrompt(&canon, provider)
		var reasoningModeFallback *reasoningModeFallbackCoordinator
		if ok {
			intent, _ := proxyModelIntentFromRequest(r)
			normalizeCanonicalModelAndReasoningForResolvedProxyModelIntent(&canon, resolvedModel, clientReasoningEffort, provider, providerCfg, intent)
			applyProviderMaxOutputTokens(&canon, provider)
			finalizeAnthropicReasoningForUpstream(&canon, provider, providerCfg)
			applyProxyModelIntentReasoningMode(r, &canon)
			enforceSuffixReasoningModePrecedence(&canon)
			applyDefaultProReasoningMode(&canon, providerCfg)
			canon, reasoningModeFallback, err = prepareReasoningModeFallback(canon, provider, providerCfg, reasoningModeFallbackKeyForRequest(r, providerID, providerCfg, canon.Model, authorization))
			if err != nil {
				var unsupportedMode unsupportedReasoningModeError
				if errors.As(err, &unsupportedMode) {
					errorsx.WriteJSON(w, http.StatusBadRequest, "unsupported_reasoning_mode", err.Error())
					return
				}
				errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
				return
			}
			if err := applyUltraMultiAgent(&canon, intent, provider, providerCfg); err != nil {
				errorsx.WriteJSON(w, http.StatusBadRequest, "unsupported_upstream_feature", err.Error())
				return
			}
			applyProviderOpenAIServiceTierOverride(&canon, provider, providerCfg)
			applyResponsesPromptCacheHintDrop(&canon, provider, providerCfg)
			if message := unsupportedResponsesNativeFeature(canon, provider, providerCfg); message != "" {
				errorsx.WriteJSON(w, http.StatusBadRequest, "unsupported_upstream_feature", message)
				return
			}
		}
		baseEstimate := int64(estimateCanonicalInputTokens(canon))
		r = r.Clone(withTokenEstimatorObservation(r.Context(), tokenEstimatorObservationInput{
			ProviderID:         providerID,
			EndpointType:       providerCfg.UpstreamEndpointType,
			FinalUpstreamModel: canon.Model,
			BaseEstimate:       baseEstimate,
			Canon:              canon,
		}))
		if err := setDirectionalObservabilityHeadersWithClientReasoningMode(w, r, provider, providerCfg, providerID, &canon, rawClientModel, clientServiceTier, clientReasoningParameters, clientReasoningEffort, clientReasoningMode, reasoningModeFallback); err != nil {
			if writeRequestValidationError(w, err) {
				return
			}
			errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
			return
		}
		if writeContextLimitExceededIfNeeded(r.Context(), w, provider, canon, clientReasoningProtocolChat) {
			return
		}
		canon.RequestID = requestID
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
		usageRecorder := combinedUsageRecorder(
			cacheInfoUsageRecorder(r, canon.RequestID, providerID, providerCfg.UpstreamEndpointType),
			tokenEstimatorUsageRecorder(ctx, canon.RequestID, providerCfg.UpstreamEndpointType, bypassProviderModelAllowanceForRequest(r) || shouldBypassUsageRecorderForRequest(r)),
		)
		var cancel context.CancelFunc
		if providerCfg.TotalTimeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, providerCfg.TotalTimeout)
			defer cancel()
		}

		if canon.Stream {
			canon, stream, err := executeWithReasoningModeFallback(canon, reasoningModeFallback, func(request model.CanonicalRequest) (*upstream.EventStream, error) {
				return client.OpenEventStreamLazy(ctx, request, authorization)
			})
			setReasoningModeObservabilityHeaders(w, canon, reasoningModeFallback)
			if err != nil {
				if writeRequestValidationError(w, err) {
					return
				}
				if isUpstreamTimeout(err, ctx) {
					errorsx.WriteJSON(w, http.StatusGatewayTimeout, "upstream_timeout", "upstream request timed out")
					return
				}
				if writeUpstreamErrorForProtocol(w, err, clientReasoningProtocolChat) {
					return
				}
				errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
				return
			}
			defer stream.Close()
			overflow, err := stream.ProbeContextOverflowBeforeOutput()
			if err != nil {
				errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
				return
			}
			if overflow != nil {
				errorsx.WriteJSON(w, http.StatusBadRequest, "context_length_exceeded", overflow.Message)
				return
			}
			flusher := startSSE(w)
			if err := writeChatSSELive(ctx, stream, w, flusher, canon, providerCfg.UpstreamEndpointType, providerCfg.UpstreamThinkingTagStyle, usageRecorder); err != nil {
				var terminalFailure *aggregate.TerminalFailureError
				if errors.As(err, &terminalFailure) {
					return
				}
				if isUpstreamTimeout(err, ctx) {
					_ = writeChatTerminalFailure(w, flusher, "upstream_timeout", "upstream request timed out", nil)
					return
				}
				_ = writeChatTerminalFailure(w, flusher, "upstreamStreamBroken", err.Error(), nil)
				return
			}
			return
		}

		if providerCfg.DownstreamNonStreamStrategy == config.DownstreamNonStreamStrategyUpstreamNonStream {
			canon, payload, err := executeWithReasoningModeFallback(canon, reasoningModeFallback, func(request model.CanonicalRequest) (map[string]any, error) {
				return client.Response(ctx, request, authorization)
			})
			setReasoningModeObservabilityHeaders(w, canon, reasoningModeFallback)
			if err != nil {
				if writeRequestValidationError(w, err) {
					return
				}
				if isUpstreamTimeout(err, ctx) {
					errorsx.WriteJSON(w, http.StatusGatewayTimeout, "upstream_timeout", "upstream request timed out")
					return
				}
				if writeUpstreamErrorForProtocol(w, err, clientReasoningProtocolChat) {
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
			w.Header().Set(headerThisUsageTokens, formatThisUsageTokens(result.Usage))
			w.Header().Set(headerThisUsageCacheWriteTokens, formatThisUsageCacheWriteTokens(result.Usage))
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

		collector := aggregate.NewCollector()
		if reasoningModeFallback != nil {
			var events []upstream.Event
			canon, events, err = executeWithReasoningModeFallback(canon, reasoningModeFallback, func(request model.CanonicalRequest) ([]upstream.Event, error) {
				return client.Stream(ctx, request, authorization)
			})
			setReasoningModeObservabilityHeaders(w, canon, reasoningModeFallback)
			if err == nil {
				for _, evt := range events {
					collector.Accept(evt)
				}
			}
		} else {
			err = client.StreamInto(ctx, canon, authorization, func(evt upstream.Event) error {
				collector.Accept(evt)
				return nil
			})
		}
		if err != nil {
			if writeRequestValidationError(w, err) {
				return
			}
			if isUpstreamTimeout(err, ctx) {
				errorsx.WriteJSON(w, http.StatusGatewayTimeout, "upstream_timeout", "upstream request timed out")
				return
			}
			if writeUpstreamErrorForProtocol(w, err, clientReasoningProtocolChat) {
				return
			}
			errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
			return
		}

		result, err := collector.Result()
		if err != nil {
			var terminalFailure *aggregate.TerminalFailureError
			if errors.As(err, &terminalFailure) {
				writeTerminalFailureError(w, terminalFailure, clientReasoningProtocolChat)
				return
			}
			errorsx.WriteJSON(w, http.StatusBadGateway, "invalid_upstream_stream", err.Error())
			return
		}
		if len(result.UnsupportedContentTypes) > 0 {
			errorsx.WriteJSON(w, http.StatusBadGateway, "unsupported_output_mapping", "upstream returned unsupported chat output content")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(headerThisUsageTokens, formatThisUsageTokens(result.Usage))
		w.Header().Set(headerThisUsageCacheWriteTokens, formatThisUsageCacheWriteTokens(result.Usage))
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
