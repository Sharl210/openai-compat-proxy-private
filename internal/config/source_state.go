package config

import (
	"os"
	"path/filepath"
	"reflect"
	"time"
)

type runtimeSourceStamp struct {
	exists  bool
	mode    os.FileMode
	size    int64
	modTime time.Time
}

type runtimeSourceState struct {
	files map[string]runtimeSourceStamp
	dirs  map[string]runtimeSourceStamp
}

func runtimeSourceStateChanged(snapshot *RuntimeSnapshot) (bool, error) {
	current, err := captureRuntimeSourceState(snapshot)
	if err != nil {
		return false, err
	}
	return !reflect.DeepEqual(current, snapshot.sourceState), nil
}

func captureRuntimeSourceState(snapshot *RuntimeSnapshot) (runtimeSourceState, error) {
	filePaths := make([]string, 0, 1+len(snapshot.ProviderPathByID))
	filePaths = append(filePaths, snapshot.RootEnvPath)
	for _, path := range snapshot.ProviderPathByID {
		filePaths = append(filePaths, path)
	}
	for _, paths := range snapshot.PromptPathsByID {
		filePaths = append(filePaths, paths...)
	}
	files, err := captureRuntimeSourceStamps(filePaths)
	if err != nil {
		return runtimeSourceState{}, err
	}

	dirPaths := append([]string{snapshot.Config.ProvidersDir}, snapshot.PromptWatchDirs()...)
	dirs, err := captureRuntimeSourceStamps(dirPaths)
	if err != nil {
		return runtimeSourceState{}, err
	}
	return runtimeSourceState{files: files, dirs: dirs}, nil
}

func runtimeSourceStampFromInfo(info os.FileInfo) runtimeSourceStamp {
	return runtimeSourceStamp{
		exists:  true,
		mode:    info.Mode(),
		size:    info.Size(),
		modTime: info.ModTime(),
	}
}

func recordRuntimeSourceStamp(stamps map[string]runtimeSourceStamp, path string, stamp runtimeSourceStamp) {
	if _, exists := stamps[path]; exists {
		return
	}
	stamps[path] = stamp
}

func captureRuntimeSourceStamp(path string) (runtimeSourceStamp, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return runtimeSourceStamp{}, nil
		}
		return runtimeSourceStamp{}, err
	}
	return runtimeSourceStampFromInfo(info), nil
}

func captureRuntimeSourceStamps(paths []string) (map[string]runtimeSourceStamp, error) {
	stamps := make(map[string]runtimeSourceStamp, len(paths))
	if err := captureRuntimeSourceStampsInto(stamps, paths); err != nil {
		return nil, err
	}
	return stamps, nil
}

func captureRuntimeSourceStampsInto(stamps map[string]runtimeSourceStamp, paths []string) error {
	for _, path := range paths {
		if path == "" {
			continue
		}
		cleanPath := filepath.Clean(path)
		stamp, err := captureRuntimeSourceStamp(cleanPath)
		if err != nil {
			return err
		}
		recordRuntimeSourceStamp(stamps, cleanPath, stamp)
	}
	return nil
}
