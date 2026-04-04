package httpapi

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/errorsx"
)

const (
	adminSessionCookieName  = "openai_compat_admin_session"
	adminIndexPath          = "adminui_static/index.html"
	adminAssetsPrefix       = "/_admin/assets/"
	adminAPIPrefix          = "/_admin/api/"
	adminMaxTextFileSize    = 1 << 20
	adminLogTailSize        = 32 << 10
	adminSessionTTL         = 12 * time.Hour
	adminRememberTTL        = 30 * 24 * time.Hour
	adminIdleJobTimeout     = 10 * time.Minute
	adminJobStatusRunning   = "running"
	adminJobStatusFailed    = "failed"
	adminJobStatusSucceeded = "succeeded"
)

//go:embed adminui_static/*
var adminStaticFiles embed.FS

var errAdminUnauthorized = errors.New("admin unauthorized")
var errAdminCSRF = errors.New("admin csrf invalid")
var errAdminJobRunning = errors.New("admin job already running")

type adminUI struct {
	store        *config.RuntimeStore
	assets       fs.FS
	runner       adminActionRunner
	loginLimiter *adminLoginLimiter
	client       *http.Client
}

type adminActionRunner interface {
	Start(action string, label string) (*adminJob, error)
	Get(id string) (*adminJob, bool)
	Current() *adminJob
}

type adminJob struct {
	ID         string    `json:"id"`
	Action     string    `json:"action"`
	Label      string    `json:"label"`
	Status     string    `json:"status"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	ExitCode   int       `json:"exit_code"`
	Output     string    `json:"output"`
}

type adminLoginLimiter struct {
	mu        sync.Mutex
	stateByIP map[string]adminLoginState
}

type adminLoginState struct {
	Failures     int
	BlockedUntil time.Time
	LastFailure  time.Time
}

type adminSessionPayload struct {
	ExpiresAt int64  `json:"exp"`
	Nonce     string `json:"nonce"`
}

type adminFileEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	IsDir     bool   `json:"is_dir"`
	IsText    bool   `json:"is_text"`
	Editable  bool   `json:"editable"`
	Size      int64  `json:"size"`
	Modified  string `json:"modified"`
	IsSymlink bool   `json:"is_symlink"`
}

type adminEnvEntry struct {
	Key          string   `json:"key"`
	Value        string   `json:"value"`
	LeadingLines []string `json:"leading_lines"`
}

type adminValidationResult struct {
	HotReloadOK       bool   `json:"hot_reload_ok"`
	HotReloadError    string `json:"hot_reload_error,omitempty"`
	RestartOK         bool   `json:"restart_ok"`
	RestartError      string `json:"restart_error,omitempty"`
	ActiveRootVersion string `json:"active_root_version,omitempty"`
}

type adminCommandRunner struct {
	rootDir string
	mu      sync.Mutex
}

func newAdminUI(store *config.RuntimeStore) *adminUI {
	assets, _ := fs.Sub(adminStaticFiles, "adminui_static")
	ui := &adminUI{
		store:        store,
		assets:       assets,
		loginLimiter: &adminLoginLimiter{stateByIP: map[string]adminLoginState{}},
		client:       &http.Client{Timeout: 2 * time.Second},
	}
	ui.runner = &adminCommandRunner{rootDir: ui.rootDir()}
	return ui
}

func (a *adminUI) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/", allowMethods(a.handleIndex(), http.MethodGet))
	mux.Handle(adminAssetsPrefix, http.StripPrefix(adminAssetsPrefix, http.FileServer(http.FS(a.assets))))
	mux.HandleFunc("/_admin/api/bootstrap", allowMethods(a.handleBootstrap(), http.MethodGet))
	mux.HandleFunc("/_admin/api/login", allowMethods(a.handleLogin(), http.MethodPost))
	mux.HandleFunc("/_admin/api/logout", allowMethods(a.handleLogout(), http.MethodPost))
	mux.HandleFunc("/_admin/api/tree", allowMethods(a.handleTree(), http.MethodGet))
	mux.HandleFunc("/_admin/api/file", a.handleFile())
	mux.HandleFunc("/_admin/api/action", allowMethods(a.handleAction(), http.MethodPost))
	mux.HandleFunc("/_admin/api/jobs", allowMethods(a.handleJob(), http.MethodGet))
	mux.HandleFunc("/_admin/api/status", allowMethods(a.handleStatus(), http.MethodGet))
}

func (a *adminUI) matchesPath(path string) bool {
	return path == "/" || strings.HasPrefix(path, adminAssetsPrefix) || strings.HasPrefix(path, adminAPIPrefix)
}

func (a *adminUI) applyHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/css") && !strings.HasPrefix(w.Header().Get("Content-Type"), "application/javascript") {
		w.Header().Set("Cache-Control", "no-store")
	}
}

func (a *adminUI) handleIndex() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			errorsx.WriteJSON(w, http.StatusNotFound, "not_found", "route not found")
			return
		}
		content, err := fs.ReadFile(a.assets, "index.html")
		if err != nil {
			errorsx.WriteJSON(w, http.StatusInternalServerError, "admin_ui_unavailable", err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		errorsx.WriteRaw(w, http.StatusOK, "text/html; charset=utf-8", content)
	}
}

func (a *adminUI) handleBootstrap() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session, csrf, err := a.requireSession(r)
		if err != nil {
			errorsx.WriteJSON(w, http.StatusUnauthorized, "unauthorized", "admin login required")
			return
		}
		_ = session
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"authenticated": true,
			"csrf_token":    csrf,
			"root_name":     filepath.Base(a.rootDir()),
			"actions":       a.actionsPayload(),
			"validation":    a.evaluateValidation(),
			"status":        a.runtimeStatus(),
		})
	}
}

func (a *adminUI) handleStatus() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, err := a.requireSession(r); err != nil {
			errorsx.WriteJSON(w, http.StatusUnauthorized, "unauthorized", "admin login required")
			return
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"validation": a.evaluateValidation(),
			"status":     a.runtimeStatus(),
			"job":        a.runner.Current(),
		})
	}
}

func (a *adminUI) handleLogin() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		proxyKey := a.proxyKey()
		if proxyKey == "" {
			errorsx.WriteJSON(w, http.StatusServiceUnavailable, "admin_ui_disabled", "PROXY_API_KEY is required for admin ui")
			return
		}
		ip := clientIP(r)
		if blocked, retryAfter := a.loginLimiter.Check(ip, time.Now()); blocked {
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
			errorsx.WriteJSON(w, http.StatusTooManyRequests, "too_many_requests", "too many login attempts")
			return
		}
		var req struct {
			Password string `json:"password"`
			Remember bool   `json:"remember"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", "invalid json body")
			return
		}
		if !constantTimeEqual(req.Password, proxyKey) {
			a.loginLimiter.RecordFailure(ip, time.Now())
			errorsx.WriteJSON(w, http.StatusUnauthorized, "unauthorized", "invalid admin password")
			return
		}
		a.loginLimiter.RecordSuccess(ip)
		cookie, csrf, err := a.issueSessionCookie(req.Remember)
		if err != nil {
			errorsx.WriteJSON(w, http.StatusInternalServerError, "session_error", err.Error())
			return
		}
		cookie.Secure = isSecureRequest(r)
		http.SetCookie(w, cookie)
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"authenticated": true,
			"csrf_token":    csrf,
			"actions":       a.actionsPayload(),
		})
	}
}

