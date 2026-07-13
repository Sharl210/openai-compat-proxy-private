package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProxyModelLiteralPrecedence_realtimeExactBeatsStaticDerivedBase(t *testing.T) {
	literalModel := "realtime-base-high-pro-ultra-noprompt"
	tests := []struct {
		name         string
		path         string
		alphaModels  []string
		betaModels   []string
		wantProvider string
	}{
		{
			name:         "bare cross provider",
			path:         "/v1/responses",
			alphaModels:  []string{"realtime-base"},
			betaModels:   []string{literalModel},
			wantProvider: "beta",
		},
		{
			name:         "bare same provider",
			path:         "/v1/responses",
			alphaModels:  []string{"realtime-base", literalModel},
			betaModels:   []string{"other-model"},
			wantProvider: "alpha",
		},
		{
			name:         "explicit provider",
			path:         "/alpha/v1/responses",
			alphaModels:  []string{"realtime-base", literalModel},
			betaModels:   []string{"other-model"},
			wantProvider: "alpha",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			alpha := newOverlaySuffixUpstream(t, tt.alphaModels)
			defer alpha.Close()
			beta := newOverlaySuffixUpstream(t, tt.betaModels)
			defer beta.Close()
			cfg := defaultOverlaySuffixConfig(alpha.URL, beta.URL)
			cfg.Providers[0].ManualModels = append(cfg.Providers[0].ManualModels, "realtime-base")
			server := NewServer(cfg)
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(`{"model":"`+literalModel+`","input":"hello"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			// When
			server.ServeHTTP(rec, req)

			// Then
			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("X-Provider-Name"); got != tt.wantProvider {
				t.Fatalf("expected provider %q, got %q", tt.wantProvider, got)
			}
			upstream := alpha
			if tt.wantProvider == "beta" {
				upstream = beta
			}
			captured := <-upstream.requests
			if got := captured.body["model"]; got != literalModel {
				t.Fatalf("expected exact literal upstream model unchanged, got %#v", got)
			}
		})
	}
}
