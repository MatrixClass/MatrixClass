package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func stage(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	files := map[string][]byte{
		"pkg/matrixclass-teacher-linux-amd64":          {0x7f, 'E', 'L', 'F', 1, 2},
		"pkg/install.sh":                               []byte("#!/bin/sh\necho hi\n"),
		"pkg/README.md":                                []byte("# readme\n"),
		"pkg/T.app/Contents/MacOS/matrixclass-teacher": {0xca, 0xfe, 0xba, 0xbe},
		"pkg/T.app/Contents/Info.plist":                []byte("<plist/>"),
		"pkg/matrixclass-student.exe":                  []byte("MZ\x90\x00"),
	}
	for name, data := range files {
		p := filepath.Join(base, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return base
}

var wantModes = map[string]os.FileMode{
	"pkg/matrixclass-teacher-linux-amd64":          0o755,
	"pkg/install.sh":                               0o755,
	"pkg/README.md":                                0o644,
	"pkg/T.app/Contents/MacOS/matrixclass-teacher": 0o755,
	"pkg/T.app/Contents/Info.plist":                0o644,
	"pkg/matrixclass-student.exe":                  0o755,
}

func TestZipModes(t *testing.T) {
	base := stage(t)
	out := filepath.Join(t.TempDir(), "x.zip")
	if err := create(out, base, []string{"pkg"}, nil); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	got := map[string]os.FileMode{}
	for _, f := range zr.File {
		if !f.FileInfo().IsDir() {
			got[f.Name] = f.Mode().Perm()
		}
	}
	for name, mode := range wantModes {
		if got[name] != mode {
			t.Errorf("%s mode = %o, want %o", name, got[name], mode)
		}
	}
}

func TestTarGzModesAndContent(t *testing.T) {
	base := stage(t)
	out := filepath.Join(t.TempDir(), "x.tar.gz")
	if err := create(out, base, []string{"pkg"}, nil); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	found := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		want, ok := wantModes[h.Name]
		if !ok {
			t.Errorf("unexpected entry %s", h.Name)
			continue
		}
		found++
		if os.FileMode(h.Mode).Perm() != want {
			t.Errorf("%s mode = %o, want %o", h.Name, h.Mode, want)
		}
		if h.Name == "pkg/install.sh" {
			b, _ := io.ReadAll(tr)
			if string(b) != "#!/bin/sh\necho hi\n" {
				t.Errorf("install.sh content = %q", b)
			}
		}
	}
	if found != len(wantModes) {
		t.Errorf("found %d files, want %d", found, len(wantModes))
	}
}

func TestRejectsEscapeAndBadExtension(t *testing.T) {
	base := stage(t)
	dir := t.TempDir()
	if err := create(filepath.Join(dir, "x.rar"), base, []string{"pkg"}, nil); err == nil {
		t.Error("unknown extension accepted")
	}
	if err := create(filepath.Join(dir, "x.zip"), filepath.Join(base, "pkg"), []string{"../pkg"}, nil); err == nil {
		t.Error("entry outside base accepted")
	}
}
