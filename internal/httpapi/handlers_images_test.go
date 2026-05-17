package httpapi

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestImagesGenerationsPassesThroughJSONAndMappedModel(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1713833628,"data":[{"b64_json":"ZmFrZQ=="}]}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:              "openai",
			Enabled:         true,
			UpstreamBaseURL: upstream.URL,
			UpstreamAPIKey:  "provider-upstream-key",
			ModelMap: []config.ModelMapEntry{
				config.NewModelMapEntry("gpt-image-1", "gpt-image-2"),
			},
			ManualModels: []string{"gpt-image-1"},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"gpt-image-1","prompt":"otter"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer root-secret")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/images/generations" {
		t.Fatalf("expected upstream path /images/generations, got %q", gotPath)
	}
	if gotAuth != "Bearer provider-upstream-key" {
		t.Fatalf("expected provider upstream auth, got %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"model":"gpt-image-2"`) {
		t.Fatalf("expected mapped model in upstream body, got %s", gotBody)
	}
	if !strings.Contains(gotBody, `"prompt":"otter"`) {
		t.Fatalf("expected prompt passthrough, got %s", gotBody)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("expected application/json response, got %q", got)
	}
	if !strings.Contains(rec.Body.String(), `"b64_json":"ZmFrZQ=="`) {
		t.Fatalf("expected upstream response passthrough, got %s", rec.Body.String())
	}
}

func TestImagesEditsPassesThroughMultipartAndMappedModel(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody string
	var gotContentType string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1713833628,"data":[{"b64_json":"ZWRpdA=="}]}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:              "openai",
			Enabled:         true,
			UpstreamBaseURL: upstream.URL,
			UpstreamAPIKey:  "provider-upstream-key",
			ModelMap: []config.ModelMapEntry{
				config.NewModelMapEntry("gpt-image-1", "gpt-image-2"),
			},
			ManualModels: []string{"gpt-image-1"},
		}},
	})

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("model", "gpt-image-1")
	_ = writer.WriteField("prompt", "edit otter")
	part, err := writer.CreateFormFile("image", "sample.png")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	_, _ = part.Write([]byte("fake-png-data"))
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer root-secret")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/images/edits" {
		t.Fatalf("expected upstream path /images/edits, got %q", gotPath)
	}
	if gotAuth != "Bearer provider-upstream-key" {
		t.Fatalf("expected provider upstream auth, got %q", gotAuth)
	}
	if !strings.HasPrefix(gotContentType, "multipart/form-data;") {
		t.Fatalf("expected multipart content type upstream, got %q", gotContentType)
	}
	if !strings.Contains(gotBody, `name="model"`) || !strings.Contains(gotBody, "gpt-image-2") {
		t.Fatalf("expected mapped multipart model field, got %s", gotBody)
	}
	if !strings.Contains(gotBody, `name="prompt"`) || !strings.Contains(gotBody, "edit otter") {
		t.Fatalf("expected prompt multipart passthrough, got %s", gotBody)
	}
	if !strings.Contains(gotBody, `filename="sample.png"`) || !strings.Contains(gotBody, "fake-png-data") {
		t.Fatalf("expected image file passthrough, got %s", gotBody)
	}
}

func TestImagesVariationsPassesThroughMultipartAndUpstreamAuthPassthrough(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"variation unsupported upstream"}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                     "openai",
			Enabled:                true,
			UpstreamBaseURL:        upstream.URL,
			ProxyAPIKeyOverride:    "empty",
			ProxyAPIKeyOverrideSet: true,
			ModelMap: []config.ModelMapEntry{
				config.NewModelMapEntry("dall-e-2", "dall-e-2"),
			},
			ManualModels: []string{"dall-e-2"},
		}},
	})

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("model", "dall-e-2")
	part, err := writer.CreateFormFile("image", "sample.png")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	_, _ = part.Write([]byte("variation-png-data"))
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/images/variations", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer real-upstream-token")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected upstream status passthrough, got %d body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/images/variations" {
		t.Fatalf("expected upstream path /images/variations, got %q", gotPath)
	}
	if gotAuth != "Bearer real-upstream-token" {
		t.Fatalf("expected upstream auth passthrough, got %q", gotAuth)
	}
	if !strings.Contains(gotBody, `filename="sample.png"`) || !strings.Contains(gotBody, "variation-png-data") {
		t.Fatalf("expected variation file passthrough, got %s", gotBody)
	}
	if !strings.Contains(rec.Body.String(), `variation unsupported upstream`) {
		t.Fatalf("expected upstream error body passthrough, got %s", rec.Body.String())
	}
}

func TestWithRequestIDSkipsImagesLoggingAndArchive(t *testing.T) {
	logDir := initMiddlewareTestLogger(t)
	archiveDir := t.TempDir()
	store := config.NewStaticRuntimeStore(config.Config{LogEnable: true, DebugArchiveRootDir: archiveDir})
	h := withRequestID(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewBufferString(`{"model":"gpt-image-1"}`))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	requestID := rec.Header().Get("X-Request-Id")
	if requestID == "" {
		t.Fatal("expected request id header")
	}
	if files := middlewareLogFiles(t, logDir); len(files) != 0 {
		t.Fatalf("expected no log files for image requests, got %v", files)
	}
	if _, err := os.Stat(filepath.Join(archiveDir, requestID)); !os.IsNotExist(err) {
		t.Fatalf("expected no archive directory for image requests, got err=%v", err)
	}
	text, err := readAllMiddlewareLogs(logDir)
	if err != nil {
		t.Fatalf("read middleware logs: %v", err)
	}
	if strings.Contains(text, `/v1/images/generations`) {
		t.Fatalf("expected image path to stay out of logs, got %s", text)
	}
	if strings.Contains(text, requestID) {
		t.Fatalf("expected image request id to stay out of logs, got %s", text)
	}
}

