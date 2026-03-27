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
			if statusStore, _ := requestStatusStoreFromRequest(r); statusStore != nil {
				statusStore.markFailed(w.Header().Get("X-Request-Id"), "proxy_internal_error", "unsupported_provider_contract", "provider does not support responses")
			}
			setRequestStatusHeaders(w, r, provider.ID, w.Header().Get("X-Request-Id"), statusCheckProxyKeyForRequest(r, providerCfg, provider), "proxy_internal_error")
			errorsx.WriteJSON(w, http.StatusBadRequest, "unsupported_provider_contract", "provider does not support responses")
			return
		}
		client := upstream.NewClient(providerCfg.UpstreamBaseURL, providerCfg)
		setNormalizationVersionHeader(w)
		requestID := w.Header().Get("X-Request-Id")
		statusStore, _ := requestStatusStoreFromRequest(r)
		providerID := provider.ID
		statusCheckKey := statusCheckProxyKeyForRequest(r, providerCfg, provider)
		usageRecorder := cacheInfoUsageRecorder(r, requestID, providerID)
		authorization, err := authHeaderForUpstream(r, providerCfg)
		if err != nil {
			if statusStore != nil {
				statusStore.markFailed(requestID, "proxy_internal_error", "missing_upstream_auth", err.Error())
			}
			setRequestStatusHeaders(w, r, providerID, requestID, statusCheckKey, "proxy_internal_error")
			errorsx.WriteJSON(w, http.StatusUnauthorized, "missing_upstream_auth", err.Error())
			return
		}

		canon, err := responsesadapter.DecodeRequest(r.Body)
		if err != nil {
			if statusStore != nil {
				statusStore.markFailed(requestID, "proxy_internal_error", "invalid_request", err.Error())
			}
			setRequestStatusHeaders(w, r, providerID, requestID, statusCheckKey, "proxy_internal_error")
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		applyProviderSystemPrompt(&canon, provider)
		if ok {
			mappedModel, effort := provider.ResolveModelAndEffort(canon.Model, provider.EnableReasoningEffortSuffix)
			canon.Model = mappedModel
			canon.Reasoning = applyResolvedReasoningEffort(canon.Reasoning, effort)
		}
		responseHealthFlag := "health"
		if canon.Stream {
			responseHealthFlag = "streaming"
		}
		setRequestStatusHeaders(w, r, providerID, requestID, statusCheckKey, responseHealthFlag)
		canon.RequestID = requestID
		canon.AuthMode = authModeForUpstream(r, providerCfg)
		attrs := map[string]any{
			"request_id":            canon.RequestID,
			"route":                 "/v1/responses",
			"auth_mode":             canon.AuthMode,
			"model":                 canon.Model,
			"stream":                canon.Stream,
			"include_usage":         canon.IncludeUsage,
			"message_count":         len(canon.Messages),
			"tool_count":            len(canon.Tools),
			"has_reasoning":         canon.Reasoning != nil,
			"normalization_version": normalizationVersion,
		}
		for k, v := range canonicalLogAttrs(canon) {
			attrs[k] = v
		}
		logging.Event("canonical_request_built", attrs)

		ctx := r.Context()
		var cancel context.CancelFunc
		if providerCfg.TotalTimeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, providerCfg.TotalTimeout)
			defer cancel()
		}

		if canon.Stream {
			stream, err := client.OpenEventStream(ctx, canon, authorization)
			if err != nil {
				if statusStore != nil {
					statusStore.markFailed(canon.RequestID, "upstream_timeout", "upstream_timeout", "upstream request timed out")
				}
				if isUpstreamTimeout(err, ctx) {
					setRequestStatusHeaders(w, r, providerID, canon.RequestID, statusCheckKey, "upstream_timeout")
					errorsx.WriteJSON(w, http.StatusGatewayTimeout, "upstream_timeout", "upstream request timed out")
					return
				}
				if statusStore != nil {
					statusStore.markFailed(canon.RequestID, "upstream_error", "upstream_error", err.Error())
				}
				setRequestStatusHeaders(w, r, providerID, canon.RequestID, statusCheckKey, "upstream_error")
				if writeUpstreamError(w, err) {
					return
				}
				errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
				return
			}
			defer stream.Close()
			if statusStore != nil {
				statusStore.markStreaming(canon.RequestID)
			}
			flusher := startSSE(w)
			if err := writeResponsesSSELive(ctx, stream, w, flusher, canon, usageRecorder); err != nil {
				var terminalFailure *aggregate.TerminalFailureError
				if errors.As(err, &terminalFailure) {
					if statusStore != nil {
						statusStore.markFailed(canon.RequestID, terminalFailure.HealthFlag, terminalFailure.HealthFlag, terminalFailure.Message)
					}
					return
				}
				if isUpstreamTimeout(err, ctx) {
					if statusStore != nil {
						statusStore.markFailed(canon.RequestID, "upstream_timeout", "upstream_timeout", "upstream request timed out")
					}
					_ = writeResponsesTerminalFailure(w, flusher, canon.RequestID, "upstream_timeout", "upstream request timed out")
					return
				}
				if statusStore != nil {
					statusStore.markFailed(canon.RequestID, "upstream_stream_broken", "upstream_stream_broken", err.Error())
				}
				_ = writeResponsesTerminalFailure(w, flusher, canon.RequestID, "upstream_stream_broken", err.Error())
				return
			}
			if statusStore != nil {
				statusStore.markCompleted(canon.RequestID)
			}
			return
		}

		if providerCfg.DownstreamNonStreamStrategy == config.DownstreamNonStreamStrategyUpstreamNonStream {
			payload, err := client.Response(ctx, canon, authorization)
			if err != nil {
				if statusStore != nil {
					statusStore.markFailed(canon.RequestID, "upstream_timeout", "upstream_timeout", "upstream request timed out")
				}
				if isUpstreamTimeout(err, ctx) {
					setRequestStatusHeaders(w, r, providerID, canon.RequestID, statusCheckKey, "upstream_timeout")
					errorsx.WriteJSON(w, http.StatusGatewayTimeout, "upstream_timeout", "upstream request timed out")
					return
				}
				if statusStore != nil {
					statusStore.markFailed(canon.RequestID, "upstream_error", "upstream_error", err.Error())
				}
				setRequestStatusHeaders(w, r, providerID, canon.RequestID, statusCheckKey, "upstream_error")
				if writeUpstreamError(w, err) {
					return
				}
				errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", err.Error())
				return
			}
			normalized := payload
			if result, err := aggregate.ResultFromResponsePayload(payload); err == nil {
				normalized = responsesadapter.BuildResponse(result)
			}
			w.Header().Set("Content-Type", "application/json")
			if err := writeJSON(w, normalized); err != nil {
				if statusStore != nil {
					statusStore.markFailed(canon.RequestID, "proxy_internal_error", "encode_error", err.Error())
				}
				setRequestStatusHeaders(w, r, providerID, canon.RequestID, statusCheckKey, "proxy_internal_error")
				errorsx.WriteJSON(w, http.StatusInternalServerError, "encode_error", err.Error())
				return
			}
			if statusStore != nil {
				statusStore.markCompleted(canon.RequestID)
			}
			if usageRecorder != nil && len(payload) > 0 {
				if result, err := aggregate.ResultFromResponsePayload(payload); err == nil {
					usageRecorder(result.Usage)
				}
			}
			return
		}

		events, err := client.Stream(ctx, canon, authorization)
		if err != nil {
			if statusStore != nil {
				statusStore.markFailed(canon.RequestID, "upstream_timeout", "upstream_timeout", "upstream request timed out")
			}
			if isUpstreamTimeout(err, ctx) {
				setRequestStatusHeaders(w, r, providerID, canon.RequestID, statusCheckKey, "upstream_timeout")
				errorsx.WriteJSON(w, http.StatusGatewayTimeout, "upstream_timeout", "upstream request timed out")
				return
			}
			if statusStore != nil {
				statusStore.markFailed(canon.RequestID, "upstream_error", "upstream_error", err.Error())
			}
			setRequestStatusHeaders(w, r, providerID, canon.RequestID, statusCheckKey, "upstream_error")
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
				if statusStore != nil {
					statusStore.markFailed(canon.RequestID, terminalFailure.HealthFlag, terminalFailure.HealthFlag, terminalFailure.Message)
				}
				setRequestStatusHeaders(w, r, providerID, canon.RequestID, statusCheckKey, terminalFailure.HealthFlag)
				statusCode := http.StatusBadGateway
				if terminalFailure.HealthFlag == "upstream_timeout" {
					statusCode = http.StatusGatewayTimeout
				}
				errorsx.WriteJSON(w, statusCode, terminalFailure.HealthFlag, terminalFailure.Message)
				return
			}
			if statusStore != nil {
				statusStore.markFailed(canon.RequestID, "proxy_internal_error", "invalid_upstream_stream", err.Error())
			}
			setRequestStatusHeaders(w, r, providerID, canon.RequestID, statusCheckKey, "proxy_internal_error")
			errorsx.WriteJSON(w, http.StatusBadGateway, "invalid_upstream_stream", err.Error())
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := writeJSON(w, responsesadapter.BuildResponse(result)); err != nil {
			if statusStore != nil {
				statusStore.markFailed(canon.RequestID, "proxy_internal_error", "encode_error", err.Error())
			}
			setRequestStatusHeaders(w, r, providerID, canon.RequestID, statusCheckKey, "proxy_internal_error")
			errorsx.WriteJSON(w, http.StatusInternalServerError, "encode_error", err.Error())
			return
		}
		if statusStore != nil {
			statusStore.markCompleted(canon.RequestID)
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
