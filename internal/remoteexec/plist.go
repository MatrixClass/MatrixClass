package remoteexec

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
)

// This file contains a small, dependency-free property list decoder used to
// read macOS application bundle metadata (Contents/Info.plist). It supports
// the XML format and the binary "bplist00" format. It is deliberately not
// build-tagged so the parser can be unit tested on every OS.
//
// Decoded values are: map[string]any (dict), []any (array/set), string,
// bool, int64, uint64 (only for values above MaxInt64), float64, []byte
// (data) and plistUID. Dates are returned as their raw textual (XML) or
// numeric (binary, seconds since 2001-01-01) representation.

var errInvalidPlist = errors.New("remoteexec: invalid property list")

const (
	maxPlistSize  = 8 << 20
	maxPlistDepth = 64
)

type plistUID uint64

// decodePlist detects the plist format and decodes the top-level object.
func decodePlist(data []byte) (any, error) {
	if len(data) > maxPlistSize {
		return nil, fmt.Errorf("%w: too large", errInvalidPlist)
	}
	if bytes.HasPrefix(data, []byte("bplist00")) {
		return decodeBinaryPlist(data)
	}
	trimmed := bytes.TrimLeft(data, "\xef\xbb\xbf \t\r\n")
	if bytes.HasPrefix(trimmed, []byte("<")) {
		return decodeXMLPlist(trimmed)
	}
	return nil, fmt.Errorf("%w: unsupported format", errInvalidPlist)
}

// plistDict decodes data and requires a dictionary at the top level.
func plistDict(data []byte) (map[string]any, error) {
	value, err := decodePlist(data)
	if err != nil {
		return nil, err
	}
	dict, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: top-level object is not a dictionary", errInvalidPlist)
	}
	return dict, nil
}

func plistString(dict map[string]any, key string) string {
	if value, ok := dict[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

// plistBool interprets booleans as well as the "YES"/"1"/"true" strings and
// non-zero integers that older bundles sometimes use.
func plistBool(dict map[string]any, key string) bool {
	switch value := dict[key].(type) {
	case bool:
		return value
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "1", "yes", "true":
			return true
		}
	case int64:
		return value != 0
	case uint64:
		return value != 0
	}
	return false
}

// ---- XML ----

func decodeXMLPlist(data []byte) (any, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	// Info.plist files are UTF-8; accept any declared charset as-is.
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errInvalidPlist, err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Local == "plist" {
			continue
		}
		return decodeXMLValue(dec, start, 0)
	}
}

func decodeXMLValue(dec *xml.Decoder, start xml.StartElement, depth int) (any, error) {
	if depth > maxPlistDepth {
		return nil, fmt.Errorf("%w: nesting too deep", errInvalidPlist)
	}
	switch start.Name.Local {
	case "dict":
		dict := make(map[string]any)
		var key *string
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, fmt.Errorf("%w: %v", errInvalidPlist, err)
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local == "key" {
					text, err := xmlText(dec)
					if err != nil {
						return nil, err
					}
					key = &text
					continue
				}
				value, err := decodeXMLValue(dec, t, depth+1)
				if err != nil {
					return nil, err
				}
				if key == nil {
					return nil, fmt.Errorf("%w: dict value without key", errInvalidPlist)
				}
				dict[*key] = value
				key = nil
			case xml.EndElement:
				return dict, nil
			}
		}
	case "array":
		var values []any
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, fmt.Errorf("%w: %v", errInvalidPlist, err)
			}
			switch t := tok.(type) {
			case xml.StartElement:
				value, err := decodeXMLValue(dec, t, depth+1)
				if err != nil {
					return nil, err
				}
				values = append(values, value)
			case xml.EndElement:
				if values == nil {
					values = []any{}
				}
				return values, nil
			}
		}
	case "true", "false":
		if err := dec.Skip(); err != nil {
			return nil, fmt.Errorf("%w: %v", errInvalidPlist, err)
		}
		return start.Name.Local == "true", nil
	}
	text, err := xmlText(dec)
	if err != nil {
		return nil, err
	}
	switch start.Name.Local {
	case "string", "date":
		return text, nil
	case "integer":
		text = strings.TrimSpace(text)
		if v, err := strconv.ParseInt(text, 0, 64); err == nil {
			return v, nil
		}
		if v, err := strconv.ParseUint(text, 0, 64); err == nil {
			return v, nil
		}
		return nil, fmt.Errorf("%w: bad integer %q", errInvalidPlist, text)
	case "real":
		v, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if err != nil {
			return nil, fmt.Errorf("%w: bad real %q", errInvalidPlist, text)
		}
		return v, nil
	case "data":
		clean := strings.Map(func(r rune) rune {
			if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
				return -1
			}
			return r
		}, text)
		v, err := base64.StdEncoding.DecodeString(clean)
		if err != nil {
			return nil, fmt.Errorf("%w: bad data: %v", errInvalidPlist, err)
		}
		return v, nil
	default:
		return nil, fmt.Errorf("%w: unknown element <%s>", errInvalidPlist, start.Name.Local)
	}
}

