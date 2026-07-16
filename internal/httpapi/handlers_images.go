package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/errorsx"
)

var imageArtifactRootDirOverride string
var imageArtifactTTL = 24 * time.Hour
var imageArtifactNow = time.Now
var imageArtifactCleanupInterval = 30 * time.Minute

var imageArtifactsInit sync.Map

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

		client := upstreamClientForProvider(r, prepared.providerID, prepared.providerCfg)
		status, respContentType, payload, err := client.PassThrough(ctx, prepared.authorization, http.MethodPost, upstreamPath, contentType, body)
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

		payload, respContentType, err = rewriteImageResponseForRequestedFormat(r, prepared.requestedFormat, payload, respContentType)
		if err != nil {
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

		client := upstreamClientForProvider(r, prepared.providerID, prepared.providerCfg)
		status, respContentType, payload, err := client.PassThrough(ctx, prepared.authorization, http.MethodPost, upstreamPath, prepared.contentType, body)
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
	providerID      string
	providerCfg     config.Config
	authorization   string
	resolvedModel   string
	body            []byte
	contentType     string
	requestedFormat string
}

type preparedJSONPassthroughRequest struct {
	providerID    string
	providerCfg   config.Config
	authorization string
	resolvedModel string
	body          []byte
	contentType   string
}

