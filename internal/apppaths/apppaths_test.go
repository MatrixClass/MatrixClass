package apppaths

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func testEnv(goos, exe, home string, writable bool, vars map[string]string) env {
	return env{
		goos:      goos,
		exePath:   exe,
		home:      home,
		getenv:    func(k string) string { return vars[k] },
		writable:  func(string) bool { return writable },
		userCfg:   func() (string, error) { return filepath.Join(home, "cfg"), nil },
		userCache: func() (string, error) { return filepath.Join(home, "cache"), nil },
	}
}

func TestWindowsWritableFolderStaysPortable(t *testing.T) {
	exeDir := t.TempDir()
	p := resolve(testEnv("windows", filepath.Join(exeDir, "nexlab-teacher.exe"), "/home/u", true, nil), "nexlab-teacher.db")
	if !p.Portable || p.DataDir != exeDir || p.LogDir != filepath.Join(exeDir, "logs") {
		t.Fatalf("expected portable layout in exe dir, got %+v", p)
	}
}

func TestWindowsReadOnlyFolderUsesUserDirs(t *testing.T) {
	home := t.TempDir()
	p := resolve(testEnv("windows", `C:\Program Files\MatrixClass\nexlab-teacher.exe`, home, false, nil), "nexlab-teacher.db")
	if p.Portable {
		t.Fatalf("read-only install must not be portable: %+v", p)
	}
	if p.DataDir != filepath.Join(home, "cfg", "MatrixClass") || p.LogDir != filepath.Join(home, "cache", "MatrixClass", "logs") {
		t.Fatalf("unexpected windows user dirs: %+v", p)
	}
}

func TestMacAppBundleNeverPortable(t *testing.T) {
	p := resolve(testEnv("darwin", "/Applications/MatrixClass Teacher.app/Contents/MacOS/matrixclass-teacher", "/Users/t", true, nil), "nexlab-teacher.db")
	if p.Portable {
		t.Fatalf("must not write inside .app bundle: %+v", p)
	}
	if p.LogDir != filepath.Join("/Users/t", "Library", "Logs", "MatrixClass") {
		t.Fatalf("unexpected mac log dir: %s", p.LogDir)
	}
	if p.DataDir != filepath.Join("/Users/t", "cfg", "MatrixClass") {
		t.Fatalf("unexpected mac data dir: %s", p.DataDir)
	}
}

func TestLinuxSystemInstallUsesXDG(t *testing.T) {
	vars := map[string]string{"XDG_DATA_HOME": "/home/s/.data", "XDG_STATE_HOME": "/home/s/.state"}
	p := resolve(testEnv("linux", "/opt/matrixclass/matrixclass-student", "/home/s", true, vars), "")
	if p.Portable || p.DataDir != filepath.Join("/home/s/.data", "matrixclass") || p.LogDir != filepath.Join("/home/s/.state", "matrixclass", "logs") {
		t.Fatalf("unexpected linux XDG layout: %+v", p)
	}
	p = resolve(testEnv("linux", "/usr/bin/matrixclass-student", "/home/s", true, map[string]string{"XDG_DATA_HOME": "relative"}), "")
	if p.DataDir != filepath.Join("/home/s", ".local", "share", "matrixclass") {
		t.Fatalf("relative XDG var must be ignored, got %s", p.DataDir)
	}
}

func TestExistingPortableDatabaseIsKeptOnUnix(t *testing.T) {
	exeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(exeDir, "nexlab-teacher.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	p := resolve(testEnv("linux", filepath.Join(exeDir, "teacher"), "/home/u", true, nil), "nexlab-teacher.db")
	if !p.Portable || p.DataDir != exeDir {
		t.Fatalf("existing portable data must keep being used: %+v", p)
	}
}

func TestUserConfigErrorFallsBackToHome(t *testing.T) {
	e := testEnv("darwin", "/Applications/X.app/Contents/MacOS/x", "/Users/t", true, nil)
	e.userCfg = func() (string, error) { return "", errors.New("no config dir") }
	if got := userDataDir(e); got != filepath.Join("/Users/t", ".matrixclass") {
		t.Fatalf("unexpected fallback: %s", got)
	}
}

func TestDirWritable(t *testing.T) {
	if !dirWritable(t.TempDir()) {
		t.Fatalf("temp dir should be writable")
	}
	if dirWritable(filepath.Join(t.TempDir(), "missing")) {
		t.Fatalf("missing dir should not be writable")
	}
}
