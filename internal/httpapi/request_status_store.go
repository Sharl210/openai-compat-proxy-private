package httpapi

import (
	"sync"
	"time"
)

type requestStatus struct {
	RequestID    string    `json:"request_id"`
	ProviderID   string    `json:"provider"`
	Route        string    `json:"route"`
	Status       string    `json:"status"`
	HealthFlag   string    `json:"health_flag"`
	Stage        string    `json:"stage"`
	StartedAt    time.Time `json:"started_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Completed    bool      `json:"completed"`
	ErrorCode    string    `json:"error_code,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
}

type requestStatusStore struct {
	mu    sync.RWMutex
	items map[string]requestStatus
}

func newRequestStatusStore() *requestStatusStore {
	return &requestStatusStore{items: map[string]requestStatus{}}
}

func (s *requestStatusStore) start(requestID, providerID, route string) requestStatus {
	now := time.Now().UTC()
	status := requestStatus{
		RequestID:  requestID,
		ProviderID: providerID,
		Route:      route,
		Status:     "in_progress",
		HealthFlag: "health",
		Stage:      "opening",
		StartedAt:  now,
		UpdatedAt:  now,
	}
	s.mu.Lock()
	s.items[requestID] = status
	s.mu.Unlock()
	return status
}

func (s *requestStatusStore) markStreaming(requestID string) {
	s.update(requestID, func(status requestStatus) requestStatus {
		status.Stage = "streaming"
		status.UpdatedAt = time.Now().UTC()
		return status
	})
}

func (s *requestStatusStore) markCompleted(requestID string) {
	s.update(requestID, func(status requestStatus) requestStatus {
		status.Status = "completed"
		status.Completed = true
		status.Stage = "completed"
		status.HealthFlag = "health"
		status.ErrorCode = ""
		status.ErrorMessage = ""
		status.UpdatedAt = time.Now().UTC()
		return status
	})
}

func (s *requestStatusStore) markFailed(requestID, healthFlag, errorCode, errorMessage string) {
	s.update(requestID, func(status requestStatus) requestStatus {
		status.Status = "failed"
		status.Completed = false
		status.Stage = "failed"
		status.HealthFlag = healthFlag
		status.ErrorCode = errorCode
		status.ErrorMessage = errorMessage
		status.UpdatedAt = time.Now().UTC()
		return status
	})
}

func (s *requestStatusStore) get(requestID string) (requestStatus, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	status, ok := s.items[requestID]
	return status, ok
}

func (s *requestStatusStore) update(requestID string, mutate func(requestStatus) requestStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status, ok := s.items[requestID]
	if !ok {
		return
	}
	s.items[requestID] = mutate(status)
}
