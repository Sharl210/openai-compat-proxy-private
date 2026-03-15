package httpapi

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

var requestCounter uint64

const normalizationVersion = "v1"

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := fmt.Sprintf("req-%d-%d", time.Now().UnixNano(), atomic.AddUint64(&requestCounter, 1))
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r)
	})
}

func setNormalizationVersionHeader(w http.ResponseWriter) {
	w.Header().Set("X-Proxy-Normalization-Version", normalizationVersion)
}
