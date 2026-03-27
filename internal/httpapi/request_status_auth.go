package httpapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"sync"
)

type requestStatusAuthGrant struct {
	ProviderID string
	RequestID  string
}

type requestStatusAuthStore struct {
	mu     sync.Mutex
	secret []byte
	grants map[string]requestStatusAuthGrant
}

func newRequestStatusAuthStore() *requestStatusAuthStore {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		for i := range secret {
			secret[i] = byte(i + 1)
		}
	}
	return &requestStatusAuthStore{
		secret: secret,
		grants: map[string]requestStatusAuthGrant{},
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
	s.grants[token] = requestStatusAuthGrant{ProviderID: providerID, RequestID: requestID}
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
	return true
}

func (s *requestStatusAuthStore) sign(payload string) []byte {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(payload))
	return mac.Sum(nil)
}
