package media

// Pure helpers for the xdg-desktop-portal ScreenCast/Screenshot client in
// portal_linux.go. No GOOS constraint: godbus types are portable and nothing
// here touches a bus, so request-path derivation, option building, response
// parsing, error mapping and restore-token persistence are unit tested on
// every OS.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/godbus/dbus/v5"
)

const (
	portalBusName         = "org.freedesktop.portal.Desktop"
	portalObjectPath      = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	portalScreenCastIface = "org.freedesktop.portal.ScreenCast"
	portalScreenshotIface = "org.freedesktop.portal.Screenshot"
	portalRequestIface    = "org.freedesktop.portal.Request"
	portalSessionIface    = "org.freedesktop.portal.Session"

	portalSourceMonitor = 1
	portalSourceWindow  = 2
	portalSourceVirtual = 4

	portalCursorHidden   = 1
	portalCursorEmbedded = 2
	portalCursorMetadata = 4

	portalPersistNone       = 0
	portalPersistTransient  = 1
	portalPersistPersistent = 2

	portalResponseSuccess   = 0
	portalResponseCancelled = 1
	portalResponseOther     = 2

	portalTokenPrefix = "matrixclass_"

	portalRestoreTokenDir  = "matrixclass"
	portalRestoreTokenFile = "portal-restore-token"
	portalMaxRestoreToken  = 512
)

// errPortalInterfaceMissing means the portal service runs but lacks the
// requested interface (old xdg-desktop-portal, no backend for this desktop).
var errPortalInterfaceMissing = fmt.Errorf("%w: interface not provided by the desktop portal", ErrWaylandPortalUnavailable)

// portalSenderToken converts a unique bus name (":1.42") into the form used in
// portal request/session object paths ("1_42").
func portalSenderToken(unique string) string {
	return strings.ReplaceAll(strings.TrimPrefix(unique, ":"), ".", "_")
}

func portalRequestPath(unique, token string) dbus.ObjectPath {
	return dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + portalSenderToken(unique) + "/" + token)
}

func portalSessionPath(unique, token string) dbus.ObjectPath {
	return dbus.ObjectPath("/org/freedesktop/portal/desktop/session/" + portalSenderToken(unique) + "/" + token)
}

// portalNewToken returns a fresh handle token; tokens must be valid object path
// elements ([A-Za-z0-9_]).
func portalNewToken() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return portalTokenPrefix + hex.EncodeToString(b[:])
}

// portalResponseError maps a Request.Response code to an error.
func portalResponseError(code uint32) error {
	switch code {
	case portalResponseSuccess:
		return nil
	case portalResponseCancelled:
		return ErrWaylandPortalCancelled
	case portalResponseOther:
		return ErrWaylandPortalPermission
	}
	return fmt.Errorf("%w: unexpected portal response %d", ErrWaylandPortalPermission, code)
}

// portalMapCallError classifies D-Bus call failures.
func portalMapCallError(err error) error {
	if err == nil {
		return nil
	}
	var name string
	var value dbus.Error
	var ptr *dbus.Error
	switch {
	case errors.As(err, &value):
		name = value.Name
	case errors.As(err, &ptr) && ptr != nil:
		name = ptr.Name
	}
	switch name {
	case "org.freedesktop.DBus.Error.ServiceUnknown", "org.freedesktop.DBus.Error.NameHasNoOwner",
		"org.freedesktop.DBus.Error.Spawn.ServiceNotFound", "org.freedesktop.DBus.Error.Spawn.ChildExited":
		return fmt.Errorf("%w: %v", ErrWaylandPortalUnavailable, err)
	case "org.freedesktop.DBus.Error.UnknownMethod", "org.freedesktop.DBus.Error.UnknownInterface",
		"org.freedesktop.DBus.Error.UnknownObject", "org.freedesktop.DBus.Error.UnknownProperty",
		"org.freedesktop.DBus.Error.InvalidArgs":
		return fmt.Errorf("%w (%v)", errPortalInterfaceMissing, err)
	case "org.freedesktop.portal.Error.NotAllowed":
		return fmt.Errorf("%w: %v", ErrWaylandPortalPermission, err)
	}
	return err
}

