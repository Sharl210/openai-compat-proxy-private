package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"openai-compat-proxy/internal/cacheinfo"
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

func TestImagesBareAliasMatchesCanonicalBehavior(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1713833628,"data":[{"b64_json":"ZmFrZQ=="}]}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "image",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:              "image",
			Enabled:         true,
			UpstreamBaseURL: upstream.URL,
			UpstreamAPIKey:  "provider-upstream-key",
			ManualModels:    []string{"gpt-image-2"},
		}},
	})

	for _, path := range []string{"/v1/images/generations", "/images/generations", "/image/v1/images/generations", "/image/images/generations"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"gpt-image-2","prompt":"otter"}`))
			req.Header.Set("Authorization", "Bearer root-secret")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			if gotPath != "/images/generations" {
				t.Fatalf("expected upstream path /images/generations, got %q", gotPath)
			}
		})
	}
}

func TestEmbeddingsBareAliasMatchesCanonicalBehavior(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"model":"text-embedding-3-small"}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "image",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:              "image",
			Enabled:         true,
			UpstreamBaseURL: upstream.URL,
			UpstreamAPIKey:  "provider-upstream-key",
			ManualModels:    []string{"text-embedding-3-small"},
		}},
	})

	for _, path := range []string{"/v1/embeddings", "/embeddings", "/image/v1/embeddings", "/image/embeddings"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"text-embedding-3-small","input":"hello"}`))
			req.Header.Set("Authorization", "Bearer root-secret")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			if gotPath != "/embeddings" {
				t.Fatalf("expected upstream path /embeddings, got %q", gotPath)
			}
		})
	}
}

func TestRerankBareAliasMatchesCanonicalBehavior(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"index":0,"relevance_score":0.98}],"model":"rerank-1"}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "image",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:              "image",
			Enabled:         true,
			UpstreamBaseURL: upstream.URL,
			UpstreamAPIKey:  "provider-upstream-key",
			ManualModels:    []string{"rerank-1"},
		}},
	})

	for _, path := range []string{"/v1/rerank", "/rerank", "/image/v1/rerank", "/image/rerank"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"rerank-1","query":"hello","documents":["a"]}`))
			req.Header.Set("Authorization", "Bearer root-secret")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			if gotPath != "/rerank" {
				t.Fatalf("expected upstream path /rerank, got %q", gotPath)
			}
		})
	}
}

func TestNewEndpointsDoNotRecordCacheInfoUsage(t *testing.T) {
	providersDir := t.TempDir()
	manager := cacheinfo.NewManager(providersDir, time.UTC, []string{"image"}, nil)
	for _, route := range []struct {
		path string
		body string
	}{
		{path: "/v1/images/generations", body: `{"model":"gpt-image-2","response_format":"b64_json"}`},
		{path: "/v1/embeddings", body: `{"model":"text-embedding-3-small","input":"hello"}`},
		{path: "/v1/rerank", body: `{"model":"rerank-1","query":"hello","documents":["a"]}`},
	} {
		t.Run(route.path, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch route.path {
				case "/v1/images/generations":
					_, _ = w.Write([]byte(`{"created":1713833628,"data":[{"b64_json":"ZmFrZQ=="}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
				case "/v1/embeddings":
					_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"model":"text-embedding-3-small","usage":{"prompt_tokens":3,"total_tokens":3}}`))
				default:
					_, _ = w.Write([]byte(`{"results":[{"index":0,"relevance_score":0.98}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
				}
			}))
			defer upstream.Close()

			server := NewServerWithStore(config.NewStaticRuntimeStore(config.Config{
				ProxyAPIKey:          "root-secret",
				DefaultProvider:      "image",
				EnableLegacyV1Routes: true,
				Providers: []config.ProviderConfig{{
					ID:              "image",
					Enabled:         true,
					UpstreamBaseURL: upstream.URL,
					UpstreamAPIKey:  "provider-upstream-key",
					ManualModels:    []string{"gpt-image-2", "text-embedding-3-small", "rerank-1"},
				}},
			}), manager, nil)

			req := httptest.NewRequest(http.MethodPost, route.path, strings.NewReader(route.body)).WithContext(withCacheInfoManager(context.Background(), manager))
			req.Header.Set("Authorization", "Bearer root-secret")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			stats, err := cacheinfo.LoadProviderStats(providersDir, "image")
			if err != nil {
				return
			}
			if stats != nil && (stats.Today.RequestCount != 0 || stats.Today.InputTokens != 0 || stats.Today.OutputTokens != 0 || stats.Today.TotalTokens != 0 || stats.Today.CachedTokens != 0 || stats.Today.CacheCreationTokens != 0) {
				t.Fatalf("expected no cacheinfo usage recorded for %s, got %#v", route.path, stats.Today)
			}
		})
	}
}

