package httpapi

import (
	"context"
	"errors"
	"net/http"

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
			if statusStore, _ := requestStatusStoreFromRequest(r); statusStore != nil {
				statusStore.markFailed(w.Header().Get("X-Request-Id"), "proxy_internal_error", "unsupported_provider_contract", "provider does not support anthropic messages")
			}
			setRequestStatusHeaders(w, r, provider.ID, w.Header().Get("X-Request-Id"), statusCheckProxyKeyForRequest(r, providerCfg, provider), "proxy_internal_error")
			errorsx.WriteJSON(w, http.StatusBadRequest, "unsupported_provider_contract", "provider does not support anthropic messages")
			return
		}
		client := upstream.NewClient(providerCfg.UpstreamBaseURL, providerCfg)
		requestID := w.Header().Get("X-Request-Id")
		statusStore, _ := requestStatusStoreFromRequest(r)
		providerID := provider.ID
		statusCheckKey := statusCheckProxyKeyForRequest(r, providerCfg, provider)
		authorization, err := authHeaderForUpstream(r, providerCfg)
		if err != nil {
			if statusStore != nil {
				statusStore.markFailed(requestID, "proxy_internal_error", "missing_upstream_auth", err.Error())
			}
			setRequestStatusHeaders(w, r, providerID, requestID, statusCheckKey, "proxy_internal_error")
			errorsx.WriteJSON(w, http.StatusUnauthorized, "missing_upstream_auth", err.Error())
			return
		}
		canon, err := anthropicadapter.DecodeRequest(r.Body)
		if err != nil {
			if statusStore != nil {
				statusStore.markFailed(requestID, "proxy_internal_error", "invalid_request", err.Error())
			}
			setRequestStatusHeaders(w, r, providerID, requestID, statusCheckKey, "proxy_internal_error")
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		applyProviderSystemPrompt(&canon, provider)
		canon.RequestID = requestID
		mappedModel, effort := provider.ResolveModelAndEffort(canon.Model, provider.EnableReasoningEffortSuffix)
		canon.Model = mappedModel
		canon.Reasoning = applyResolvedReasoningEffort(canon.Reasoning, effort)
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
			streamState := &anthropicStreamState{}
			if err := writeAnthropicSSELive(ctx, stream, w, flusher, canon, streamState); err != nil {
				var terminalFailure *aggregate.TerminalFailureError
				if errors.As(err, &terminalFailure) {
					if statusStore != nil {
						statusStore.markFailed(canon.RequestID, terminalFailure.HealthFlag, terminalFailure.HealthFlag, terminalFailure.Message)
					}
					_ = writeAnthropicTerminalFailure(w, flusher, streamState, canon.RequestID, terminalFailure.HealthFlag, terminalFailure.Message)
					return
				}
				if isUpstreamTimeout(err, ctx) {
					if statusStore != nil {
						statusStore.markFailed(canon.RequestID, "upstream_timeout", "upstream_timeout", "upstream request timed out")
					}
					_ = writeAnthropicTerminalFailure(w, flusher, streamState, canon.RequestID, "upstream_timeout", "upstream request timed out")
					return
				}
				if statusStore != nil {
					statusStore.markFailed(canon.RequestID, "upstream_stream_broken", "upstream_stream_broken", err.Error())
				}
				_ = writeAnthropicTerminalFailure(w, flusher, streamState, canon.RequestID, "upstream_stream_broken", err.Error())
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
			result, err := aggregate.ResultFromResponsePayload(payload)
			if err != nil {
				if statusStore != nil {
					statusStore.markFailed(canon.RequestID, "proxy_internal_error", "invalid_upstream_response", err.Error())
				}
				setRequestStatusHeaders(w, r, providerID, canon.RequestID, statusCheckKey, "proxy_internal_error")
				errorsx.WriteJSON(w, http.StatusBadGateway, "invalid_upstream_response", err.Error())
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if err := writeJSON(w, anthropicadapter.BuildResponse(result, canon.Model)); err != nil {
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
			if statusStore != nil {
				statusStore.markFailed(canon.RequestID, "proxy_internal_error", "invalid_upstream_stream", err.Error())
			}
			setRequestStatusHeaders(w, r, providerID, canon.RequestID, statusCheckKey, "proxy_internal_error")
			errorsx.WriteJSON(w, http.StatusBadGateway, "invalid_upstream_stream", err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := writeJSON(w, anthropicadapter.BuildResponse(result, canon.Model)); err != nil {
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
	}
}