// portalScreenCastConfig captures the interface version and capabilities read
// from the ScreenCast portal properties.
type portalScreenCastConfig struct {
	Version              uint32
	AvailableSourceTypes uint32
	AvailableCursorModes uint32
	RestoreToken         string
}

// portalSelectSourcesOptions builds SelectSources options, only using keys the
// advertised interface version understands.
func portalSelectSourcesOptions(cfg portalScreenCastConfig, handleToken string) map[string]dbus.Variant {
	opts := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(handleToken),
		"types":        dbus.MakeVariant(uint32(portalSourceMonitor)),
		"multiple":     dbus.MakeVariant(false),
	}
	if cfg.Version >= 2 && cfg.AvailableCursorModes&portalCursorEmbedded != 0 {
		opts["cursor_mode"] = dbus.MakeVariant(uint32(portalCursorEmbedded))
	}
	if cfg.Version >= 4 {
		opts["persist_mode"] = dbus.MakeVariant(uint32(portalPersistPersistent))
		if portalValidRestoreToken(cfg.RestoreToken) {
			opts["restore_token"] = dbus.MakeVariant(cfg.RestoreToken)
		}
	}
	return opts
}

// portalStream describes one stream returned by ScreenCast.Start.
type portalStream struct {
	NodeID     uint32
	SourceType uint32
	X, Y       int
	Width      int
	Height     int
	ID         string
}

func portalVariantValue(v any) any {
	for {
		variant, ok := v.(dbus.Variant)
		if !ok {
			return v
		}
		v = variant.Value()
	}
}

func portalUint32(v any) (uint32, bool) {
	switch n := portalVariantValue(v).(type) {
	case uint32:
		return n, true
	case int32:
		if n >= 0 {
			return uint32(n), true
		}
	case uint64:
		if n <= 0xffffffff {
			return uint32(n), true
		}
	case int64:
		if n >= 0 && n <= 0xffffffff {
			return uint32(n), true
		}
	}
	return 0, false
}

func portalPair(v any) (int, int, bool) {
	var items []any
	switch t := portalVariantValue(v).(type) {
	case []any:
		items = t
	case []int32:
		for _, x := range t {
			items = append(items, x)
		}
	default:
		return 0, 0, false
	}
	if len(items) != 2 {
		return 0, 0, false
	}
	a, okA := portalVariantValue(items[0]).(int32)
	b, okB := portalVariantValue(items[1]).(int32)
	return int(a), int(b), okA && okB
}

func portalVariantMap(v any) (map[string]dbus.Variant, bool) {
	switch t := portalVariantValue(v).(type) {
	case map[string]dbus.Variant:
		return t, true
	case map[string]any:
		out := make(map[string]dbus.Variant, len(t))
		for k, x := range t {
			out[k] = dbus.MakeVariant(x)
		}
		return out, true
	}
	return nil, false
}

// portalParseStreams decodes the a(ua{sv}) "streams" result.
func portalParseStreams(v any) ([]portalStream, error) {
	raw, ok := portalVariantValue(v).([][]any)
	if !ok {
		if list, okList := portalVariantValue(v).([]any); okList {
			for _, item := range list {
				entry, okEntry := portalVariantValue(item).([]any)
				if !okEntry {
					return nil, fmt.Errorf("media: unexpected portal stream entry %T", item)
				}
				raw = append(raw, entry)
			}
		} else {
			return nil, fmt.Errorf("media: unexpected portal streams type %T", portalVariantValue(v))
		}
	}
	streams := make([]portalStream, 0, len(raw))
	for _, entry := range raw {
		if len(entry) != 2 {
			return nil, fmt.Errorf("media: malformed portal stream entry with %d fields", len(entry))
		}
		node, ok := portalUint32(entry[0])
		if !ok {
			return nil, fmt.Errorf("media: malformed portal stream node id %T", entry[0])
		}
		stream := portalStream{NodeID: node}
		if props, ok := portalVariantMap(entry[1]); ok {
			if st, ok := portalUint32(props["source_type"]); ok {
				stream.SourceType = st
			}
			if x, y, ok := portalPair(props["position"]); ok {
				stream.X, stream.Y = x, y
			}
			if w, h, ok := portalPair(props["size"]); ok {
				stream.Width, stream.Height = w, h
			}
			if id, ok := portalVariantValue(props["id"]).(string); ok {
				stream.ID = id
			}
		}
		streams = append(streams, stream)
	}
	return streams, nil
}

