package main

import (
	"log"
	"net/http"
	"os"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/httpapi"
	"openai-compat-proxy/internal/logging"
)

func main() {
	cfg := config.LoadFromEnv()
	closeFn, err := logging.Init(cfg, os.Stdout)
	if err != nil {
		log.Fatal(err)
	}
	defer closeFn()
	if err := http.ListenAndServe(cfg.ListenAddr, httpapi.NewServer(cfg)); err != nil {
		log.Fatal(err)
	}
}
