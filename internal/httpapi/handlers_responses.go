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
	"openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/upstream"
)

type preparedResponsesRequest struct {
	canon                     model.CanonicalRequest
	provider                  config.ProviderConfig
	providerCfg               config.Config
	providerID                string
	clientModel               string
	authorization             string
	requestID                 string
	clientReasoningParameters string
	clientReasoningEffort     string
	clientReasoningMode       string
	client                    *upstream.Client
	reasoningModeFallback     *reasoningModeFallbackCoordinator
	history                   *responsesHistoryStore
	historyScope              string
	usageRecorder             usageRecorderFunc
}

type initialResponsesRequest struct {
	canon               model.CanonicalRequest
	provider            config.ProviderConfig
	providerCfg         config.Config
	providerID          string
	rawClientModel      string
	clientModel         string
	resolvedModel       string
	clientReasoningMode string
	requestID           string
}

func handleResponses() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		prepared, ok := prepareResponsesRequest(w, r, false)
		if !ok {
			return
		}
		canon := prepared.canon
		providerCfg := prepared.providerCfg
		providerID := prepared.providerID
		client := prepared.client
		reasoningModeFallback := prepared.reasoningModeFallback
		authorization := prepared.authorization
		usageRecorder := prepared.usageRecorder
		history := prepared.history
		historyScope := prepared.historyScope

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
			w.Header().Del(headerThisUsageTokens)
			canon, stream, err := executeWithReasoningModeFallback(canon, reasoningModeFallback, func(request model.CanonicalRequest) (*upstream.EventStream, error) {
				return client.OpenEventStreamLazy(ctx, request, authorization)
			})
			setReasoningModeObservabilityHeaders(w, canon, reasoningModeFallback)
			if err != nil {
				if isUpstreamTimeout(err, ctx) {
					errorsx.WriteJSON(w, http.StatusGatewayTimeout, "upstream_timeout", "upstream request timed out")
					return
				}
				if writeUpstreamErrorForProtocol(w, err, clientReasoningProtocolResponses) {
					return
				}
				errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
				return
			}
			if reasoningModeFallback != nil && reasoningModeFallback.retried {
				if err := refreshFallbackUpstreamReasoningObservabilityHeaders(w, canon, providerCfg); err != nil {
					errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
					return
				}
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
			var initialState *responsesStreamState
			if shouldInjectSyntheticResponsesReasoning(providerCfg.UpstreamEndpointType, providerCfg.UpstreamThinkingTagStyle) {
				initialState, err = startResponsesSyntheticPrelude(w, flusher, canon, providerCfg.UpstreamEndpointType, providerCfg.UpstreamThinkingTagStyle)
				if err != nil {
					_ = writeResponsesTerminalFailure(w, flusher, canon.RequestID, "stream_setup_error", err.Error())
					return
				}
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
				history.Save(providerID, responseID, buildResponsesHistorySnapshot(canon.Messages, assistantHistoryMessagesFromResult(result)), historyScope)
			}
			return
		}

		if providerCfg.DownstreamNonStreamStrategy == config.DownstreamNonStreamStrategyUpstreamNonStream {
			canon, payload, err := executeWithReasoningModeFallback(canon, reasoningModeFallback, func(request model.CanonicalRequest) (map[string]any, error) {
				return client.Response(ctx, request, authorization)
			})
			setReasoningModeObservabilityHeaders(w, canon, reasoningModeFallback)
			if err != nil {
				if isUpstreamTimeout(err, ctx) {
					errorsx.WriteJSON(w, http.StatusGatewayTimeout, "upstream_timeout", "upstream request timed out")
					return
				}
				if writeUpstreamErrorForProtocol(w, err, clientReasoningProtocolResponses) {
					return
				}
				errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
				return
			}
			if reasoningModeFallback != nil && reasoningModeFallback.retried {
				if err := refreshFallbackUpstreamReasoningObservabilityHeaders(w, canon, providerCfg); err != nil {
					errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
					return
				}
			}
			result, err := aggregate.ResultFromResponsePayload(payload)
			if err != nil {
				errorsx.WriteJSON(w, http.StatusBadGateway, "invalid_upstream_response", err.Error())
				return
			}
			normalized := responsesadapter.BuildResponse(result)
			logNonStreamResponsesOutput(canon.RequestID, normalized)
			if responseID, _ := normalized["id"].(string); responseID != "" {
				history.Save(providerID, responseID, buildResponsesHistorySnapshot(canon.Messages, assistantHistoryMessagesFromResult(result)), historyScope)
			}
			mergePreservedResponsesTopLevelFields(normalized, canon.ResponseInputItems)
			w.Header().Set(headerThisUsageTokens, formatThisUsageTokens(result.Usage))
			w.Header().Set(headerThisUsageCacheWriteTokens, formatThisUsageCacheWriteTokens(result.Usage))
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

		collector := aggregate.NewCollector()
		responsesState := newResponsesStreamState(canon.RequestID, providerCfg.UpstreamEndpointType)
		acceptEvent := func(evt upstream.Event) error {
			if evt.Event == "response.output_text.delta" && shouldInjectSyntheticResponsesReasoningBeforeText(providerCfg.UpstreamEndpointType, responsesState, evt) {
				collector.Accept(upstream.Event{Event: "response.reasoning.delta", Data: map[string]any{"summary": syntheticReasoningPrelude(), aggregate.InternalReasoningSourceKey: aggregate.ReasoningSourceSynthetic}})
				responsesState.syntheticInjected = true
			}
			collector.Accept(evt)
			if evt.Event == "response.output_text.delta" {
				responsesState.textStarted = true
			}
			if evt.Event == "response.reasoning.delta" || evt.Event == "response.reasoning_summary_text.delta" {
				responsesState.realReasoningSeen = true
			}
			return nil
		}
		var err error
		if reasoningModeFallback != nil {
			var events []upstream.Event
			canon, events, err = executeWithReasoningModeFallback(canon, reasoningModeFallback, func(request model.CanonicalRequest) ([]upstream.Event, error) {
				return client.Stream(ctx, request, authorization)
			})
			setReasoningModeObservabilityHeaders(w, canon, reasoningModeFallback)
			if err == nil {
				for _, evt := range events {
					_ = acceptEvent(evt)
				}
			}
		} else {
			err = client.StreamInto(ctx, canon, authorization, acceptEvent)
		}
		if err != nil {
			if isUpstreamTimeout(err, ctx) {
				errorsx.WriteJSON(w, http.StatusGatewayTimeout, "upstream_timeout", "upstream request timed out")
				return
			}
			if writeUpstreamErrorForProtocol(w, err, clientReasoningProtocolResponses) {
				return
			}
			errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
			return
		}

		result, err := collector.Result()
		if err != nil {
			var terminalFailure *aggregate.TerminalFailureError
			if errors.As(err, &terminalFailure) {
				writeTerminalFailureError(w, terminalFailure, clientReasoningProtocolResponses)
				return
			}
			errorsx.WriteJSON(w, http.StatusBadGateway, "invalid_upstream_stream", err.Error())
			return
		}

		w.Header().Set("Content-Type", "application/json")
		normalized := responsesadapter.BuildResponse(result)
		logNonStreamResponsesOutput(canon.RequestID, normalized)
		if responseID, _ := normalized["id"].(string); responseID != "" {
			history.Save(providerID, responseID, buildResponsesHistorySnapshot(canon.Messages, assistantHistoryMessagesFromResult(result)), historyScope)
		}
		mergePreservedResponsesTopLevelFields(normalized, canon.ResponseInputItems)
		w.Header().Set(headerThisUsageTokens, formatThisUsageTokens(result.Usage))
		w.Header().Set(headerThisUsageCacheWriteTokens, formatThisUsageCacheWriteTokens(result.Usage))
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

func handleResponsesCompact() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		prepared, ok := prepareResponsesCompactRequest(w, r)
		if !ok {
			return
		}

		canon := prepared.canon
		attrs := map[string]any{
			"request_id":    canon.RequestID,
			"route":         canonicalV1ResponsesCompactPath,
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
		if prepared.providerCfg.TotalTimeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, prepared.providerCfg.TotalTimeout)
			defer cancel()
		}

		if prepared.providerCfg.UpstreamEndpointType == config.UpstreamEndpointTypeResponses {
			payload, err := prepared.client.Compact(ctx, canon, prepared.authorization)
			if err != nil {
				if isUpstreamTimeout(err, ctx) {
					errorsx.WriteJSON(w, http.StatusGatewayTimeout, "upstream_timeout", "upstream request timed out")
					return
				}
				if writeUpstreamErrorForProtocol(w, err, clientReasoningProtocolResponses) {
					return
				}
				errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
				return
			}

			w.Header().Set("Content-Type", "application/json")
			if err := writeJSON(w, payload); err != nil {
				errorsx.WriteJSON(w, http.StatusInternalServerError, "encode_error", err.Error())
				return
			}
			if prepared.usageRecorder != nil {
				if usage, _ := payload["usage"].(map[string]any); len(usage) > 0 {
					prepared.usageRecorder(usage)
				}
			}
			return
		}

		payload, err := prepared.client.Response(ctx, canon, prepared.authorization)
		if err != nil {
			if writeRequestValidationError(w, err) {
				return
			}
			if isUpstreamTimeout(err, ctx) {
				errorsx.WriteJSON(w, http.StatusGatewayTimeout, "upstream_timeout", "upstream request timed out")
				return
			}
			if writeUpstreamErrorForProtocol(w, err, clientReasoningProtocolResponses) {
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
			message := "upstream returned unsupported chat output content"
			if prepared.providerCfg.UpstreamEndpointType == config.UpstreamEndpointTypeAnthropic {
				message = "upstream returned unsupported anthropic output content"
			}
			errorsx.WriteJSON(w, http.StatusBadGateway, "unsupported_output_mapping", message)
			return
		}
		normalized := responsesadapter.BuildResponse(result)
		logNonStreamResponsesOutput(canon.RequestID, normalized)
		if responseID, _ := normalized["id"].(string); responseID != "" {
			prepared.history.Save(prepared.providerID, responseID, buildResponsesHistorySnapshot(canon.Messages, assistantHistoryMessagesFromResult(result)), prepared.historyScope)
		}
		mergePreservedResponsesTopLevelFields(normalized, canon.ResponseInputItems)
		w.Header().Set(headerThisUsageTokens, formatThisUsageTokens(result.Usage))
		w.Header().Set(headerThisUsageCacheWriteTokens, formatThisUsageCacheWriteTokens(result.Usage))
		w.Header().Set("Content-Type", "application/json")
		if err := writeJSON(w, normalized); err != nil {
			errorsx.WriteJSON(w, http.StatusInternalServerError, "encode_error", err.Error())
			return
		}
		if prepared.usageRecorder != nil {
			prepared.usageRecorder(result.Usage)
		}
	}
}

