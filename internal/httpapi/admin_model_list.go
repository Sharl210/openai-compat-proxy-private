package httpapi

import (
	"net/http"
	"sort"
	"strings"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/errorsx"
)

// adminModelListEntry 是模型列表中的一行：原始名（上游原名）与伪原始名（RAW 替换后展示名）。
type adminModelListEntry struct {
	Raw    string `json:"raw"`
	Pseudo string `json:"pseudo"`
}

// adminModelListResponse 是 /_admin/api/model-list 的响应：
// raw_models 是上游未改动的原始模型列表（上游列表板块），
// mapped_models 是原始名↔伪原始名映射表（内部列表板块）。
// provider 为 __root__ 时聚合所有默认 provider。
type adminModelListResponse struct {
	ProviderID     string                 `json:"provider_id"`
	ProviderName   string                 `json:"provider_name"`
	RawModels      []string               `json:"raw_models"`
	MappedModels   []adminModelListEntry  `json:"mapped_models"`
	Providers      []string               `json:"providers,omitempty"`
	ProviderErrors []adminProviderError   `json:"provider_errors,omitempty"`
	Error          string                 `json:"error,omitempty"`
}

// adminProviderError 是某个 provider 拉取模型列表失败时的错误：
// 标明配置文件（providers/<pid>.env 或 .env）与具体报错，便于定位。
type adminProviderError struct {
	ProviderID string `json:"provider_id"`
	File       string `json:"file"`
	Error      string `json:"error"`
}

const rootProviderID = "__root__"

// handleModelList 返回指定 provider（或根提供商聚合）的模型列表与映射关系。
// 仅当用户在 WebUI 点击“展示模型列表”时调用，无额外常驻开销。
func (a *adminUI) handleModelList() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, err := a.requireSession(r); err != nil {
			errorsx.WriteJSON(w, http.StatusUnauthorized, "unauthorized", "admin login required")
			return
		}
		snapshot := a.store.Active()
		if snapshot == nil {
			errorsx.WriteJSON(w, http.StatusServiceUnavailable, "no_snapshot", "runtime snapshot unavailable")
			return
		}
		providerID := strings.TrimSpace(r.URL.Query().Get("provider"))
		if providerID == "" {
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", "provider query parameter is required")
			return
		}
		if providerID == rootProviderID {
			a.writeRootModelList(w, r, snapshot)
			return
		}
		provider, err := snapshot.Config.ProviderByID(providerID)
		if err != nil {
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_provider", "unknown provider: "+providerID)
			return
		}
		rawModels, mapped, fetchErr := fetchProviderModelListForAdmin(r, snapshot, provider, providerID)
		if fetchErr != "" {
			errorsx.WriteJSON(w, http.StatusBadGateway, "upstream_error", providerConfigFileName(providerID)+": "+fetchErr)
			return
		}
		writeAdminJSON(w, http.StatusOK, adminModelListResponse{
			ProviderID:   providerID,
			ProviderName: providerID,
			RawModels:    rawModels,
			MappedModels: mapped,
		})
	}
}

// writeRootModelList 聚合所有默认 provider 的模型列表（根提供商展示）。
func (a *adminUI) writeRootModelList(w http.ResponseWriter, r *http.Request, snapshot *config.RuntimeSnapshot) {
	providerIDs := snapshot.DefaultProviderIDs
	if len(providerIDs) == 0 {
		errorsx.WriteJSON(w, http.StatusNotFound, "models_unavailable", "no default providers configured")
		return
	}
	allRaw := make([]string, 0)
	allMapped := make([]adminModelListEntry, 0)
	seenRaw := make(map[string]struct{})
	seenPseudo := make(map[string]struct{})
	providerErrors := make([]adminProviderError, 0)
	for _, pid := range providerIDs {
		provider, err := snapshot.Config.ProviderByID(pid)
		if err != nil || !provider.Enabled {
			continue
		}
		rawModels, mapped, fetchErr := fetchProviderModelListForAdmin(r, snapshot, provider, pid)
		if fetchErr != "" {
			providerErrors = append(providerErrors, adminProviderError{
				ProviderID: pid,
				File:       providerConfigFileName(pid),
				Error:      fetchErr,
			})
			continue
		}
		for _, raw := range rawModels {
			if _, dup := seenRaw[raw]; dup {
				continue
			}
			seenRaw[raw] = struct{}{}
			allRaw = append(allRaw, raw)
		}
		for _, m := range mapped {
			if _, dup := seenPseudo[m.Pseudo]; dup {
				continue
			}
			seenPseudo[m.Pseudo] = struct{}{}
			allMapped = append(allMapped, m)
		}
	}
	sort.Strings(allRaw)
	writeAdminJSON(w, http.StatusOK, adminModelListResponse{
		ProviderID:     rootProviderID,
		ProviderName:   "根提供商（全部默认 provider）",
		RawModels:      allRaw,
		MappedModels:   allMapped,
		Providers:      providerIDs,
		ProviderErrors: providerErrors,
	})
}

// providerConfigFileName 返回 provider 对应的配置文件路径（providers/<pid>.env）。
func providerConfigFileName(providerID string) string {
	if providerID == "" {
		return ".env"
	}
	return "providers/" + providerID + ".env"
}

// fetchProviderModelListForAdmin 拉取单个 provider 的上游模型并产出映射表。
// 返回 (rawModels, mappedModels, errorMessage)；errorMessage 为空表示成功。
func fetchProviderModelListForAdmin(r *http.Request, snapshot *config.RuntimeSnapshot, provider config.ProviderConfig, providerID string) ([]string, []adminModelListEntry, string) {
	providerCfg := providerConfigForID(snapshot, providerID)
	authorization, authErr := authHeaderForOverlayProviderUpstream(r, providerCfg, providerID)
	if authErr != nil {
		return nil, nil, authErr.Error()
	}
	client := upstreamClientForProvider(r, providerID, providerCfg)
	bodies, ok, fetchErr := fetchProviderModelsBodies(r.Context(), client, authorization, provider)
	if fetchErr != nil {
		return nil, nil, fetchErr.Error()
	}
	if !ok {
		return nil, nil, "upstream models unavailable for provider: " + providerID
	}
	rawSet := rawModelIDSet(decodeModelEntries(bodies.raw))
	if len(rawSet) == 0 {
		rawSet = rawModelIDSet(decodeModelEntries(bodies.visible))
	}
	rawModels := make([]string, 0, len(rawSet))
	for id := range rawSet {
		rawModels = append(rawModels, id)
	}
	sort.Strings(rawModels)
	return rawModels, providerModelListEntries(provider, rawModels), ""
}

// providerModelListEntries 给定原始名集合，产出 原始名↔伪原始名 映射行。
func providerModelListEntries(provider config.ProviderConfig, rawModels []string) []adminModelListEntry {
	mapped := make([]adminModelListEntry, 0, len(rawModels))
	seen := make(map[string]struct{}, len(rawModels))
	for _, raw := range rawModels {
		pseudo := provider.ApplyRawModelNameReplace(raw)
		if pseudo == "" {
			pseudo = raw
		}
		if _, dup := seen[pseudo]; dup {
			continue
		}
		seen[pseudo] = struct{}{}
		mapped = append(mapped, adminModelListEntry{Raw: raw, Pseudo: pseudo})
	}
	return mapped
}
