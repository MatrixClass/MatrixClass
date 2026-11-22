package main

import (
	"bytes"
	"debug/macho"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// thinMachO returns a minimal valid 64-bit little-endian Mach-O executable
// header (no load commands) followed by a recognisable payload.
func thinMachO(cpu macho.Cpu, subCpu uint32, payload string) []byte {
	var b bytes.Buffer
	for _, v := range []uint32{macho.Magic64, uint32(cpu), subCpu, uint32(macho.TypeExec), 0, 0, 0, 0} {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString(payload)
	return b.Bytes()
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func checkFat(t *testing.T, out string, want map[macho.Cpu][]byte) {
	t.Helper()
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	ff, err := macho.NewFatFile(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("debug/macho cannot parse fat output: %v", err)
	}
	defer ff.Close()
	if ff.Magic != macho.MagicFat {
		t.Fatalf("magic = %#x, want %#x", ff.Magic, macho.MagicFat)
	}
	if len(ff.Arches) != len(want) {
		t.Fatalf("got %d arches, want %d", len(ff.Arches), len(want))
	}
	for i, a := range ff.Arches {
		data, ok := want[a.Cpu]
		if !ok {
			t.Fatalf("unexpected arch %v", a.Cpu)
		}
		if a.Align != alignFor(a.Cpu) {
			t.Errorf("%v align = 2^%d, want 2^%d", a.Cpu, a.Align, alignFor(a.Cpu))
		}
		if a.Offset%(1<<a.Align) != 0 {
			t.Errorf("%v offset %d not aligned to 2^%d", a.Cpu, a.Offset, a.Align)
		}
		if i > 0 {
			prev := ff.Arches[i-1]
			if a.Offset < prev.Offset+prev.Size {
				t.Errorf("%v overlaps previous slice", a.Cpu)
			}
		}
		if a.Size != uint32(len(data)) || !bytes.Equal(raw[a.Offset:a.Offset+a.Size], data) {
			t.Errorf("%v slice bytes differ from input", a.Cpu)
		}
		if a.File == nil || a.File.Cpu != a.Cpu {
			t.Errorf("%v slice did not parse as thin Mach-O", a.Cpu)
		}
	}
	if ff.Arches[0].Cpu != macho.CpuAmd64 {
		t.Errorf("first slice = %v, want amd64 (lipo-compatible order)", ff.Arches[0].Cpu)
	}
}

func TestMakeFatSynthetic(t *testing.T) {
	dir := t.TempDir()
	amd := thinMachO(macho.CpuAmd64, 3, strings.Repeat("x86", 1000))
	arm := thinMachO(macho.CpuArm64, 0, strings.Repeat("arm", 3000))
	// Pass arm64 first to verify slices are reordered deterministically.
	inArm := writeFile(t, dir, "arm64", arm)
	inAmd := writeFile(t, dir, "amd64", amd)
	out := filepath.Join(dir, "sub", "universal")
	if err := makeFat(out, []string{inArm, inAmd}); err != nil {
		t.Fatal(err)
	}
	checkFat(t, out, map[macho.Cpu][]byte{macho.CpuAmd64: amd, macho.CpuArm64: arm})
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		t.Fatalf("stat output: %v", err)
	}
	var info bytes.Buffer
	if err := describe(&info, out); err != nil || !strings.Contains(info.String(), "2 architectures") {
		t.Fatalf("describe = %q, %v", info.String(), err)
	}
}

func TestMakeFatRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	amd := writeFile(t, dir, "amd64", thinMachO(macho.CpuAmd64, 3, "a"))
	amd2 := writeFile(t, dir, "amd64b", thinMachO(macho.CpuAmd64, 3, "b"))
	junk := writeFile(t, dir, "junk", []byte("not a mach-o file at all"))
	out := filepath.Join(dir, "out")
	if err := makeFat(out, []string{amd, amd2}); err == nil {
		t.Error("duplicate architectures accepted")
	}
	if err := makeFat(out, []string{amd, junk}); err == nil {
		t.Error("non Mach-O input accepted")
	}
	if err := makeFat(out, []string{amd}); err == nil {
		t.Error("single input accepted")
	}
	arm := writeFile(t, dir, "arm64", thinMachO(macho.CpuArm64, 0, "c"))
	if err := makeFat(out, []string{amd, arm}); err != nil {
		t.Fatal(err)
	}
	if err := makeFat(filepath.Join(dir, "out2"), []string{out, arm}); err == nil {
		t.Error("fat input accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "out2")); !os.IsNotExist(err) {
		t.Error("failed run left an output file behind")
	}
}

// TestMakeFatRealGoBinaries cross-compiles a tiny program for both macOS
// architectures and combines them, exercising real Go linker output.
func TestMakeFatRealGoBinaries(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-compiles two binaries")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	dir := t.TempDir()
	src := writeFile(t, dir, "main.go", []byte("package main\n\nfunc main() { println(\"hi\") }\n"))
	writeFile(t, dir, "go.mod", []byte("module hello\n\ngo 1.21\n"))
	want := map[macho.Cpu][]byte{}
	var inputs []string
	for arch, cpu := range map[string]macho.Cpu{"amd64": macho.CpuAmd64, "arm64": macho.CpuArm64} {
		out := filepath.Join(dir, "hello-"+arch)
		cmd := exec.Command(goBin, "build", "-trimpath", "-o", out, src)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOOS=darwin", "GOARCH="+arch, "CGO_ENABLED=0", "GOFLAGS=", "GOWORK=off")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build darwin/%s: %v\n%s", arch, err, b)
		}
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		want[cpu] = data
		inputs = append(inputs, out)
	}
	fat := filepath.Join(dir, "hello-universal")
	if err := makeFat(fat, inputs); err != nil {
		t.Fatal(err)
	}
	checkFat(t, fat, want)
}
