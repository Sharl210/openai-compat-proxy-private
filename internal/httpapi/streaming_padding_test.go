package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWriteSSEPaddingWritesCommentFrame(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := writeSSEPadding(rec, nil); err != nil {
		t.Fatalf("writeSSEPadding error: %v", err)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, ": ") {
		t.Fatalf("expected SSE comment prefix, got %q", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("expected SSE comment frame terminator, got %q", body)
	}
}

func TestWithRequestIDFlushesStreamingPreludeImmediately(t *testing.T) {
	handler := withRequestID(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := startSSE(w)
		if err := writeSSEPadding(w, flusher); err != nil {
			t.Fatalf("writeSSEPadding error: %v", err)
		}
		if err := writeSyntheticResponsesReasoning(w, flusher, syntheticReasoningPlaceholder); err != nil {
			t.Fatalf("writeSyntheticResponsesReasoning error: %v", err)
		}
		time.Sleep(400 * time.Millisecond)
	}))
	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{"ok":true}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if elapsed := time.Since(start); elapsed >= 250*time.Millisecond {
		t.Fatalf("expected response headers before handler sleep finished, got %s", elapsed)
	}

	buf := make([]byte, 8192)
	readDone := make(chan string, 1)
	go func() {
		var out strings.Builder
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				out.Write(buf[:n])
				if strings.Contains(out.String(), "代理层占位") {
					readDone <- out.String()
					return
				}
			}
			if err != nil {
				readDone <- out.String()
				return
			}
		}
	}()

	select {
	case body := <-readDone:
		if !strings.Contains(body, "代理层占位") {
			t.Fatalf("expected early placeholder bytes, got %q", body)
		}
	case <-time.After(200 * time.Millisecond):
		_ = resp.Body.Close()
		t.Fatal("expected streaming prelude bytes before handler finished sleeping")
	}
}
