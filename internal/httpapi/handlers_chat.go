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
	"openai-compat-proxy/internal/upstream"
)

func handleChat(cfg config.Config) http.HandlerFunc {
	client := upstream.NewClient(cfg.UpstreamBaseURL)

	return func(w http.ResponseWriter, r *http.Request) {
		setNormalizationVersionHeader(w)
		authorization, err := authHeaderForUpstream(r, cfg)
		if err != nil {
			errorsx.WriteJSON(w, http.StatusUnauthorized, "missing_upstream_auth", err.Error())
			return
		}

		canon, err := chatadapter.DecodeRequest(r.Body)
		if err != nil {
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		canon.RequestID = w.Header().Get("X-Request-Id")
		canon.AuthMode = authModeForUpstream(r, cfg)
		logging.Event("canonical_request_built", map[string]any{
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
		})

		ctx := r.Context()
		var cancel context.CancelFunc
		if cfg.TotalTimeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, cfg.TotalTimeout)
			defer cancel()
		}

		if canon.Stream {
			flusher := startSSE(w)
			if err := writeChatSSELive(ctx, client, w, flusher, canon, authorization); err != nil {
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
			errorsx.WriteJSON(w, http.StatusBadGateway, "unsupported_output_mapping", "upstream returned unsupported chat output content")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := writeJSON(w, chatadapter.BuildResponse(result)); err != nil {
			errorsx.WriteJSON(w, http.StatusInternalServerError, "encode_error", err.Error())
			return
		}
		logging.Event("downstream_chat_usage_mapped", map[string]any{
			"request_id":    canon.RequestID,
			"cached_tokens": nestedCachedTokens(result.Usage),
			"usage_present": len(result.Usage) > 0,
		})
	}
}
