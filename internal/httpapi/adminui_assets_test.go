package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminUIAppScriptSupportsDirectoryLongPressActionMenu(t *testing.T) {
	server := newAdminUITestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/_admin/assets/app.js", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected app script 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "openTreeItemActionMenu(button.dataset.treeOpen, button.dataset.type)") {
		t.Fatalf("expected app script to open action menu for directory entries too, got %s", body)
	}
}

func TestAdminUIAppScriptAutoRefreshesStatusWhenPageActive(t *testing.T) {
	server := newAdminUITestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/_admin/assets/app.js", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected app script 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "statusAutoRefreshIntervalMs = 3000") {
		t.Fatalf("expected app script to define 3s auto refresh interval, got %s", body)
	}
	if !strings.Contains(body, "state.view === 'status'") || !strings.Contains(body, "document.visibilityState === 'visible'") {
		t.Fatalf("expected app script to gate auto refresh by active status page visibility, got %s", body)
	}
	if !strings.Contains(body, "refreshStatusWithRetry({ retryOnDisconnect: true, attempts: 1, silent: true })") {
		t.Fatalf("expected app script to auto trigger silent status refresh, got %s", body)
	}
}

func TestAdminUIAppScriptRendersServiceStartedAtCard(t *testing.T) {
	server := newAdminUITestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/_admin/assets/app.js", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected app script 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "服务启动时间") || !strings.Contains(body, "formatServiceStartedAt(status.started_at)") {
		t.Fatalf("expected app script to render service started-at status card, got %s", body)
	}
}

func TestAdminUIAppScriptUsesMetaChipStyleForProjectFileRefreshButton(t *testing.T) {
	server := newAdminUITestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/_admin/assets/app.js", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected app script 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "id=\"refresh-tree-button\" class=\"badge info material-state-chip compact-meta-button\"") {
		t.Fatalf("expected project-file refresh button to use the same meta chip style as current directory and project file pills, got %s", body)
	}
	if !strings.Contains(body, "refreshTreeButton.addEventListener('click', refreshCurrentDirectory)") {
		t.Fatalf("expected project-file refresh button to refresh current directory, got %s", body)
	}
	if !strings.Contains(body, "async function refreshCurrentDirectory()") {
		t.Fatalf("expected app script to define refreshCurrentDirectory helper, got %s", body)
	}
}

func TestAdminUICSSKeepsBrowserRefreshButtonPinnedTopRight(t *testing.T) {
	server := newAdminUITestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/_admin/assets/app.css", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected app css 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, ".compact-meta-row") || !strings.Contains(body, "flex-wrap: nowrap;") {
		t.Fatalf("expected compact meta row to prevent the refresh button from wrapping to a new line, got %s", body)
	}
	if !strings.Contains(body, "gap: 8px;") {
		t.Fatalf("expected compact meta row spacing to be tightened to 8px, got %s", body)
	}
	if !strings.Contains(body, "min-width: 0;") {
		t.Fatalf("expected compact meta left group to be shrinkable, got %s", body)
	}
	if !strings.Contains(body, "margin-left: auto;") || !strings.Contains(body, "align-self: flex-start;") {
		t.Fatalf("expected refresh button to stay pinned at the top-right, got %s", body)
	}
}