// xmlText reads character data up to the end of the current element.
func xmlText(dec *xml.Decoder) (string, error) {
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", fmt.Errorf("%w: %v", errInvalidPlist, err)
		}
		switch t := tok.(type) {
		case xml.CharData:
			b.Write(t)
		case xml.StartElement:
			return "", fmt.Errorf("%w: unexpected <%s> in text element", errInvalidPlist, t.Name.Local)
		case xml.EndElement:
			return b.String(), nil
		}
	}
}

// ---- binary (bplist00) ----

type binaryPlist struct {
	data       []byte
	offsets    []uint64
	refSize    int
	inProgress map[uint64]bool
}

func decodeBinaryPlist(data []byte) (any, error) {
	const headerLen, trailerLen = 8, 32
	if len(data) < headerLen+trailerLen {
		return nil, fmt.Errorf("%w: truncated binary plist", errInvalidPlist)
	}
	trailer := data[len(data)-trailerLen:]
	offsetSize := int(trailer[6])
	refSize := int(trailer[7])
	numObjects := binary.BigEndian.Uint64(trailer[8:16])
	topObject := binary.BigEndian.Uint64(trailer[16:24])
	tableOffset := binary.BigEndian.Uint64(trailer[24:32])
	if !validIntSize(offsetSize) || !validIntSize(refSize) || numObjects == 0 || topObject >= numObjects {
		return nil, fmt.Errorf("%w: bad trailer", errInvalidPlist)
	}
	tableEnd := uint64(len(data) - trailerLen)
	if numObjects > tableEnd || tableOffset < headerLen || tableOffset > tableEnd ||
		numObjects*uint64(offsetSize) > tableEnd-tableOffset {
		return nil, fmt.Errorf("%w: bad offset table", errInvalidPlist)
	}
	p := &binaryPlist{data: data[:tableOffset], refSize: refSize, inProgress: make(map[uint64]bool)}
	p.offsets = make([]uint64, numObjects)
	for i := range p.offsets {
		start := tableOffset + uint64(i*offsetSize)
		p.offsets[i] = readBigEndian(data[start : start+uint64(offsetSize)])
	}
	return p.object(topObject, 0)
}

func validIntSize(size int) bool {
	return size == 1 || size == 2 || size == 4 || size == 8
}

