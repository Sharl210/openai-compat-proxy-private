package perfbench

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"openai-compat-proxy/internal/config"
)

func semanticScenarioConfig(item scenario, upstreamURL, tempRoot string) (config.Config, error) {
	providersDir := filepath.Join(tempRoot, "providers")
	if err := os.MkdirAll(providersDir, 0o700); err != nil {
		return config.Config{}, fmt.Errorf("create semantic providers dir: %w", err)
	}
	cfg := config.Default()
	cfg.CacheInfoTimezone = "UTC"
	cfg.ProxyAPIKey = "perf-proxy-secret"
	cfg.ProvidersDir = providersDir
	cfg.DefaultProvider = "perf"
	cfg.EnableLegacyV1Routes = true
	cfg.DownstreamNonStreamStrategy = config.DownstreamNonStreamStrategyProxyBuffer
	if item.Delivery == deliveryUpstreamNonStream {
		cfg.DownstreamNonStreamStrategy = config.DownstreamNonStreamStrategyUpstreamNonStream
	}
	cfg.LogEnable = item.Profile == profileLog || item.Profile == profileLogArchive
	cfg.LogFilePath = filepath.Join(tempRoot, "logs")
	cfg.LogMaxRequests = 10
	cfg.LogMaxBodySizeMB = 64
	cfg.DebugArchiveRootDir = ""
	if item.Profile == profileLogArchive {
		cfg.DebugArchiveRootDir = filepath.Join(tempRoot, "archives")
	}
	cfg.DebugArchiveMaxRequests = 10
	retryCount := 0
	if item.Profile == profileRetryOnce {
		retryCount = 1
	}
	cfg.Providers = []config.ProviderConfig{{
		ID:                                    "perf",
		Enabled:                               true,
		UpstreamBaseURL:                       upstreamURL,
		UpstreamAPIKey:                        "perf-upstream-secret",
		UpstreamEndpointType:                  string(item.Upstream),
		SupportsChat:                          true,
		SupportsResponses:                     true,
		SupportsModels:                        true,
		SupportsAnthropicMessages:             true,
		ManualModels:                          []string{"perf-model"},
		AnthropicVersion:                      "2023-06-01",
		UpstreamRetryCount:                    retryCount,
		UpstreamRetryCountSet:                 true,
		UpstreamRetryDelay:                    time.Millisecond,
		UpstreamRetryDelaySet:                 true,
		ModelLimitContextTokens:               1_000_000_000,
		ModelLimitContextTokensSet:            true,
		MapReasoningSuffixToAnthropicThinking: true,
	}}
	return cfg, nil
}

func semanticScenarioRequestBody(item scenario) ([]byte, error) {
	body, err := buildScenarioRequest(item)
	if err != nil {
		return nil, err
	}
	if item.Downstream != downstreamResponses || item.Upstream != upstreamResponses || item.Profile != profilePlain {
		return body, nil
	}
	if len(body) == 0 || body[len(body)-1] != '}' {
		return nil, fmt.Errorf("semantic request is not a JSON object")
	}
	prefix := append([]byte(nil), body[:len(body)-1]...)
	return append(prefix, []byte(`,"prompt_cache_key":"perf-explicit-cache-key"}`)...), nil
}

func (r semanticScenarioRuntime) do(body []byte) (semanticHTTPResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.downstream.URL+semanticDownstreamPath(r.item.Downstream), bytes.NewReader(body))
	if err != nil {
		return semanticHTTPResult{}, fmt.Errorf("build downstream request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer perf-proxy-secret")
	request.Header.Set("Content-Type", "application/json")
	if r.item.Downstream == downstreamMessages {
		request.Header.Set("Anthropic-Version", "2023-06-01")
	}
	response, err := r.downstream.Client().Do(request)
	if err != nil {
		return semanticHTTPResult{}, fmt.Errorf("execute downstream request: %w", err)
	}
	responseBody, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return semanticHTTPResult{}, fmt.Errorf("read downstream response: %w", err)
	}
	return semanticHTTPResult{
		Status: response.StatusCode, Header: response.Header.Clone(), Body: responseBody,
		ContentType: response.Header.Get("Content-Type"), RequestID: response.Header.Get("X-Request-Id"),
	}, nil
}

func semanticDownstreamPath(protocol downstreamProtocol) string {
	switch protocol {
	case downstreamResponses:
		return "/v1/responses"
	case downstreamChat:
		return "/v1/chat/completions"
	case downstreamMessages:
		return "/v1/messages"
	default:
		return ""
	}
}

func (r semanticScenarioRuntime) collectHistorySecondRequest(previousID string, captureCount int, imageFact semanticImageFact) (semanticHistoryRequestEvidence, error) {
	stream := r.item.Delivery == deliveryStream
	body := []byte(fmt.Sprintf(`{"model":"perf-model","previous_response_id":%s,"input":[{"type":"function_call_output","call_id":"call_fixture","output":"fixture-result"},{"role":"user","content":[{"type":"input_text","text":"continue"}]}],"stream":%t}`,
		strconv.Quote(previousID), stream))
	response, err := r.do(body)
	if err != nil {
		return semanticHistoryRequestEvidence{}, err
	}
	if response.Status != http.StatusOK {
		return semanticHistoryRequestEvidence{}, fmt.Errorf("history second downstream status %d: %s", response.Status, response.Body)
	}
	captures := r.fake.capturedRequests()
	if len(captures) != captureCount+1 {
		return semanticHistoryRequestEvidence{}, fmt.Errorf("history upstream captures = %d, want %d", len(captures), captureCount+1)
	}
	second := captures[len(captures)-1]
	for _, required := range [][]byte{[]byte("lookup"), []byte("fixture-result"), []byte("continue")} {
		if !bytes.Contains(second.Body, required) {
			return semanticHistoryRequestEvidence{}, fmt.Errorf("history second upstream body missing %q", required)
		}
	}
	decodedImage, err := decodedSemanticImageFact(second.Body, imageFact)
	if err != nil {
		return semanticHistoryRequestEvidence{}, fmt.Errorf("decode restored history image: %w", err)
	}
	contentMode := "json"
	if bytes.Contains(second.Body, []byte(`"stream":true`)) {
		contentMode = "sse"
	}
	return semanticHistoryRequestEvidence{
		Method:                      second.Method,
		Endpoint:                    second.Path,
		BodySHA256:                  sha256Hex(second.Body),
		BodyBytes:                   int64(len(second.Body)),
		ContentLength:               second.ContentLength,
		RequestContentType:          second.Header.Get("Content-Type"),
		DecodedImageSHA256:          decodedImage.SHA256,
		DecodedImageBytes:           decodedImage.Bytes,
		PromptCacheKey:              semanticPromptCacheKey(second.Body),
		ContentMode:                 contentMode,
		UpstreamResponseContentType: second.ResponseContentType,
		UpstreamResponseMode:        second.ResponseMode,
		RestoredToolName:            "lookup",
		RestoredToolResult:          "fixture-result",
		RestoredUserText:            "continue",
	}, nil
}
