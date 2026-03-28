package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"openai-compat-proxy/internal/config"
)

func handleHealthz(store *config.RuntimeStore) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if store == nil || store.Active() == nil {
			writeHealthzError(w, http.StatusServiceUnavailable, "runtime config unavailable")
			return
		}
		if err := validateHealthConfig(store.Active().Config); err != nil {
			writeHealthzError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
}

func writeHealthzError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "error",
		"error": map[string]any{
			"message": message,
		},
	})
}

func validateHealthConfig(cfg config.Config) error {
	enabledProviders := 0
	if len(cfg.Providers) == 0 {
		return config.ErrInvalidConfig("at least one provider must be configured")
	}
	if strings.TrimSpace(cfg.DefaultProvider) != "" {
		provider, err := cfg.DefaultProviderConfig()
		if err != nil {
			return config.ErrInvalidConfig("default provider not found")
		}
		if !provider.Enabled {
			return config.ErrInvalidConfig("default provider must be enabled")
		}
		if strings.TrimSpace(provider.UpstreamBaseURL) == "" {
			return config.ErrInvalidConfig("default provider must define UPSTREAM_BASE_URL")
		}
	}
	for _, provider := range cfg.Providers {
		if !provider.Enabled {
			continue
		}
		enabledProviders++
		if strings.TrimSpace(provider.UpstreamBaseURL) == "" {
			return config.ErrInvalidConfig("enabled provider must define UPSTREAM_BASE_URL")
		}
	}
	if enabledProviders == 0 {
		return config.ErrInvalidConfig("at least one enabled provider is required")
	}
	return nil
}
