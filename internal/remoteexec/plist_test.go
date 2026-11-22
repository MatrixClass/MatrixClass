package remoteexec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
	"unicode/utf16"
)

const sampleXMLInfoPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDisplayName</key>
	<string>Demo &amp; Co</string>
	<key>CFBundleExecutable</key>
	<string>Demo</string>
	<key>CFBundleIdentifier</key>
	<string>com.example.demo</string>
	<key>LSUIElement</key>
	<true/>
	<key>LSMinimumSystemVersion</key>
	<string>11.0</string>
	<key>Count</key>
	<integer>-42</integer>
	<key>Ratio</key>
	<real>1.5</real>
	<key>Blob</key>
	<data>
	aGVs
	bG8=
	</data>
	<key>CFBundleDocumentTypes</key>
	<array>
		<dict>
			<key>CFBundleTypeName</key>
			<string>Text</string>
		</dict>
		<string/>
	</array>
</dict>
</plist>`

func TestDecodeXMLPlist(t *testing.T) {
	dict, err := plistDict([]byte(sampleXMLInfoPlist))
	if err != nil {
		t.Fatalf("plistDict: %v", err)
	}
	if got := plistString(dict, "CFBundleDisplayName"); got != "Demo & Co" {
		t.Fatalf("display name = %q", got)
	}
	if got := plistString(dict, "CFBundleIdentifier"); got != "com.example.demo" {
		t.Fatalf("identifier = %q", got)
	}
	if !plistBool(dict, "LSUIElement") || plistBool(dict, "Missing") {
		t.Fatal("unexpected bool decoding")
	}
	if dict["Count"] != int64(-42) || dict["Ratio"] != 1.5 {
		t.Fatalf("unexpected numbers: %#v %#v", dict["Count"], dict["Ratio"])
	}
	if blob, _ := dict["Blob"].([]byte); string(blob) != "hello" {
		t.Fatalf("unexpected data: %q", blob)
	}
	types, ok := dict["CFBundleDocumentTypes"].([]any)
	if !ok || len(types) != 2 {
		t.Fatalf("unexpected array: %#v", dict["CFBundleDocumentTypes"])
	}
	if inner, ok := types[0].(map[string]any); !ok || inner["CFBundleTypeName"] != "Text" {
		t.Fatalf("unexpected nested dict: %#v", types[0])
	}
}

func TestDecodePlistRejectsGarbage(t *testing.T) {
	for _, input := range []string{"", "hello", "<plist><dict><key>a</key>", "bplist00short"} {
		if _, err := decodePlist([]byte(input)); !errors.Is(err, errInvalidPlist) {
			t.Fatalf("decodePlist(%q) error = %v, want errInvalidPlist", input, err)
		}
	}
	if _, err := plistDict([]byte(`<plist><array><string>x</string></array></plist>`)); err == nil {
		t.Fatal("expected non-dict top level to be rejected")
	}
}

func TestPlistBoolAcceptsLegacyStrings(t *testing.T) {
	dict := map[string]any{"a": "YES", "b": "1", "c": int64(1), "d": "NO", "e": false}
	for _, key := range []string{"a", "b", "c"} {
		if !plistBool(dict, key) {
			t.Fatalf("plistBool(%q) = false", key)
		}
	}
	for _, key := range []string{"d", "e"} {
		if plistBool(dict, key) {
			t.Fatalf("plistBool(%q) = true", key)
		}
	}
}

// bplistWriter is a minimal bplist00 encoder used to build test fixtures.
type bplistWriter struct {
	objects [][]byte
}

func (w *bplistWriter) add(obj []byte) int {
	w.objects = append(w.objects, obj)
	return len(w.objects) - 1
}

func (w *bplistWriter) ascii(s string) int {
	return w.add(append(bplistHeader(0x5, len(s)), s...))
}

func (w *bplistWriter) utf16(s string) int {
	units := utf16.Encode([]rune(s))
	obj := bplistHeader(0x6, len(units))
	for _, u := range units {
		obj = binary.BigEndian.AppendUint16(obj, u)
	}
	return w.add(obj)
}

func (w *bplistWriter) boolean(v bool) int {
	if v {
		return w.add([]byte{0x09})
	}
	return w.add([]byte{0x08})
}

func (w *bplistWriter) integer(v int64) int {
	return w.add(binary.BigEndian.AppendUint64([]byte{0x13}, uint64(v)))
}

func (w *bplistWriter) array(refs ...int) int {
	obj := bplistHeader(0xA, len(refs))
	for _, r := range refs {
		obj = append(obj, byte(r))
	}
	return w.add(obj)
}

func (w *bplistWriter) dict(pairs ...int) int {
	obj := bplistHeader(0xD, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		obj = append(obj, byte(pairs[i]))
	}
	for i := 1; i < len(pairs); i += 2 {
		obj = append(obj, byte(pairs[i]))
	}
	return w.add(obj)
}

func bplistHeader(kind byte, n int) []byte {
	if n < 15 {
		return []byte{kind<<4 | byte(n)}
	}
	return []byte{kind<<4 | 0x0F, 0x11, byte(n >> 8), byte(n)}
}

func (w *bplistWriter) bytes(top int) []byte {
	var buf bytes.Buffer
	buf.WriteString("bplist00")
	offsets := make([]int, len(w.objects))
	for i, obj := range w.objects {
		offsets[i] = buf.Len()
		buf.Write(obj)
	}
	tableOffset := buf.Len()
	for _, off := range offsets {
		buf.Write(binary.BigEndian.AppendUint16(nil, uint16(off)))
	}
	trailer := make([]byte, 32)
	trailer[6] = 2 // offset int size
	trailer[7] = 1 // object ref size
	binary.BigEndian.PutUint64(trailer[8:], uint64(len(w.objects)))
	binary.BigEndian.PutUint64(trailer[16:], uint64(top))
	binary.BigEndian.PutUint64(trailer[24:], uint64(tableOffset))
	buf.Write(trailer)
	return buf.Bytes()
}

func TestDecodeBinaryPlist(t *testing.T) {
	var w bplistWriter
	longName := "A Rather Long Application Name"
	top := w.dict(
		w.ascii("CFBundleName"), w.ascii(longName),
		w.ascii("CFBundleDisplayName"), w.utf16("演示应用"),
		w.ascii("CFBundleExecutable"), w.ascii("Demo"),
		w.ascii("LSBackgroundOnly"), w.boolean(false),
		w.ascii("Version"), w.integer(7),
		w.ascii("List"), w.array(w.ascii("x"), w.boolean(true)),
	)
	dict, err := plistDict(w.bytes(top))
	if err != nil {
		t.Fatalf("plistDict: %v", err)
	}
	if got := plistString(dict, "CFBundleName"); got != longName {
		t.Fatalf("CFBundleName = %q", got)
	}
	if got := plistString(dict, "CFBundleDisplayName"); got != "演示应用" {
		t.Fatalf("CFBundleDisplayName = %q", got)
	}
	if plistBool(dict, "LSBackgroundOnly") {
		t.Fatal("LSBackgroundOnly should be false")
	}
	if dict["Version"] != int64(7) {
		t.Fatalf("Version = %#v", dict["Version"])
	}
	list, ok := dict["List"].([]any)
	if !ok || len(list) != 2 || list[0] != "x" || list[1] != true {
		t.Fatalf("List = %#v", dict["List"])
	}
}

func TestDecodeBinaryPlistRejectsCycles(t *testing.T) {
	var w bplistWriter
	// Object 0 is an array that references itself.
	w.add([]byte{0xA1, 0x00})
	if _, err := decodePlist(w.bytes(0)); !errors.Is(err, errInvalidPlist) {
		t.Fatalf("cycle error = %v, want errInvalidPlist", err)
	}
}

func TestDecodeBinaryPlistRejectsCorruptOffsets(t *testing.T) {
	var w bplistWriter
	top := w.dict(w.ascii("k"), w.ascii("v"))
	data := w.bytes(top)
	// Point the offset table past the end of the file.
	corrupt := append([]byte(nil), data...)
	binary.BigEndian.PutUint64(corrupt[len(corrupt)-8:], uint64(len(corrupt)))
	if _, err := decodePlist(corrupt); !errors.Is(err, errInvalidPlist) {
		t.Fatalf("corrupt table error = %v", err)
	}
	// Truncate an object so its payload runs out of range.
	short := append([]byte(nil), data...)
	short[8] = 0x5E // claim a 14-byte string for the first object
	if _, err := decodePlist(short); err == nil {
		t.Fatal("expected out-of-range string to be rejected")
	}
}
