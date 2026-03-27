package httpapi

import (
	"context"
	"net/http"

	chatadapter "openai-compat-proxy/internal/adapter/chat"
	"openai-compat-proxy/internal/aggregate"
	"openai-compat-proxy/internal/errorsx"
	"openai-compat-proxy/internal/logging"
	"openai-compat-proxy/internal/upstream"
)

func handleChat() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		providerCfg := providerConfigForRequest(r)
		provider, ok := providerForRequest(r)
		if !ok || !provider.SupportsChat {
			if statusStore, _ := requestStatusStoreFromRequest(r); statusStore != nil {
				statusStore.markFailed(w.Header().Get("X-Request-Id"), "proxy_internal_error", "unsupported_provider_contract", "provider does not support chat completions")
			}
			setRequestStatusHeaders(w, r, provider.ID, w.Header().Get("X-Request-Id"), statusCheckProxyKeyForRequest(r, providerCfg, provider), "proxy_internal_error")
			errorsx.WriteJSON(w, http.StatusBadRequest, "unsupported_provider_contract", "provider does not support chat completions")
			return
		}
		client := upstream.NewClient(providerCfg.UpstreamBaseURL, providerCfg)
		setNormalizationVersionHeader(w)
		authorization, err := authHeaderForUpstream(r, providerCfg)
		if err != nil {
			errorsx.WriteJSON(w, http.StatusUnauthorized, "missing_upstream_auth", err.Error())
			return
		}

		canon, err := chatadapter.DecodeRequest(r.Body)
		if err != nil {
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		applyProviderSystemPrompt(&canon, provider)
		if ok {
			mappedModel, effort := provider.ResolveModelAndEffort(canon.Model, provider.EnableReasoningEffortSuffix)
			canon.Model = mappedModel
			canon.Reasoning = applyResolvedReasoningEffort(canon.Reasoning, effort)
		}
		canon.RequestID = w.Header().Get("X-Request-Id")
		canon.AuthMode = authModeForUpstream(r, providerCfg)
		statusStore, _ := requestStatusStoreFromRequest(r)
		providerID := provider.ID
		statusCheckKey := statusCheckProxyKeyForRequest(r, providerCfg, provider)
		attrs := map[string]any{
			"request_id":            canon.RequestID,
			"route":                 "/v1/chat/completions",
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
			if err := writeChatSSELive(ctx, stream, w, flusher, canon); err != nil {
				if statusStore != nil {
					statusStore.markFailed(canon.RequestID, "upstream_stream_broken", "upstream_stream_broken", err.Error())
				}
				_ = writeChatTerminalFailure(w, flusher, "upstream_stream_broken", err.Error())
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
		if len(result.UnsupportedContentTypes) > 0 {
			if statusStore != nil {
				statusStore.markFailed(canon.RequestID, "proxy_internal_error", "unsupported_output_mapping", "upstream returned unsupported chat output content")
			}
			setRequestStatusHeaders(w, r, providerID, canon.RequestID, statusCheckKey, "proxy_internal_error")
			errorsx.WriteJSON(w, http.StatusBadGateway, "unsupported_output_mapping", "upstream returned unsupported chat output content")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := writeJSON(w, chatadapter.BuildResponse(result)); err != nil {
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
		logging.Event("downstream_chat_usage_mapped", map[string]any{
			"request_id":    canon.RequestID,
			"cached_tokens": nestedCachedTokens(result.Usage),
			"usage_present": len(result.Usage) > 0,
		})
	}
}
