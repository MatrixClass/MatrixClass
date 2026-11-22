package remoteexec

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeTestBundle(t *testing.T, dir, name, plist, executable string) string {
	t.Helper()
	bundle := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Join(bundle, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if plist != "" {
		if err := os.WriteFile(filepath.Join(bundle, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if executable != "" {
		if err := os.WriteFile(filepath.Join(bundle, "Contents", "MacOS", executable), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return bundle
}

func infoPlist(entries string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>` + entries + `</dict></plist>`
}

func TestReadAppBundleUsesInfoPlist(t *testing.T) {
	dir := t.TempDir()
	bundle := writeTestBundle(t, dir, "Demo.app", infoPlist(
		`<key>CFBundleName</key><string>DemoName</string>`+
			`<key>CFBundleDisplayName</key><string>Demo Display</string>`+
			`<key>CFBundleExecutable</key><string>demo-bin</string>`+
			`<key>CFBundleIdentifier</key><string>com.Example.Demo</string>`), "demo-bin")
	app, identity, ok := readAppBundle(bundle, "applications")
	if !ok {
		t.Fatal("expected bundle to be readable")
	}
	want := Application{
		ID:         applicationID(bundle),
		Name:       "Demo Display",
		Executable: filepath.Join(bundle, "Contents", "MacOS", "demo-bin"),
		Source:     "applications",
	}
	if !reflect.DeepEqual(app, want) {
		t.Fatalf("app = %+v, want %+v", app, want)
	}
	if identity != "com.example.demo" {
		t.Fatalf("identity = %q", identity)
	}
}

func TestReadAppBundleFallsBackToBundleName(t *testing.T) {
	dir := t.TempDir()
	bundle := writeTestBundle(t, dir, "Plain Tool.app", infoPlist(""), "Plain Tool")
	app, identity, ok := readAppBundle(bundle, "x")
	if !ok || app.Name != "Plain Tool" || app.Executable != filepath.Join(bundle, "Contents", "MacOS", "Plain Tool") {
		t.Fatalf("unexpected app: %+v ok=%v", app, ok)
	}
	if identity == "" {
		t.Fatal("expected path identity when CFBundleIdentifier is missing")
	}
}

func TestReadAppBundleRejectsInvalidBundles(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]struct{ plist, exe string }{
		"NoPlist.app":    {"", "NoPlist"},
		"NoExe.app":      {infoPlist(`<key>CFBundleExecutable</key><string>missing</string>`), "other"},
		"Background.app": {infoPlist(`<key>LSBackgroundOnly</key><true/>`), "Background"},
		"Framework.app":  {infoPlist(`<key>CFBundlePackageType</key><string>FMWK</string>`), "Framework"},
		"Escape.app":     {infoPlist(`<key>CFBundleExecutable</key><string>../../evil</string>`), "Escape"},
		"Broken.app":     {"not a plist", "Broken"},
	}
	for name, c := range cases {
		bundle := writeTestBundle(t, dir, name, c.plist, c.exe)
		if app, _, ok := readAppBundle(bundle, "x"); ok {
			t.Fatalf("%s: expected rejection, got %+v", name, app)
		}
	}
}

func TestDiscoverAppBundlesScansNestedFoldersAndDeduplicates(t *testing.T) {
	user := t.TempDir()
	system := t.TempDir()
	same := infoPlist(`<key>CFBundleIdentifier</key><string>com.example.same</string><key>CFBundleExecutable</key><string>Same</string>`)
	writeTestBundle(t, user, "Same.app", same, "Same")
	writeTestBundle(t, system, "Same.app", same, "Same")
	writeTestBundle(t, filepath.Join(system, "Utilities"), "Terminal.app", infoPlist(`<key>CFBundleName</key><string>Terminal</string>`), "Terminal")
	// Helper apps nested inside a bundle must not be listed.
	outer := writeTestBundle(t, system, "Outer.app", infoPlist(""), "Outer")
	writeTestBundle(t, filepath.Join(outer, "Contents", "Helpers"), "Helper.app", infoPlist(""), "Helper")
	// Too deep to be scanned.
	writeTestBundle(t, filepath.Join(system, "a", "b", "c"), "Deep.app", infoPlist(""), "Deep")

	apps := discoverAppBundles([]appBundleRoot{{user, "applications-user"}, {system, "applications-system"}})
	names := map[string]string{}
	for _, app := range apps {
		if _, dup := names[app.Name]; dup {
			t.Fatalf("duplicate app %q in %+v", app.Name, apps)
		}
		names[app.Name] = app.Source
	}
	want := map[string]string{"Same": "applications-user", "Terminal": "applications-system", "Outer": "applications-system"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("discovered = %v, want %v", names, want)
	}
}

func TestAppBundleForExecutable(t *testing.T) {
	bundle := filepath.Join(string(filepath.Separator)+"Applications", "Safari.app")
	cases := []struct {
		path string
		want string
		ok   bool
	}{
		{bundle, bundle, true},
		{filepath.Join(bundle, "Contents", "MacOS", "Safari"), bundle, true},
		{filepath.Join(bundle, "Contents", "Resources", "x"), "", false},
		{filepath.Join(string(filepath.Separator)+"usr", "bin", "python3"), "", false},
	}
	for _, c := range cases {
		got, ok := appBundleForExecutable(c.path)
		if got != c.want || ok != c.ok {
			t.Fatalf("appBundleForExecutable(%q) = %q, %v; want %q, %v", c.path, got, ok, c.want, c.ok)
		}
	}
}

func TestDarwinLaunchCommand(t *testing.T) {
	bundle := filepath.Join(string(filepath.Separator)+"Applications", "TextEdit.app")
	exe := filepath.Join(bundle, "Contents", "MacOS", "TextEdit")

	name, args := darwinLaunchCommand(exe, []string{"/tmp/a b.txt"}, true)
	if name != "/usr/bin/open" || !reflect.DeepEqual(args, []string{"-a", bundle, "-W", "--args", "/tmp/a b.txt"}) {
		t.Fatalf("bundle launch = %q %q", name, args)
	}
	name, args = darwinLaunchCommand(bundle, nil, false)
	if name != "/usr/bin/open" || !reflect.DeepEqual(args, []string{"-a", bundle}) {
		t.Fatalf("bundle launch without args = %q %q", name, args)
	}
	plain := filepath.Join(string(filepath.Separator)+"usr", "local", "bin", "tool")
	name, args = darwinLaunchCommand(plain, []string{"-v"}, false)
	if name != plain || !reflect.DeepEqual(args, []string{"-v"}) {
		t.Fatalf("plain launch = %q %q", name, args)
	}
}

func TestSplitCommandLinePosixForMacArguments(t *testing.T) {
	got := splitCommandLine(`--file "/Users/me/My Docs/a.txt" 'single quoted' plain\ space`, true)
	want := []string{"--file", "/Users/me/My Docs/a.txt", "single quoted", "plain space"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitCommandLine = %q, want %q", got, want)
	}
}
