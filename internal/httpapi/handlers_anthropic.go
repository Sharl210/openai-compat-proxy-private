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
		providerCfg := providerConfigForRequest(r)
		setNormalizationVersionHeader(w)
		provider, ok := providerForRequest(r)
		if !ok || !provider.SupportsAnthropicMessages {
			errorsx.WriteJSON(w, http.StatusBadRequest, "unsupported_provider_contract", "provider does not support anthropic messages")
			return
		}
		client := upstream.NewClient(providerCfg.UpstreamBaseURL, providerCfg)
		requestID := w.Header().Get("X-Request-Id")
		providerID := provider.ID
		if strings.TrimSpace(r.Header.Get("anthropic-version")) == "" {
			clearTransparencyHeaders(w)
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", "missing anthropic-version header")
			return
		}
		authorization, err := authHeaderForUpstream(r, providerCfg)
		if err != nil {
			clearTransparencyHeaders(w)
			errorsx.WriteJSON(w, http.StatusUnauthorized, "missing_upstream_auth", err.Error())
			return
		}
		canon, err := anthropicadapter.DecodeRequest(r.Body)
		if err != nil {
			clearTransparencyHeaders(w)
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		clientModel := canon.Model
		clientReasoningParameters := clientToProxyReasoningParameters(clientReasoningProtocolMessages, clientModel, canon.Reasoning, provider.EnableReasoningEffortSuffix, canon.MaxOutputTokens)
		clientReasoningEffort := clientToProxyReasoningEffort(clientModel, canon.Reasoning, provider.EnableReasoningEffortSuffix)
		canon.Messages = prepareCanonicalMessages(canon.Messages)
		applyProviderSystemPrompt(&canon, provider)
		normalizeCanonicalModelAndReasoningForProvider(&canon, provider, providerCfg)
		if err := setDirectionalObservabilityHeaders(w, providerCfg, canon, clientModel, clientReasoningParameters, clientReasoningEffort); err != nil {
			errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
			return
		}
		canon.RequestID = requestID
		usageRecorder := cacheInfoUsageRecorder(r, canon.RequestID, providerID, providerCfg.UpstreamEndpointType)
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
			streamState := &anthropicStreamState{}
			if err := writeAnthropicSSELive(ctx, stream, w, flusher, canon, streamState, providerCfg.UpstreamEndpointType, usageRecorder); err != nil {
				var terminalFailure *aggregate.TerminalFailureError
				if errors.As(err, &terminalFailure) {
					_ = writeAnthropicTerminalFailure(w, flusher, streamState, canon.RequestID, terminalFailure.HealthFlag, terminalFailure.Message)
					return
				}
				if isUpstreamTimeout(err, ctx) {
					_ = writeAnthropicTerminalFailure(w, flusher, streamState, canon.RequestID, "upstream_timeout", "upstream request timed out")
					return
				}
				_ = writeAnthropicTerminalFailure(w, flusher, streamState, canon.RequestID, "upstreamStreamBroken", err.Error())
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
