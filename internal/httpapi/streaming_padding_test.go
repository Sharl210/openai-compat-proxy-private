package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
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
