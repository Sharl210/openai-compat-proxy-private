package httpapi

import (
	"encoding/json"
	"net/http"
	"openai-compat-proxy/internal/errorsx"
	"strings"
)

func handleRequestStatus(store *requestStatusStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path, ok := parseRequestStatusPath(r.URL.Path)
		if !ok {
			errorsx.WriteJSON(w, http.StatusNotFound, "not_found", "request not found")
			return
		}
		status, ok := store.get(path.RequestID)
		if !ok {
			errorsx.WriteJSON(w, http.StatusNotFound, "not_found", "request not found")
			return
		}
		if status.ProviderID != path.ProviderID {
			errorsx.WriteJSON(w, http.StatusNotFound, "not_found", "request not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status)
	}
}

type requestStatusPath struct {
	ProviderID string
	RequestID  string
}

func parseRequestStatusPath(path string) (requestStatusPath, bool) {
	trimmed := strings.Trim(path, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 4 || parts[1] != "v1" || parts[2] != "requests" || parts[3] == "" {
		return requestStatusPath{}, false
	}
	return requestStatusPath{ProviderID: parts[0], RequestID: parts[3]}, true
}
