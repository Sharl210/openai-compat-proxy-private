package httpapi

import (
	"context"
	"errors"
	"net/http"

	chatadapter "openai-compat-proxy/internal/adapter/chat"
	"openai-compat-proxy/internal/aggregate"
	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/errorsx"
	"openai-compat-proxy/internal/upstream"
)

func handleChat(cfg config.Config) http.HandlerFunc {
	client := upstream.NewClient(cfg.UpstreamBaseURL)

	return func(w http.ResponseWriter, r *http.Request) {
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

		ctx := r.Context()
		var cancel context.CancelFunc
		if cfg.TotalTimeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, cfg.TotalTimeout)
			defer cancel()
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

		if canon.Stream {
			flusher := startSSE(w)
			if err := writeChatSSE(w, flusher, events, canon.IncludeUsage); err != nil {
				errorsx.WriteJSON(w, http.StatusInternalServerError, "stream_write_error", err.Error())
			}
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := writeJSON(w, chatadapter.BuildResponse(result)); err != nil {
			errorsx.WriteJSON(w, http.StatusInternalServerError, "encode_error", err.Error())
			return
		}
	}
}
