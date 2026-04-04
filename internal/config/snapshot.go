package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	PromptPathsByID     map[string][]string
	providerMTimeByID   map[string]time.Time
}

func FormatVersionTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	loc, err := Default().CacheInfoLocation()
	if err != nil || loc == nil {
		return t.UTC().Format(versionTimeLayout)
	}
	return t.In(loc).Format(versionTimeLayout)
}

func FormatVersionTimeInLocation(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return ""
	}
	if loc == nil {
		return FormatVersionTime(t)
	}
	return t.In(loc).Format(versionTimeLayout)
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
	if err := ValidateRootEnvValues(values); err != nil {
		return nil, err
	}
	return buildRuntimeSnapshotFromValues(rootEnvPath, rootInfo.ModTime(), values)
}

func BuildRuntimeSnapshotForRefresh(rootEnvPath string, previous Config) (*RuntimeSnapshot, error) {
	rootInfo, err := os.Stat(rootEnvPath)
	if err != nil {
		return nil, err
	}
	values, err := parseEnvFile(rootEnvPath)
	if err != nil {
		return nil, err
	}
	if err := validateHotReloadableRootEnvValues(values); err != nil {
		return nil, err
	}
	snapshot, err := buildRuntimeSnapshotFromValues(rootEnvPath, rootInfo.ModTime(), values)
	if err != nil {
		return nil, err
	}
	snapshot.Config.applyStartupOnlyFrom(previous)
	if err := snapshot.Config.Validate(); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func buildRuntimeSnapshotFromValues(rootEnvPath string, rootEnvMTime time.Time, values map[string]string) (*RuntimeSnapshot, error) {
	cfg := LoadFromValues(values)
	cfg.ProvidersDir = ResolveProvidersDir(rootEnvPath, cfg.ProvidersDir)
	versionLocation, err := cfg.CacheInfoLocation()
	if err != nil {
		return nil, err
	}
	providers, providerVersions, providerPaths, promptPaths, providerMTimes, err := loadProvidersWithMetadata(cfg.ProvidersDir, versionLocation)
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
		RootEnvMTime:        rootEnvMTime,
		RootEnvVersion:      FormatVersionTimeInLocation(rootEnvMTime, versionLocation),
		ProviderVersionByID: providerVersions,
		ProviderPathByID:    providerPaths,
		PromptPathsByID:     promptPaths,
		providerMTimeByID:   providerMTimes,
	}, nil
}

func validateHotReloadableRootEnvValues(values map[string]string) error {
	if err := validateStrictBool(values, "ENABLE_LEGACY_V1_ROUTES"); err != nil {
		return err
	}
	if err := validateMasqueradeTarget(values, "UPSTREAM_MASQUERADE_TARGET"); err != nil {
		return err
	}
	if err := validateStrictBool(values, "UPSTREAM_INJECT_METADATA_USER_ID"); err != nil {
		return err
	}
	if err := validateStrictBool(values, "UPSTREAM_INJECT_CLAUDE_SYSTEM_PROMPT"); err != nil {
		return err
	}
	if err := validatePositiveDuration(values, "CONNECT_TIMEOUT"); err != nil {
		return err
	}
	if err := validatePositiveDuration(values, "FIRST_BYTE_TIMEOUT"); err != nil {
		return err
	}
	if err := validatePositiveDuration(values, "IDLE_TIMEOUT"); err != nil {
		return err
	}
	if err := validatePositiveDuration(values, "TOTAL_TIMEOUT"); err != nil {
		return err
	}
	if err := validateDownstreamNonStreamStrategy(values, "DOWNSTREAM_NON_STREAM_STRATEGY"); err != nil {
		return err
	}
	return nil
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

func loadProvidersWithMetadata(dir string, versionLocation *time.Location) ([]ProviderConfig, map[string]string, map[string]string, map[string][]string, map[string]time.Time, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	providers := make([]ProviderConfig, 0, len(entries))
	versions := map[string]string{}
	paths := map[string]string{}
	promptPaths := map[string][]string{}
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
			return nil, nil, nil, nil, nil, err
		}
		info, err := os.Stat(fullPath)
		if err != nil {
			return nil, nil, nil, nil, nil, err
		}
		providerVersionTime := info.ModTime()
		provider.SystemPromptText, providerVersionTime, err = loadSystemPromptText(provider.SystemPromptFiles, providerVersionTime)
		if err != nil {
			return nil, nil, nil, nil, nil, err
		}
		if provider.ID == "" {
			return nil, nil, nil, nil, nil, ErrInvalidConfig(fmt.Sprintf("provider file %s is missing PROVIDER_ID", name))
		}
		if _, exists := seen[provider.ID]; exists {
			return nil, nil, nil, nil, nil, ErrInvalidConfig(fmt.Sprintf("duplicate provider id: %s", provider.ID))
		}
		seen[provider.ID] = struct{}{}
		providers = append(providers, provider)
		versions[provider.ID] = FormatVersionTimeInLocation(providerVersionTime, versionLocation)
		paths[provider.ID] = fullPath
		promptPaths[provider.ID] = append([]string(nil), provider.SystemPromptFiles...)
		mtimes[provider.ID] = providerVersionTime
	}
	sortProviders(providers)
	return providers, versions, paths, promptPaths, mtimes, nil
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

func loadSystemPromptText(paths []string, latest time.Time) (string, time.Time, error) {
	if len(paths) == 0 {
		return "", latest, nil
	}
	sections := make([]string, 0, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", latest, err
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		content, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", latest, err
		}
		raw := string(content)
		if strings.TrimSpace(raw) == "" {
			continue
		}
		trimmed := strings.TrimRight(raw, "\r\n")
		sections = append(sections, trimmed)
	}
	return strings.Join(sections, "\n\n"), latest, nil
}

func (s *RuntimeSnapshot) PromptWatchDirs() []string {
	if s == nil {
		return nil
	}
	seen := map[string]struct{}{}
	dirs := make([]string, 0)
	for _, paths := range s.PromptPathsByID {
		for _, path := range paths {
			dir := filepath.Dir(path)
			if dir == "" {
				continue
			}
			if _, ok := seen[dir]; ok {
				continue
			}
			seen[dir] = struct{}{}
			dirs = append(dirs, dir)
		}
	}
	slices.Sort(dirs)
	return dirs
}