func (a *adminUI) handleLogout() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: adminSessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: isSecureRequest(r)})
		writeAdminJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func (a *adminUI) handleTree() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, err := a.requireSession(r); err != nil {
			errorsx.WriteJSON(w, http.StatusUnauthorized, "unauthorized", "admin login required")
			return
		}
		rel := strings.TrimSpace(r.URL.Query().Get("path"))
		resolved, err := a.resolvePath(rel, true)
		if err != nil {
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_path", err.Error())
			return
		}
		entries, err := os.ReadDir(resolved)
		if err != nil {
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_path", err.Error())
			return
		}
		items := make([]adminFileEntry, 0, len(entries))
		modifiedAt := make(map[string]time.Time, len(entries))
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if !info.IsDir() && !a.isVisibleTreeFile(entry.Name()) {
				continue
			}
			itemRel := filepath.ToSlash(filepath.Join(rel, entry.Name()))
			mode := info.Mode()
			isSymlink := mode&os.ModeSymlink != 0
			isDir := info.IsDir()
			isText := false
			editable := false
			if !isDir && !isSymlink {
				if text, err := a.isTextFile(filepath.Join(resolved, entry.Name())); err == nil {
					isText = text
					editable = text
				}
			}
			items = append(items, adminFileEntry{
				Name:      entry.Name(),
				Path:      itemRel,
				IsDir:     isDir,
				IsText:    isText,
				Editable:  editable,
				Size:      info.Size(),
				Modified:  info.ModTime().Format(time.RFC3339),
				IsSymlink: isSymlink,
			})
			modifiedAt[itemRel] = info.ModTime()
		}
		if a.isLogDirectory(resolved) {
			sort.Slice(items, func(i, j int) bool {
				if items[i].IsDir != items[j].IsDir {
					return items[i].IsDir
				}
				left := modifiedAt[items[i].Path]
				right := modifiedAt[items[j].Path]
				if !left.Equal(right) {
					return left.After(right)
				}
				return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
			})
		} else {
			sort.Slice(items, func(i, j int) bool {
				if items[i].IsDir != items[j].IsDir {
					return items[i].IsDir
				}
				return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
			})
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"path":  filepath.ToSlash(rel),
			"items": items,
		})
	}
}

