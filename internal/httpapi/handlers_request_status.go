package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/errorsx"
)

func handleRequestStatus(store *requestStatusStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path, ok := parseRequestStatusPath(r.URL.Path, config.Config{})
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

func parseRequestStatusPath(path string, cfg config.Config) (requestStatusPath, bool) {
	trimmed := strings.Trim(path, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 4 || parts[1] != "v1" || parts[2] != "requests" || parts[3] == "" {
		return requestStatusPath{}, false
	}
	providerID := parts[0]
	if cfg.ProvidersDir != "" || len(cfg.Providers) > 0 {
		provider, err := cfg.ProviderByID(providerID)
		if err != nil || !provider.Enabled {
			return requestStatusPath{}, false
		}
	}
	return requestStatusPath{ProviderID: providerID, RequestID: parts[3]}, true
}
