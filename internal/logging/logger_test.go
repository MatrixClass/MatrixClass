package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
