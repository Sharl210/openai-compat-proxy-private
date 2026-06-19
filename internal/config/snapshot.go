package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"openai-compat-proxy/internal/reasoning"
)

const versionTimeLayout = "2006-01-02 15:04:05.000"

const systemPromptSectionSeparator = "================================"

type RuntimeSnapshot struct {
	Config                     Config
	RootEnvPath                string
	RootEnvMTime               time.Time
	RootEnvVersion             string
	DefaultProviderIDs         []string
	DefaultModelOwners         map[string]string
	DefaultVisibleModels       []string
	DefaultModelRawIDs         map[string]string
	DefaultTaggedModelOwners   map[string]string
	DefaultTaggedVisibleModels []string
	DefaultTaggedModelRawIDs   map[string]string
	ProviderVersionByID        map[string]string
	ProviderPathByID           map[string]string
	PromptPathsByID            map[string][]string
	providerMTimeByID          map[string]time.Time
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
	normalizeRuntimeConfigDefaults(&cfg)
	applyRootProviderTokenDefaults(&cfg)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	defaultProviderIDs, defaultModelOwners, defaultVisibleModels, defaultTaggedModelOwners, defaultTaggedVisibleModels, defaultModelRawIDs, defaultTaggedModelRawIDs, err := buildDefaultOverlayModelIndex(cfg)
	if err != nil {
		return nil, err
	}
	return &RuntimeSnapshot{
		Config:                     cfg,
		RootEnvPath:                rootEnvPath,
		RootEnvMTime:               rootEnvMTime,
		RootEnvVersion:             FormatVersionTimeInLocation(rootEnvMTime, versionLocation),
		DefaultProviderIDs:         defaultProviderIDs,
		DefaultModelOwners:         defaultModelOwners,
		DefaultVisibleModels:       defaultVisibleModels,
		DefaultModelRawIDs:         defaultModelRawIDs,
		DefaultTaggedModelOwners:   defaultTaggedModelOwners,
		DefaultTaggedVisibleModels: defaultTaggedVisibleModels,
		DefaultTaggedModelRawIDs:   defaultTaggedModelRawIDs,
		ProviderVersionByID:        providerVersions,
		ProviderPathByID:           providerPaths,
		PromptPathsByID:            promptPaths,
		providerMTimeByID:          providerMTimes,
	}, nil
}

func applyRootProviderTokenDefaults(cfg *Config) {
	if cfg == nil {
		return
	}
	for i := range cfg.Providers {
		if !cfg.Providers[i].UpstreamMaxOutputTokensSet {
			cfg.Providers[i].UpstreamMaxOutputTokens = cfg.UpstreamMaxOutputTokens
			cfg.Providers[i].UpstreamMaxOutputTokenRules = append([]ScopedIntRule(nil), cfg.UpstreamMaxOutputTokenRules...)
		}
		if !cfg.Providers[i].AnthropicMaxThinkingBudgetSet {
			cfg.Providers[i].AnthropicMaxThinkingBudget = cfg.AnthropicMaxThinkingBudget
		}
		if !cfg.Providers[i].ModelLimitContextTokensSet {
			cfg.Providers[i].ModelLimitContextTokens = cfg.ModelLimitContextTokens
			cfg.Providers[i].ModelLimitContextTokenRules = append([]ScopedIntRule(nil), cfg.ModelLimitContextTokenRules...)
		}
		if !cfg.Providers[i].ForceUpstreamMaxOutputTokensSet {
			cfg.Providers[i].ForceUpstreamMaxOutputTokens = cfg.ForceUpstreamMaxOutputTokens
		}
		if !cfg.Providers[i].UpstreamRetryCountSet {
			cfg.Providers[i].UpstreamRetryCount = cfg.UpstreamRetryCount
		}
		if !cfg.Providers[i].UpstreamRetryDelaySet {
			cfg.Providers[i].UpstreamRetryDelay = cfg.UpstreamRetryDelay
		}
		if !cfg.Providers[i].UpstreamCacheControlSet {
			cfg.Providers[i].UpstreamCacheControl = cfg.UpstreamCacheControl
		}
	}
}