func readBigEndian(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

func (p *binaryPlist) bytesAt(offset, n uint64) ([]byte, error) {
	if offset > uint64(len(p.data)) || n > uint64(len(p.data))-offset {
		return nil, fmt.Errorf("%w: object out of range", errInvalidPlist)
	}
	return p.data[offset : offset+n], nil
}

// count reads the length encoded in the low nibble of marker, or in the
// integer object that follows it when the nibble is 0xF. It returns the
// length and the offset of the payload.
func (p *binaryPlist) count(offset uint64, marker byte) (uint64, uint64, error) {
	n := uint64(marker & 0x0F)
	pos := offset + 1
	if n != 0x0F {
		return n, pos, nil
	}
	head, err := p.bytesAt(pos, 1)
	if err != nil {
		return 0, 0, err
	}
	if head[0]>>4 != 0x1 {
		return 0, 0, fmt.Errorf("%w: bad length marker", errInvalidPlist)
	}
	size := uint64(1) << (head[0] & 0x0F)
	if size > 8 {
		return 0, 0, fmt.Errorf("%w: length too large", errInvalidPlist)
	}
	raw, err := p.bytesAt(pos+1, size)
	if err != nil {
		return 0, 0, err
	}
	return readBigEndian(raw), pos + 1 + size, nil
}

func (p *binaryPlist) refs(pos, n uint64) ([]uint64, error) {
	if n > uint64(len(p.data)) {
		return nil, fmt.Errorf("%w: too many references", errInvalidPlist)
	}
	raw, err := p.bytesAt(pos, n*uint64(p.refSize))
	if err != nil {
		return nil, err
	}
	out := make([]uint64, n)
	for i := range out {
		out[i] = readBigEndian(raw[i*p.refSize : (i+1)*p.refSize])
	}
	return out, nil
}

func (p *binaryPlist) object(index uint64, depth int) (any, error) {
	if depth > maxPlistDepth {
		return nil, fmt.Errorf("%w: nesting too deep", errInvalidPlist)
	}
	if index >= uint64(len(p.offsets)) {
		return nil, fmt.Errorf("%w: bad object reference", errInvalidPlist)
	}
	if p.inProgress[index] {
		return nil, fmt.Errorf("%w: reference cycle", errInvalidPlist)
	}
	offset := p.offsets[index]
	head, err := p.bytesAt(offset, 1)
	if err != nil {
		return nil, err
	}
	marker := head[0]
	switch marker >> 4 {
	case 0x0:
		switch marker {
		case 0x00:
			return nil, nil
		case 0x08:
			return false, nil
		case 0x09:
			return true, nil
		}
		return nil, fmt.Errorf("%w: unsupported singleton %#x", errInvalidPlist, marker)
	case 0x1:
		size := uint64(1) << (marker & 0x0F)
		raw, err := p.bytesAt(offset+1, size)
		if err != nil {
			return nil, err
		}
		switch size {
		case 1, 2, 4:
			return int64(readBigEndian(raw)), nil
		case 8:
			return int64(readBigEndian(raw)), nil
		case 16:
			// 128-bit integers store the value in the low 8 bytes.
			v := readBigEndian(raw[8:])
			if v > math.MaxInt64 {
				return v, nil
			}
			return int64(v), nil
		}
		return nil, fmt.Errorf("%w: bad integer size", errInvalidPlist)
	case 0x2, 0x3:
		size := uint64(1) << (marker & 0x0F)
		if marker>>4 == 0x3 {
			size = 8
		}
		raw, err := p.bytesAt(offset+1, size)
		if err != nil {
			return nil, err
		}
		switch size {
		case 4:
			return float64(math.Float32frombits(uint32(readBigEndian(raw)))), nil
		case 8:
			return math.Float64frombits(readBigEndian(raw)), nil
		}
		return nil, fmt.Errorf("%w: bad real size", errInvalidPlist)
	case 0x4:
		n, pos, err := p.count(offset, marker)
		if err != nil {
			return nil, err
		}
		raw, err := p.bytesAt(pos, n)
		if err != nil {
			return nil, err
		}
		return append([]byte(nil), raw...), nil
	case 0x5:
		n, pos, err := p.count(offset, marker)
		if err != nil {
			return nil, err
		}
		raw, err := p.bytesAt(pos, n)
		if err != nil {
			return nil, err
		}
		return string(raw), nil
	case 0x6:
		n, pos, err := p.count(offset, marker)
		if err != nil {
			return nil, err
		}
		if n > uint64(len(p.data))/2 {
			return nil, fmt.Errorf("%w: string too long", errInvalidPlist)
		}
		raw, err := p.bytesAt(pos, n*2)
		if err != nil {
			return nil, err
		}
		units := make([]uint16, n)
		for i := range units {
			units[i] = binary.BigEndian.Uint16(raw[i*2:])
		}
		return string(utf16.Decode(units)), nil
	case 0x8:
		size := uint64(marker&0x0F) + 1
		raw, err := p.bytesAt(offset+1, size)
		if err != nil {
			return nil, err
		}
		return plistUID(readBigEndian(raw)), nil
	case 0xA, 0xC:
		n, pos, err := p.count(offset, marker)
		if err != nil {
			return nil, err
		}
		refs, err := p.refs(pos, n)
		if err != nil {
			return nil, err
		}
		p.inProgress[index] = true
		defer delete(p.inProgress, index)
		values := make([]any, 0, len(refs))
		for _, ref := range refs {
			value, err := p.object(ref, depth+1)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		return values, nil
	case 0xD:
		n, pos, err := p.count(offset, marker)
		if err != nil {
			return nil, err
		}
		refs, err := p.refs(pos, n*2)
		if err != nil {
			return nil, err
		}
		p.inProgress[index] = true
		defer delete(p.inProgress, index)
		dict := make(map[string]any, n)
		for i := uint64(0); i < n; i++ {
			keyValue, err := p.object(refs[i], depth+1)
			if err != nil {
				return nil, err
			}
			key, ok := keyValue.(string)
			if !ok {
				return nil, fmt.Errorf("%w: non-string dictionary key", errInvalidPlist)
			}
			value, err := p.object(refs[n+i], depth+1)
			if err != nil {
				return nil, err
			}
			dict[key] = value
		}
		return dict, nil
	}
	return nil, fmt.Errorf("%w: unknown object marker %#x", errInvalidPlist, marker)
}