func (a *adminUI) isVisibleTreeFile(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	return lower == ".env" || strings.HasSuffix(lower, ".env") || strings.HasSuffix(lower, ".example") || strings.HasSuffix(lower, ".txt") || strings.HasSuffix(lower, ".json")
}

func (a *adminUI) isLogDirectory(resolved string) bool {
	snapshot := a.store.Active()
	if snapshot == nil {
		return false
	}
	logDir := strings.TrimSpace(snapshot.Config.LogFilePath)
	if logDir == "" {
		return false
	}
	logDirPath := logDir
	if !filepath.IsAbs(logDirPath) {
		logDirPath = filepath.Join(a.rootDir(), logDirPath)
	}
	return filepath.Clean(resolved) == filepath.Clean(logDirPath)
}

func (a *adminUI) handleFile() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			a.handleFileRead()(w, r)
		case http.MethodPost:
			a.handleFileCreate()(w, r)
		case http.MethodPatch:
			a.handleFileRename()(w, r)
		case http.MethodDelete:
			a.handleFileDelete()(w, r)
		case http.MethodPut:
			a.handleFileWrite()(w, r)
		default:
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost+", "+http.MethodPatch+", "+http.MethodPut+", "+http.MethodDelete)
			errorsx.WriteJSON(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
	}
}

func (a *adminUI) handleFileRead() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, err := a.requireSession(r); err != nil {
			errorsx.WriteJSON(w, http.StatusUnauthorized, "unauthorized", "admin login required")
			return
		}
		rel := strings.TrimSpace(r.URL.Query().Get("path"))
		resolved, err := a.resolvePath(rel, false)
		if err != nil {
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_path", err.Error())
			return
		}
		if info, err := os.Stat(resolved); err == nil {
			if info.Size() > adminMaxTextFileSize {
				errorsx.WriteJSON(w, http.StatusBadRequest, "file_too_large", "file exceeds admin ui size limit")
				return
			}
		}
		content, err := os.ReadFile(resolved)
		if err != nil {
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_path", err.Error())
			return
		}
		if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
			errorsx.WriteJSON(w, http.StatusBadRequest, "binary_file", "binary file is not editable in admin ui")
			return
		}
		text := string(content)
		if a.isEnvFile(rel) {
			entries, tail := parseAdminEnvText(text)
			writeAdminJSON(w, http.StatusOK, map[string]any{
				"path":        filepath.ToSlash(rel),
				"mode":        "env",
				"content":     text,
				"env_entries": entries,
				"tail_lines":  tail,
				"language":    detectAdminLanguage(rel),
			})
			return
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"path":     filepath.ToSlash(rel),
			"mode":     "text",
			"content":  text,
			"language": detectAdminLanguage(rel),
		})
	}
}

func (a *adminUI) handleFileWrite() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, err := a.requireSession(r); err != nil {
			errorsx.WriteJSON(w, http.StatusUnauthorized, "unauthorized", "admin login required")
			return
		}
		if err := a.requireCSRF(r); err != nil {
			errorsx.WriteJSON(w, http.StatusForbidden, "csrf_invalid", "missing or invalid csrf token")
			return
		}
		var req struct {
			Path       string          `json:"path"`
			Mode       string          `json:"mode"`
			Content    string          `json:"content"`
			EnvEntries []adminEnvEntry `json:"env_entries"`
			TailLines  []string        `json:"tail_lines"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", "invalid json body")
			return
		}
		resolved, err := a.resolvePath(req.Path, false)
		if err != nil {
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_path", err.Error())
			return
		}
		info, err := os.Lstat(resolved)
		if err != nil {
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_path", err.Error())
			return
		}
		if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_path", "only regular files can be edited")
			return
		}
		var content string
		if req.Mode == "env" {
			content = renderAdminEnvText(req.EnvEntries, req.TailLines)
		} else {
			content = req.Content
		}
		if !utf8.ValidString(content) {
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", "content must be valid utf-8")
			return
		}
		if err := os.WriteFile(resolved, []byte(content), info.Mode().Perm()); err != nil {
			errorsx.WriteJSON(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"path":       filepath.ToSlash(req.Path),
			"validation": a.evaluateValidation(),
		})
	}
}

func (a *adminUI) handleFileCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, err := a.requireSession(r); err != nil {
			errorsx.WriteJSON(w, http.StatusUnauthorized, "unauthorized", "admin login required")
			return
		}
		if err := a.requireCSRF(r); err != nil {
			errorsx.WriteJSON(w, http.StatusForbidden, "csrf_invalid", "missing or invalid csrf token")
			return
		}
		var req struct {
			Dir  string `json:"dir"`
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", "invalid json body")
			return
		}
		path, err := a.createEnvFromTemplate(req.Dir, req.Name)
		if err != nil {
			errorsx.WriteJSON(w, http.StatusBadRequest, "create_failed", err.Error())
			return
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"ok":   true,
			"path": filepath.ToSlash(path),
		})
	}
}

func (a *adminUI) handleFileRename() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, err := a.requireSession(r); err != nil {
			errorsx.WriteJSON(w, http.StatusUnauthorized, "unauthorized", "admin login required")
			return
		}
		if err := a.requireCSRF(r); err != nil {
			errorsx.WriteJSON(w, http.StatusForbidden, "csrf_invalid", "missing or invalid csrf token")
			return
		}
		var req struct {
			Path    string `json:"path"`
			NewName string `json:"new_name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", "invalid json body")
			return
		}
		newPath, err := a.renameAdminFile(req.Path, req.NewName)
		if err != nil {
			errorsx.WriteJSON(w, http.StatusBadRequest, "rename_failed", err.Error())
			return
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"old_path": filepath.ToSlash(req.Path),
			"path":     filepath.ToSlash(newPath),
		})
	}
}

