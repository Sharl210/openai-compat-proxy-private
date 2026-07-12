package perfbench

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/httpapi"
	"openai-compat-proxy/internal/logging"
)

type semanticImageFact struct {
	SHA256 string
	Bytes  int64
}

type semanticHTTPResult struct {
	Status      int
	Header      http.Header
	Body        []byte
	ContentType string
	RequestID   string
}

type semanticScenarioRuntime struct {
	item       scenario
	downstream *httptest.Server
	fake       *semanticFakeUpstream
}

func collectSemanticMatrix() ([]semanticRecord, error) {
	imageFacts := make(map[int64]semanticImageFact, 3)
	for _, size := range []int64{1 << 20, 8 << 20, 32 << 20} {
		fixture := generatedImageFixture(size)
		imageFacts[size] = semanticImageFact{SHA256: sha256Hex(fixture), Bytes: int64(len(fixture))}
	}

	catalog := scenarioCatalog()
	records := make([]semanticRecord, 0, len(catalog))
	for _, item := range catalog {
		record, err := collectSemanticScenario(item, imageFacts[item.ImageBytes])
		if err != nil {
			return nil, fmt.Errorf("scenario %s: %w", item.ID, err)
		}
		records = append(records, record)
	}
	return records, nil
}

func collectSemanticScenario(item scenario, imageFact semanticImageFact) (record semanticRecord, err error) {
	tempRoot, err := os.MkdirTemp("", "perfbench-semantic-")
	if err != nil {
		return semanticRecord{}, fmt.Errorf("create semantic temp root: %w", err)
	}
	defer func() {
		err = errors.Join(err, os.RemoveAll(tempRoot))
	}()

	fake := newSemanticFakeUpstream(item)
	defer fake.close()
	cfg, err := semanticScenarioConfig(item, fake.url(), tempRoot)
	if err != nil {
		return semanticRecord{}, err
	}
	closeLogger, err := logging.Init(cfg, io.Discard)
	if err != nil {
		return semanticRecord{}, fmt.Errorf("initialize semantic logger: %w", err)
	}
	defer func() {
		_, disableErr := logging.Init(config.Config{}, io.Discard)
		err = errors.Join(err, closeLogger(), disableErr)
	}()

	runtime := semanticScenarioRuntime{
		item: item, fake: fake, downstream: httptest.NewServer(httpapi.NewServer(cfg)),
	}
	defer runtime.downstream.Close()
	body, err := semanticScenarioRequestBody(item)
	if err != nil {
		return semanticRecord{}, err
	}
	response, err := runtime.do(body)
	if err != nil {
		return semanticRecord{}, err
	}
	parsed, err := parseSemanticDownstream(item.Downstream, response.ContentType, response.Body)
	if err != nil {
		return semanticRecord{}, err
	}

	mainCaptures := fake.capturedRequests()
	if err := validateSemanticCaptureSet(item, mainCaptures); err != nil {
		return semanticRecord{}, err
	}
	observed := mainCaptures[0]
	successful := mainCaptures[len(mainCaptures)-1]
	decodedImage, err := decodedSemanticImageFact(observed.Body, imageFact)
	if err != nil {
		return semanticRecord{}, err
	}
	record = semanticRecord{
		ScenarioID:            item.ID,
		Downstream:            item.Downstream,
		Upstream:              item.Upstream,
		Delivery:              item.Delivery,
		ImageBytes:            item.ImageBytes,
		Profile:               item.Profile,
		FinalEndpoint:         observed.Path,
		Method:                observed.Method,
		ObservedBodySHA256:    sha256Hex(observed.Body),
		ObservedBodyBytes:     int64(len(observed.Body)),
		ContentLength:         observed.ContentLength,
		DecodedImageSHA256:    decodedImage.SHA256,
		DecodedImageBytes:     decodedImage.Bytes,
		PromptCacheKey:        semanticPromptCacheKey(observed.Body),
		DownstreamStatus:      response.Status,
		DownstreamContentType: response.ContentType,
		UpstreamContentType:   successful.ResponseContentType,
		UpstreamResponseMode:  successful.ResponseMode,
		ProxyHeaders:          stableSemanticProxyHeaders(response.Header),
		NormalizedOutput:      parsed.NormalizedOutput,
		Reasoning:             parsed.Reasoning,
		Tools:                 parsed.Tools,
		Usage:                 parsed.Usage,
		FinishReason:          parsed.FinishReason,
		TerminalStatus:        parsed.TerminalStatus,
	}
	if item.Profile == profileRetryOnce {
		record.RetryAttempts = semanticAttemptDigests(mainCaptures)
	}
	if item.Profile == profileHistoryRestore {
		history, historyErr := runtime.collectHistorySecondRequest(parsed.ResponseID, len(mainCaptures), imageFact)
		err = historyErr
		if err != nil {
			return semanticRecord{}, err
		}
		record.HistorySecondRequest = &history
	}
	if item.Profile == profileLog || item.Profile == profileLogArchive {
		record.LogEvents, err = collectSemanticLogEvents(cfg.LogFilePath, response.RequestID)
		if err != nil {
			return semanticRecord{}, err
		}
	}
	if item.Profile == profileLogArchive {
		record.Archives, err = collectSemanticArchiveEvidence(cfg.DebugArchiveRootDir, response.RequestID)
		if err != nil {
			return semanticRecord{}, err
		}
	}
	if err := validateCollectedSemanticRecord(record, imageFact); err != nil {
		return semanticRecord{}, err
	}
	return record, nil
}
