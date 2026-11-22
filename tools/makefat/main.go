// Command makefat combines thin Mach-O executables into a single fat
// (universal) binary, like `lipo -create`, without requiring Apple tools.
// It lets Windows and Linux hosts produce macOS Universal 2 binaries.
//
// Usage:
//
//	go run ./tools/makefat -o out thin-amd64 thin-arm64
//	go run ./tools/makefat -info fat-binary
package main

import (
	"bytes"
	"debug/macho"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

const (
	fatMagic      = 0xcafebabe
	fatHeaderSize = 8
	fatArchSize   = 20
	maxFatOffset  = 1<<32 - 1
)

// slice is one thin Mach-O image destined for the fat file.
type slice struct {
	name   string
	cpu    macho.Cpu
	subCpu uint32
	align  uint32 // log2 of the required file alignment
	data   []byte
}

// alignFor mirrors lipo's choice: 16 KiB pages for arm64, 4 KiB otherwise.
func alignFor(cpu macho.Cpu) uint32 {
	switch cpu {
	case macho.CpuArm64:
		return 14
	default:
		return 12
	}
}

func readThin(path string) (slice, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return slice{}, err
	}
	if len(data) >= 4 {
		magic := binary.BigEndian.Uint32(data)
		if magic == fatMagic || magic == fatMagic+1 {
			return slice{}, fmt.Errorf("%s: input is already a fat binary", path)
		}
	}
	f, err := macho.NewFile(bytes.NewReader(data))
	if err != nil {
		return slice{}, fmt.Errorf("%s: not a thin Mach-O file: %w", path, err)
	}
	defer f.Close()
	return slice{
		name:   path,
		cpu:    f.Cpu,
		subCpu: f.SubCpu,
		align:  alignFor(f.Cpu),
		data:   data,
	}, nil
}

func alignUp(v, align uint64) uint64 {
	return (v + align - 1) &^ (align - 1)
}

// buildFat lays out the fat header followed by each slice at an offset
// aligned to the slice's required alignment.
func buildFat(w io.Writer, slices []slice) error {
	if len(slices) < 2 {
		return errors.New("need at least two thin Mach-O inputs")
	}
	seen := map[macho.Cpu]string{}
	for _, s := range slices {
		if prev, ok := seen[s.cpu]; ok {
			return fmt.Errorf("duplicate architecture %v in %s and %s", s.cpu, prev, s.name)
		}
		seen[s.cpu] = s.name
	}
	// Deterministic order: smaller alignment first (x86_64 before arm64),
	// matching the layout lipo produces for Universal 2 binaries.
	sort.SliceStable(slices, func(i, j int) bool {
		if slices[i].align != slices[j].align {
			return slices[i].align < slices[j].align
		}
		return slices[i].cpu < slices[j].cpu
	})

	header := make([]byte, fatHeaderSize+fatArchSize*len(slices))
	binary.BigEndian.PutUint32(header[0:], fatMagic)
	binary.BigEndian.PutUint32(header[4:], uint32(len(slices)))
	offsets := make([]uint64, len(slices))
	next := uint64(len(header))
	for i, s := range slices {
		off := alignUp(next, 1<<s.align)
		end := off + uint64(len(s.data))
		if end > maxFatOffset {
			return fmt.Errorf("fat binary exceeds 4 GiB (%s)", s.name)
		}
		offsets[i] = off
		next = end
		entry := header[fatHeaderSize+fatArchSize*i:]
		binary.BigEndian.PutUint32(entry[0:], uint32(s.cpu))
		binary.BigEndian.PutUint32(entry[4:], s.subCpu)
		binary.BigEndian.PutUint32(entry[8:], uint32(off))
		binary.BigEndian.PutUint32(entry[12:], uint32(len(s.data)))
		binary.BigEndian.PutUint32(entry[16:], s.align)
	}

	if _, err := w.Write(header); err != nil {
		return err
	}
	pos := uint64(len(header))
	for i, s := range slices {
		if pad := offsets[i] - pos; pad > 0 {
			if _, err := w.Write(make([]byte, pad)); err != nil {
				return err
			}
		}
		if _, err := w.Write(s.data); err != nil {
			return err
		}
		pos = offsets[i] + uint64(len(s.data))
	}
	return nil
}

// makeFat reads the thin inputs and atomically writes the fat output.
func makeFat(out string, inputs []string) error {
	slices := make([]slice, 0, len(inputs))
	for _, in := range inputs {
		s, err := readThin(in)
		if err != nil {
			return err
		}
		slices = append(slices, s)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(out), ".makefat-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := buildFat(tmp, slices); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}
	_ = os.Remove(out)
	return os.Rename(tmpName, out)
}

// describe prints the architectures contained in a fat or thin Mach-O file.
func describe(w io.Writer, path string) error {
	ff, err := macho.OpenFat(path)
	if err == nil {
		defer ff.Close()
		fmt.Fprintf(w, "%s: fat Mach-O with %d architectures\n", path, len(ff.Arches))
		for _, a := range ff.Arches {
			fmt.Fprintf(w, "  %-8v subtype=%#x offset=%d size=%d align=2^%d\n", a.Cpu, a.SubCpu, a.Offset, a.Size, a.Align)
		}
		return nil
	}
	f, terr := macho.Open(path)
	if terr != nil {
		return fmt.Errorf("%s: not a Mach-O file: %v", path, err)
	}
	defer f.Close()
	fmt.Fprintf(w, "%s: thin Mach-O %v\n", path, f.Cpu)
	return nil
}

func main() {
	out := flag.String("o", "", "output path for the fat binary")
	info := flag.Bool("info", false, "print the architectures of the given Mach-O files instead of creating one")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: makefat -o <output> <thin-macho>...\n       makefat -info <macho>...")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *info {
		if flag.NArg() == 0 {
			flag.Usage()
			os.Exit(2)
		}
		for _, p := range flag.Args() {
			if err := describe(os.Stdout, p); err != nil {
				fmt.Fprintln(os.Stderr, "makefat:", err)
				os.Exit(1)
			}
		}
		return
	}
	if *out == "" || flag.NArg() < 2 {
		flag.Usage()
		os.Exit(2)
	}
	if err := makeFat(*out, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "makefat:", err)
		os.Exit(1)
	}
}
