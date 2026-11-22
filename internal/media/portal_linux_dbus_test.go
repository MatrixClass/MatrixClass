package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestPortalRequestAndSessionPaths(t *testing.T) {
	if got := portalRequestPath(":1.42", "matrixclass_ab"); got != "/org/freedesktop/portal/desktop/request/1_42/matrixclass_ab" {
		t.Fatalf("request path %q", got)
	}
	if got := portalSessionPath(":1.7", "t"); got != "/org/freedesktop/portal/desktop/session/1_7/t" {
		t.Fatalf("session path %q", got)
	}
	tok := portalNewToken()
	if !strings.HasPrefix(tok, portalTokenPrefix) || !portalRequestPath(":1.1", tok).IsValid() || tok == portalNewToken() {
		t.Fatalf("bad token %q", tok)
	}
}

func TestPortalResponseCodes(t *testing.T) {
	if portalResponseError(0) != nil {
		t.Fatal("success mapped to error")
	}
	if !errors.Is(portalResponseError(1), ErrWaylandPortalCancelled) {
		t.Fatal("1 must be cancel")
	}
	if !errors.Is(portalResponseError(2), ErrWaylandPortalPermission) {
		t.Fatal("2 must be denied/other")
	}
	if !errors.Is(portalResponseError(7), ErrWaylandPortalPermission) {
		t.Fatal("unknown codes must not count as success")
	}
}

