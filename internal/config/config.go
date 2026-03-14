package config

import (
	"os"
	"time"
)

type Config struct {
	ListenAddr       string
	ProxyAPIKey      string
	UpstreamBaseURL  string
	UpstreamAPIKey   string
	ConnectTimeout   time.Duration
	FirstByteTimeout time.Duration
	IdleTimeout      time.Duration
	TotalTimeout     time.Duration
}

func Default() Config {
	return Config{
		ListenAddr:       ":8080",
		ConnectTimeout:   10 * time.Second,
		FirstByteTimeout: 30 * time.Second,
		IdleTimeout:      30 * time.Second,
		TotalTimeout:     2 * time.Minute,
	}
}

func LoadFromEnv() Config {
	cfg := Default()
	if value := os.Getenv("LISTEN_ADDR"); value != "" {
		cfg.ListenAddr = value
	}
	if value := os.Getenv("PROXY_API_KEY"); value != "" {
		cfg.ProxyAPIKey = value
	}
	if value := os.Getenv("UPSTREAM_BASE_URL"); value != "" {
		cfg.UpstreamBaseURL = value
	}
	if value := os.Getenv("UPSTREAM_API_KEY"); value != "" {
		cfg.UpstreamAPIKey = value
	}
	return cfg
}
