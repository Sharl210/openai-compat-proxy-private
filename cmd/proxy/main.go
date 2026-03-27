package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"openai-compat-proxy/internal/cacheinfo"
	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/httpapi"
	"openai-compat-proxy/internal/logging"
)

func main() {
	store, err := config.NewRuntimeStore(".env")
	if err != nil {
		log.Fatal(err)
	}
	cfg := store.Active().Config
	closeFn, err := logging.Init(cfg, os.Stdout)
	if err != nil {
		log.Fatal(err)
	}
	defer closeFn()
	if err := store.StartWatching(context.Background(), 300*time.Millisecond, 5*time.Second); err != nil {
		log.Fatal(err)
	}

	var cacheMgr *cacheinfo.Manager
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
			cacheMgr.Start(context.Background())
			defer cacheMgr.Stop()
		}
	}

	if err := http.ListenAndServe(cfg.ListenAddr, httpapi.NewServerWithStore(store, cacheMgr)); err != nil {
		log.Fatal(err)
	}
}
