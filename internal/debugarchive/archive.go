package debugarchive

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"openai-compat-proxy/internal/model"
)

type RawEventEnvelope struct {
	EventName string          `json:"event_name"`
	Raw       json.RawMessage `json:"raw"`
}

type FinalSnapshot struct {
	StatusCode int            `json:"status_code"`
	Response   map[string]any `json:"response,omitempty"`
	Error      map[string]any `json:"error,omitempty"`
}

type ArchiveWriter struct {
	baseDir   string
	rootDir   string
	requestID string
	maxDirs   int

	reqFile       *os.File
	rawFile       *os.File
	canonicalFile *os.File
	finalFile     *os.File

	mu sync.Mutex
}

var pruneMu sync.Mutex

func NewArchiveWriter(rootDir, requestID string) *ArchiveWriter {
	return NewArchiveWriterWithRetention(rootDir, requestID, 0)
}

func NewArchiveWriterWithRetention(rootDir, requestID string, maxDirs int) *ArchiveWriter {
	reqDir := filepath.Join(rootDir, requestID)
	if err := os.MkdirAll(reqDir, 0755); err != nil {
		return nil
	}

	w := &ArchiveWriter{
		baseDir:   reqDir,
		rootDir:   rootDir,
		requestID: requestID,
		maxDirs:   maxDirs,
	}

	w.reqFile = w.openFile("request.ndjson")
	w.rawFile = w.openFile("raw.ndjson")
	w.canonicalFile = w.openFile("canonical.ndjson")
	w.finalFile = w.openFile("final.ndjson")

	return w
}

func (w *ArchiveWriter) openFile(name string) *os.File {
	f, err := os.OpenFile(filepath.Join(w.baseDir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil
	}
	return f
}

func (w *ArchiveWriter) WriteRequest(payload map[string]any) error {
	return w.writeJSON(w.reqFile, payload)
}

func (w *ArchiveWriter) WriteRawEvent(event RawEventEnvelope) error {
	return w.writeJSON(w.rawFile, event)
}

func (w *ArchiveWriter) WriteCanonicalEvent(event model.CanonicalEvent) error {
	return w.writeJSON(w.canonicalFile, event)
}

func (w *ArchiveWriter) WriteFinalSnapshot(snapshot FinalSnapshot) error {
	return w.writeJSON(w.finalFile, snapshot)
}

func (w *ArchiveWriter) writeJSON(f *os.File, v any) error {
	if f == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err = f.Write(append(data, '\n'))
	return err
}

func (w *ArchiveWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.reqFile != nil {
		if err := w.reqFile.Close(); err != nil {
			return err
		}
		w.reqFile = nil
	}
	if w.rawFile != nil {
		if err := w.rawFile.Close(); err != nil {
			return err
		}
		w.rawFile = nil
	}
	if w.canonicalFile != nil {
		if err := w.canonicalFile.Close(); err != nil {
			return err
		}
		w.canonicalFile = nil
	}
	if w.finalFile != nil {
		if err := w.finalFile.Close(); err != nil {
			return err
		}
		w.finalFile = nil
	}
	w.cleanupOldRequestDirs()
	return nil
}

func (w *ArchiveWriter) cleanupOldRequestDirs() {
	if w == nil || w.rootDir == "" || w.maxDirs <= 0 {
		return
	}
	pruneMu.Lock()
	defer pruneMu.Unlock()
	entries, err := os.ReadDir(w.rootDir)
	if err != nil {
		return
	}
	type dirInfo struct {
		name    string
		modTime int64
	}
	var dirs []dirInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		dirs = append(dirs, dirInfo{name: entry.Name(), modTime: info.ModTime().UnixNano()})
	}
	if len(dirs) <= w.maxDirs {
		return
	}
	sort.Slice(dirs, func(i, j int) bool {
		if dirs[i].modTime == dirs[j].modTime {
			return dirs[i].name < dirs[j].name
		}
		return dirs[i].modTime < dirs[j].modTime
	})
	for _, dir := range dirs[:len(dirs)-w.maxDirs] {
		_ = os.RemoveAll(filepath.Join(w.rootDir, dir.name))
	}
}