func buildDefaultOverlayModelIndex(cfg Config) ([]string, map[string]string, []string, map[string]string, []string, map[string]string, map[string]string, error) {
	providerIDs, err := cfg.DefaultProviderIDs()
	if err != nil {
		return nil, nil, nil, nil, nil, nil, nil, err
	}
	owners := make(map[string]string)
	rawIDs := make(map[string]string)
	taggedOwners := make(map[string]string)
	taggedRawIDs := make(map[string]string)
	if len(providerIDs) == 0 {
		return nil, owners, nil, taggedOwners, nil, rawIDs, taggedRawIDs, nil
	}
	visibleByProvider := make(map[string][]string, len(providerIDs))
	externalByProvider := make(map[string]map[string]string, len(providerIDs))
	modelCount := make(map[string]int)
	for _, id := range providerIDs {
		provider, err := cfg.ProviderByID(id)
		if err != nil {
			return nil, nil, nil, nil, nil, nil, nil, err
		}
		visible := providerVisibleModelIDs(provider)
		visibleByProvider[id] = visible
		externalByProvider[id] = make(map[string]string, len(visible))
		for _, modelID := range visible {
			externalID := provider.ExternalModelID(modelID, true)
			externalByProvider[id][modelID] = externalID
			modelCount[externalID]++
			taggedID := formatTaggedModelID(id, externalID)
			taggedOwners[taggedID] = id
			taggedRawIDs[taggedID] = modelID
		}
	}
	visible := make([]string, 0, len(taggedOwners)+len(modelCount))
	seen := make(map[string]struct{}, len(taggedOwners)+len(modelCount))
	if cfg.EnableDefaultProviderModelTags {
		for i := len(providerIDs) - 1; i >= 0; i-- {
			id := providerIDs[i]
			for _, modelID := range visibleByProvider[id] {
				externalID := externalByProvider[id][modelID]
				visibleID := externalID
				if cfg.EnableAllDefaultProviderModelTags || modelCount[externalID] > 1 {
					visibleID = formatTaggedModelID(id, externalID)
				}
				if _, ok := seen[visibleID]; ok {
					continue
				}
				seen[visibleID] = struct{}{}
				owners[visibleID] = id
				rawIDs[visibleID] = modelID
				visible = append(visible, visibleID)
			}
		}
	} else {
		for _, id := range providerIDs {
			for _, modelID := range visibleByProvider[id] {
				externalID := externalByProvider[id][modelID]
				owners[externalID] = id
				rawIDs[externalID] = modelID
			}
		}
		for i := len(providerIDs) - 1; i >= 0; i-- {
			id := providerIDs[i]
			for _, modelID := range visibleByProvider[id] {
				externalID := externalByProvider[id][modelID]
				if owners[externalID] != id {
					continue
				}
				if _, ok := seen[externalID]; ok {
					continue
				}
				seen[externalID] = struct{}{}
				visible = append(visible, externalID)
			}
		}
	}
	taggedVisible := make([]string, 0, len(taggedOwners))
	for i := len(providerIDs) - 1; i >= 0; i-- {
		id := providerIDs[i]
		for _, modelID := range visibleByProvider[id] {
			taggedVisible = append(taggedVisible, formatTaggedModelID(id, externalByProvider[id][modelID]))
		}
	}
	return providerIDs, owners, visible, taggedOwners, taggedVisible, rawIDs, taggedRawIDs, nil
}

func formatTaggedModelID(providerID string, modelID string) string {
	return "[" + providerID + "]" + modelID
}

func parseTaggedModelID(model string) (string, string, bool) {
	if !strings.HasPrefix(model, "[") {
		return "", "", false
	}
	end := strings.Index(model, "]")
	if end <= 1 || end == len(model)-1 {
		return "", "", false
	}
	providerID := model[1:end]
	baseModel := model[end+1:]
	if strings.TrimSpace(providerID) == "" || strings.TrimSpace(baseModel) == "" {
		return "", "", false
	}
	return providerID, baseModel, true
}