// portalParseStartResults extracts streams and the new restore token.
func portalParseStartResults(results map[string]dbus.Variant) ([]portalStream, string, error) {
	v, ok := results["streams"]
	if !ok {
		return nil, "", errors.New("media: portal Start returned no streams")
	}
	streams, err := portalParseStreams(v)
	if err != nil {
		return nil, "", err
	}
	if len(streams) == 0 {
		return nil, "", errors.New("media: portal Start returned an empty stream list")
	}
	token, _ := portalVariantValue(results["restore_token"]).(string)
	if !portalValidRestoreToken(token) {
		token = ""
	}
	return streams, token, nil
}

// portalParseSessionHandle reads session_handle from CreateSession results;
// historic portals sent it as a string, the spec says object path.
func portalParseSessionHandle(results map[string]dbus.Variant) (dbus.ObjectPath, error) {
	switch h := portalVariantValue(results["session_handle"]).(type) {
	case dbus.ObjectPath:
		if h.IsValid() {
			return h, nil
		}
	case string:
		if p := dbus.ObjectPath(h); p.IsValid() {
			return p, nil
		}
	}
	return "", errors.New("media: portal CreateSession returned no valid session handle")
}

// portalChooseStream prefers a monitor stream; any stream otherwise.
func portalChooseStream(streams []portalStream) (portalStream, error) {
	if len(streams) == 0 {
		return portalStream{}, errors.New("media: portal returned no streams")
	}
	for _, s := range streams {
		if s.SourceType == portalSourceMonitor {
			return s, nil
		}
	}
	return streams[0], nil
}

// --- restore token persistence -----------------------------------------------

func portalValidRestoreToken(token string) bool {
	if token == "" || len(token) > portalMaxRestoreToken {
		return false
	}
	for i := 0; i < len(token); i++ {
		if c := token[i]; c <= ' ' || c > '~' {
			return false
		}
	}
	return true
}

// portalRestoreTokenPath returns $XDG_CONFIG_HOME/matrixclass/portal-restore-token,
// falling back to ~/.config per the XDG base directory spec.
func portalRestoreTokenPath() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" && filepath.IsAbs(dir) {
		return filepath.Join(dir, portalRestoreTokenDir, portalRestoreTokenFile), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("media: cannot resolve config dir for portal restore token: %v", err)
	}
	return filepath.Join(home, ".config", portalRestoreTokenDir, portalRestoreTokenFile), nil
}

// portalFileTokenStore persists the single-use restore token so the user only
// approves screen sharing once (persist_mode=2).
type portalFileTokenStore struct {
	path string
}

func (s portalFileTokenStore) load() string {
	if s.path == "" {
		return ""
	}
	data, err := os.ReadFile(s.path)
	if err != nil || len(data) > portalMaxRestoreToken+16 {
		return ""
	}
	token := strings.TrimSpace(string(data))
	if !portalValidRestoreToken(token) {
		return ""
	}
	return token
}

// save writes token atomically with 0600 permissions; an empty token removes
// the file (restore tokens are single use, so a consumed one is useless).
func (s portalFileTokenStore) save(token string) error {
	if s.path == "" {
		return nil
	}
	if !portalValidRestoreToken(token) {
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, portalRestoreTokenFile+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_ = tmp.Chmod(0o600)
	if _, err := tmp.WriteString(token + "\n"); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, s.path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
