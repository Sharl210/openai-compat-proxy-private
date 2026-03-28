package httpapi

import (
	"net/http"
	"strings"

	"openai-compat-proxy/internal/auth"
	"openai-compat-proxy/internal/cacheinfo"
	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/errorsx"
)

type Server struct {
	store   *config.RuntimeStore
	mux     *http.ServeMux
	handler http.Handler

	CacheInfo *cacheinfo.Manager
}

func NewServer(cfg config.Config) *Server {
	return NewServerWithStore(config.NewStaticRuntimeStore(cfg), nil)
}

func NewServerWithStore(store *config.RuntimeStore, cacheMgr *cacheinfo.Manager) *Server {
	srv := &Server{
		store:     store,
		CacheInfo: cacheMgr,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz(store))
	mux.HandleFunc("/v1/models", allowMethods(handleModels(), http.MethodGet))
	mux.HandleFunc("/v1/responses", allowMethods(handleResponses(), http.MethodPost))
	mux.HandleFunc("/v1/chat/completions", allowMethods(handleChat(), http.MethodPost))
	mux.HandleFunc("/v1/messages", allowMethods(handleAnthropicMessages(), http.MethodPost))
	srv.mux = mux
	srv.handler = withRequestID(http.HandlerFunc(srv.serveHTTP))
	return srv
}

func allowMethods(next http.HandlerFunc, methods ...string) http.HandlerFunc {
	allowed := make(map[string]struct{}, len(methods))
	normalized := make([]string, 0, len(methods))
	for _, method := range methods {
		method = strings.TrimSpace(method)
		if method == "" {
			continue
		}
		if _, ok := allowed[method]; ok {
			continue
		}
		allowed[method] = struct{}{}
		normalized = append(normalized, method)
	}
	allowHeader := strings.Join(normalized, ", ")
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := allowed[r.Method]; !ok {
			if allowHeader != "" {
				w.Header().Set("Allow", allowHeader)
			}
			errorsx.WriteJSON(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		next(w, r)
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.handler == nil {
		s.handler = withRequestID(http.HandlerFunc(s.serveHTTP))
	}
	s.handler.ServeHTTP(w, r)
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		s.mux.ServeHTTP(w, r)
		return
	}
	if s.store == nil {
		errorsx.WriteJSON(w, http.StatusServiceUnavailable, "config_unavailable", "runtime config unavailable")
		return
	}
	snapshot := s.store.Active()
	if snapshot == nil {
		errorsx.WriteJSON(w, http.StatusServiceUnavailable, "config_unavailable", "runtime config unavailable")
		return
	}
	if info, err := resolveRouteInfo(r.URL.Path, snapshot.Config); err == nil {
		provider, err := snapshot.Config.ProviderByID(info.ProviderID)
		if err != nil {
			errorsx.WriteJSON(w, http.StatusNotFound, "not_found", "route not found")
			return
		}
		ctx := r.Context()
		if s.CacheInfo != nil {
			ctx = withCacheInfoManager(ctx, s.CacheInfo)
		}
		r = r.Clone(withRuntimeSnapshot(withRouteInfo(ctx, info), snapshot))
		r.URL.Path = info.CanonicalPath
		if err := auth.ValidateProxyAuthForProvider(r, snapshot.Config.ProxyAPIKey, provider, info.Legacy); err != nil {
			errorsx.WriteJSON(w, http.StatusUnauthorized, "unauthorized", "invalid proxy api key")
			return
		}
		setConfigVersionHeaders(w, snapshot, info.ProviderID)
		s.mux.ServeHTTP(w, r)
		return
	}

	errorsx.WriteJSON(w, http.StatusNotFound, "not_found", "route not found")
}
