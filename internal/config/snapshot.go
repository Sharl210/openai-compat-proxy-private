package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const versionTimeLayout = "2006-01-02 15:04:05.000"

type RuntimeSnapshot struct {
	Config              Config
	RootEnvPath         string
	RootEnvMTime        time.Time
	RootEnvVersion      string
	ProviderVersionByID map[string]string
	ProviderPathByID    map[string]string
	providerMTimeByID   map[string]time.Time
}

func FormatVersionTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(versionTimeLayout)
}

func formatVersionTime(t time.Time) string {
	return FormatVersionTime(t)
}

func BuildRuntimeSnapshot(rootEnvPath string) (*RuntimeSnapshot, error) {
	rootInfo, err := os.Stat(rootEnvPath)
	if err != nil {
		return nil, err
	}
	values, err := parseEnvFile(rootEnvPath)
	if err != nil {
		return nil, err
	}
	cfg := LoadFromValues(values)
	cfg.ProvidersDir = ResolveProvidersDir(rootEnvPath, cfg.ProvidersDir)
	providers, providerVersions, providerPaths, providerMTimes, err := loadProvidersWithMetadata(cfg.ProvidersDir)
	if err != nil {
		return nil, err
	}
	cfg.Providers = providers
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &RuntimeSnapshot{
		Config:              cfg,
		RootEnvPath:         rootEnvPath,
		RootEnvMTime:        rootInfo.ModTime(),
		RootEnvVersion:      FormatVersionTime(rootInfo.ModTime()),
		ProviderVersionByID: providerVersions,
		ProviderPathByID:    providerPaths,
		providerMTimeByID:   providerMTimes,
	}, nil
}

func parseEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			return nil, ErrInvalidConfig(fmt.Sprintf("invalid env line in %s: %s", path, line))
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func loadProvidersWithMetadata(dir string) ([]ProviderConfig, map[string]string, map[string]string, map[string]time.Time, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	providers := make([]ProviderConfig, 0, len(entries))
	versions := map[string]string{}
	paths := map[string]string{}
	mtimes := map[string]time.Time{}
	seen := map[string]struct{}{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".env") || strings.HasSuffix(name, ".env.example") {
			continue
		}
		fullPath := filepath.Join(dir, name)
		provider, err := loadProviderFile(fullPath)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		if provider.ID == "" {
			return nil, nil, nil, nil, ErrInvalidConfig(fmt.Sprintf("provider file %s is missing PROVIDER_ID", name))
		}
		if _, exists := seen[provider.ID]; exists {
			return nil, nil, nil, nil, ErrInvalidConfig(fmt.Sprintf("duplicate provider id: %s", provider.ID))
		}
		info, err := os.Stat(fullPath)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		seen[provider.ID] = struct{}{}
		providers = append(providers, provider)
		versions[provider.ID] = FormatVersionTime(info.ModTime())
		paths[provider.ID] = fullPath
		mtimes[provider.ID] = info.ModTime()
	}
	sortProviders(providers)
	return providers, versions, paths, mtimes, nil
}

func sortProviders(providers []ProviderConfig) {
	for i := 0; i < len(providers); i++ {
		for j := i + 1; j < len(providers); j++ {
			if providers[j].ID < providers[i].ID {
				providers[i], providers[j] = providers[j], providers[i]
			}
		}
	}
}