func providerVisibleModelIDs(provider ProviderConfig) []string {
	return provider.VisibleModelIDs()
}

func (s *RuntimeSnapshot) ResolveDefaultProviderIDForModel(model string) (string, bool) {
	if s == nil {
		return "", false
	}
	if strippedModel, stripped := stripNoPromptModelSuffix(model); stripped {
		providerID, ok := s.ResolveDefaultProviderIDForModel(strippedModel)
		if !ok {
			return "", false
		}
		provider, err := s.Config.ProviderByID(providerID)
		if err != nil || !provider.EffectiveNoPromptModelSuffix(s.Config.EnableNoPromptModelSuffix) || provider.HidesModel(model) {
			return "", false
		}
		return providerID, true
	}
	if owner, ok := s.DefaultModelOwners[model]; ok {
		return owner, true
	}
	baseModel, _, ok := reasoning.SplitSuffix(model)
	if !ok {
		return "", false
	}
	owner, ok := s.DefaultModelOwners[baseModel]
	if !ok {
		return "", false
	}
	provider, err := s.Config.ProviderByID(owner)
	if err != nil || (!provider.EnableReasoningEffortSuffix && !provider.HasManualReasonSuffixForModel(model)) || provider.HidesModel(model) {
		return "", false
	}
	return owner, true
}

func (s *RuntimeSnapshot) rawDefaultModelID(model string) string {
	if s == nil || len(s.DefaultModelRawIDs) == 0 {
		return model
	}
	if raw := strings.TrimSpace(s.DefaultModelRawIDs[model]); raw != "" {
		return raw
	}
	if baseModel, effort, ok := reasoning.SplitSuffix(model); ok {
		if rawBase := strings.TrimSpace(s.DefaultModelRawIDs[baseModel]); rawBase != "" {
			return rawBase + "-" + effort
		}
	}
	return model
}

func (s *RuntimeSnapshot) rawTaggedDefaultModelID(model string) string {
	if s == nil || len(s.DefaultTaggedModelRawIDs) == 0 {
		_, baseModel, ok := parseTaggedModelID(model)
		if ok {
			return baseModel
		}
		return model
	}
	if raw := strings.TrimSpace(s.DefaultTaggedModelRawIDs[model]); raw != "" {
		return raw
	}
	_, baseModel, ok := parseTaggedModelID(model)
	if ok {
		return baseModel
	}
	return model
}

func (s *RuntimeSnapshot) ResolveTaggedDefaultProviderIDForModel(model string) (string, string, bool) {
	if s == nil || !s.Config.EnableDefaultProviderModelTags {
		return "", "", false
	}
	providerID, baseModel, ok := parseTaggedModelID(model)
	if !ok {
		return "", "", false
	}
	provider, err := s.Config.ProviderByID(providerID)
	if err != nil {
		return "", "", false
	}
	if s.DefaultTaggedModelOwners[formatTaggedModelID(providerID, baseModel)] != providerID {
		baseVisibleModel, _, ok := reasoning.SplitSuffix(baseModel)
		if !ok || (!provider.EnableReasoningEffortSuffix && !provider.HasManualReasonSuffixForModel(baseModel)) || provider.HidesModel(baseModel) || s.DefaultTaggedModelOwners[formatTaggedModelID(providerID, baseVisibleModel)] != providerID {
			if strippedModel, stripped := stripNoPromptModelSuffix(baseModel); stripped && provider.EffectiveNoPromptModelSuffix(s.Config.EnableNoPromptModelSuffix) && !provider.HidesModel(baseModel) {
				if _, _, strippedOK := s.ResolveTaggedDefaultProviderIDForModel(formatTaggedModelID(providerID, strippedModel)); strippedOK {
					return providerID, strippedModel, true
				}
			}
			return "", "", false
		}
	}
	return providerID, s.rawTaggedDefaultModelID(model), true
}