func TestPortalMapCallError(t *testing.T) {
	missing := portalMapCallError(dbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown"})
	if !errors.Is(missing, ErrWaylandPortalUnavailable) || errors.Is(missing, errPortalInterfaceMissing) {
		t.Fatalf("service unknown: %v", missing)
	}
	iface := portalMapCallError(&dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownInterface"})
	if !errors.Is(iface, errPortalInterfaceMissing) || !errors.Is(iface, ErrWaylandPortalUnavailable) {
		t.Fatalf("unknown interface: %v", iface)
	}
	if !errors.Is(portalMapCallError(dbus.Error{Name: "org.freedesktop.portal.Error.NotAllowed"}), ErrWaylandPortalPermission) {
		t.Fatal("NotAllowed must map to permission")
	}
	other := errors.New("boom")
	if portalMapCallError(other) != other || portalMapCallError(nil) != nil {
		t.Fatal("unrelated errors must pass through")
	}
}

func TestPortalSelectSourcesOptionsByVersion(t *testing.T) {
	v1 := portalSelectSourcesOptions(portalScreenCastConfig{Version: 1, AvailableCursorModes: 7, RestoreToken: "abc"}, "h")
	for _, key := range []string{"cursor_mode", "persist_mode", "restore_token"} {
		if _, ok := v1[key]; ok {
			t.Fatalf("v1 must not send %s", key)
		}
	}
	if v1["types"].Value() != uint32(portalSourceMonitor) || v1["multiple"].Value() != false || v1["handle_token"].Value() != "h" {
		t.Fatalf("v1 base options wrong: %v", v1)
	}
	v3 := portalSelectSourcesOptions(portalScreenCastConfig{Version: 3, AvailableCursorModes: portalCursorHidden}, "h")
	if _, ok := v3["cursor_mode"]; ok {
		t.Fatal("embedded cursor not available but requested")
	}
	v4 := portalSelectSourcesOptions(portalScreenCastConfig{Version: 4, AvailableCursorModes: portalCursorHidden | portalCursorEmbedded, RestoreToken: "tok-1"}, "h")
	if v4["cursor_mode"].Value() != uint32(portalCursorEmbedded) || v4["persist_mode"].Value() != uint32(portalPersistPersistent) || v4["restore_token"].Value() != "tok-1" {
		t.Fatalf("v4 options wrong: %v", v4)
	}
	v5 := portalSelectSourcesOptions(portalScreenCastConfig{Version: 5, RestoreToken: "bad token\n"}, "h")
	if _, ok := v5["restore_token"]; ok {
		t.Fatal("invalid restore token sent")
	}
}

type portalTestSize struct{ W, H int32 }

type portalTestStream struct {
	Node  uint32
	Props map[string]dbus.Variant
}

// portalWireResponse encodes a Request.Response signal exactly as the portal
// sends it and decodes it again, so parsing sees godbus' generic wire shapes.
func portalWireResponse(t *testing.T, results map[string]dbus.Variant) map[string]dbus.Variant {
	t.Helper()
	msg := &dbus.Message{
		Type: dbus.TypeSignal,
		Headers: map[dbus.HeaderField]dbus.Variant{
			dbus.FieldPath:      dbus.MakeVariant(dbus.ObjectPath("/org/freedesktop/portal/desktop/request/1_2/t")),
			dbus.FieldInterface: dbus.MakeVariant(portalRequestIface),
			dbus.FieldMember:    dbus.MakeVariant("Response"),
			dbus.FieldSignature: dbus.MakeVariant(dbus.SignatureOf(uint32(0), results)),
		},
		Body: []any{uint32(0), results},
	}
	var buf bytes.Buffer
	if err := msg.EncodeTo(&buf, binary.LittleEndian); err != nil {
		t.Fatal(err)
	}
	decoded, err := dbus.DecodeMessage(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if code, ok := portalUint32(decoded.Body[0]); !ok || portalResponseError(code) != nil {
		t.Fatalf("response code %v", decoded.Body[0])
	}
	out, ok := portalVariantMap(decoded.Body[1])
	if !ok {
		t.Fatalf("results type %T", decoded.Body[1])
	}
	return out
}

func TestPortalParseStartResultsFromWireFormat(t *testing.T) {
	streams := []portalTestStream{
		{57, map[string]dbus.Variant{"source_type": dbus.MakeVariant(uint32(2)), "size": dbus.MakeVariant(portalTestSize{800, 600})}},
		{58, map[string]dbus.Variant{"source_type": dbus.MakeVariant(uint32(1)), "position": dbus.MakeVariant(portalTestSize{1920, 0}),
			"size": dbus.MakeVariant(portalTestSize{2560, 1440}), "id": dbus.MakeVariant("mon-2")}},
	}
	results := portalWireResponse(t, map[string]dbus.Variant{"streams": dbus.MakeVariant(streams), "restore_token": dbus.MakeVariant("f3c1-restore")})
	if sig := results["streams"].Signature().String(); sig != "a(ua{sv})" {
		t.Fatalf("wire signature %q", sig)
	}
	list, token, err := portalParseStartResults(results)
	if err != nil {
		t.Fatal(err)
	}
	if token != "f3c1-restore" || len(list) != 2 {
		t.Fatalf("token=%q streams=%+v", token, list)
	}
	chosen, err := portalChooseStream(list)
	if err != nil || chosen.NodeID != 58 || chosen.Width != 2560 || chosen.Height != 1440 || chosen.X != 1920 || chosen.ID != "mon-2" {
		t.Fatalf("chosen %+v err %v", chosen, err)
	}
	// Minimal result without properties or restore token (old portal).
	minimal := portalWireResponse(t, map[string]dbus.Variant{"streams": dbus.MakeVariant([]portalTestStream{{9, map[string]dbus.Variant{}}})})
	list, token, err = portalParseStartResults(minimal)
	if err != nil || token != "" || len(list) != 1 || list[0].NodeID != 9 {
		t.Fatalf("minimal result: %+v %q %v", list, token, err)
	}
	if _, _, err := portalParseStartResults(map[string]dbus.Variant{}); err == nil {
		t.Fatal("missing streams accepted")
	}
	empty := portalWireResponse(t, map[string]dbus.Variant{"streams": dbus.MakeVariant([]portalTestStream{})})
	if _, _, err := portalParseStartResults(empty); err == nil {
		t.Fatal("empty streams accepted")
	}
	if _, _, err := portalParseStartResults(map[string]dbus.Variant{"streams": dbus.MakeVariant("nope")}); err == nil {
		t.Fatal("wrong type accepted")
	}
	session := portalWireResponse(t, map[string]dbus.Variant{"session_handle": dbus.MakeVariant("/org/freedesktop/portal/desktop/session/1_2/s")})
	if p, err := portalParseSessionHandle(session); err != nil || p != "/org/freedesktop/portal/desktop/session/1_2/s" {
		t.Fatalf("session handle over the wire: %v %v", p, err)
	}
}

func TestPortalParseSessionHandle(t *testing.T) {
	path := dbus.ObjectPath("/org/freedesktop/portal/desktop/session/1_2/t")
	if got, err := portalParseSessionHandle(map[string]dbus.Variant{"session_handle": dbus.MakeVariant(path)}); err != nil || got != path {
		t.Fatalf("object path: %v %v", got, err)
	}
	if got, err := portalParseSessionHandle(map[string]dbus.Variant{"session_handle": dbus.MakeVariant(string(path))}); err != nil || got != path {
		t.Fatalf("string form: %v %v", got, err)
	}
	if _, err := portalParseSessionHandle(map[string]dbus.Variant{"session_handle": dbus.MakeVariant("not a path")}); err == nil {
		t.Fatal("invalid handle accepted")
	}
}

func TestPortalRestoreTokenStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path, err := portalRestoreTokenPath()
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "matrixclass", "portal-restore-token") {
		t.Fatalf("path %q", path)
	}
	store := portalFileTokenStore{path: path}
	if store.load() != "" {
		t.Fatal("token before save")
	}
	if err := store.save("2b4f-uuid-token"); err != nil {
		t.Fatal(err)
	}
	if got := store.load(); got != "2b4f-uuid-token" {
		t.Fatalf("load %q", got)
	}
	if err := store.save("rotated"); err != nil || store.load() != "rotated" {
		t.Fatalf("rotate failed: %v", err)
	}
	// Single-use tokens: an empty replacement deletes the stale one.
	if err := store.save(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("token file still present: %v", err)
	}
	if err := store.save(""); err != nil {
		t.Fatal("removing a missing token must be fine")
	}
	if err := os.WriteFile(path, []byte("evil\x00token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if store.load() != "" {
		t.Fatal("corrupt token accepted")
	}
	t.Setenv("XDG_CONFIG_HOME", "relative/dir")
	if p, err := portalRestoreTokenPath(); err == nil && strings.HasPrefix(p, "relative") {
		t.Fatal("relative XDG_CONFIG_HOME must be ignored per XDG spec")
	}
}