func prepareResponsesRequest(w http.ResponseWriter, r *http.Request, compact bool) (*preparedResponsesRequest, bool) {
	initial, ok := decodeAndResolveResponsesRequest(w, r)
	if !ok {
		return nil, false
	}
	return finalizePreparedResponsesRequest(w, r, initial, compact)
}

func prepareResponsesCompactRequest(w http.ResponseWriter, r *http.Request) (*preparedResponsesRequest, bool) {
	initial, ok := decodeAndResolveResponsesRequest(w, r)
	if !ok {
		return nil, false
	}
	if initial.canon.Stream {
		clearTransparencyHeaders(w)
		errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", "responses compact does not support stream=true")
		return nil, false
	}
	return finalizePreparedResponsesRequest(w, r, initial, true)
}

func decodeAndResolveResponsesRequest(w http.ResponseWriter, r *http.Request) (*initialResponsesRequest, bool) {
	setNormalizationVersionHeader(w)
	requestID := w.Header().Get("X-Request-Id")

	canon, err := responsesadapter.DecodeRequest(r.Body)
	if err != nil {
		clearTransparencyHeaders(w)
		errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", err.Error())
		return nil, false
	}
	canon.ResponsesOpenAIBeta = r.Header.Get("OpenAI-Beta")
	selectionEffort := clientToProxyReasoningEffort(canon.Model, canon.Reasoning, false)
	if selectionEffort != "" {
		*r = *r.Clone(context.WithValue(r.Context(), routeProviderSelectionEffortKey, selectionEffort))
	}
	resolveProviderModelDiscoveryBeforeProviderSelection(r, canon.Model)
	provider, providerCfg, providerID, resolvedModel, ok, selectionErr := providerSelectionForModelRequest(r, canon.Model)
	if !ok {
		if hasNoPromptModelSuffix(canon.Model) {
			w.Header().Set(headerClientToProxyNoPrompt, "false")
		}
		if writeUpstreamErrorForProtocol(w, selectionErr, clientReasoningProtocolResponses) {
			return nil, false
		}
		errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_model", "requested model cannot be routed")
		return nil, false
	}
	if !provider.SupportsResponses {
		errorsx.WriteJSON(w, http.StatusBadRequest, "unsupported_provider_contract", "provider does not support responses")
		return nil, false
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
	return &initialResponsesRequest{
		canon:               canon,
		provider:            provider,
		providerCfg:         providerCfg,
		providerID:          providerID,
		rawClientModel:      rawClientModel,
		clientModel:         clientModel,
		resolvedModel:       resolvedModel,
		clientReasoningMode: clientReasoningMode,
		requestID:           requestID,
	}, true
}

func finalizePreparedResponsesRequest(w http.ResponseWriter, r *http.Request, initial *initialResponsesRequest, compact bool) (*preparedResponsesRequest, bool) {
	if initial == nil {
		return nil, false
	}
	canon := initial.canon
	provider := initial.provider
	providerCfg := initial.providerCfg
	providerID := initial.providerID
	rawClientModel := initial.rawClientModel
	clientModel := initial.clientModel
	resolvedModel := initial.resolvedModel
	clientReasoningMode := initial.clientReasoningMode
	requestID := initial.requestID
	history := responsesHistoryFromRequest(r)
	canon.Model = resolvedModel
	if snapshot, ok := runtimeSnapshotFromRequest(r); ok {
		setConfigVersionHeaders(w, snapshot, providerID)
	}
	authorization, err := authHeaderForResolvedProviderUpstream(r, providerCfg, providerID)
	if err != nil {
		clearTransparencyHeaders(w)
		errorsx.WriteJSON(w, http.StatusUnauthorized, "missing_upstream_auth", err.Error())
		return nil, false
	}
	clientReasoningEffort := clientToProxyReasoningEffort(clientModel, canon.Reasoning, provider.EnableReasoningEffortSuffix)
	*r = *r.Clone(context.WithValue(r.Context(), routeRequestEffortKey, clientReasoningEffort))
	if err := ensureProviderModelAllowed(r.Context(), r, provider, providerCfg, clientModel, authorization); err != nil {
		writeModelAllowanceError(w, err)
		return nil, false
	}
	clientServiceTier := serviceTierFromTopLevelFields(canon.PreservedTopLevelFields)
	clientReasoningParameters := clientToProxyReasoningParameters(clientReasoningProtocolResponses, clientModel, canon.Reasoning, provider.EnableReasoningEffortSuffix, canon.MaxOutputTokens)
	intent, _ := proxyModelIntentFromRequest(r)
	normalizeCanonicalModelAndReasoningForResolvedProxyModelIntent(&canon, resolvedModel, clientReasoningEffort, provider, providerCfg, intent)
	authMode := authModeForResolvedProviderUpstream(r, providerCfg, providerID)
	client := upstreamClientForProvider(r, providerID, providerCfg)
	historyScope := responsesHistoryReplayScope(responsesHistoryReplayProvenance{
		ProviderID:                providerID,
		DownstreamEndpoint:        r.URL.Path,
		UpstreamEndpointType:      providerCfg.UpstreamEndpointType,
		NormalizedUpstreamBaseURL: providerCfg.UpstreamBaseURL,
		FinalUpstreamModel:        canon.Model,
		CredentialFingerprint:     authorizationFingerprint(authorization),
		InboundCallerFingerprint:  inboundCallerIdentityFromRequest(r),
	})
	if !compact && providerCfg.UpstreamEndpointType != config.UpstreamEndpointTypeResponses {
		canon.IncludeUsage = true
	}
	if !compact && providerCfg.UpstreamEndpointType == config.UpstreamEndpointTypeAnthropic {
		if !rehydrateOpaqueAnthropicThinking(history, &canon, providerID, historyScope) {
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", "opaque thinking replay must match server-held history")
			return nil, false
		}
	}
	previousHistoryRestored := false
	if !compact && providerCfg.UpstreamEndpointType != config.UpstreamEndpointTypeResponses && shouldRestorePreviousConversation(canon.Messages) {
		if previousResponseID := previousResponseIDFromItems(canon.ResponseInputItems); previousResponseID != "" {
			currentMessages := prepareCanonicalMessages(canon.Messages)
			if previousHistory := history.LoadScoped(providerID, previousResponseID, historyScope); len(previousHistory) > 0 {
				canon.Messages = append(previousHistory, currentMessages...)
				previousHistoryRestored = true
			} else {
				canon.Messages = currentMessages
			}
		}
	}
	if !previousHistoryRestored {
		canon.Messages = prepareCanonicalMessages(canon.Messages)
	}
	applyProviderSystemPrompt(&canon, provider)
	if !compact && providerCfg.UpstreamEndpointType == config.UpstreamEndpointTypeAnthropic && !previousHistoryRestored {
		canon.Messages = recoverToolCallsForMessages(history, canon.Messages, providerID, historyScope)
	}
	if !compact && providerCfg.UpstreamEndpointType == config.UpstreamEndpointTypeResponses && providerCfg.MasqueradeTarget == config.MasqueradeTargetOpenCode {
		if previousResponseIDFromItems(canon.ResponseInputItems) != "" {
			canon.ResponseItemReferencesByCallID = recoverResponseItemReferencesForMessages(history, canon.Messages, providerID, historyScope)
		}
	}
	applyProviderMaxOutputTokens(&canon, provider)
	finalizeAnthropicReasoningForUpstream(&canon, provider, providerCfg)
	applyProxyModelIntentReasoningMode(r, &canon)
	enforceSuffixReasoningModePrecedence(&canon)
	applyDefaultProReasoningMode(&canon, providerCfg)
	canon, reasoningModeFallback, err := prepareReasoningModeFallback(canon, provider, providerCfg, reasoningModeFallbackKeyForRequest(r, providerID, providerCfg, canon.Model, authorization))
	if err != nil {
		var unsupportedMode unsupportedReasoningModeError
		if errors.As(err, &unsupportedMode) {
			errorsx.WriteJSON(w, http.StatusBadRequest, "unsupported_reasoning_mode", err.Error())
			return nil, false
		}
		errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
		return nil, false
	}
	if err := applyUltraMultiAgent(&canon, intent, provider, providerCfg); err != nil {
		errorsx.WriteJSON(w, http.StatusBadRequest, "unsupported_upstream_feature", err.Error())
		return nil, false
	}
	applyResponsesPromptCacheHintDrop(&canon, provider, providerCfg)
	if message := unsupportedResponsesNativeFeature(canon, provider, providerCfg); message != "" {
		errorsx.WriteJSON(w, http.StatusBadRequest, "unsupported_upstream_feature", message)
		return nil, false
	}
	applyProviderOpenAIServiceTierOverride(&canon, provider, providerCfg)
	observationCtx := withTokenEstimatorObservation(r.Context(), tokenEstimatorObservationInput{
		ProviderID:         providerID,
		EndpointType:       providerCfg.UpstreamEndpointType,
		FinalUpstreamModel: canon.Model,
		BaseEstimate:       int64(estimateCanonicalInputTokens(canon)),
		Canon:              canon,
	})
	*r = *r.Clone(observationCtx)
	if err := setDirectionalObservabilityHeadersWithClientReasoningMode(w, r, provider, providerCfg, providerID, &canon, rawClientModel, clientServiceTier, clientReasoningParameters, clientReasoningEffort, clientReasoningMode, reasoningModeFallback); err != nil {
		if writeRequestValidationError(w, err) {
			return nil, false
		}
		errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
		return nil, false
	}
	if !compact && writeContextLimitExceededIfNeeded(r.Context(), w, provider, canon, clientReasoningProtocolResponses) {
		return nil, false
	}
	canon.RequestID = requestID
	canon.AuthMode = authMode
	usageRecorder := combinedUsageRecorder(
		cacheInfoUsageRecorder(r, requestID, providerID, providerCfg.UpstreamEndpointType),
		tokenEstimatorUsageRecorder(observationCtx, requestID, providerCfg.UpstreamEndpointType, bypassProviderModelAllowanceForRequest(r) || shouldBypassUsageRecorderForRequest(r)),
	)
	return &preparedResponsesRequest{
		canon:                     canon,
		provider:                  provider,
		providerCfg:               providerCfg,
		providerID:                providerID,
		clientModel:               clientModel,
		authorization:             authorization,
		requestID:                 requestID,
		clientReasoningParameters: clientReasoningParameters,
		clientReasoningEffort:     clientReasoningEffort,
		client:                    client,
		reasoningModeFallback:     reasoningModeFallback,
		history:                   history,
		historyScope:              historyScope,
		usageRecorder:             usageRecorder,
	}, true
}

func rehydrateOpaqueAnthropicThinking(history *responsesHistoryStore, canon *model.CanonicalRequest, providerID, scope string) bool {
	if canon == nil {
		return false
	}
	hasOpaqueInput := false
	for _, item := range canon.ResponseInputItems {
		if stringValue(item["type"]) != "reasoning" {
			continue
		}
		if _, ok := item["encrypted_content"]; ok {
			hasOpaqueInput = true
		}
		if _, ok := item["signature"]; ok {
			hasOpaqueInput = true
		}
	}
	for _, message := range canon.Messages {
		for _, block := range message.ReasoningBlocks {
			if _, hasEncrypted := block["encrypted_content"]; hasEncrypted {
				hasOpaqueInput = true
			}
			if _, hasSignature := block["signature"]; hasSignature {
				hasOpaqueInput = true
			}
		}
	}
	if !hasOpaqueInput {
		return true
	}
	rehydrated := 0
	for messageIndex := range canon.Messages {
		for blockIndex, block := range canon.Messages[messageIndex].ReasoningBlocks {
			if _, hasEncrypted := block["encrypted_content"]; !hasEncrypted {
				if _, hasSignature := block["signature"]; !hasSignature {
					continue
				}
			}
			serverBlock, ok := history.LoadOpaqueThinking(providerID, scope, block)
			if !ok {
				return false
			}
			canon.Messages[messageIndex].ReasoningBlocks[blockIndex] = serverBlock
			rehydrated++
		}
	}
	if rehydrated == 0 {
		return false
	}
	filtered := make([]map[string]any, 0, len(canon.ResponseInputItems))
	for _, item := range canon.ResponseInputItems {
		if stringValue(item["type"]) == "reasoning" {
			if _, hasEncrypted := item["encrypted_content"]; hasEncrypted {
				continue
			}
			if _, hasSignature := item["signature"]; hasSignature {
				continue
			}
		}
		filtered = append(filtered, item)
	}
	canon.ResponseInputItems = filtered
	return true
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
