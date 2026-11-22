// Command mkarchive creates .zip or .tar.gz release archives with correct
// Unix permissions on any host. Windows filesystems have no executable bit,
// so archives produced there with tar.exe or Compress-Archive extract
// binaries as non-executable on Linux and macOS. mkarchive marks ELF,
// Mach-O, PE and shebang files (and anything under an .app's Contents/MacOS)
// as 0755 and everything else as 0644.
//
// Usage:
//
//	go run ./tools/mkarchive -o out.tar.gz -C <base-dir> <entry>...
//	go run ./tools/mkarchive -o out.zip    -C <base-dir> <entry>...
//
// Entries are paths relative to -C and are stored with that relative path.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type entry struct {
	name  string // slash-separated archive path
	src   string // filesystem path ("" for directories)
	dir   bool
	mode  fs.FileMode
	mtime time.Time
}

var execMagics = [][]byte{
	{0x7f, 'E', 'L', 'F'},    // ELF
	{0xcf, 0xfa, 0xed, 0xfe}, // Mach-O 64-bit LE
	{0xce, 0xfa, 0xed, 0xfe}, // Mach-O 32-bit LE
	{0xca, 0xfe, 0xba, 0xbe}, // Mach-O fat
	{'M', 'Z'},               // PE (harmless, keeps .exe runnable under Wine/WSL)
	{'#', '!'},               // script
}

func isExecutable(name, src string) (bool, error) {
	if strings.Contains(name, ".app/Contents/MacOS/") || strings.HasSuffix(name, ".sh") {
		return true, nil
	}
	f, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer f.Close()
	head := make([]byte, 4)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	for _, m := range execMagics {
		if bytes.HasPrefix(head, m) {
			return true, nil
		}
	}
	return false, nil
}

func modTime(info fs.FileInfo, epoch *time.Time) time.Time {
	if epoch != nil {
		return *epoch
	}
	return info.ModTime()
}

func collect(base string, roots []string, epoch *time.Time) ([]entry, error) {
	var entries []entry
	seen := map[string]bool{}
	for _, root := range roots {
		start := filepath.Join(base, filepath.FromSlash(root))
		err := filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(base, p)
			if err != nil {
				return err
			}
			name := filepath.ToSlash(rel)
			if name == "." || strings.HasPrefix(name, "../") {
				return fmt.Errorf("entry %q escapes base directory", root)
			}
			if seen[name] {
				return nil
			}
			seen[name] = true
			info, err := d.Info()
			if err != nil {
				return err
			}
			switch {
			case d.IsDir():
				entries = append(entries, entry{name: name, dir: true, mode: 0o755, mtime: modTime(info, epoch)})
			case info.Mode().IsRegular():
				exe, err := isExecutable(name, p)
				if err != nil {
					return err
				}
				mode := fs.FileMode(0o644)
				if exe {
					mode = 0o755
				}
				entries = append(entries, entry{name: name, src: p, mode: mode, mtime: modTime(info, epoch)})
			default:
				return fmt.Errorf("%s: unsupported file type %v", p, info.Mode().Type())
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	return entries, nil
}

func copyFile(w io.Writer, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

func writeZip(w io.Writer, entries []entry) error {
	zw := zip.NewWriter(w)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Modified: e.mtime, Method: zip.Deflate}
		if e.dir {
			h.Name += "/"
			h.Method = zip.Store
			h.SetMode(fs.ModeDir | e.mode)
		} else {
			h.SetMode(e.mode)
		}
		fw, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		if !e.dir {
			if err := copyFile(fw, e.src); err != nil {
				return err
			}
		}
	}
	return zw.Close()
}

func writeTarGz(w io.Writer, entries []entry) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: int64(e.mode), ModTime: e.mtime, Format: tar.FormatPAX, Uname: "root", Gname: "root"}
		if e.dir {
			h.Typeflag = tar.TypeDir
			h.Name += "/"
		} else {
			st, err := os.Stat(e.src)
			if err != nil {
				return err
			}
			h.Typeflag = tar.TypeReg
			h.Size = st.Size()
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if !e.dir {
			if err := copyFile(tw, e.src); err != nil {
				return err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func create(out, base string, roots []string, epoch *time.Time) error {
	var write func(io.Writer, []entry) error
	switch {
	case strings.HasSuffix(out, ".zip"):
		write = writeZip
	case strings.HasSuffix(out, ".tar.gz"), strings.HasSuffix(out, ".tgz"):
		write = writeTarGz
	default:
		return errors.New("output must end in .zip, .tar.gz or .tgz")
	}
	entries, err := collect(base, roots, epoch)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return errors.New("nothing to archive")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(out), ".mkarchive-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := write(tmp, entries); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	_ = os.Remove(out)
	return os.Rename(tmp.Name(), out)
}

func main() {
	out := flag.String("o", "", "output archive (.zip, .tar.gz or .tgz)")
	base := flag.String("C", ".", "base directory that entries are relative to")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: mkarchive -o <out.zip|out.tar.gz> [-C dir] <entry>...")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *out == "" || flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	var epoch *time.Time
	if s := os.Getenv("SOURCE_DATE_EPOCH"); s != "" {
		sec, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			fmt.Fprintln(os.Stderr, "mkarchive: invalid SOURCE_DATE_EPOCH:", err)
			os.Exit(2)
		}
		t := time.Unix(sec, 0).UTC()
		epoch = &t
	}
	roots := make([]string, 0, flag.NArg())
	for _, a := range flag.Args() {
		roots = append(roots, path.Clean(filepath.ToSlash(a)))
	}
	if err := create(*out, *base, roots, epoch); err != nil {
		fmt.Fprintln(os.Stderr, "mkarchive:", err)
		os.Exit(1)
	}
}
