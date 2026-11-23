//go:build linux

package remoteexec

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDesktopFileFiltersAndParsesExec(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.desktop")
	content := "[Desktop Entry]\nType=Application\nName=Demo App\nExec=/usr/bin/demo --open %U\nPath=/opt/demo\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	app, ok := parseDesktopFile(path, "test")
	if !ok || app.Name != "Demo App" || app.Executable != "/usr/bin/demo" || app.Arguments != "--open" {
		t.Fatalf("unexpected desktop app: %+v, %v", app, ok)
	}
}

func TestParseDesktopFileRejectsNoDisplay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hidden.desktop")
	if err := os.WriteFile(path, []byte("[Desktop Entry]\nType=Application\nName=Hidden\nNoDisplay=true\nExec=/usr/bin/hidden\n"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if _, ok := parseDesktopFile(path, "test"); ok {
		t.Fatal("expected NoDisplay application to be filtered")
	}
}

func TestParseDesktopFilePreservesQuotedExecutableAndArguments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quoted.desktop")
	content := "[Desktop Entry]\nType=Application\nName=Quoted\nExec='/opt/My Apps/demo' --profile 'student mode' %U\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	app, ok := parseDesktopFile(path, "test")
	if !ok || app.Executable != "/opt/My Apps/demo" || app.Arguments != "--profile 'student mode'" {
		t.Fatalf("unexpected quoted desktop app: %+v, %v", app, ok)
	}
}
