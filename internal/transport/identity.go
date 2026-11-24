package transport

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"nexlab/internal/protocol"
)

var (
	ErrUntrustedTeacher  = errors.New("transport: teacher identity does not match the trusted fingerprint")
	ErrInvalidBeaconSig  = errors.New("transport: discovery beacon signature is invalid")
	ErrInvalidFingerprnt = errors.New("transport: fingerprint must contain 32 hexadecimal characters")
)

// TeacherIdentity is the long-term Ed25519 key a teacher console uses to
// prove who it is to students. Copying the key file to several teacher
// machines lets all of them serve the same classroom.
type TeacherIdentity struct {
	private ed25519.PrivateKey
}

func GenerateTeacherIdentity() (*TeacherIdentity, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &TeacherIdentity{private: priv}, nil
}

// LoadOrCreateTeacherIdentity reads the seed stored at path, creating a new
// identity (file mode 0600) when it does not exist yet.
func LoadOrCreateTeacherIdentity(path string) (*TeacherIdentity, error) {
	if raw, err := os.ReadFile(path); err == nil {
		seed, err := hex.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("transport: invalid teacher identity file %s", path)
		}
		return &TeacherIdentity{private: ed25519.NewKeyFromSeed(seed)}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	id, err := GenerateTeacherIdentity()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(id.private.Seed())+"\n"), 0o600); err != nil {
		return nil, err
	}
	return id, nil
}

func (id *TeacherIdentity) PublicKey() ed25519.PublicKey {
	return id.private.Public().(ed25519.PublicKey)
}

func (id *TeacherIdentity) Fingerprint() string {
	return Fingerprint(id.PublicKey())
}

func (id *TeacherIdentity) sign(msg []byte) []byte {
	return ed25519.Sign(id.private, msg)
}

// Fingerprint returns the first 128 bits of SHA-256(pub) as eight groups of
// four uppercase hex digits, e.g. "1A2B-3C4D-...".
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	raw := strings.ToUpper(hex.EncodeToString(sum[:16]))
	groups := make([]string, 0, 8)
	for i := 0; i < len(raw); i += 4 {
		groups = append(groups, raw[i:i+4])
	}
	return strings.Join(groups, "-")
}

// NormalizeFingerprint accepts any spacing, dashes or case and returns the
// canonical grouped form.
func NormalizeFingerprint(raw string) (string, error) {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(raw)) {
		switch {
		case (r >= '0' && r <= '9') || (r >= 'A' && r <= 'F'):
			b.WriteRune(r)
		case r == '-' || r == ' ' || r == ':' || r == '\t':
		default:
			return "", ErrInvalidFingerprnt
		}
	}
	hexStr := b.String()
	if len(hexStr) != 32 {
		return "", ErrInvalidFingerprnt
	}
	groups := make([]string, 0, 8)
	for i := 0; i < len(hexStr); i += 4 {
		groups = append(groups, hexStr[i:i+4])
	}
	return strings.Join(groups, "-"), nil
}

// TeacherTrust decides which teacher identities a student accepts.
type TeacherTrust interface {
	// Pinned returns the trusted fingerprint, or "" when nothing is pinned.
	Pinned() string
	// Accept verifies pub and pins it on first use when nothing is pinned.
	Accept(pub ed25519.PublicKey) error
}

// MemoryTrust is a trust-on-first-use store that lives only in memory.
type MemoryTrust struct {
	mu  sync.Mutex
	pin string
}

func NewMemoryTrust(pin string) *MemoryTrust {
	normalized, _ := NormalizeFingerprint(pin)
	return &MemoryTrust{pin: normalized}
}

func (t *MemoryTrust) Pinned() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pin
}

func (t *MemoryTrust) Accept(pub ed25519.PublicKey) error {
	fp := Fingerprint(pub)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pin == "" {
		t.pin = fp
		return nil
	}
	if t.pin != fp {
		return ErrUntrustedTeacher
	}
	return nil
}

// FileTrust persists the pinned fingerprint in a small text file.
type FileTrust struct {
	mu   sync.Mutex
	path string
}

func NewFileTrust(path string) *FileTrust {
	return &FileTrust{path: path}
}

func (t *FileTrust) Pinned() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.readLocked()
}

func (t *FileTrust) readLocked() string {
	raw, err := os.ReadFile(t.path)
	if err != nil {
		return ""
	}
	fp, err := NormalizeFingerprint(string(raw))
	if err != nil {
		return ""
	}
	return fp
}

func (t *FileTrust) Accept(pub ed25519.PublicKey) error {
	fp := Fingerprint(pub)
	t.mu.Lock()
	defer t.mu.Unlock()
	pinned := t.readLocked()
	if pinned == "" {
		return t.writeLocked(fp)
	}
	if pinned != fp {
		return ErrUntrustedTeacher
	}
	return nil
}

// SetPinned stores fp (any accepted format); an empty string clears the pin
// so the next teacher that proves its identity is trusted.
func (t *FileTrust) SetPinned(fp string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if strings.TrimSpace(fp) == "" {
		err := os.Remove(t.path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	normalized, err := NormalizeFingerprint(fp)
	if err != nil {
		return err
	}
	return t.writeLocked(normalized)
}

func (t *FileTrust) writeLocked(fp string) error {
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(t.path, []byte(fp+"\n"), 0o600)
}

func beaconSigningBytes(b protocol.DiscoveryBeacon) []byte {
	b.Sig = ""
	raw, _ := json.Marshal(b)
	return append([]byte("MatrixClass/beacon/v1\x00"), raw...)
}

// SignBeacon embeds the teacher public key and an Ed25519 signature.
func SignBeacon(id *TeacherIdentity, b protocol.DiscoveryBeacon) protocol.DiscoveryBeacon {
	b.PubKey = base64.StdEncoding.EncodeToString(id.PublicKey())
	b.Sig = base64.StdEncoding.EncodeToString(id.sign(beaconSigningBytes(b)))
	return b
}

// VerifyBeacon checks the beacon signature and returns the signer key.
func VerifyBeacon(b protocol.DiscoveryBeacon) (ed25519.PublicKey, error) {
	pub, err := base64.StdEncoding.DecodeString(b.PubKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, ErrInvalidBeaconSig
	}
	sig, err := base64.StdEncoding.DecodeString(b.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, ErrInvalidBeaconSig
	}
	if !ed25519.Verify(pub, beaconSigningBytes(b), sig) {
		return nil, ErrInvalidBeaconSig
	}
	return ed25519.PublicKey(pub), nil
}
