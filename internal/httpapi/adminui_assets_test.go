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

func TestAdminUICSSShowsEnvCommentBlocksAsScrollableCode(t *testing.T) {
	server := newAdminUITestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/_admin/assets/app.css", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected app css 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, ".env-comment-block") {
		t.Fatalf("expected env comment block styles, got %s", body)
	}
	for _, want := range []string{
		"white-space: pre;",
		"word-break: normal;",
		"overflow-wrap: normal;",
		"overflow-x: auto;",
		"font-size: calc(13px * var(--env-comment-zoom, 1));",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected env comment block code display style %q, got %s", want, body)
		}
	}
	if strings.Contains(body, ".env-comment-block {\n  margin: 6px 0 0;\n  padding: 8px 10px;\n  border-radius: 12px;\n  background: var(--md-sys-color-surface-container);\n  border: 1px solid var(--md-sys-color-outline-variant);\n  color: var(--md-sys-color-on-surface-variant);\n  line-height: 1.55;\n  white-space: pre-wrap;") {
		t.Fatalf("expected env comment block to stop wrapping automatically, got %s", body)
	}
}

func TestAdminUIAppScriptBindsIndependentEnvCommentZoom(t *testing.T) {
	server := newAdminUITestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/_admin/assets/app.js", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected app script 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"envCommentZoom: loadEnvCommentZoom()",
		"admin-env-comment-zoom",
		"function bindEnvCommentBlock(block)",
		"envContainer.querySelectorAll('.env-comment-block').forEach(bindEnvCommentBlock)",
		"event.ctrlKey",
		"touchesInsideElement(event.touches, block)",
		"setEnvCommentZoom(state.envCommentZoom",
		"tabindex=\"0\"",
		"aria-label=\"环境变量说明\"",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected app script to contain env comment zoom behavior %q, got %s", want, body)
		}
	}
	if strings.Contains(body, "setEditorZoom(state.envCommentZoom") || strings.Contains(body, "setEnvCommentZoom(state.editorZoom") {
		t.Fatalf("expected env comment zoom to stay independent from editor zoom, got %s", body)
	}
}