func prepareImagesRequest(w http.ResponseWriter, r *http.Request) (*preparedImagesRequest, bool) {
	ensureImageArtifactsReady(r)
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
	provider, providerCfg, providerID, resolvedModel, ok, selectionErr := providerSelectionForModelRequest(r, modelName)
	if !ok {
		if writeUpstreamError(w, selectionErr) {
			return nil, false
		}
		errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_model", "requested model is not in models list")
		return nil, false
	}
	if mappedModel := provider.ResolveModel(resolvedModel, provider.EnableReasoningEffortSuffix); strings.TrimSpace(mappedModel) != "" {
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
	requestedFormat, _ := extractRequestedImageResponseFormat(body, contentType)
	return &preparedImagesRequest{
		providerID:      providerID,
		providerCfg:     providerCfg,
		authorization:   authorization,
		resolvedModel:   resolvedModel,
		body:            body,
		contentType:     contentType,
		requestedFormat: requestedFormat,
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
	provider, providerCfg, providerID, resolvedModel, ok, selectionErr := providerSelectionForModelRequest(r, modelName)
	if !ok {
		if writeUpstreamError(w, selectionErr) {
			return nil, false
		}
		errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_model", "requested model is not in models list")
		return nil, false
	}
	if mappedModel := provider.ResolveModel(resolvedModel, provider.EnableReasoningEffortSuffix); strings.TrimSpace(mappedModel) != "" {
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
		providerID:    providerID,
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

func rewriteImageResponseForRequestedFormat(r *http.Request, requestedFormat string, payload []byte, contentType string) ([]byte, string, error) {
	requestedFormat = strings.TrimSpace(requestedFormat)
	if requestedFormat == "" {
		requestedFormat = "b64_json"
	}
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, "", err
	}
	data, _ := body["data"].([]any)
	if len(data) == 0 {
		return payload, contentType, nil
	}
	switch requestedFormat {
	case "b64_json":
		changed, err := convertImageResponseURLToB64(data)
		if err != nil {
			return nil, "", err
		}
		if !changed {
			return payload, contentType, nil
		}
	case "url":
		changed, err := convertImageResponseB64ToURL(r, data)
		if err != nil {
			return nil, "", err
		}
		if !changed {
			return payload, contentType, nil
		}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, "", err
	}
	if contentType == "" {
		contentType = "application/json"
	}
	return encoded, contentType, nil
}

func extractRequestedImageResponseFormat(body []byte, contentType string) (string, bool) {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if strings.HasPrefix(mediaType, "multipart/") {
		reader, err := multipart.NewReader(bytes.NewReader(body), multipartBoundary(contentType)).ReadForm(64 << 20)
		if err != nil {
			return "", false
		}
		defer reader.RemoveAll()
		values := reader.Value["response_format"]
		if len(values) == 0 {
			return "", false
		}
		text := strings.TrimSpace(values[0])
		if text == "" {
			return "", false
		}
		return text, true
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return "", false
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", false
	}
	text, _ := payload["response_format"].(string)
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	return text, true
}

func convertImageResponseURLToB64(data []any) (bool, error) {
	changed := false
	for _, raw := range data {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		if existing, _ := item["b64_json"].(string); strings.TrimSpace(existing) != "" {
			continue
		}
		urlText, _ := item["url"].(string)
		urlText = strings.TrimSpace(urlText)
		if urlText == "" {
			continue
		}
		resp, err := http.Get(urlText)
		if err != nil {
			return false, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if readErr != nil {
			return false, readErr
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return false, fmt.Errorf("download image url failed with status %d", resp.StatusCode)
		}
		item["b64_json"] = base64.StdEncoding.EncodeToString(body)
		delete(item, "url")
		changed = true
	}
	return changed, nil
}

func convertImageResponseB64ToURL(r *http.Request, data []any) (bool, error) {
	changed := false
	for idx, raw := range data {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		if existing, _ := item["url"].(string); strings.TrimSpace(existing) != "" {
			continue
		}
		b64, _ := item["b64_json"].(string)
		b64 = strings.TrimSpace(b64)
		if b64 == "" {
			continue
		}
		content, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return false, err
		}
		publicURL, err := writeImageArtifact(r, idx, content, inferImageExtension(item))
		if err != nil {
			return false, err
		}
		item["url"] = publicURL
		delete(item, "b64_json")
		changed = true
	}
	return changed, nil
}

func inferImageExtension(item map[string]any) string {
	if item == nil {
		return ".bin"
	}
	format, _ := item["output_format"].(string)
	format = strings.TrimSpace(strings.ToLower(format))
	switch format {
	case "png":
		return ".png"
	case "jpg", "jpeg":
		return ".jpg"
	case "webp":
		return ".webp"
	default:
		return ".bin"
	}
}

func imageArtifactRootDir(r *http.Request) string {
	if imageArtifactRootDirOverride != "" {
		return imageArtifactRootDirOverride
	}
	if snapshot, ok := runtimeSnapshotFromRequest(r); ok && snapshot != nil && snapshot.RootEnvPath != "" {
		return imageArtifactRootDirFromSnapshot(snapshot)
	}
	return ""
}

func imageArtifactRootDirFromSnapshot(snapshot *config.RuntimeSnapshot) string {
	if imageArtifactRootDirOverride != "" {
		return imageArtifactRootDirOverride
	}
	if snapshot == nil || snapshot.RootEnvPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(snapshot.RootEnvPath), ".image-artifacts")
}

func ensureImageArtifactsReady(r *http.Request) {
	root := imageArtifactRootDir(r)
	ensureImageArtifactsReadyForRoot(root)
}

func ensureImageArtifactsReadyForRoot(root string) {
	if root == "" {
		return
	}
	if _, loaded := imageArtifactsInit.LoadOrStore(root, struct{}{}); loaded {
		return
	}
	_ = os.MkdirAll(root, 0o755)
	pruneExpiredImageArtifacts(root)
	go func(dir string) {
		ticker := time.NewTicker(imageArtifactCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			pruneExpiredImageArtifacts(dir)
		}
	}(root)
}

func pruneExpiredImageArtifacts(root string) {
	if root == "" {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	now := imageArtifactNow()
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if artifactExpired(entry.Name(), info.ModTime(), now) {
			_ = os.Remove(filepath.Join(root, entry.Name()))
		}
	}
}

func artifactExpired(name string, modTime time.Time, now time.Time) bool {
	createdAt, ok := imageArtifactCreatedAt(name)
	if !ok {
		createdAt = modTime
	}
	return now.Sub(createdAt) >= imageArtifactTTL
}

func imageArtifactCreatedAt(name string) (time.Time, bool) {
	base := filepath.Base(name)
	prefix, _, ok := strings.Cut(base, "_")
	if !ok {
		return time.Time{}, false
	}
	seconds, err := strconv.ParseInt(prefix, 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0), true
}

func writeImageArtifact(r *http.Request, index int, content []byte, ext string) (string, error) {
	root := imageArtifactRootDir(r)
	if root == "" {
		return "", fmt.Errorf("image artifact root unavailable")
	}
	pruneExpiredImageArtifacts(root)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	now := imageArtifactNow()
	name := fmt.Sprintf("%d_%d%s", now.Unix(), index, ext)
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", err
	}
	baseURL := imageArtifactBaseURL(r)
	if baseURL == "" {
		return "", fmt.Errorf("image artifact base url unavailable")
	}
	return baseURL + "/_images/" + name, nil
}

func imageArtifactBaseURL(r *http.Request) string {
	if r == nil || r.Host == "" {
		return ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func handleImageArtifact() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ensureImageArtifactsReady(r)
		name := strings.TrimPrefix(r.URL.Path, "/_images/")
		name = strings.TrimSpace(name)
		if name == "" || strings.Contains(name, "/") || strings.Contains(name, "..") {
			errorsx.WriteJSON(w, http.StatusNotFound, "not_found", "route not found")
			return
		}
		root := imageArtifactRootDir(r)
		if root == "" {
			errorsx.WriteJSON(w, http.StatusNotFound, "not_found", "route not found")
			return
		}
		path := filepath.Join(root, name)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			errorsx.WriteJSON(w, http.StatusNotFound, "not_found", "route not found")
			return
		}
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(imageArtifactTTL/time.Second)))
		http.ServeFile(w, r, path)
	}
}
