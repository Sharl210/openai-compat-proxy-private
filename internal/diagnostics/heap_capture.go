package diagnostics

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/pprof"
)

type HeapProfileCapture struct {
	Directory string
	Path      string
	Size      int64
}

type heapProfileWriter func(io.Writer, int) error
type heapCaptureLogFunc func(string, ...any)

func CaptureHeapProfile() (HeapProfileCapture, error) {
	profile := pprof.Lookup("heap")
	if profile == nil {
		return HeapProfileCapture{}, fmt.Errorf("heap profile is unavailable")
	}
	return captureHeapProfileWithWriter(os.TempDir(), profile.WriteTo)
}

func captureHeapProfileWithWriter(parentDir string, writeProfile heapProfileWriter) (_ HeapProfileCapture, returnErr error) {
	if writeProfile == nil {
		return HeapProfileCapture{}, fmt.Errorf("heap profile writer is required")
	}

	dir, err := os.MkdirTemp(parentDir, "openai-compat-proxy-heap-")
	if err != nil {
		return HeapProfileCapture{}, fmt.Errorf("create heap profile directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return HeapProfileCapture{}, fmt.Errorf("make heap profile directory private: %w", err)
	}

	completed := false
	defer func() {
		if !completed {
			_ = os.RemoveAll(dir)
		}
	}()

	path := filepath.Join(dir, "heap.pb.gz")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return HeapProfileCapture{}, fmt.Errorf("create heap profile: %w", err)
	}
	defer func() {
		if file != nil {
			_ = file.Close()
		}
	}()

	if err := writeProfile(file, 0); err != nil {
		return HeapProfileCapture{}, fmt.Errorf("write heap profile: %w", err)
	}
	if err := file.Sync(); err != nil {
		return HeapProfileCapture{}, fmt.Errorf("sync heap profile: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		return HeapProfileCapture{}, fmt.Errorf("stat heap profile: %w", err)
	}
	if err := file.Close(); err != nil {
		return HeapProfileCapture{}, fmt.Errorf("close heap profile: %w", err)
	}
	file = nil

	completed = true
	return HeapProfileCapture{Directory: dir, Path: path, Size: info.Size()}, nil
}
