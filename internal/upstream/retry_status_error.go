package upstream

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type HTTPRetryEvidence struct {
	AttemptCount          int
	AllAttemptsMatchFinal bool
}

type HTTPStatusError struct {
	StatusCode       int
	ContentType      string
	BodyBytes        []byte
	Body             string
	RetriesPerformed int
	RetryDelay       time.Duration
	RetryEvidence    HTTPRetryEvidence
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("upstream status %d: %s", e.StatusCode, e.Body)
}

type retryStatusErrorEvidence struct {
	attemptCount int
	firstStatus  int
	firstDigest  [sha256.Size]byte
	hasFirst     bool
	allMatch     bool
}

func newRetryStatusErrorEvidence() *retryStatusErrorEvidence {
	return &retryStatusErrorEvidence{allMatch: true}
}

func (e *retryStatusErrorEvidence) observe(err error) {
	if e == nil {
		return
	}
	e.attemptCount++
	var httpErr *HTTPStatusError
	if !errors.As(err, &httpErr) {
		e.allMatch = false
		return
	}
	digest := sha256.Sum256(httpErr.BodyBytes)
	if !e.hasFirst {
		e.firstStatus = httpErr.StatusCode
		e.firstDigest = digest
		e.hasFirst = true
		return
	}
	if httpErr.StatusCode != e.firstStatus || digest != e.firstDigest {
		e.allMatch = false
	}
}

func (e *retryStatusErrorEvidence) attach(err error) error {
	if e == nil {
		return err
	}
	var httpErr *HTTPStatusError
	if !errors.As(err, &httpErr) {
		return err
	}
	cloned := *httpErr
	cloned.RetryEvidence = HTTPRetryEvidence{
		AttemptCount:          e.attemptCount,
		AllAttemptsMatchFinal: e.hasFirst && e.allMatch,
	}
	return &cloned
}

func readHTTPStatusError(resp *http.Response) *HTTPStatusError {
	bodyBytes, _ := io.ReadAll(resp.Body)
	message := strings.TrimSpace(string(bodyBytes))
	if message == "" {
		message = http.StatusText(resp.StatusCode)
		bodyBytes = []byte(message)
	}
	return &HTTPStatusError{
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		BodyBytes:   bodyBytes,
		Body:        message,
	}
}

func shouldRetryRequestFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == http.StatusTooManyRequests || httpErr.StatusCode >= 500
	}
	return true
}

func annotateRetryExhaustion(err error, retryCount int, retryDelay time.Duration) error {
	if err == nil || retryCount <= 0 || !shouldRetryRequestFailure(err) {
		return err
	}
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) {
		cloned := *httpErr
		cloned.RetriesPerformed = retryCount
		cloned.RetryDelay = retryDelay
		return &cloned
	}
	return fmt.Errorf("%s%w", buildRetryNotice(retryCount, retryDelay), err)
}
