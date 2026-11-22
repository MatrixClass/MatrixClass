package logging

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoggerWritesRoleLogAndRotatesBySize(t *testing.T) {
	dir := t.TempDir()
	logger, err := New(Config{Dir: dir, Role: "teacher", MaxBytes: 64})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer logger.Close()

	logger.Printf("first event")
	logger.Printf("second event that forces rotation")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	var contents []string
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "teacher-") || filepath.Ext(entry.Name()) != ".log" {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		if readErr != nil {
			t.Fatalf("ReadFile failed: %v", readErr)
		}
		contents = append(contents, string(data))
	}
	if len(contents) < 2 {
		t.Fatalf("expected rotated log files, got %d", len(contents))
	}
	joined := strings.Join(contents, "\n")
	if !strings.Contains(joined, "first event") || !strings.Contains(joined, "second event") {
		t.Fatalf("expected both log messages, got %q", joined)
	}
}

func TestLoggerResetsIndexOnDayChangeAndRejectsWriteAfterClose(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	logger, err := New(Config{Dir: dir, Role: "r", MaxBytes: 16, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	_, _ = logger.Write([]byte("0123456789abc\n"))
	_, _ = logger.Write([]byte("0123456789abc\n"))
	if !strings.HasSuffix(logger.CurrentPath(), "r-2026-01-01-1.log") {
		t.Fatalf("expected rotation to index 1, got %s", logger.CurrentPath())
	}
	now = now.Add(24 * time.Hour)
	_, _ = logger.Write([]byte("next day\n"))
	if !strings.HasSuffix(logger.CurrentPath(), "r-2026-01-02.log") {
		t.Fatalf("expected index reset on new day, got %s", logger.CurrentPath())
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if _, err := logger.Write([]byte("after close\n")); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("expected os.ErrClosed after Close, got %v", err)
	}
	if logger.CurrentPath() != "" {
		t.Fatalf("expected no file reopened after Close")
	}
}
