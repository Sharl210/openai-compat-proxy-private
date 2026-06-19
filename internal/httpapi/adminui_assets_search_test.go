package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminUIAppScriptIncludesFileSearchDialog(t *testing.T) {
	server := newAdminUITestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/_admin/assets/app.js", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected app script 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"search-tree-button",
		"renderFileSearchModal",
		"handleFileSearchAdvancedChange",
		"loadFileSearchHistory",
		"persistFileSearchHistory",
		"admin-file-search-filename-history",
		"admin-file-search-content-history",
		"admin-file-search-size-settings",
		"loadFileSearchSizeSettings",
		"persistFileSearchSizeSettings",
		"buildFileSearchRequestParams",
		"fileSearchValueToBytes",
		"restoreSearchResultsState",
		"file-search-query",
		"file-search-recursive",
		"file-search-advanced",
		"file-search-advanced-options",
		"file-search-min-size",
		"file-search-min-size-unit",
		"file-search-max-size",
		"file-search-max-size-unit",
		"file-search-content",
		"file-search-filename-history-button",
		"file-search-content-history-button",
		"file-search-history-menu",
		"file-search-history-item",
		"file-search-history-delete",
		"file-search-history-clear",
		"file-search-case-sensitive",
		"file-search-regex",
		"fileSearchLoading",
		"file-search-loading",
		"搜索中",
		"min_size_bytes",
		"max_size_bytes",
		"content_contains",
		"case_sensitive",
		"regex",
		"/_admin/api/search",
		"搜索子目录",
		"高级搜索",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected app script to include %q, got %s", want, body)
		}
	}
	if !strings.Contains(body, `id="file-search-recursive" type="checkbox"`) || !strings.Contains(body, `state.fileSearchModal.recursive === false ? '' : 'checked'`) {
		t.Fatalf("expected search recursive checkbox to default checked from modal state, got %s", body)
	}
	if strings.Contains(body, `id="file-search-advanced" type="checkbox" checked`) {
		t.Fatalf("expected advanced search checkbox not checked by default, got %s", body)
	}
	recursiveIndex := strings.Index(body, `id="file-search-recursive"`)
	advancedOptionsIndex := strings.Index(body, `id="file-search-advanced-options"`)
	if recursiveIndex < 0 || advancedOptionsIndex < 0 || recursiveIndex > advancedOptionsIndex {
		t.Fatalf("expected recursive checkbox before advanced options, got recursive=%d advancedOptions=%d body=%s", recursiveIndex, advancedOptionsIndex, body)
	}
	if !strings.Contains(body, `state.fileSearchModal = null;`) || !strings.Contains(body, `state.fileSearchLoading = true;`) {
		t.Fatalf("expected search submit to close modal and show loading before awaiting API, got %s", body)
	}
	if !strings.Contains(body, `snapshotFileSearchResult(state.fileSearchResult)`) || !strings.Contains(body, `restoreSearchResultsState`) {
		t.Fatalf("expected file search history state restoration hooks, got %s", body)
	}
}

func TestAdminUIAppCSSIncludesFileSearchStates(t *testing.T) {
	server := newAdminUITestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/_admin/assets/app.css", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected css asset 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		".file-search-advanced-options",
		".file-search-loading",
		".file-search-history-menu",
		".file-search-history-item",
		".file-search-history-delete",
		".file-search-history-clear",
		".file-search-size-group",
		".file-search-size-unit",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected app css to include %s styles, got %s", want, body)
		}
	}
}