func (s *RuntimeSnapshot) ResolveDefaultProviderSelection(model string) (string, string, bool) {
	if s == nil {
		return "", model, false
	}
	if providerID, baseModel, ok := s.ResolveTaggedDefaultProviderIDForModel(model); ok {
		return providerID, baseModel, true
	}
	if !s.Config.EnableDefaultProviderModelTags {
		if providerID, ok := s.ResolveDefaultProviderIDForModel(model); ok {
			if strippedModel, stripped := stripNoPromptModelSuffix(model); stripped {
				provider, err := s.Config.ProviderByID(providerID)
				if err == nil && provider.EffectiveNoPromptModelSuffix(s.Config.EnableNoPromptModelSuffix) && !provider.HidesModel(model) {
					rawStripped := s.rawDefaultModelID(strippedModel)
					return providerID, rawStripped, true
				}
			}
			return providerID, s.rawDefaultModelID(model), true
		}
		if baseModel, ok := stripNoPromptModelSuffix(model); ok {
			if owner, resolvedModel, resolved := s.ResolveDefaultProviderSelection(baseModel); resolved {
				provider, err := s.Config.ProviderByID(owner)
				if err == nil && provider.EffectiveNoPromptModelSuffix(s.Config.EnableNoPromptModelSuffix) && !provider.HidesModel(baseModel) {
					return owner, resolvedModel, true
				}
			}
		}
		return "", model, false
	}
	if owner, ok := s.DefaultModelOwners[model]; ok {
		return owner, s.rawDefaultModelID(model), true
	}
	if baseModel, ok := stripNoPromptModelSuffix(model); ok {
		if owner, resolvedModel, resolved := s.ResolveDefaultProviderSelection(baseModel); resolved {
			provider, err := s.Config.ProviderByID(owner)
			if err == nil && provider.EffectiveNoPromptModelSuffix(s.Config.EnableNoPromptModelSuffix) && !provider.HidesModel(baseModel) {
				return owner, resolvedModel, true
			}
		}
	}
	return "", model, false
}

func stripNoPromptModelSuffix(model string) (string, bool) {
	trimmed, _, hasNoPrompt := stripRootProxySuffixMarkers(model)
	if !hasNoPrompt {
		return model, false
	}
	return trimmed, true
}

func providerResolvesModel(provider ProviderConfig, model string) bool {
	if provider.HidesModel(model) {
		return false
	}
	if provider.resolveModel(model) != "" {
		return true
	}
	if !provider.EnableReasoningEffortSuffix && !provider.HasManualReasonSuffixForModel(model) {
		return false
	}
	baseModel, _, ok := splitReasoningSuffix(model)
	if !ok {
		return false
	}
	if provider.HidesModel(baseModel) {
		return false
	}
	return provider.resolveModel(baseModel) != ""
}

func validateHotReloadableRootEnvValues(values map[string]string) error {
	if err := validateStrictBool(values, "ENABLE_LEGACY_V1_ROUTES"); err != nil {
		return err
	}
	if err := validateStrictBool(values, "ENABLE_DEFAULT_PROVIDER_MODEL_TAGS"); err != nil {
		return err
	}
	if err := validateStrictBool(values, "ENABLE_ALL_DEFAULT_PROVIDER_MODEL_TAGS"); err != nil {
		return err
	}
	if err := validateStrictBool(values, "ENABLE_NOPROMPT_MODEL_SUFFIX"); err != nil {
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
	if err := validateRootModelMap(values, "V1_MODEL_MAP"); err != nil {
		return err
	}
	if err := validateRootUpstreamMaxOutputTokens(values, "UPSTREAM_MAX_OUTPUT_TOKENS"); err != nil {
		return err
	}
	if err := validateRootModelLimitContextTokens(values, "MODEL_LIMIT_CONTEXT_TOKENS"); err != nil {
		return err
	}
	if err := validateStrictBool(values, "FORCE_UPSTREAM_MAX_OUTPUT_TOKENS"); err != nil {
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
	return strings.Join(sections, "\n\n"+systemPromptSectionSeparator+"\n\n"), latest, nil
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
