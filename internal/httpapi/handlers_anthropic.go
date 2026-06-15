package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	anthropicadapter "openai-compat-proxy/internal/adapter/anthropic"
	"openai-compat-proxy/internal/aggregate"
	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/errorsx"
	"openai-compat-proxy/internal/upstream"
)

func handleAnthropicMessages() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setNormalizationVersionHeader(w)
		requestID := w.Header().Get("X-Request-Id")
		if strings.TrimSpace(r.Header.Get("anthropic-version")) == "" {
			clearTransparencyHeaders(w)
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", "missing anthropic-version header")
			return
		}
		canon, err := anthropicadapter.DecodeRequest(r.Body)
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
			if writeUpstreamError(w, selectionErr) {
				return
			}
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_model", "requested model is not in models list")
			return
		}
		if !provider.SupportsAnthropicMessages {
			errorsx.WriteJSON(w, http.StatusBadRequest, "unsupported_provider_contract", "provider does not support anthropic messages")
			return
		}
	rawClientModel := canon.Model
	clientModel := canon.Model
	if !provider.HidesModel(canon.Model) {
		applyNoPromptModelSuffix(&canon, providerCfg)
		clientModel = canon.Model
	}
		if hasNoPromptModelSuffix(canon.Model) {
			w.Header().Set(headerClientToProxyNoPrompt, "false")
		}
		if info, ok := routeInfoFromRequest(r); ok && info.Legacy && canon.SkipProviderSystemPrompt {
			*r = *r.Clone(context.WithValue(r.Context(), legacyRoutingModelKey, clientModel))
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
	clientReasoningParameters := clientToProxyReasoningParameters(clientReasoningProtocolMessages, clientModel, canon.Reasoning, provider.EnableReasoningEffortSuffix, canon.MaxOutputTokens)
	clientReasoningEffort := clientToProxyReasoningEffort(clientModel, canon.Reasoning, provider.EnableReasoningEffortSuffix)
	*r = *r.Clone(context.WithValue(r.Context(), routeRequestEffortKey, clientReasoningEffort))
	canon.Messages = prepareCanonicalMessages(canon.Messages)
		if providerCfg.UpstreamEndpointType != config.UpstreamEndpointTypeAnthropic {
			stripAnthropicCacheControl(&canon)
		}
		if providerCfg.UpstreamEndpointType == config.UpstreamEndpointTypeResponses {
			delete(canon.PreservedTopLevelFields, "metadata")
		}
		applyProviderSystemPrompt(&canon, provider)
		normalizeCanonicalModelAndReasoningForProvider(&canon, resolvedModel, clientReasoningEffort, provider, providerCfg)
		applyProviderMaxOutputTokens(&canon, provider)
		finalizeAnthropicReasoningForUpstream(&canon, provider, providerCfg)
		applyProviderOpenAIServiceTierOverride(&canon, provider, providerCfg)
		baseEstimate := int64(estimateCanonicalInputTokens(canon))
		r = r.Clone(withTokenEstimatorObservation(r.Context(), tokenEstimatorObservationInput{
			ProviderID:         providerID,
			EndpointType:       providerCfg.UpstreamEndpointType,
			FinalUpstreamModel: canon.Model,
			BaseEstimate:       baseEstimate,
			Canon:              canon,
		}))
		if err := setDirectionalObservabilityHeaders(w, provider, providerCfg, canon, rawClientModel, clientServiceTier, clientReasoningParameters, clientReasoningEffort); err != nil {
			if writeRequestValidationError(w, err) {
				return
			}
			errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
			return
		}
		if writeContextLimitExceededIfNeeded(r.Context(), w, provider, canon, clientReasoningProtocolMessages) {
			return
		}
		canon.RequestID = requestID
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
			stream, err := client.OpenEventStreamLazy(ctx, canon, authorization)
			if err != nil {
				if writeRequestValidationError(w, err) {
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
			flusher := startSSE(w)
			streamState := &anthropicStreamState{}
			if err := writeAnthropicSSELive(ctx, stream, w, flusher, canon, streamState, providerCfg.UpstreamEndpointType, usageRecorder); err != nil {
				var terminalFailure *aggregate.TerminalFailureError
				if errors.As(err, &terminalFailure) {
					_ = writeAnthropicTerminalFailure(w, flusher, streamState, canon.RequestID, terminalFailure.HealthFlag, terminalFailure.Message, nil)
					return
				}
				if isUpstreamTimeout(err, ctx) {
					_ = writeAnthropicTerminalFailure(w, flusher, streamState, canon.RequestID, "upstream_timeout", "upstream request timed out", nil)
					return
				}
				_ = writeAnthropicTerminalFailure(w, flusher, streamState, canon.RequestID, "upstreamStreamBroken", err.Error(), nil)
				return
			}
			return
		}
		if providerCfg.DownstreamNonStreamStrategy == config.DownstreamNonStreamStrategyUpstreamNonStream {
			payload, err := client.Response(ctx, canon, authorization)
			if err != nil {
				if writeRequestValidationError(w, err) {
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
			result, err := aggregate.ResultFromResponsePayload(payload)
			if err != nil {
				errorsx.WriteJSON(w, http.StatusBadGateway, "invalid_upstream_response", err.Error())
				return
			}
			if len(result.UnsupportedContentTypes) > 0 {
				errorsx.WriteJSON(w, http.StatusBadGateway, "unsupported_output_mapping", "upstream returned unsupported anthropic output content")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if err := writeJSON(w, anthropicadapter.BuildResponse(result, canon.RequestID, canon.Model)); err != nil {
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
			if writeRequestValidationError(w, err) {
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
		collector := aggregate.NewCollector()
		for _, evt := range events {
			collector.Accept(evt)
		}
		result, err := collector.Result()
		if err != nil {
			var terminalFailure *aggregate.TerminalFailureError
			if errors.As(err, &terminalFailure) {
				writeTerminalFailureError(w, terminalFailure)
				return
			}
			errorsx.WriteJSON(w, http.StatusBadGateway, "invalid_upstream_stream", err.Error())
			return
		}
		if len(result.UnsupportedContentTypes) > 0 {
			errorsx.WriteJSON(w, http.StatusBadGateway, "unsupported_output_mapping", "upstream returned unsupported anthropic output content")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := writeJSON(w, anthropicadapter.BuildResponse(result, canon.RequestID, canon.Model)); err != nil {
			errorsx.WriteJSON(w, http.StatusInternalServerError, "encode_error", err.Error())
			return
		}
		if usageRecorder != nil {
			usageRecorder(result.Usage)
		}
	}
}
