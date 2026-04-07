package config

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

type RuntimeStore struct {
	rootEnvPath string
	active      atomic.Pointer[RuntimeSnapshot]
	mu          sync.Mutex
}

func NewRuntimeStore(rootEnvPath string) (*RuntimeStore, error) {
	snapshot, err := BuildRuntimeSnapshot(rootEnvPath)
	if err != nil {
		return nil, err
	}
	store := &RuntimeStore{rootEnvPath: rootEnvPath}
	store.active.Store(snapshot)
	return store, nil
}

func NewStaticRuntimeStore(cfg Config) *RuntimeStore {
	store := &RuntimeStore{}
	ids, owners, visible, _ := buildDefaultOverlayModelIndex(cfg)
	store.active.Store(&RuntimeSnapshot{Config: cfg, DefaultProviderIDs: ids, DefaultModelOwners: owners, DefaultVisibleModels: visible, ProviderVersionByID: map[string]string{}, ProviderPathByID: map[string]string{}, PromptPathsByID: map[string][]string{}, providerMTimeByID: map[string]time.Time{}})
	return store
}

func (s *RuntimeStore) Active() *RuntimeSnapshot {
	if s == nil {
		return nil
	}
	return s.active.Load()
}

func (s *RuntimeStore) Refresh() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.active.Load()
	if current == nil {
		snapshot, err := BuildRuntimeSnapshot(s.rootEnvPath)
		if err != nil {
			return err
		}
		s.active.Store(snapshot)
		return nil
	}
	snapshot, err := BuildRuntimeSnapshotForRefresh(s.rootEnvPath, current.Config)
	if err != nil {
		return err
	}
	hotChanged := !snapshot.Config.hotReloadableRootEquals(current.Config)
	if !hotChanged {
		snapshot.RootEnvMTime = current.RootEnvMTime
		snapshot.RootEnvVersion = current.RootEnvVersion
	}
	s.active.Store(snapshot)
	return nil
}

func (s *RuntimeStore) StartPolling(ctx context.Context, interval time.Duration) {
	if s == nil || interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.Refresh()
			}
		}
	}()
}

func (s *RuntimeStore) StartWatching(ctx context.Context, debounce time.Duration, resyncInterval time.Duration) error {
	if s == nil {
		return nil
	}
	if debounce <= 0 {
		debounce = 250 * time.Millisecond
	}
	if resyncInterval <= 0 {
		resyncInterval = 5 * time.Second
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	watchDirs, _, providersDir, promptPaths, err := s.watchDirs()
	if err != nil {
		_ = watcher.Close()
		return err
	}
	tracked := map[string]struct{}{}
	for _, path := range watchDirs {
		if err := addWatch(watcher, tracked, path); err != nil {
			_ = watcher.Close()
			return err
		}
	}

	go func() {
		defer watcher.Close()
		var (
			debounceTimer *time.Timer
			debounceC     <-chan time.Time
		)
		resyncTicker := time.NewTicker(resyncInterval)
		defer resyncTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				if debounceTimer != nil {
					debounceTimer.Stop()
				}
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if isWatchDirRemoved(event, watchDirs) {
					delete(tracked, event.Name)
				}
				if !shouldRefreshForEvent(event, s.rootEnvPath, providersDir, promptPaths) {
					continue
				}
				debounceTimer, debounceC = resetDebounceTimer(debounceTimer, debounce, debounceC)
			case <-debounceC:
				_ = s.Refresh()
				watchDirs, _, providersDir, promptPaths, _ = s.ensureWatchDirs(watcher, tracked)
				debounceTimer = nil
				debounceC = nil
			case <-resyncTicker.C:
				watchDirs, _, providersDir, promptPaths, _ = s.ensureWatchDirs(watcher, tracked)
				_ = s.Refresh()
			case _, ok := <-watcher.Errors:
				if !ok {
					return
				}
				watchDirs, _, providersDir, promptPaths, _ = s.ensureWatchDirs(watcher, tracked)
				_ = s.Refresh()
			}
		}
	}()
	return nil
}

func (s *RuntimeStore) watchDirs() ([]string, string, string, []string, error) {
	snapshot := s.Active()
	if snapshot == nil {
		return nil, "", "", nil, ErrInvalidConfig("runtime config unavailable")
	}
	rootDir := filepath.Dir(snapshot.RootEnvPath)
	providersDir := snapshot.Config.ProvidersDir
	if providersDir == "" {
		return nil, "", "", nil, ErrInvalidConfig("providers dir is required")
	}
	promptPaths := flattenPromptPaths(snapshot.PromptPathsByID)
	paths := []string{rootDir, providersDir}
	paths = append(paths, snapshot.PromptWatchDirs()...)
	return dedupePaths(paths), rootDir, providersDir, promptPaths, nil
}

func (s *RuntimeStore) ensureWatchDirs(watcher *fsnotify.Watcher, tracked map[string]struct{}) ([]string, string, string, []string, error) {
	watchDirs, rootDir, providersDir, promptPaths, err := s.watchDirs()
	if err != nil {
		return nil, "", "", nil, err
	}
	for _, path := range watchDirs {
		if err := addWatch(watcher, tracked, path); err != nil {
			return watchDirs, rootDir, providersDir, promptPaths, err
		}
	}
	return watchDirs, rootDir, providersDir, promptPaths, nil
}

func addWatch(watcher *fsnotify.Watcher, tracked map[string]struct{}, path string) error {
	if _, ok := tracked[path]; ok {
		return nil
	}
	if err := watcher.Add(path); err != nil {
		if isPathMissing(err) {
			return nil
		}
		return err
	}
	tracked[path] = struct{}{}
	return nil
}

func resetDebounceTimer(timer *time.Timer, delay time.Duration, current <-chan time.Time) (*time.Timer, <-chan time.Time) {
	if timer == nil {
		timer = time.NewTimer(delay)
		return timer, timer.C
	}
	if !timer.Stop() {
		select {
		case <-current:
		default:
		}
	}
	timer.Reset(delay)
	return timer, timer.C
}

func shouldRefreshForEvent(event fsnotify.Event, rootEnvPath string, providersDir string, promptPaths []string) bool {
	if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) == 0 {
		return false
	}
	cleanName := filepath.Clean(event.Name)
	if cleanName == filepath.Clean(rootEnvPath) {
		return true
	}
	cleanProvidersDir := filepath.Clean(providersDir)
	for _, promptPath := range promptPaths {
		if cleanName == filepath.Clean(promptPath) {
			return true
		}
	}
	if filepath.Dir(cleanName) != cleanProvidersDir {
		return false
	}
	base := filepath.Base(cleanName)
	if strings.HasSuffix(base, ".env") && !strings.HasSuffix(base, ".env.example") {
		return true
	}
	return false
}

func isWatchDirRemoved(event fsnotify.Event, watchDirs []string) bool {
	if event.Op&(fsnotify.Remove|fsnotify.Rename) == 0 {
		return false
	}
	name := filepath.Clean(event.Name)
	for _, dir := range watchDirs {
		if name == filepath.Clean(dir) {
			return true
		}
	}
	return false
}

func flattenPromptPaths(pathsByID map[string][]string) []string {
	flat := make([]string, 0)
	for _, paths := range pathsByID {
		flat = append(flat, paths...)
	}
	return dedupePaths(flat)
}

func dedupePaths(paths []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		cleaned := filepath.Clean(path)
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		result = append(result, cleaned)
	}
	return result
}

func isPathMissing(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "no such file or directory")
}
