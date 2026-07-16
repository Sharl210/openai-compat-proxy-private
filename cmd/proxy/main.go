package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"openai-compat-proxy/internal/cacheinfo"
	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/diagnostics"
	"openai-compat-proxy/internal/httpapi"
	"openai-compat-proxy/internal/logging"
	"openai-compat-proxy/internal/tokenestimator"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	runtimeCtx, stopRuntime := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopRuntime()

	store, err := config.NewRuntimeStore(".env")
	if err != nil {
		return err
	}
	cfg := store.Active().Config
	closeFn, err := logging.Init(cfg, os.Stdout)
	if err != nil {
		return err
	}
	defer closeFn()
	defer diagnostics.StartHeapCaptureSignalHandler(log.Printf)()
	if err := store.StartWatching(runtimeCtx, 300*time.Millisecond, 5*time.Second); err != nil {
		return err
	}

	var cacheMgr *cacheinfo.Manager
	var estimatorMgr *tokenestimator.Manager
	location, err := cfg.CacheInfoLocation()
	if err != nil {
		log.Printf("warning: failed to load cache info timezone %q, falling back to local: %v", cfg.CacheInfoTimezone, err)
		location = time.Local
	}
	enabledProviders := make([]string, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		if p.Enabled {
			enabledProviders = append(enabledProviders, p.ID)
		}
	}
	if len(enabledProviders) > 0 && location != nil {
		if err := cacheinfo.EnsureCacheInfoDir(cfg.ProvidersDir); err != nil {
			log.Printf("warning: failed to initialize cache info directory %q: %v", cfg.ProvidersDir, err)
		} else {
			cacheMgr = cacheinfo.NewManager(cfg.ProvidersDir, location, enabledProviders, nil)
			cacheMgr.SetEnabledProvidersSource(func() []string {
				snapshot := store.Active()
				if snapshot == nil {
					return nil
				}
				ids := make([]string, 0, len(snapshot.Config.Providers))
				for _, provider := range snapshot.Config.Providers {
					if provider.Enabled {
						ids = append(ids, provider.ID)
					}
				}
				return ids
			})
			cacheMgr.SetDefaultProvidersSource(func() []string {
				snapshot := store.Active()
				if snapshot == nil {
					return nil
				}
				return append([]string(nil), snapshot.DefaultProviderIDs...)
			})
			cacheMgr.Start(runtimeCtx)
			defer cacheMgr.Stop()
		}
	}

	if cfg.ProvidersDir != "" {
		estimatorMgr = tokenestimator.NewManager(cfg.ProvidersDir, location, func() []string {
			snapshot := store.Active()
			if snapshot == nil {
				return nil
			}
			ids := make([]string, 0, len(snapshot.Config.Providers))
			for _, provider := range snapshot.Config.Providers {
				if provider.Enabled {
					ids = append(ids, provider.ID)
				}
			}
			return ids
		})
		defer func() {
			_ = estimatorMgr.Flush(context.Background())
		}()
	}

	apiServer := httpapi.NewServerWithStore(store, cacheMgr, estimatorMgr)
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		apiServer.Close()
		return err
	}
	server := &http.Server{Handler: apiServer}
	if err := serveHTTP(runtimeCtx, server, listener, apiServer.Close); err != nil {
		return err
	}
	return nil
}

const httpShutdownTimeout = 30 * time.Second

func serveHTTP(ctx context.Context, server *http.Server, listener net.Listener, closeHandler func()) error {
	return serveHTTPWithShutdownTimeout(ctx, server, listener, closeHandler, httpShutdownTimeout)
}

func serveHTTPWithShutdownTimeout(ctx context.Context, server *http.Server, listener net.Listener, closeHandler func(), shutdownTimeout time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if server == nil {
		return errors.New("http server is nil")
	}
	if listener == nil {
		return errors.New("http listener is nil")
	}
	if closeHandler == nil {
		closeHandler = func() {}
	}
	defer closeHandler()

	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(listener)
	}()

	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownCtx)
		if shutdownErr != nil {
			closeErr := server.Close()
			serveErr := <-serveResult
			if errors.Is(closeErr, http.ErrServerClosed) {
				closeErr = nil
			}
			if errors.Is(serveErr, http.ErrServerClosed) {
				serveErr = nil
			}
			switch {
			case closeErr != nil && serveErr != nil:
				return errors.Join(shutdownErr, closeErr, serveErr)
			case closeErr != nil:
				return errors.Join(shutdownErr, closeErr)
			case serveErr != nil:
				return errors.Join(shutdownErr, serveErr)
			default:
				return shutdownErr
			}
		}
		err := <-serveResult
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
