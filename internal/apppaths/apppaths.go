// Package apppaths decides where the teacher/student apps keep their database
// and logs on every platform.
//
// Portable installs (a writable folder next to the executable, the historical
// Windows layout) keep using that folder so existing data is not orphaned.
// Installed layouts — a macOS .app bundle, /usr, /opt, Program Files or any
// read-only directory — use the per-user standard locations instead:
//
//	Windows  data: %AppData%\MatrixClass         logs: %LocalAppData%\MatrixClass\logs
//	macOS    data: ~/Library/Application Support/MatrixClass
//	         logs: ~/Library/Logs/MatrixClass
//	Linux    data: $XDG_DATA_HOME/matrixclass (~/.local/share/matrixclass)
//	         logs: $XDG_STATE_HOME/matrixclass/logs (~/.local/state/matrixclass/logs)
package apppaths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	appName      = "MatrixClass"
	appNameLower = "matrixclass"
)

// Paths holds the resolved directories. Both exist (best effort) on return.
type Paths struct {
	DataDir  string
	LogDir   string
	Portable bool
}

// env abstracts the process environment for tests.
type env struct {
	goos      string
	exePath   string
	home      string
	getenv    func(string) string
	writable  func(dir string) bool
	userCfg   func() (string, error)
	userCache func() (string, error)
}

func realEnv() env {
	exe, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	home, _ := os.UserHomeDir()
	return env{
		goos:      runtime.GOOS,
		exePath:   exe,
		home:      home,
		getenv:    os.Getenv,
		writable:  dirWritable,
		userCfg:   os.UserConfigDir,
		userCache: os.UserCacheDir,
	}
}

// Resolve returns the data and log directories for this process, creating
// them when possible. dbFile is the database file name used to detect an
// existing portable install (e.g. "nexlab-teacher.db"; may be empty).
func Resolve(dbFile string) Paths {
	p := resolve(realEnv(), dbFile)
	_ = os.MkdirAll(p.DataDir, 0o755)
	_ = os.MkdirAll(p.LogDir, 0o755)
	return p
}

func resolve(e env, dbFile string) Paths {
	if exeDir := filepath.Dir(e.exePath); e.exePath != "" && portableAllowed(e, exeDir, dbFile) {
		return Paths{DataDir: exeDir, LogDir: filepath.Join(exeDir, "logs"), Portable: true}
	}
	return Paths{DataDir: userDataDir(e), LogDir: userLogDir(e)}
}

// portableAllowed reports whether the executable's folder may hold data.
func portableAllowed(e env, exeDir, dbFile string) bool {
	if insideAppBundle(exeDir) {
		// Writing into a signed bundle breaks its signature; never do it.
		return false
	}
	if e.goos == "linux" && systemDir(exeDir) {
		return false
	}
	if dbFile != "" {
		if _, err := os.Stat(filepath.Join(exeDir, dbFile)); err == nil {
			// Existing portable data: keep using it if we can still write.
			return e.writable(exeDir)
		}
	}
	if e.goos != "windows" {
		// On macOS/Linux default to per-user locations for fresh installs.
		return false
	}
	return e.writable(exeDir)
}

func insideAppBundle(dir string) bool {
	clean := filepath.ToSlash(dir)
	return strings.Contains(clean, ".app/Contents/")
}

func systemDir(dir string) bool {
	clean := filepath.ToSlash(dir)
	for _, prefix := range []string{"/usr/", "/opt/", "/bin", "/sbin", "/snap/", "/var/lib/flatpak/"} {
		if strings.HasPrefix(clean+"/", prefix) || strings.HasPrefix(clean, prefix) {
			return true
		}
	}
	return false
}

func userDataDir(e env) string {
	switch e.goos {
	case "linux":
		if x := e.getenv("XDG_DATA_HOME"); unixAbs(x) {
			return filepath.Join(x, appNameLower)
		}
		return filepath.Join(e.home, ".local", "share", appNameLower)
	default:
		// Windows: %AppData%; macOS: ~/Library/Application Support.
		if dir, err := e.userCfg(); err == nil && dir != "" {
			return filepath.Join(dir, appName)
		}
		return filepath.Join(e.home, "."+appNameLower)
	}
}

func userLogDir(e env) string {
	switch e.goos {
	case "darwin":
		return filepath.Join(e.home, "Library", "Logs", appName)
	case "linux":
		if x := e.getenv("XDG_STATE_HOME"); unixAbs(x) {
			return filepath.Join(x, appNameLower, "logs")
		}
		return filepath.Join(e.home, ".local", "state", appNameLower, "logs")
	default:
		// Windows: %LocalAppData%\MatrixClass\logs.
		if dir, err := e.userCache(); err == nil && dir != "" {
			return filepath.Join(dir, appName, "logs")
		}
		return filepath.Join(userDataDir(e), "logs")
	}
}

// dirWritable checks writability by creating and removing a temp file, which
// is the only reliable test across ACLs, read-only mounts and sandboxes.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// unixAbs reports whether p is an absolute Unix path. The XDG spec requires
// absolute values and says relative ones must be ignored.
func unixAbs(p string) bool { return strings.HasPrefix(p, "/") }
