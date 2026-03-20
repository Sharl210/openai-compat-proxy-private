package httpapi

import (
	"context"
	"errors"
	"net/http"

	anthropicadapter "openai-compat-proxy/internal/adapter/anthropic"
	"openai-compat-proxy/internal/aggregate"
	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/errorsx"
	"openai-compat-proxy/internal/reasoning"
	"openai-compat-proxy/internal/upstream"
)

func handleAnthropicMessages(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		providerCfg := providerConfigForRequest(r, cfg)
		setNormalizationVersionHeader(w)
		provider, ok := providerForRequest(r, cfg)
		if !ok || !provider.SupportsAnthropicMessages {
			errorsx.WriteJSON(w, http.StatusBadRequest, "unsupported_provider_contract", "provider does not support anthropic messages")
			return
		}
		client := upstream.NewClient(providerCfg.UpstreamBaseURL)
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
		canon = reasoning.ApplyModelSuffix(canon, provider.EnableReasoningEffortSuffix)
		canon.Model = provider.ResolveModel(canon.Model)
		ctx := r.Context()
		var cancel context.CancelFunc
		if providerCfg.TotalTimeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, providerCfg.TotalTimeout)
			defer cancel()
		}
		if canon.Stream {
			flusher := startSSE(w)
			if err := writeAnthropicSSELive(ctx, client, w, flusher, canon, authorization); err != nil {
				return
			}
			return
		}
		events, err := client.Stream(ctx, canon, authorization)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
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
		w.Header().Set("Content-Type", "application/json")
		if err := writeJSON(w, anthropicadapter.BuildResponse(result, canon.Model)); err != nil {
			errorsx.WriteJSON(w, http.StatusInternalServerError, "encode_error", err.Error())
			return
		}
	}
}
