package httpapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"sync"
	"time"
)

const (
	defaultRequestStatusTokenTTL            = 15 * time.Minute
	defaultRequestStatusAuthGCSweepInterval = 256
)

type requestStatusAuthGrant struct {
	ProviderID string
	RequestID  string
	issuedAt   time.Time
}

type requestStatusAuthStore struct {
	mu sync.Mutex

	secret []byte
	grants map[string]requestStatusAuthGrant

	ttl             time.Duration
	now             func() time.Time
	gcSweepInterval uint32
	opsSinceGC      uint32
}

func newRequestStatusAuthStore() *requestStatusAuthStore {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		for i := range secret {
			secret[i] = byte(i + 1)
		}
	}
	return &requestStatusAuthStore{
		secret:          secret,
		grants:          map[string]requestStatusAuthGrant{},
		ttl:             defaultRequestStatusTokenTTL,
		now:             time.Now,
		gcSweepInterval: defaultRequestStatusAuthGCSweepInterval,
	}
}

func (s *requestStatusAuthStore) issueToken(providerID, requestID string) string {
	if s == nil || providerID == "" || requestID == "" {
		return ""
	}
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		for i := range nonceBytes {
			nonceBytes[i] = byte(len(providerID) + len(requestID) + i + 1)
		}
	}
	payload := providerID + ":" + requestID + ":" + base64.RawURLEncoding.EncodeToString(nonceBytes)
	token := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(s.sign(payload))
	s.mu.Lock()
	s.grants[token] = requestStatusAuthGrant{ProviderID: providerID, RequestID: requestID, issuedAt: s.nowUnsafe()}
	s.scheduleGCLocked()
	s.mu.Unlock()
	return token
}

func (s *requestStatusAuthStore) consumeToken(token, providerID, requestID string) bool {
	if s == nil || token == "" || providerID == "" || requestID == "" {
		return false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return false
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	payload := string(payloadBytes)
	if !hmac.Equal(sigBytes, s.sign(payload)) {
		return false
	}
	fields := strings.Split(payload, ":")
	if len(fields) != 3 || fields[0] != providerID || fields[1] != requestID {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	grant, ok := s.grants[token]
	if !ok || grant.ProviderID != providerID || grant.RequestID != requestID {
		return false
	}
	delete(s.grants, token)
	s.scheduleGCLocked()
	return true
}

func (s *requestStatusAuthStore) sign(payload string) []byte {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func (s *requestStatusAuthStore) scheduleGCLocked() {
	if s == nil {
		return
	}
	if s.gcSweepInterval == 0 {
		s.gcSweepInterval = 1
	}
	s.opsSinceGC++
	if s.opsSinceGC < s.gcSweepInterval {
		return
	}
	s.gcExpiredGrantsLocked()
	s.opsSinceGC = 0
}

func (s *requestStatusAuthStore) gcExpiredGrantsLocked() {
	if s == nil {
		return
	}
	now := s.nowUnsafe()
	for token, grant := range s.grants {
		age := now.Sub(grant.issuedAt)
		if s.ttl <= 0 || age > s.ttl {
			delete(s.grants, token)
		}
	}
}

func (s *requestStatusAuthStore) forceGC() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcExpiredGrantsLocked()
	s.opsSinceGC = 0
}

func (s *requestStatusAuthStore) nowUnsafe() time.Time {
	if s == nil || s.now == nil {
		return time.Now()
	}
	return s.now()
}
