package debugarchive

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"openai-compat-proxy/internal/model"
)

type RawEventEnvelope struct {
	EventName string          `json:"event_name"`
	Raw       json.RawMessage `json:"raw"`
}

type FinalSnapshot struct {
	StatusCode int                `json:"status_code"`
	Response   map[string]any     `json:"response,omitempty"`
	Error      map[string]any     `json:"error,omitempty"`
}

type ArchiveWriter struct {
	baseDir   string
	requestID string

	reqFile       *os.File
	rawFile       *os.File
	canonicalFile *os.File
	finalFile     *os.File

	mu sync.Mutex
}

func NewArchiveWriter(rootDir, requestID string) *ArchiveWriter {
	reqDir := filepath.Join(rootDir, requestID)
	if err := os.MkdirAll(reqDir, 0755); err != nil {
		return nil
	}

	w := &ArchiveWriter{
		baseDir:   reqDir,
		requestID: requestID,
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

	var errs []error
	if w.reqFile != nil {
		errs = append(errs, w.reqFile.Close())
		w.reqFile = nil
	}
	if w.rawFile != nil {
		errs = append(errs, w.rawFile.Close())
		w.rawFile = nil
	}
	if w.canonicalFile != nil {
		errs = append(errs, w.canonicalFile.Close())
		w.canonicalFile = nil
	}
	if w.finalFile != nil {
		errs = append(errs, w.finalFile.Close())
		w.finalFile = nil
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}
