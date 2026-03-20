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
	modelpkg "openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/upstream"
)

func handleResponses(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		providerCfg := providerConfigForRequest(r, cfg)
		client := upstream.NewClient(providerCfg.UpstreamBaseURL)
		setNormalizationVersionHeader(w)
		authorization, err := authHeaderForUpstream(r, providerCfg)
		if err != nil {
			errorsx.WriteJSON(w, http.StatusUnauthorized, "missing_upstream_auth", err.Error())
			return
		}

		canon, err := responsesadapter.DecodeRequest(r.Body)
		if err != nil {
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if provider, ok := providerForRequest(r, cfg); ok {
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
		}
		canon.RequestID = w.Header().Get("X-Request-Id")
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
			flusher := startSSE(w)
			if err := writeResponsesSSELive(ctx, client, w, flusher, canon, authorization); err != nil {
				if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
					return
				}
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
		if err := writeJSON(w, responsesadapter.BuildResponse(result)); err != nil {
			errorsx.WriteJSON(w, http.StatusInternalServerError, "encode_error", err.Error())
			return
		}
		logging.Event("downstream_responses_usage_mapped", map[string]any{
			"request_id":    canon.RequestID,
			"cached_tokens": nestedCachedTokens(result.Usage),
			"usage_present": len(result.Usage) > 0,
		})
	}
}
