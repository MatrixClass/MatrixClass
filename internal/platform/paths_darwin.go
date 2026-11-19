//go:build darwin

package platform

import (
	"os"
	"path/filepath"
)

const darwinApplicationName = "MatrixClass"

// DarwinPaths contains the standard per-user locations used by MatrixClass on macOS.
type DarwinPaths struct {
	ConfigDir    string
	DatabaseDir  string
	LogDir       string
	CacheDir     string
	DownloadsDir string
	DesktopDir   string
	ReceiveDir   string
}

var darwinUserHomeDir = os.UserHomeDir

// DarwinPathsForHome builds macOS paths without reading or creating directories.
// It is kept injectable so path layout can be tested without relying on the host user.
func DarwinPathsForHome(home string) DarwinPaths {
	if home == "" {
		return DarwinPaths{}
	}
	home = filepath.Clean(home)
	supportDir := filepath.Join(home, "Library", "Application Support", darwinApplicationName)
	desktopDir := filepath.Join(home, "Desktop")
	return DarwinPaths{
		ConfigDir:    supportDir,
		DatabaseDir:  supportDir,
		LogDir:       filepath.Join(home, "Library", "Logs", darwinApplicationName),
		CacheDir:     filepath.Join(home, "Library", "Caches", darwinApplicationName),
		DownloadsDir: filepath.Join(home, "Downloads"),
		DesktopDir:   desktopDir,
		ReceiveDir:   desktopDir,
	}
}

func currentDarwinPaths() DarwinPaths {
	home, err := darwinUserHomeDir()
	if err != nil {
		return DarwinPaths{}
	}
	return DarwinPathsForHome(home)
}

// ConfigDir returns the macOS Application Support directory for MatrixClass.
func ConfigDir() string {
	return currentDarwinPaths().ConfigDir
}

// DatabaseDir returns the directory intended for the SQLite database.
func DatabaseDir() string {
	return currentDarwinPaths().DatabaseDir
}

// LogDir returns the macOS per-user log directory for MatrixClass.
func LogDir() string {
	return currentDarwinPaths().LogDir
}

// CacheDir returns the macOS per-user cache directory for MatrixClass.
func CacheDir() string {
	return currentDarwinPaths().CacheDir
}

// DefaultDownloadsDir returns the user's Downloads directory.
func DefaultDownloadsDir() string {
	return currentDarwinPaths().DownloadsDir
}

// DefaultDesktopDir returns the user's Desktop directory.
func DefaultDesktopDir() string {
	return currentDarwinPaths().DesktopDir
}

// DefaultReceiveDir returns the default directory for received files.
func DefaultReceiveDir() string {
	return currentDarwinPaths().ReceiveDir
}
