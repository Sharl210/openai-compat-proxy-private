package httpapi

import (
	"net/http"

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
	mux.HandleFunc("/v1/models", handleModels())
	mux.HandleFunc("/v1/responses", handleResponses())
	mux.HandleFunc("/v1/chat/completions", handleChat())
	mux.HandleFunc("/v1/messages", handleAnthropicMessages())
	srv.mux = mux
	srv.handler = withRequestID(http.HandlerFunc(srv.serveHTTP))
	return srv
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
		setConfigVersionHeaders(w, snapshot, info.ProviderID)
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
		s.mux.ServeHTTP(w, r)
		return
	}

	errorsx.WriteJSON(w, http.StatusNotFound, "not_found", "route not found")
}