func TestEmbeddingsPassesThroughJSONAndMappedModel(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"text-embedding-3-large","usage":{"prompt_tokens":3,"total_tokens":3}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:              "openai",
			Enabled:         true,
			UpstreamBaseURL: upstream.URL,
			UpstreamAPIKey:  "provider-upstream-key",
			ModelMap: []config.ModelMapEntry{
				config.NewModelMapEntry("text-embedding-3-small", "text-embedding-3-large"),
			},
			ManualModels: []string{"text-embedding-3-small"},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"text-embedding-3-small","input":"hello","encoding_format":"float"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer root-secret")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/embeddings" {
		t.Fatalf("expected upstream path /embeddings, got %q", gotPath)
	}
	if gotAuth != "Bearer provider-upstream-key" {
		t.Fatalf("expected provider upstream auth, got %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"model":"text-embedding-3-large"`) {
		t.Fatalf("expected mapped model in upstream body, got %s", gotBody)
	}
	if !strings.Contains(gotBody, `"input":"hello"`) || !strings.Contains(gotBody, `"encoding_format":"float"`) {
		t.Fatalf("expected embeddings payload passthrough, got %s", gotBody)
	}
	if !strings.Contains(rec.Body.String(), `"object":"list"`) || !strings.Contains(rec.Body.String(), `"embedding":[0.1,0.2]`) {
		t.Fatalf("expected upstream response passthrough, got %s", rec.Body.String())
	}
}

func TestWithRequestIDSkipsEmbeddingsLoggingAndArchive(t *testing.T) {
	logDir := initMiddlewareTestLogger(t)
	archiveDir := t.TempDir()
	store := config.NewStaticRuntimeStore(config.Config{LogEnable: true, DebugArchiveRootDir: archiveDir})
	h := withRequestID(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewBufferString(`{"model":"text-embedding-3-small"}`))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	requestID := rec.Header().Get("X-Request-Id")
	if requestID == "" {
		t.Fatal("expected request id header")
	}
	if files := middlewareLogFiles(t, logDir); len(files) != 0 {
		t.Fatalf("expected no log files for embeddings requests, got %v", files)
	}
	if _, err := os.Stat(filepath.Join(archiveDir, requestID)); !os.IsNotExist(err) {
		t.Fatalf("expected no archive directory for embeddings requests, got err=%v", err)
	}
}

func TestRerankPassesThroughJSONAndMappedModel(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"index":0,"relevance_score":0.98}],"model":"rerank-2"}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:              "openai",
			Enabled:         true,
			UpstreamBaseURL: upstream.URL,
			UpstreamAPIKey:  "provider-upstream-key",
			ModelMap: []config.ModelMapEntry{
				config.NewModelMapEntry("rerank-1", "rerank-2"),
			},
			ManualModels: []string{"rerank-1"},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/rerank", strings.NewReader(`{"model":"rerank-1","query":"hello","documents":["a","b"]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer root-secret")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/rerank" {
		t.Fatalf("expected upstream path /rerank, got %q", gotPath)
	}
	if gotAuth != "Bearer provider-upstream-key" {
		t.Fatalf("expected provider upstream auth, got %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"model":"rerank-2"`) {
		t.Fatalf("expected mapped model in upstream body, got %s", gotBody)
	}
	if !strings.Contains(gotBody, `"query":"hello"`) || !strings.Contains(gotBody, `"documents":["a","b"]`) {
		t.Fatalf("expected rerank payload passthrough, got %s", gotBody)
	}
	if !strings.Contains(rec.Body.String(), `"relevance_score":0.98`) {
		t.Fatalf("expected upstream response passthrough, got %s", rec.Body.String())
	}
}

func TestWithRequestIDSkipsRerankLoggingAndArchive(t *testing.T) {
	logDir := initMiddlewareTestLogger(t)
	archiveDir := t.TempDir()
	store := config.NewStaticRuntimeStore(config.Config{LogEnable: true, DebugArchiveRootDir: archiveDir})
	h := withRequestID(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/rerank", bytes.NewBufferString(`{"model":"rerank-1"}`))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	requestID := rec.Header().Get("X-Request-Id")
	if requestID == "" {
		t.Fatal("expected request id header")
	}
	if files := middlewareLogFiles(t, logDir); len(files) != 0 {
		t.Fatalf("expected no log files for rerank requests, got %v", files)
	}
	if _, err := os.Stat(filepath.Join(archiveDir, requestID)); !os.IsNotExist(err) {
		t.Fatalf("expected no archive directory for rerank requests, got err=%v", err)
	}
}

func readAllMiddlewareLogs(logDir string) (string, error) {
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return "", err
	}
	var builder strings.Builder
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".txt") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(logDir, entry.Name()))
		if err != nil {
			return "", err
		}
		builder.Write(data)
	}
	return builder.String(), nil
}
