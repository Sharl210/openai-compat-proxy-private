package debugarchive

import (
	"context"
	"os"
	"strings"
)

const EnvRootDir = "OPENAI_COMPAT_DEBUG_ARCHIVE_DIR"

type contextKey struct{}

func WithArchiveWriter(ctx context.Context, writer *ArchiveWriter) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, contextKey{}, writer)
}

func ArchiveWriterFromContext(ctx context.Context) *ArchiveWriter {
	if ctx == nil {
		return nil
	}
	writer, _ := ctx.Value(contextKey{}).(*ArchiveWriter)
	return writer
}

func NewWriterFromEnv(requestID string) *ArchiveWriter {
	root := strings.TrimSpace(os.Getenv(EnvRootDir))
	if root == "" || requestID == "" {
		return nil
	}
	return NewArchiveWriter(root, requestID)
}
