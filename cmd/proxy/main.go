package main

import (
	"log"
	"net/http"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/httpapi"
)

func main() {
	cfg := config.LoadFromEnv()
	if err := http.ListenAndServe(cfg.ListenAddr, httpapi.NewServer(cfg)); err != nil {
		log.Fatal(err)
	}
}