func (a *adminUI) handleFileDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, err := a.requireSession(r); err != nil {
			errorsx.WriteJSON(w, http.StatusUnauthorized, "unauthorized", "admin login required")
			return
		}
		if err := a.requireCSRF(r); err != nil {
			errorsx.WriteJSON(w, http.StatusForbidden, "csrf_invalid", "missing or invalid csrf token")
			return
		}
		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", "invalid json body")
			return
		}
		if err := a.deleteAdminFile(req.Path); err != nil {
			errorsx.WriteJSON(w, http.StatusBadRequest, "delete_failed", err.Error())
			return
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"ok":   true,
			"path": filepath.ToSlash(req.Path),
		})
	}
}

func (a *adminUI) handleAction() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, err := a.requireSession(r); err != nil {
			errorsx.WriteJSON(w, http.StatusUnauthorized, "unauthorized", "admin login required")
			return
		}
		if err := a.requireCSRF(r); err != nil {
			errorsx.WriteJSON(w, http.StatusForbidden, "csrf_invalid", "missing or invalid csrf token")
			return
		}
		var req struct {
			Action string `json:"action"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", "invalid json body")
			return
		}
		label, ok := adminActionLabels()[req.Action]
		if !ok {
			errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", "unsupported admin action")
			return
		}
		job, err := a.runner.Start(req.Action, label)
		if err != nil {
			if errors.Is(err, errAdminJobRunning) {
				errorsx.WriteJSON(w, http.StatusConflict, "job_running", "another admin action is already running")
				return
			}
			errorsx.WriteJSON(w, http.StatusInternalServerError, "job_start_failed", err.Error())
			return
		}
		writeAdminJSON(w, http.StatusAccepted, map[string]any{
			"action": req.Action,
			"job":    job,
		})
	}
}

func (a *adminUI) handleJob() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, err := a.requireSession(r); err != nil {
			errorsx.WriteJSON(w, http.StatusUnauthorized, "unauthorized", "admin login required")
			return
		}
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			job := a.runner.Current()
			if job == nil {
				writeAdminJSON(w, http.StatusOK, map[string]any{"job": nil})
				return
			}
			writeAdminJSON(w, http.StatusOK, map[string]any{"job": job})
			return
		}
		job, ok := a.runner.Get(id)
		if !ok {
			errorsx.WriteJSON(w, http.StatusNotFound, "not_found", "job not found")
			return
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{"job": job})
	}
}

func (a *adminUI) proxyKey() string {
	snapshot := a.store.Active()
	if snapshot == nil {
		return ""
	}
	return strings.TrimSpace(snapshot.Config.ProxyAPIKey)
}

func (a *adminUI) rootDir() string {
	snapshot := a.store.Active()
	if snapshot == nil || strings.TrimSpace(snapshot.RootEnvPath) == "" {
		return ""
	}
	return filepath.Dir(snapshot.RootEnvPath)
}

func (a *adminUI) rootEnvPath() string {
	snapshot := a.store.Active()
	if snapshot == nil {
		return ""
	}
	return snapshot.RootEnvPath
}

func (a *adminUI) issueSessionCookie(remember bool) (*http.Cookie, string, error) {
	proxyKey := a.proxyKey()
	if proxyKey == "" {
		return nil, "", errors.New("proxy api key is empty")
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, "", err
	}
	ttl := adminSessionTTL
	if remember {
		ttl = adminRememberTTL
	}
	payload := adminSessionPayload{ExpiresAt: time.Now().Add(ttl).Unix(), Nonce: hex.EncodeToString(nonce)}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(rawPayload)
	signature := signAdminValue(proxyKey, encodedPayload)
	value := encodedPayload + "." + signature
	csrf := signAdminValue(proxyKey, encodedPayload+"|csrf")
	cookie := &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   false,
	}
	if remember {
		cookie.Expires = time.Now().Add(ttl)
		cookie.MaxAge = int(ttl.Seconds())
	}
	return cookie, csrf, nil
}

func (a *adminUI) requireSession(r *http.Request) (adminSessionPayload, string, error) {
	proxyKey := a.proxyKey()
	if proxyKey == "" {
		return adminSessionPayload{}, "", errAdminUnauthorized
	}
	cookie, err := r.Cookie(adminSessionCookieName)
	if err != nil {
		return adminSessionPayload{}, "", errAdminUnauthorized
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return adminSessionPayload{}, "", errAdminUnauthorized
	}
	encodedPayload, signature := parts[0], parts[1]
	if !constantTimeEqual(signature, signAdminValue(proxyKey, encodedPayload)) {
		return adminSessionPayload{}, "", errAdminUnauthorized
	}
	rawPayload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return adminSessionPayload{}, "", errAdminUnauthorized
	}
	var payload adminSessionPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return adminSessionPayload{}, "", errAdminUnauthorized
	}
	if time.Now().Unix() > payload.ExpiresAt {
		return adminSessionPayload{}, "", errAdminUnauthorized
	}
	csrf := signAdminValue(proxyKey, encodedPayload+"|csrf")
	return payload, csrf, nil
}

func (a *adminUI) requireCSRF(r *http.Request) error {
	_, csrf, err := a.requireSession(r)
	if err != nil {
		return err
	}
	if !constantTimeEqual(strings.TrimSpace(r.Header.Get("X-Admin-CSRF")), csrf) {
		return errAdminCSRF
	}
	return nil
}

func (a *adminUI) resolvePath(rel string, wantDir bool) (string, error) {
	root := a.rootDir()
	if root == "" {
		return "", errors.New("admin root unavailable")
	}
	cleanRel := strings.TrimPrefix(filepath.Clean("/"+rel), "/")
	if cleanRel == "." {
		cleanRel = ""
	}
	if strings.HasPrefix(cleanRel, "../") || cleanRel == ".." {
		return "", errors.New("path escapes project root")
	}
	candidate := filepath.Join(root, cleanRel)
	info, err := os.Lstat(candidate)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("symlink targets are not editable from admin ui")
	}
	if wantDir && !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	if !wantDir && info.IsDir() {
		return "", errors.New("path is a directory")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	resolvedPath, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	relToRoot, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil {
		return "", err
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes project root")
	}
	return resolvedPath, nil
}

func (a *adminUI) createEnvFromTemplate(dirRel string, name string) (string, error) {
	cleanName, err := validateAdminFileName(name)
	if err != nil {
		return "", err
	}
	resolvedDir, err := a.resolvePath(dirRel, true)
	if err != nil {
		return "", err
	}
	if !a.canCreateEnvInDir(resolvedDir) {
		return "", errors.New("new env is only allowed in project root or providers root")
	}
	templatePath := filepath.Join(resolvedDir, ".env.example")
	templateContent, err := os.ReadFile(templatePath)
	if err != nil {
		return "", err
	}
	content := string(templateContent)
	targetName := cleanName + ".env"
	targetPath := filepath.Join(resolvedDir, targetName)
	if _, err := os.Stat(targetPath); err == nil {
		return "", errors.New("target file already exists")
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if a.isProvidersDirectory(resolvedDir) {
		content = rewriteProviderID(content, cleanName)
	}
	if err := os.WriteFile(targetPath, []byte(content), 0o644); err != nil {
		return "", err
	}
	if dirRel == "" {
		return targetName, nil
	}
	return filepath.ToSlash(filepath.Join(dirRel, targetName)), nil
}

func (a *adminUI) renameAdminFile(path string, newName string) (string, error) {
	cleanName, err := validateAdminFileName(newName)
	if err != nil {
		return "", err
	}
	resolved, err := a.resolvePath(path, false)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return "", err
	}
	if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("only regular files can be renamed")
	}
	newPath := filepath.Join(filepath.Dir(resolved), cleanName)
	if _, err := os.Stat(newPath); err == nil {
		return "", errors.New("target file already exists")
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if a.isProviderEnvFile(resolved) && strings.HasSuffix(cleanName, ".env") {
		content, err := os.ReadFile(resolved)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(resolved, []byte(rewriteProviderID(string(content), strings.TrimSuffix(cleanName, ".env"))), info.Mode().Perm()); err != nil {
			return "", err
		}
	}
	if err := os.Rename(resolved, newPath); err != nil {
		return "", err
	}
	root := a.rootDir()
	rel, err := filepath.Rel(root, newPath)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

func (a *adminUI) deleteAdminFile(path string) error {
	resolved, err := a.resolvePath(path, false)
	if err != nil {
		return err
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return err
	}
	if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("only regular files can be deleted")
	}
	return os.Remove(resolved)
}

func (a *adminUI) canCreateEnvInDir(resolved string) bool {
	return filepath.Clean(resolved) == filepath.Clean(a.rootDir()) || a.isProvidersDirectory(resolved)
}

func (a *adminUI) isProvidersDirectory(resolved string) bool {
	snapshot := a.store.Active()
	if snapshot == nil {
		return false
	}
	return filepath.Clean(resolved) == filepath.Clean(snapshot.Config.ProvidersDir)
}

func (a *adminUI) isProviderEnvFile(resolved string) bool {
	if !strings.HasSuffix(strings.ToLower(resolved), ".env") {
		return false
	}
	return filepath.Clean(filepath.Dir(resolved)) == filepath.Clean(a.providersDir())
}

func (a *adminUI) providersDir() string {
	snapshot := a.store.Active()
	if snapshot == nil {
		return ""
	}
	return snapshot.Config.ProvidersDir
}

func validateAdminFileName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", errors.New("file name is required")
	}
	if strings.ContainsAny(trimmed, `/\\`) {
		return "", errors.New("file name must not contain path separators")
	}
	return trimmed, nil
}

func rewriteProviderID(content string, providerID string) string {
	lines := strings.Split(content, "\n")
	replaced := false
	for index, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "PROVIDER_ID=") {
			lines[index] = "PROVIDER_ID=" + providerID
			replaced = true
			break
		}
	}
	if !replaced {
		lines = append([]string{"PROVIDER_ID=" + providerID}, lines...)
	}
	return strings.Join(lines, "\n")
}

func (a *adminUI) isTextFile(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if info.IsDir() || info.Size() > adminMaxTextFileSize {
		return false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return utf8.Valid(data) && bytes.IndexByte(data, 0) < 0, nil
}

func (a *adminUI) evaluateValidation() adminValidationResult {
	result := adminValidationResult{}
	if snapshot := a.store.Active(); snapshot != nil {
		result.ActiveRootVersion = snapshot.RootEnvVersion
	}
	if err := a.store.Refresh(); err != nil {
		result.HotReloadError = err.Error()
	} else {
		result.HotReloadOK = true
	}
	if _, err := config.BuildRuntimeSnapshot(a.rootEnvPath()); err != nil {
		result.RestartError = err.Error()
	} else {
		result.RestartOK = true
	}
	return result
}

func (a *adminUI) runtimeStatus() map[string]any {
	snapshot := a.store.Active()
	status := map[string]any{
		"root_dir":             a.rootDir(),
		"listen_addr":          "",
		"health_ok":            false,
		"proxy_key_configured": a.proxyKey() != "",
		"pid":                  "",
		"log_dir":              "",
	}
	if snapshot == nil {
		return status
	}
	status["listen_addr"] = snapshot.Config.ListenAddr
	status["health_ok"] = a.checkHealth(snapshot.Config.ListenAddr)
	status["log_dir"] = strings.TrimSpace(snapshot.Config.LogFilePath)
	if pidBytes, err := os.ReadFile(filepath.Join(a.rootDir(), ".proxy.pid")); err == nil {
		status["pid"] = strings.TrimSpace(string(pidBytes))
	}
	if current := a.runner.Current(); current != nil {
		status["job"] = current
	}
	return status
}

func (a *adminUI) checkHealth(listenAddr string) bool {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		if strings.HasPrefix(listenAddr, ":") {
			host = "127.0.0.1"
			port = strings.TrimPrefix(listenAddr, ":")
		} else {
			return false
		}
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	resp, err := a.client.Get((&urlBuilder{Host: host, Port: port}).healthURL())
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

type urlBuilder struct{ Host, Port string }

func (u *urlBuilder) healthURL() string {
	if strings.Contains(u.Host, ":") {
		return fmt.Sprintf("http://[%s]:%s/healthz", u.Host, u.Port)
	}
	return fmt.Sprintf("http://%s:%s/healthz", u.Host, u.Port)
}

func signAdminValue(secret, value string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func constantTimeEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func clientIP(r *http.Request) string {
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		if first, _, _ := strings.Cut(forwarded, ","); strings.TrimSpace(first) != "" {
			return strings.TrimSpace(first)
		}
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (l *adminLoginLimiter) Check(ip string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.stateByIP[ip]
	if now.After(state.BlockedUntil) {
		state.BlockedUntil = time.Time{}
	}
	if state.BlockedUntil.IsZero() {
		return false, 0
	}
	return true, time.Until(state.BlockedUntil).Round(time.Second)
}

func (l *adminLoginLimiter) RecordFailure(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.stateByIP[ip]
	if now.Sub(state.LastFailure) > 10*time.Minute {
		state.Failures = 0
	}
	state.Failures++
	state.LastFailure = now
	if state.Failures >= 5 {
		state.BlockedUntil = now.Add(10 * time.Minute)
	}
	l.stateByIP[ip] = state
}

func (l *adminLoginLimiter) RecordSuccess(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.stateByIP, ip)
}

func parseAdminEnvText(text string) ([]adminEnvEntry, []string) {
	lines := strings.Split(text, "\n")
	entries := make([]adminEnvEntry, 0)
	leading := make([]string, 0)
	for idx, line := range lines {
		if idx == len(lines)-1 && line == "" {
			break
		}
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" || !strings.Contains(line, "=") {
			leading = append(leading, line)
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		entries = append(entries, adminEnvEntry{
			Key:          strings.TrimSpace(key),
			Value:        value,
			LeadingLines: append([]string(nil), leading...),
		})
		leading = leading[:0]
	}
	return entries, append([]string(nil), leading...)
}

func renderAdminEnvText(entries []adminEnvEntry, tail []string) string {
	lines := make([]string, 0)
	for _, entry := range entries {
		lines = append(lines, entry.LeadingLines...)
		lines = append(lines, fmt.Sprintf("%s=%s", strings.TrimSpace(entry.Key), entry.Value))
	}
	lines = append(lines, tail...)
	return strings.Join(lines, "\n") + "\n"
}

func detectAdminLanguage(rel string) string {
	base := filepath.Base(rel)
	if strings.HasPrefix(base, ".env") || strings.HasSuffix(base, ".env") {
		return "env"
	}
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".md":
		return "markdown"
	case ".json":
		return "json"
	case ".go":
		return "go"
	case ".sh":
		return "shell"
	case ".yml", ".yaml":
		return "yaml"
	default:
		return "text"
	}
}

func (a *adminUI) isEnvFile(rel string) bool {
	base := filepath.Base(rel)
	return base == ".env" || strings.HasSuffix(base, ".env")
}

func readAdminTail(path string, limit int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	start := int64(0)
	if info.Size() > limit {
		start = info.Size() - limit
	}
	buf := make([]byte, info.Size()-start)
	if _, err := file.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return string(buf), nil
}

func adminActionLabels() map[string]string {
	return map[string]string{
		"deploy":    "部署",
		"restart":   "重启",
		"stop":      "停止",
		"uninstall": "卸载",
	}
}

func (a *adminUI) actionsPayload() []map[string]string {
	labels := adminActionLabels()
	keys := []string{"deploy", "restart", "stop", "uninstall"}
	payload := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		payload = append(payload, map[string]string{"action": key, "label": labels[key]})
	}
	return payload
}

func writeAdminJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func (r *adminCommandRunner) Start(action string, label string) (*adminJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current := r.Current(); current != nil && current.Status == adminJobStatusRunning {
		return nil, errAdminJobRunning
	}
	if err := os.MkdirAll(r.jobsDir(), 0o755); err != nil {
		return nil, err
	}
	r.cleanupExpiredJobsLocked(time.Now())
	job := &adminJob{
		ID:        fmt.Sprintf("job-%d", time.Now().UnixNano()),
		Action:    action,
		Label:     label,
		Status:    adminJobStatusRunning,
		StartedAt: time.Now().UTC(),
	}
	if err := r.writeJob(job); err != nil {
		return nil, err
	}
	if err := os.WriteFile(r.currentJobPath(), []byte(job.ID), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(r.jobLogPath(job.ID), nil, 0o644); err != nil {
		return nil, err
	}
	script, ok := adminScriptPaths(r.rootDir)[action]
	if !ok {
		job.Status = adminJobStatusFailed
		job.ExitCode = 1
		job.FinishedAt = time.Now().UTC()
		job.Output = "unsupported action"
		_ = r.writeJob(job)
		return cloneAdminJob(job), nil
	}
	if err := r.writeWrapperScript(job, script); err != nil {
		return nil, err
	}
	cmd := exec.Command("bash", r.jobScriptPath(job.ID))
	cmd.Dir = r.rootDir
	cmd.Stdin = nil
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer devNull.Close()
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	_ = cmd.Process.Release()
	return cloneAdminJob(job), nil
}

func adminScriptPaths(rootDir string) map[string]string {
	return map[string]string{
		"deploy":    filepath.Join(rootDir, "scripts", "deploy-linux.sh"),
		"restart":   filepath.Join(rootDir, "scripts", "restart-linux.sh"),
		"stop":      filepath.Join(rootDir, "scripts", "stop-linux.sh"),
		"uninstall": filepath.Join(rootDir, "scripts", "uninstall-linux.sh"),
	}
}

func (r *adminCommandRunner) jobsDir() string {
	return filepath.Join(r.rootDir, ".admin-ui", "jobs")
}

func (r *adminCommandRunner) jobJSONPath(id string) string {
	return filepath.Join(r.jobsDir(), id+".json")
}

func (r *adminCommandRunner) jobLogPath(id string) string {
	return filepath.Join(r.jobsDir(), id+".log")
}

func (r *adminCommandRunner) jobScriptPath(id string) string {
	return filepath.Join(r.jobsDir(), id+".runner.sh")
}

func (r *adminCommandRunner) currentJobPath() string {
	return filepath.Join(r.jobsDir(), "current-job")
}

func (r *adminCommandRunner) writeJob(job *adminJob) error {
	payload, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return os.WriteFile(r.jobJSONPath(job.ID), payload, 0o644)
}

func (r *adminCommandRunner) readJob(id string) (*adminJob, error) {
	payload, err := os.ReadFile(r.jobJSONPath(id))
	if err != nil {
		return nil, err
	}
	var job adminJob
	if err := json.Unmarshal(payload, &job); err != nil {
		return nil, err
	}
	if tail, err := readAdminTail(r.jobLogPath(id), adminLogTailSize); err == nil {
		job.Output = tail
	}
	return &job, nil
}

func (r *adminCommandRunner) writeWrapperScript(job *adminJob, scriptPath string) error {
	jsonPath := r.jobJSONPath(job.ID)
	logPath := r.jobLogPath(job.ID)
	currentPath := r.currentJobPath()
	jobJSON, err := json.Marshal(job)
	if err != nil {
		return err
	}
	initial := strconv.Quote(string(jobJSON))
	statusSuccess := strconv.Quote(adminJobStatusSucceeded)
	statusFailed := strconv.Quote(adminJobStatusFailed)
	id := strconv.Quote(job.ID)
	action := strconv.Quote(job.Action)
	label := strconv.Quote(job.Label)
	rootDir := strconv.Quote(r.rootDir)
	logFile := strconv.Quote(logPath)
	jsonFile := strconv.Quote(jsonPath)
	currentFile := strconv.Quote(currentPath)
	scriptFile := strconv.Quote(scriptPath)
	content := strings.Join([]string{
		"#!/usr/bin/env bash",
		"set +e",
		"cd " + rootDir,
		"printf %s " + initial + " > " + jsonFile,
		"bash " + scriptFile + " >> " + logFile + " 2>&1",
		"exit_code=$?",
		"finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)",
		"status=" + statusSuccess,
		"if [ \"$exit_code\" -ne 0 ]; then status=" + statusFailed + "; fi",
		"cat > " + jsonFile + " <<EOF",
		"{\"id\":" + id + ",\"action\":" + action + ",\"label\":" + label + ",\"status\":\"${status}\",\"started_at\":\"" + job.StartedAt.Format(time.RFC3339) + "\",\"finished_at\":\"${finished_at}\",\"exit_code\":${exit_code},\"output\":\"\"}",
		"EOF",
		"if [ -f " + currentFile + " ] && [ \"$(cat " + currentFile + ")\" = " + id + " ]; then printf %s " + id + " > " + currentFile + "; fi",
	}, "\n") + "\n"
	if err := os.WriteFile(r.jobScriptPath(job.ID), []byte(content), 0o755); err != nil {
		return err
	}
	return nil
}

func (r *adminCommandRunner) cleanupExpiredJobsLocked(now time.Time) {
	entries, err := os.ReadDir(r.jobsDir())
	if err != nil {
		return
	}
	cutoff := now.Add(-adminIdleJobTimeout)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || entry.Name() == "current-job" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		job, err := r.readJob(id)
		if err != nil {
			continue
		}
		if job.Status == adminJobStatusRunning || job.FinishedAt.IsZero() || job.FinishedAt.After(cutoff) {
			continue
		}
		_ = os.Remove(r.jobJSONPath(id))
		_ = os.Remove(r.jobLogPath(id))
		_ = os.Remove(r.jobScriptPath(id))
	}
}

func (r *adminCommandRunner) Get(id string) (*adminJob, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, err := r.readJob(id)
	if err != nil {
		return nil, false
	}
	return cloneAdminJob(job), true
}

func (r *adminCommandRunner) Current() *adminJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, err := os.ReadFile(r.currentJobPath())
	if err != nil || strings.TrimSpace(string(id)) == "" {
		return nil
	}
	job, err := r.readJob(strings.TrimSpace(string(id)))
	if err != nil {
		return nil
	}
	return cloneAdminJob(job)
}

func cloneAdminJob(job *adminJob) *adminJob {
	if job == nil {
		return nil
	}
	clone := *job
	return &clone
}