func TestImagesEndpointDoesNotMaskUpstreamModelErrorWhenModelNotInVisibleList(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream says model unsupported"}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "image",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:              "image",
			Enabled:         true,
			UpstreamBaseURL: upstream.URL,
			UpstreamAPIKey:  "provider-upstream-key",
			SupportsModels:  true,
			ManualModels:    []string{"some-other-model"},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"gpt-image-2","prompt":"otter"}`))
	req.Header.Set("Authorization", "Bearer root-secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected upstream 400 passthrough, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "upstream says model unsupported") {
		t.Fatalf("expected upstream raw error body, got %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "requested model is not in models list") {
		t.Fatalf("expected proxy invalid_model to be bypassed for image endpoint, got %s", rec.Body.String())
	}
}

func TestEmbeddingsEndpointDoesNotMaskUpstreamModelErrorWhenModelNotInVisibleList(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"embedding model rejected upstream"}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "image",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:              "image",
			Enabled:         true,
			UpstreamBaseURL: upstream.URL,
			UpstreamAPIKey:  "provider-upstream-key",
			SupportsModels:  true,
			ManualModels:    []string{"some-other-model"},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"text-embedding-3-small","input":"hello"}`))
	req.Header.Set("Authorization", "Bearer root-secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected upstream 400 passthrough, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "embedding model rejected upstream") {
		t.Fatalf("expected upstream raw error body, got %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "requested model is not in models list") {
		t.Fatalf("expected proxy invalid_model to be bypassed for embeddings endpoint, got %s", rec.Body.String())
	}
}

func TestRerankEndpointDoesNotMaskUpstreamModelErrorWhenModelNotInVisibleList(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"rerank model rejected upstream"}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "image",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:              "image",
			Enabled:         true,
			UpstreamBaseURL: upstream.URL,
			UpstreamAPIKey:  "provider-upstream-key",
			SupportsModels:  true,
			ManualModels:    []string{"some-other-model"},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/rerank", strings.NewReader(`{"model":"rerank-1","query":"hello","documents":["a"]}`))
	req.Header.Set("Authorization", "Bearer root-secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected upstream 400 passthrough, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "rerank model rejected upstream") {
		t.Fatalf("expected upstream raw error body, got %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "requested model is not in models list") {
		t.Fatalf("expected proxy invalid_model to be bypassed for rerank endpoint, got %s", rec.Body.String())
	}
}

func TestImagesGenerationsConvertsUpstreamURLToB64JSONWhenRequested(t *testing.T) {
	imageArtifactRootDirOverride = t.TempDir()
	t.Cleanup(func() { imageArtifactRootDirOverride = "" })

	imageBytes := []byte("fake-png-binary")
	downloadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(imageBytes)
	}))
	defer downloadServer.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1713833628,"data":[{"url":"` + downloadServer.URL + `/image.png"}]}`))
	}))
	defer upstream.Close()

	proxy := httptest.NewServer(NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "image",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:              "image",
			Enabled:         true,
			UpstreamBaseURL: upstream.URL,
			UpstreamAPIKey:  "provider-upstream-key",
			ManualModels:    []string{"gpt-image-2"},
		}},
	}))
	defer proxy.Close()

	reqBody := `{"model":"gpt-image-2","prompt":"otter","response_format":"b64_json"}`
	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/images/generations", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer root-secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", resp.StatusCode, string(body))
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v body=%s", err, string(body))
	}
	data, _ := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("expected one data item, got %#v", payload)
	}
	item, _ := data[0].(map[string]any)
	if got := item["url"]; got != nil {
		t.Fatalf("expected url removed after conversion, got %#v", got)
	}
	b64, _ := item["b64_json"].(string)
	if b64 == "" {
		t.Fatalf("expected b64_json after conversion, got %#v", item)
	}
	if want := base64.StdEncoding.EncodeToString(imageBytes); b64 != want {
		t.Fatalf("expected b64_json %q, got %q", want, b64)
	}
	entries, err := os.ReadDir(imageArtifactRootDirOverride)
	if err != nil {
		t.Fatalf("read artifact dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no persisted artifact files for url->b64 conversion, got %v", entries)
	}
}

func TestImagesGenerationsDefaultsToB64JSONWhenResponseFormatOmitted(t *testing.T) {
	imageArtifactRootDirOverride = t.TempDir()
	t.Cleanup(func() { imageArtifactRootDirOverride = "" })

	imageBytes := []byte("fake-png-binary")
	downloadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(imageBytes)
	}))
	defer downloadServer.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1713833628,"data":[{"url":"` + downloadServer.URL + `/image.png"}]}`))
	}))
	defer upstream.Close()

	proxy := httptest.NewServer(NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "image",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:              "image",
			Enabled:         true,
			UpstreamBaseURL: upstream.URL,
			UpstreamAPIKey:  "provider-upstream-key",
			ManualModels:    []string{"gpt-image-2"},
		}},
	}))
	defer proxy.Close()

	reqBody := `{"model":"gpt-image-2","prompt":"otter"}`
	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/images/generations", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer root-secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", resp.StatusCode, string(body))
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v body=%s", err, string(body))
	}
	data, _ := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("expected one data item, got %#v", payload)
	}
	item, _ := data[0].(map[string]any)
	if got := item["url"]; got != nil {
		t.Fatalf("expected url removed after default conversion, got %#v", got)
	}
	b64, _ := item["b64_json"].(string)
	if b64 == "" {
		t.Fatalf("expected b64_json after default conversion, got %#v", item)
	}
	if want := base64.StdEncoding.EncodeToString(imageBytes); b64 != want {
		t.Fatalf("expected b64_json %q, got %q", want, b64)
	}
	entries, err := os.ReadDir(imageArtifactRootDirOverride)
	if err != nil {
		t.Fatalf("read artifact dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no persisted artifact files for default url->b64 conversion, got %v", entries)
	}
}

