package httpapi

import (
	"net/http"
	"strings"
	"sync"

	"openai-compat-proxy/internal/auth"
	"openai-compat-proxy/internal/cacheinfo"
	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/errorsx"
	"openai-compat-proxy/internal/tokenestimator"
	"openai-compat-proxy/internal/upstream"
)

type Server struct {
	store              *config.RuntimeStore
	mux                *http.ServeMux
	handler            http.Handler
	admin              *adminUI
	history            *responsesHistoryStore
	upstreamTransports *upstream.TransportPool
	transportReconcile sync.Mutex

	CacheInfo      *cacheinfo.Manager
	TokenEstimator *tokenestimator.Manager
}

func NewServer(cfg config.Config) *Server {
	return NewServerWithStore(config.NewStaticRuntimeStore(cfg), nil, nil)
}

func NewServerWithStore(store *config.RuntimeStore, cacheMgr *cacheinfo.Manager, tokenEstimatorMgr *tokenestimator.Manager) *Server {
	srv := &Server{
		store:              store,
		CacheInfo:          cacheMgr,
		TokenEstimator:     tokenEstimatorMgr,
		upstreamTransports: upstream.NewTransportPool(),
	}
	if snapshot := store.Active(); snapshot != nil {
		srv.history = newResponsesHistoryStore(defaultResponsesHistoryMaxSize, responsesHistoryToolCallRecoveryIndexPath(snapshot.Config.ProvidersDir))
	} else {
		srv.history = newResponsesHistoryStore(defaultResponsesHistoryMaxSize, "")
	}
	if imageArtifactRootDirOverride != "" {
		ensureImageArtifactsReadyForRoot(imageArtifactRootDirOverride)
	} else if snapshot := store.Active(); snapshot != nil {
		ensureImageArtifactsReadyForRoot(imageArtifactRootDirFromSnapshot(snapshot))
	}
	store.AddRefreshListener(srv.reconcileUpstreamTransports)
	srv.reconcileUpstreamTransports(store.Active())
	srv.admin = newAdminUI(store, cacheMgr)
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz(store))
	mux.HandleFunc("/_images/", allowMethods(handleImageArtifact(), http.MethodGet))
	if srv.admin != nil {
		srv.admin.registerRoutes(mux)
	}
	mux.HandleFunc(canonicalV1ModelsPath, allowMethods(handleModels(), http.MethodGet))
	mux.HandleFunc(canonicalV1ResponsesPath, allowMethods(handleResponses(), http.MethodPost))
	mux.HandleFunc(canonicalV1ResponsesCompactPath, allowMethods(handleResponsesCompact(), http.MethodPost))
	mux.HandleFunc(canonicalV1ChatCompletionsPath, allowMethods(handleChat(), http.MethodPost))
	mux.HandleFunc(canonicalV1MessagesPath, allowMethods(handleAnthropicMessages(), http.MethodPost))
	mux.HandleFunc(canonicalV1ImagesGenerationsPath, allowMethods(handleImageGeneration(), http.MethodPost))
	mux.HandleFunc(canonicalV1ImagesEditsPath, allowMethods(handleImageEdit(), http.MethodPost))
	mux.HandleFunc(canonicalV1ImagesVariationsPath, allowMethods(handleImageVariation(), http.MethodPost))
	mux.HandleFunc(canonicalV1EmbeddingsPath, allowMethods(handleEmbeddings(), http.MethodPost))
	mux.HandleFunc(canonicalV1RerankPath, allowMethods(handleRerank(), http.MethodPost))
	srv.mux = mux
	srv.handler = withRequestID(store, http.HandlerFunc(srv.serveHTTP))
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
		s.handler = withRequestID(s.store, http.HandlerFunc(s.serveHTTP))
	}
	s.handler.ServeHTTP(w, r)
}

func (s *Server) Close() {
	if s == nil || s.upstreamTransports == nil {
		return
	}
	s.upstreamTransports.Close()
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		s.mux.ServeHTTP(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/_images/") {
		s.mux.ServeHTTP(w, r)
		return
	}
	if s.admin != nil && s.admin.matchesPath(r.URL.Path) {
		s.admin.applyHeaders(w)
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
		if s.TokenEstimator != nil {
			ctx = withTokenEstimatorManager(ctx, s.TokenEstimator)
		}
		ctx = withRuntimeStore(ctx, s.store)
		ctx = withResponsesHistory(ctx, s.history)
		ctx = withUpstreamTransportPool(ctx, s.upstreamTransports)
		r = r.Clone(withRuntimeSnapshot(withRouteInfo(ctx, info), snapshot))
		r.URL.Path = info.CanonicalPath
		if shouldUseRootLegacyProxyAuth(info, snapshot) {
			if err := auth.ValidateProxyAuth(r, snapshot.Config.ProxyAPIKey); err != nil {
				errorsx.WriteJSON(w, http.StatusUnauthorized, "unauthorized", "invalid proxy api key")
				return
			}
		} else {
			if err := auth.ValidateProxyAuthForProvider(r, snapshot.Config.ProxyAPIKey, provider, info.Legacy); err != nil {
				errorsx.WriteJSON(w, http.StatusUnauthorized, "unauthorized", "invalid proxy api key")
				return
			}
		}
		setConfigVersionHeaders(w, snapshot, info.ProviderID)
		s.mux.ServeHTTP(w, r)
		return
	}

	errorsx.WriteJSON(w, http.StatusNotFound, "not_found", "route not found")
}

func (s *Server) reconcileUpstreamTransports(_ *config.RuntimeSnapshot) {
	if s == nil || s.store == nil {
		return
	}
	s.transportReconcile.Lock()
	defer s.transportReconcile.Unlock()
	snapshot := s.store.Active()
	if snapshot == nil {
		return
	}
	activeProviderIDs := make([]string, 0, len(snapshot.Config.Providers))
	for _, provider := range snapshot.Config.Providers {
		if provider.Enabled {
			activeProviderIDs = append(activeProviderIDs, provider.ID)
		}
	}
	s.upstreamTransports.ReconcileProviderIDs(activeProviderIDs)
}

func shouldUseRootLegacyProxyAuth(info routeInfo, snapshot *config.RuntimeSnapshot) bool {
	if !info.Legacy || snapshot == nil {
		return false
	}
	return len(snapshot.DefaultProviderIDs) > 1
}
