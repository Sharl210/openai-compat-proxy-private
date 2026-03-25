package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

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
	if err := http.ListenAndServe(cfg.ListenAddr, httpapi.NewServerWithStore(store)); err != nil {
		log.Fatal(err)
	}
}
