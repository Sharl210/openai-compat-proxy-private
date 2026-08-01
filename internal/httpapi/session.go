package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"openai-compat-proxy/internal/upstream"
)

const headerProxySessionID = "X-Proxy-Session-Id"

const maxProxySessionIDLength = 256

type proxySessionIDContextKey struct{}

var proxySessionCounter uint64

func ensureProxySessionID(r *http.Request, w http.ResponseWriter) (*http.Request, string) {
	if r == nil {
		return r, ""
	}
	if sessionID := proxySessionIDFromRequest(r); sessionID != "" {
		return withProxySessionID(r, w, sessionID)
	}
	return withProxySessionID(r, w, newProxySessionID())
}

func withProxySessionID(r *http.Request, w http.ResponseWriter, sessionID string) (*http.Request, string) {
	if r == nil {
		return r, ""
	}
	sessionID = normalizeProxySessionID(sessionID)
	if sessionID == "" {
		sessionID = newProxySessionID()
	}
	ctx := context.WithValue(r.Context(), proxySessionIDContextKey{}, sessionID)
	ctx = upstream.WithSessionID(ctx, sessionID)
	if carrier := requestLineageCarrierFromContext(ctx); carrier != nil {
		carrier.setSessionID(sessionID)
	}
	updated := r.WithContext(ctx)
	setProxySessionIDHeader(w, sessionID)
	return updated, sessionID
}

func proxySessionIDFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if sessionID, ok := r.Context().Value(proxySessionIDContextKey{}).(string); ok {
		if normalized := normalizeProxySessionID(sessionID); normalized != "" {
			return normalized
		}
	}
	return normalizeProxySessionID(r.Header.Get(headerProxySessionID))
}

func explicitProxySessionIDFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	return normalizeProxySessionID(r.Header.Get(headerProxySessionID))
}

func setProxySessionIDHeader(w http.ResponseWriter, sessionID string) {
	if w == nil {
		return
	}
	if normalized := normalizeProxySessionID(sessionID); normalized != "" {
		w.Header().Set(headerProxySessionID, normalized)
	}
}

func normalizeProxySessionID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxProxySessionIDLength || strings.ContainsAny(value, "\r\n") {
		return ""
	}
	return value
}

func newProxySessionID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		raw[6] = (raw[6] & 0x0f) | 0x40
		raw[8] = (raw[8] & 0x3f) | 0x80
		encoded := hex.EncodeToString(raw[:])
		return fmt.Sprintf("session-%s-%s-%s-%s-%s", encoded[:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:])
	}
	return fmt.Sprintf("session-%d-%d", time.Now().UnixNano(), atomic.AddUint64(&proxySessionCounter, 1))
}
