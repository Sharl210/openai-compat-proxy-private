package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/errorsx"
	"openai-compat-proxy/internal/upstream"
)

func handleImageGeneration() http.HandlerFunc {
	return handleImagesPassthrough(canonicalV1ImagesGenerationsPath, "/images/generations")
}

func handleImageEdit() http.HandlerFunc {
	return handleImagesPassthrough(canonicalV1ImagesEditsPath, "/images/edits")
}

func handleImageVariation() http.HandlerFunc {
	return handleImagesPassthrough(canonicalV1ImagesVariationsPath, "/images/variations")
}

func handleEmbeddings() http.HandlerFunc {
	return handleJSONPassthrough(canonicalV1EmbeddingsPath, "/embeddings")
}

func handleRerank() http.HandlerFunc {
	return handleJSONPassthrough(canonicalV1RerankPath, "/rerank")
}

func handleImagesPassthrough(routePath string, upstreamPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		prepared, ok := prepareImagesRequest(w, r)
		if !ok {
			return
		}

		body, contentType, err := rewriteImagesRequestBody(prepared.body, prepared.contentType, prepared.resolvedModel)
		if err != nil {
			clearTransparencyHeaders(w)
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}

		ctx := r.Context()
		var cancel context.CancelFunc
		if prepared.providerCfg.TotalTimeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, prepared.providerCfg.TotalTimeout)
			defer cancel()
		}

		status, respContentType, payload, err := upstream.PassThrough(ctx, prepared.providerCfg, prepared.authorization, http.MethodPost, upstreamPath, contentType, body)
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

		errorsx.WriteRaw(w, status, respContentType, payload)
		_ = routePath
	}
}

func handleJSONPassthrough(routePath string, upstreamPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		prepared, ok := prepareJSONPassthroughRequest(w, r)
		if !ok {
			return
		}

		body, err := rewriteJSONModelRequestBody(prepared.body, prepared.resolvedModel)
		if err != nil {
			clearTransparencyHeaders(w)
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}

		ctx := r.Context()
		var cancel context.CancelFunc
		if prepared.providerCfg.TotalTimeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, prepared.providerCfg.TotalTimeout)
			defer cancel()
		}

		status, respContentType, payload, err := upstream.PassThrough(ctx, prepared.providerCfg, prepared.authorization, http.MethodPost, upstreamPath, prepared.contentType, body)
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

		errorsx.WriteRaw(w, status, respContentType, payload)
		_ = routePath
	}
}

type preparedImagesRequest struct {
	providerCfg   config.Config
	authorization string
	resolvedModel string
	body          []byte
	contentType   string
}

type preparedJSONPassthroughRequest struct {
	providerCfg   config.Config
	authorization string
	resolvedModel string
	body          []byte
	contentType   string
}

func prepareImagesRequest(w http.ResponseWriter, r *http.Request) (*preparedImagesRequest, bool) {
	setNormalizationVersionHeader(w)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		clearTransparencyHeaders(w)
		errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", "failed to read request body")
		return nil, false
	}
	contentType := r.Header.Get("Content-Type")
	modelName, err := extractImagesRequestModel(body, contentType)
	if err != nil {
		clearTransparencyHeaders(w)
		errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", err.Error())
		return nil, false
	}
	provider, providerCfg, providerID, resolvedModel, ok := providerSelectionForModelRequest(r, modelName)
	if !ok {
		errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_model", "requested model is not in models list")
		return nil, false
	}
	if mappedModel := provider.ResolveModel(modelName, provider.EnableReasoningEffortSuffix); strings.TrimSpace(mappedModel) != "" {
		resolvedModel = mappedModel
	}
	if snapshot, ok := runtimeSnapshotFromRequest(r); ok {
		setConfigVersionHeaders(w, snapshot, providerID)
	}
	authorization, err := authHeaderForResolvedProviderUpstream(r, providerCfg, providerID)
	if err != nil {
		clearTransparencyHeaders(w)
		errorsx.WriteJSON(w, http.StatusUnauthorized, "missing_upstream_auth", err.Error())
		return nil, false
	}
	if err := ensureProviderModelAllowed(r.Context(), r, provider, providerCfg, modelName, authorization); err != nil {
		writeModelAllowanceError(w, err)
		return nil, false
	}
	return &preparedImagesRequest{
		providerCfg:   providerCfg,
		authorization: authorization,
		resolvedModel: resolvedModel,
		body:          body,
		contentType:   contentType,
	}, true
}