func TestImagesGenerationsConvertsUpstreamB64JSONToPublicURLWhenRequested(t *testing.T) {
	imageArtifactRootDirOverride = t.TempDir()
	t.Cleanup(func() { imageArtifactRootDirOverride = "" })

	imageBytes := []byte("fake-png-binary")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1713833628,"output_format":"png","data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(imageBytes) + `"}]}`))
	}))
	defer upstream.Close()

	proxy := httptest.NewServer(NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "image",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:              "image",
			Enabled:         true,
			UpstreamBaseURL: upstream.URL,
			UpstreamAPIKey:  "provider-upstream-key",
			ManualModels:    []string{"gpt-image-2"},
		}},
	}))
	defer proxy.Close()

	reqBody := `{"model":"gpt-image-2","prompt":"otter","response_format":"url"}`
	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/images/generations", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer root-secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", resp.StatusCode, string(body))
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v body=%s", err, string(body))
	}
	data, _ := payload["data"].([]any)
	item, _ := data[0].(map[string]any)
	if got := item["b64_json"]; got != nil {
		t.Fatalf("expected b64_json removed after conversion, got %#v", got)
	}
	urlText, _ := item["url"].(string)
	if !strings.Contains(urlText, "/_images/") {
		t.Fatalf("expected public artifact url, got %#v", item)
	}
	imageResp, err := http.Get(urlText)
	if err != nil {
		t.Fatalf("get direct url: %v", err)
	}
	defer imageResp.Body.Close()
	imageBody, _ := io.ReadAll(imageResp.Body)
	if imageResp.StatusCode != http.StatusOK {
		t.Fatalf("expected direct url 200, got %d body=%s", imageResp.StatusCode, string(imageBody))
	}
	if !bytes.Equal(imageBody, imageBytes) {
		t.Fatalf("expected direct url body %q, got %q", string(imageBytes), string(imageBody))
	}
	if got := imageResp.Header.Get("Cache-Control"); !strings.Contains(got, "max-age=") {
		t.Fatalf("expected cache-control max-age header, got %q", got)
	}
}

func TestImageArtifactsCleanupExpiredFilesOnServerRestart(t *testing.T) {
	imageArtifactRootDirOverride = t.TempDir()
	t.Cleanup(func() { imageArtifactRootDirOverride = "" })

	expired := filepath.Join(imageArtifactRootDirOverride, "1_expired.png")
	if err := os.WriteFile(expired, []byte("expired"), 0o644); err != nil {
		t.Fatalf("write expired artifact: %v", err)
	}
	server := NewServer(config.Config{})
	_ = server
	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatalf("expected expired artifact removed on server startup, got err=%v", err)
	}
}

func TestImageArtifactsExpireAfterTTL(t *testing.T) {
	imageArtifactRootDirOverride = t.TempDir()
	prevTTL := imageArtifactTTL
	imageArtifactTTL = time.Second
	t.Cleanup(func() {
		imageArtifactRootDirOverride = ""
		imageArtifactTTL = prevTTL
	})

	imageBytes := []byte("short-lived-image")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1713833628,"output_format":"png","data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(imageBytes) + `"}]}`))
	}))
	defer upstream.Close()

	proxy := httptest.NewServer(NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "image",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:              "image",
			Enabled:         true,
			UpstreamBaseURL: upstream.URL,
			UpstreamAPIKey:  "provider-upstream-key",
			ManualModels:    []string{"gpt-image-2"},
		}},
	}))
	defer proxy.Close()

	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/images/generations", strings.NewReader(`{"model":"gpt-image-2","response_format":"url"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer root-secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	data, _ := payload["data"].([]any)
	item, _ := data[0].(map[string]any)
	urlText, _ := item["url"].(string)
	time.Sleep(1500 * time.Millisecond)
	pruneExpiredImageArtifacts(imageArtifactRootDirOverride)
	imageResp, err := http.Get(urlText)
	if err != nil {
		t.Fatalf("get expired direct url: %v", err)
	}
	defer imageResp.Body.Close()
	if imageResp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(imageResp.Body)
		t.Fatalf("expected expired direct url 404, got %d body=%s", imageResp.StatusCode, string(body))
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
