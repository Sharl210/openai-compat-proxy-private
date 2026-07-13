package diagnostics

import (
	"bytes"
	"io"
	"os"
	"testing"
)

func TestCaptureHeapProfileWithWriter_createsPrivateProfileWithoutForcingGC(t *testing.T) {
	parentDir := t.TempDir()
	writerCalled := false

	capture, err := captureHeapProfileWithWriter(parentDir, func(writer io.Writer, debug int) error {
		writerCalled = true
		if debug != 0 {
			t.Fatalf("expected binary heap profile, got debug=%d", debug)
		}
		_, err := writer.Write([]byte("heap profile"))
		return err
	})
	if err != nil {
		t.Fatalf("capture heap profile: %v", err)
	}
	if !writerCalled {
		t.Fatal("expected heap profile writer to be called")
	}

	dirInfo, err := os.Stat(capture.Directory)
	if err != nil {
		t.Fatalf("stat capture directory: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("expected private capture directory, got %o", dirInfo.Mode().Perm())
	}

	profileInfo, err := os.Stat(capture.Path)
	if err != nil {
		t.Fatalf("stat profile: %v", err)
	}
	if profileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("expected private heap profile, got %o", profileInfo.Mode().Perm())
	}
	if capture.Size != int64(len("heap profile")) {
		t.Fatalf("expected profile size %d, got %d", len("heap profile"), capture.Size)
	}
}

func TestCaptureHeapProfileWithWriter_removesPartialOutputOnWriteFailure(t *testing.T) {
	parentDir := t.TempDir()

	_, err := captureHeapProfileWithWriter(parentDir, func(writer io.Writer, _ int) error {
		if _, writeErr := writer.Write([]byte("partial heap profile")); writeErr != nil {
			return writeErr
		}
		return os.ErrInvalid
	})
	if err == nil {
		t.Fatal("expected heap profile write failure")
	}

	entries, err := os.ReadDir(parentDir)
	if err != nil {
		t.Fatalf("read capture parent directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected failed capture cleanup, found %#v", entries)
	}
}

func TestCaptureHeapProfile_writesBinaryRuntimeProfile(t *testing.T) {
	capture, err := CaptureHeapProfile()
	if err != nil {
		t.Fatalf("capture heap profile: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(capture.Directory); err != nil {
			t.Errorf("remove capture directory: %v", err)
		}
	})

	profile, err := os.ReadFile(capture.Path)
	if err != nil {
		t.Fatalf("read heap profile: %v", err)
	}
	if !bytes.HasPrefix(profile, []byte{0x1f, 0x8b}) {
		t.Fatalf("expected gzip-compressed binary heap profile, got prefix %x", profile[:min(8, len(profile))])
	}
}
