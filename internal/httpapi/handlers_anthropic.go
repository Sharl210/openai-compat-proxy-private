package httpapi

import (
	"context"
	"errors"
	"net/http"

	anthropicadapter "openai-compat-proxy/internal/adapter/anthropic"
	"openai-compat-proxy/internal/aggregate"
	"openai-compat-proxy/internal/errorsx"
	modelpkg "openai-compat-proxy/internal/model"
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
		authorization, err := authHeaderForUpstream(r, providerCfg)
		if err != nil {
			errorsx.WriteJSON(w, http.StatusUnauthorized, "missing_upstream_auth", err.Error())
			return
		}
		canon, err := anthropicadapter.DecodeRequest(r.Body)
		if err != nil {
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		applyProviderSystemPrompt(&canon, provider)
		canon.RequestID = w.Header().Get("X-Request-Id")
		statusStore, _ := requestStatusStoreFromRequest(r)
		providerID := provider.ID
		mappedModel, effort := provider.ResolveModelAndEffort(canon.Model, provider.EnableReasoningEffortSuffix)
		canon.Model = mappedModel
		if effort != "" {
			if canon.Reasoning == nil {
				canon.Reasoning = &modelpkg.CanonicalReasoning{}
			}
			canon.Reasoning.Effort = effort
			canon.Reasoning.Raw = map[string]any{"effort": effort, "summary": "auto"}
			canon.Reasoning.Summary = "auto"
		}
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
				if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
					setRequestStatusHeaders(w, r, providerID, canon.RequestID, providerCfg.ProxyAPIKey, "upstream_timeout")
					errorsx.WriteJSON(w, http.StatusGatewayTimeout, "upstream_timeout", "upstream request timed out")
					return
				}
				if statusStore != nil {
					statusStore.markFailed(canon.RequestID, "upstream_error", "upstream_error", err.Error())
				}
				setRequestStatusHeaders(w, r, providerID, canon.RequestID, providerCfg.ProxyAPIKey, "upstream_error")
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
			if err := writeAnthropicSSELive(ctx, stream, w, flusher, canon); err != nil {
				if statusStore != nil {
					statusStore.markFailed(canon.RequestID, "upstream_stream_broken", "upstream_stream_broken", err.Error())
				}
				_ = writeAnthropicTerminalFailure(w, flusher, canon.RequestID, "upstream_stream_broken", err.Error())
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
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				setRequestStatusHeaders(w, r, providerID, canon.RequestID, providerCfg.ProxyAPIKey, "upstream_timeout")
				errorsx.WriteJSON(w, http.StatusGatewayTimeout, "upstream_timeout", "upstream request timed out")
				return
			}
			if statusStore != nil {
				statusStore.markFailed(canon.RequestID, "upstream_error", "upstream_error", err.Error())
			}
			setRequestStatusHeaders(w, r, providerID, canon.RequestID, providerCfg.ProxyAPIKey, "upstream_error")
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
			setRequestStatusHeaders(w, r, providerID, canon.RequestID, providerCfg.ProxyAPIKey, "proxy_internal_error")
			errorsx.WriteJSON(w, http.StatusBadGateway, "invalid_upstream_stream", err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := writeJSON(w, anthropicadapter.BuildResponse(result, canon.Model)); err != nil {
			if statusStore != nil {
				statusStore.markFailed(canon.RequestID, "proxy_internal_error", "encode_error", err.Error())
			}
			setRequestStatusHeaders(w, r, providerID, canon.RequestID, providerCfg.ProxyAPIKey, "proxy_internal_error")
			errorsx.WriteJSON(w, http.StatusInternalServerError, "encode_error", err.Error())
			return
		}
		if statusStore != nil {
			statusStore.markCompleted(canon.RequestID)
		}
	}
}
