//go:build darwin

package platform

import (
	"errors"
	"path/filepath"
	"testing"
)

var errHomeUnavailable = errors.New("home directory unavailable")

func TestDarwinPathsForHomeUsesMacOSDirectories(t *testing.T) {
	home := filepath.Join("/Users", "matrix-test")
	paths := DarwinPathsForHome(home)

	wantSupport := filepath.Join(home, "Library", "Application Support", "MatrixClass")
	wantLogs := filepath.Join(home, "Library", "Logs", "MatrixClass")
	wantCaches := filepath.Join(home, "Library", "Caches", "MatrixClass")

	if paths.ConfigDir != wantSupport {
		t.Fatalf("config directory = %q, want %q", paths.ConfigDir, wantSupport)
	}
	if paths.DatabaseDir != wantSupport {
		t.Fatalf("database directory = %q, want %q", paths.DatabaseDir, wantSupport)
	}
	if paths.LogDir != wantLogs {
		t.Fatalf("log directory = %q, want %q", paths.LogDir, wantLogs)
	}
	if paths.CacheDir != wantCaches {
		t.Fatalf("cache directory = %q, want %q", paths.CacheDir, wantCaches)
	}
	if paths.DownloadsDir != filepath.Join(home, "Downloads") {
		t.Fatalf("downloads directory = %q, want %q", paths.DownloadsDir, filepath.Join(home, "Downloads"))
	}
	if paths.DesktopDir != filepath.Join(home, "Desktop") {
		t.Fatalf("desktop directory = %q, want %q", paths.DesktopDir, filepath.Join(home, "Desktop"))
	}
	if paths.ReceiveDir != paths.DesktopDir {
		t.Fatalf("receive directory = %q, want desktop directory %q", paths.ReceiveDir, paths.DesktopDir)
	}
}

func TestDarwinPathsForHomeCleansTrailingSeparators(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "Users", "matrix-test") + string(filepath.Separator)
	paths := DarwinPathsForHome(home)
	want := filepath.Join(string(filepath.Separator), "Users", "matrix-test", "Library", "Caches", "MatrixClass")
	if paths.CacheDir != want {
		t.Fatalf("cache directory = %q, want %q", paths.CacheDir, want)
	}
}

func TestDarwinPathFunctionsUseConfiguredHomeResolver(t *testing.T) {
	original := darwinUserHomeDir
	darwinUserHomeDir = func() (string, error) {
		return filepath.Join(string(filepath.Separator), "Users", "resolver-test"), nil
	}
	defer func() { darwinUserHomeDir = original }()

	home := filepath.Join(string(filepath.Separator), "Users", "resolver-test")
	if ConfigDir() != filepath.Join(home, "Library", "Application Support", "MatrixClass") {
		t.Fatalf("ConfigDir did not use the configured home directory: %q", ConfigDir())
	}
	if DatabaseDir() != ConfigDir() {
		t.Fatalf("DatabaseDir = %q, want %q", DatabaseDir(), ConfigDir())
	}
	if LogDir() != filepath.Join(home, "Library", "Logs", "MatrixClass") {
		t.Fatalf("LogDir did not use the configured home directory: %q", LogDir())
	}
	if CacheDir() != filepath.Join(home, "Library", "Caches", "MatrixClass") {
		t.Fatalf("CacheDir did not use the configured home directory: %q", CacheDir())
	}
	if DefaultDownloadsDir() != filepath.Join(home, "Downloads") {
		t.Fatalf("DefaultDownloadsDir did not use the configured home directory: %q", DefaultDownloadsDir())
	}
	if DefaultDesktopDir() != filepath.Join(home, "Desktop") {
		t.Fatalf("DefaultDesktopDir did not use the configured home directory: %q", DefaultDesktopDir())
	}
	if DefaultReceiveDir() != DefaultDesktopDir() {
		t.Fatalf("DefaultReceiveDir = %q, want %q", DefaultReceiveDir(), DefaultDesktopDir())
	}
}

func TestDarwinPathFunctionsFallbackWhenHomeResolverFails(t *testing.T) {
	original := darwinUserHomeDir
	darwinUserHomeDir = func() (string, error) { return "", errHomeUnavailable }
	defer func() { darwinUserHomeDir = original }()

	if ConfigDir() != "" || DatabaseDir() != "" || LogDir() != "" || CacheDir() != "" ||
		DefaultDownloadsDir() != "" || DefaultDesktopDir() != "" || DefaultReceiveDir() != "" {
		t.Fatal("path functions should return empty paths when the home directory is unavailable")
	}
}