func prepareJSONPassthroughRequest(w http.ResponseWriter, r *http.Request) (*preparedJSONPassthroughRequest, bool) {
	setNormalizationVersionHeader(w)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		clearTransparencyHeaders(w)
		errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", "failed to read request body")
		return nil, false
	}
	contentType := r.Header.Get("Content-Type")
	modelName, err := extractJSONRequestModel(body)
	if err != nil {
		clearTransparencyHeaders(w)
		errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", err.Error())
		return nil, false
	}
	provider, providerCfg, providerID, resolvedModel, ok := providerSelectionForModelRequest(r, modelName)
	if !ok {
		errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_model", "requested model is not in models list")
		return nil, false
	}
	if mappedModel := provider.ResolveModel(modelName, provider.EnableReasoningEffortSuffix); strings.TrimSpace(mappedModel) != "" {
		resolvedModel = mappedModel
	}
	if snapshot, ok := runtimeSnapshotFromRequest(r); ok {
		setConfigVersionHeaders(w, snapshot, providerID)
	}
	authorization, err := authHeaderForResolvedProviderUpstream(r, providerCfg, providerID)
	if err != nil {
		clearTransparencyHeaders(w)
		errorsx.WriteJSON(w, http.StatusUnauthorized, "missing_upstream_auth", err.Error())
		return nil, false
	}
	if err := ensureProviderModelAllowed(r.Context(), r, provider, providerCfg, modelName, authorization); err != nil {
		writeModelAllowanceError(w, err)
		return nil, false
	}
	return &preparedJSONPassthroughRequest{
		providerCfg:   providerCfg,
		authorization: authorization,
		resolvedModel: resolvedModel,
		body:          body,
		contentType:   contentType,
	}, true
}

func extractImagesRequestModel(body []byte, contentType string) (string, error) {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if strings.HasPrefix(mediaType, "multipart/") {
		return extractMultipartImagesModel(body, contentType)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return "", nil
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	return strings.TrimSpace(stringValue(payload["model"])), nil
}

func extractJSONRequestModel(body []byte) (string, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return "", nil
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	return strings.TrimSpace(stringValue(payload["model"])), nil
}

func extractMultipartImagesModel(body []byte, contentType string) (string, error) {
	reader, err := multipart.NewReader(bytes.NewReader(body), multipartBoundary(contentType)).ReadForm(64 << 20)
	if err != nil {
		return "", err
	}
	defer reader.RemoveAll()
	values := reader.Value["model"]
	if len(values) == 0 {
		return "", nil
	}
	return strings.TrimSpace(values[0]), nil
}

func rewriteImagesRequestBody(body []byte, contentType string, resolvedModel string) ([]byte, string, error) {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if strings.HasPrefix(mediaType, "multipart/") {
		rewritten, rewrittenContentType, err := rewriteMultipartImagesRequestBody(body, contentType, resolvedModel)
		return rewritten, rewrittenContentType, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return body, contentType, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, "", err
	}
	if resolvedModel != "" {
		payload["model"] = resolvedModel
	}
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	return rewritten, contentType, nil
}

func rewriteJSONModelRequestBody(body []byte, resolvedModel string) ([]byte, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return body, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if resolvedModel != "" {
		payload["model"] = resolvedModel
	}
	return json.Marshal(payload)
}

func rewriteMultipartImagesRequestBody(body []byte, contentType string, resolvedModel string) ([]byte, string, error) {
	boundary := multipartBoundary(contentType)
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	var rewritten bytes.Buffer
	writer := multipart.NewWriter(&rewritten)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", err
		}
		data, err := io.ReadAll(part)
		part.Close()
		if err != nil {
			return nil, "", err
		}
		if filename := part.FileName(); filename != "" {
			newPart, err := writer.CreateFormFile(part.FormName(), filename)
			if err != nil {
				return nil, "", err
			}
			if _, err := newPart.Write(data); err != nil {
				return nil, "", err
			}
			continue
		}
		value := string(data)
		if part.FormName() == "model" && resolvedModel != "" {
			value = resolvedModel
		}
		if err := writer.WriteField(part.FormName(), value); err != nil {
			return nil, "", err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return rewritten.Bytes(), writer.FormDataContentType(), nil
}

func multipartBoundary(contentType string) string {
	_, params, _ := mime.ParseMediaType(contentType)
	return params["boundary"]
}
