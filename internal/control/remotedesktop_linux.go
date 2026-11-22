//go:build linux

package control

// D-Bus adapter for the xdg-desktop-portal RemoteDesktop interface, used for
// remote input on Wayland sessions where XTest cannot reach native Wayland
// clients. See remotedesktop_linux_session.go for the state machine.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	portalDest          = "org.freedesktop.portal.Desktop"
	portalPath          = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	portalRemoteDesktop = "org.freedesktop.portal.RemoteDesktop"
	portalScreenCast    = "org.freedesktop.portal.ScreenCast"
	portalRequest       = "org.freedesktop.portal.Request"
	portalSession       = "org.freedesktop.portal.Session"

	rdNotifyTimeout = 2 * time.Second
	// rdScreenCastMonitor is the ScreenCast source type bit for monitors.
	rdScreenCastMonitor uint32 = 1
)

var rdTokenCounter atomic.Uint64

type dbusRDPortal struct {
	conn      *dbus.Conn
	obj       dbus.BusObject
	sender    string
	owner     string
	version   uint32
	available uint32
	cast      bool

	mu      sync.Mutex
	waiters map[dbus.ObjectPath]chan *dbus.Signal
	session dbus.ObjectPath

	signals   chan *dbus.Signal
	done      chan struct{}
	doneOnce  sync.Once
	closeOnce sync.Once
}

func newDBusRDPortal() (rdPortal, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("%w: session bus: %v", ErrWaylandRemoteInputUnavailable, err)
	}
	obj := conn.Object(portalDest, portalPath)
	var version uint32
	if err := obj.StoreProperty(portalRemoteDesktop+".version", &version); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: %v", ErrWaylandRemoteInputUnavailable, err)
	}
	var available uint32
	if err := obj.StoreProperty(portalRemoteDesktop+".AvailableDeviceTypes", &available); err != nil {
		available = rdDeviceKeyboard | rdDevicePointer
	}
	if available&(rdDeviceKeyboard|rdDevicePointer) == 0 {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: portal offers no keyboard or pointer devices", ErrWaylandRemoteInputUnavailable)
	}
	var sources uint32
	cast := obj.StoreProperty(portalScreenCast+".AvailableSourceTypes", &sources) == nil && sources&rdScreenCastMonitor != 0
	names := conn.Names()
	if len(names) == 0 {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: no unique bus name", ErrWaylandRemoteInputUnavailable)
	}
	var owner string
	if err := conn.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, portalDest).Store(&owner); err != nil || owner == "" {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: portal has no owner", ErrWaylandRemoteInputUnavailable)
	}
	p := &dbusRDPortal{
		conn:      conn,
		owner:     owner,
		obj:       obj,
		sender:    names[0],
		version:   version,
		available: available,
		cast:      cast,
		waiters:   make(map[dbus.ObjectPath]chan *dbus.Signal),
		signals:   make(chan *dbus.Signal, 32),
		done:      make(chan struct{}),
	}
	if err := conn.AddMatchSignal(dbus.WithMatchSender(portalDest), dbus.WithMatchInterface(portalRequest), dbus.WithMatchMember("Response")); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: subscribe portal responses: %v", ErrWaylandRemoteInputUnavailable, err)
	}
	if err := conn.AddMatchSignal(dbus.WithMatchSender(portalDest), dbus.WithMatchInterface(portalSession), dbus.WithMatchMember("Closed")); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: subscribe portal session: %v", ErrWaylandRemoteInputUnavailable, err)
	}
	conn.Signal(p.signals)
	go p.route()
	return p, nil
}

func (p *dbusRDPortal) route() {
	ctxDone := p.conn.Context().Done()
	for {
		select {
		case <-p.done:
			return
		case <-ctxDone:
			p.markDone()
			return
		case sig, ok := <-p.signals:
			if !ok {
				p.markDone()
				return
			}
			p.dispatch(sig)
		}
	}
}

func (p *dbusRDPortal) dispatch(sig *dbus.Signal) {
	// Only trust signals from the portal itself; any session client could
	// otherwise forge a Response or Closed signal.
	if sig == nil || sig.Sender != p.owner {
		return
	}
	p.mu.Lock()
	session := p.session
	waiter := p.waiters[sig.Path]
	p.mu.Unlock()
	switch sig.Name {
	case portalSession + ".Closed":
		if session != "" && sig.Path == session {
			p.markDone()
		}
	case portalRequest + ".Response":
		if waiter != nil {
			select {
			case waiter <- sig:
			default:
			}
		}
	}
}

func (p *dbusRDPortal) markDone() {
	p.doneOnce.Do(func() { close(p.done) })
}

func (p *dbusRDPortal) Done() <-chan struct{} { return p.done }

func newRDToken() string {
	var buf [6]byte
	_, _ = rand.Read(buf[:])
	return fmt.Sprintf("matrixclass_%d_%s", rdTokenCounter.Add(1), hex.EncodeToString(buf[:]))
}

// request performs a portal method that answers through a Request object and
// waits for its Response signal.
func (p *dbusRDPortal) request(ctx context.Context, stage, method string, args []any, options map[string]dbus.Variant) (map[string]dbus.Variant, error) {
	token := newRDToken()
	options["handle_token"] = dbus.MakeVariant(token)
	expected := dbus.ObjectPath(rdRequestPath(p.sender, token))
	wait := make(chan *dbus.Signal, 1)
	p.mu.Lock()
	p.waiters[expected] = wait
	p.mu.Unlock()
	handle := expected
	defer func() {
		p.mu.Lock()
		delete(p.waiters, expected)
		delete(p.waiters, handle)
		p.mu.Unlock()
	}()
	callArgs := append(append([]any(nil), args...), options)
	if err := p.obj.CallWithContext(ctx, method, 0, callArgs...).Store(&handle); err != nil {
		return nil, fmt.Errorf("control: portal %s: %w", stage, err)
	}
	if handle != expected {
		// Portals older than 0.9 return an unpredictable request path.
		p.mu.Lock()
		p.waiters[handle] = wait
		p.mu.Unlock()
	}
	select {
	case sig := <-wait:
		code, results, ok := rdParseResponse(sig.Body)
		if !ok {
			return nil, fmt.Errorf("control: malformed portal %s response", stage)
		}
		if err := rdResponseError(code, stage); err != nil {
			return nil, err
		}
		return results, nil
	case <-ctx.Done():
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = p.conn.Object(portalDest, handle).CallWithContext(closeCtx, portalRequest+".Close", 0).Err
		cancel()
		return nil, ctx.Err()
	case <-p.done:
		return nil, fmt.Errorf("%w: portal connection ended during %s", ErrWaylandRemoteInputClosed, stage)
	}
}

func (p *dbusRDPortal) Establish(ctx context.Context, restoreToken string) (rdSessionInfo, error) {
	results, err := p.request(ctx, "CreateSession", portalRemoteDesktop+".CreateSession", nil, map[string]dbus.Variant{
		"session_handle_token": dbus.MakeVariant(newRDToken()),
	})
	if err != nil {
		return rdSessionInfo{}, err
	}
	session, ok := rdSessionHandle(results)
	if !ok {
		return rdSessionInfo{}, errors.New("control: portal CreateSession returned no session handle")
	}
	p.mu.Lock()
	p.session = session
	p.mu.Unlock()

	devices := map[string]dbus.Variant{
		"types": dbus.MakeVariant(p.available & (rdDeviceKeyboard | rdDevicePointer)),
	}
	if p.version >= 2 {
		devices["persist_mode"] = dbus.MakeVariant(rdPersistUntilRevoked)
		if rdValidToken(restoreToken) {
			devices["restore_token"] = dbus.MakeVariant(restoreToken)
		}
	}
	if _, err := p.request(ctx, "SelectDevices", portalRemoteDesktop+".SelectDevices", []any{session}, devices); err != nil {
		return rdSessionInfo{}, err
	}
	if p.cast {
		// A monitor stream gives NotifyPointerMotionAbsolute a coordinate
		// space; without it we fall back to relative motion.
		if _, err := p.request(ctx, "SelectSources", portalScreenCast+".SelectSources", []any{session}, map[string]dbus.Variant{
			"types":    dbus.MakeVariant(rdScreenCastMonitor),
			"multiple": dbus.MakeVariant(false),
		}); err != nil {
			if ctx.Err() != nil || errors.Is(err, ErrWaylandRemoteInputDenied) {
				return rdSessionInfo{}, err
			}
			p.cast = false
		}
	}
	results, err = p.request(ctx, "Start", portalRemoteDesktop+".Start", []any{session, ""}, map[string]dbus.Variant{})
	if err != nil {
		return rdSessionInfo{}, err
	}
	return rdParseStartResults(results), nil
}

func (p *dbusRDPortal) notify(method string, args ...any) error {
	p.mu.Lock()
	session := p.session
	p.mu.Unlock()
	if session == "" {
		return ErrWaylandRemoteInputClosed
	}
	ctx, cancel := context.WithTimeout(context.Background(), rdNotifyTimeout)
	defer cancel()
	callArgs := append([]any{session, map[string]dbus.Variant{}}, args...)
	return p.obj.CallWithContext(ctx, portalRemoteDesktop+"."+method, 0, callArgs...).Err
}

func (p *dbusRDPortal) PointerMotion(dx, dy float64) error {
	return p.notify("NotifyPointerMotion", dx, dy)
}

func (p *dbusRDPortal) PointerMotionAbsolute(stream uint32, x, y float64) error {
	return p.notify("NotifyPointerMotionAbsolute", stream, x, y)
}

func (p *dbusRDPortal) PointerButton(button int32, pressed bool) error {
	return p.notify("NotifyPointerButton", button, rdState(pressed))
}

func (p *dbusRDPortal) PointerAxisDiscrete(axis uint32, steps int32) error {
	return p.notify("NotifyPointerAxisDiscrete", axis, steps)
}

func (p *dbusRDPortal) KeyboardKeycode(keycode int32, pressed bool) error {
	return p.notify("NotifyKeyboardKeycode", keycode, rdState(pressed))
}

func rdState(pressed bool) uint32 {
	if pressed {
		return 1
	}
	return 0
}

func (p *dbusRDPortal) Close() error {
	var err error
	p.closeOnce.Do(func() {
		p.mu.Lock()
		session := p.session
		p.session = ""
		p.mu.Unlock()
		if session != "" {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_ = p.conn.Object(portalDest, session).CallWithContext(ctx, portalSession+".Close", 0).Err
			cancel()
		}
		p.markDone()
		p.conn.RemoveSignal(p.signals)
		err = p.conn.Close()
	})
	return err
}

// Restore-token persistence: the portal returns a fresh single-use token on
// every successful Start; storing it lets the next session start without a
// dialog until the student revokes the permission.

func loadRDRestoreToken() string {
	path, err := rdRestoreTokenPath(os.Getenv)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	token := strings.TrimSpace(string(data))
	if !rdValidToken(token) {
		return ""
	}
	return token
}

func saveRDRestoreToken(token string) error {
	if !rdValidToken(token) {
		return errors.New("control: invalid portal restore token")
	}
	path, err := rdRestoreTokenPath(os.Getenv)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".restore-token-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(token + "\n"); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// newLinuxPortalInputBackend builds the Wayland portal input backend. The bus
// connection and the authorization dialog are deferred until the first
// remote input (or an explicit AuthorizeRemoteInput call), so starting the
// student client never pops up a dialog.
func newLinuxPortalInputBackend() *rdInputBackend {
	backend := newRDInputBackend(newDBusRDPortal)
	backend.loadToken = loadRDRestoreToken
	backend.saveToken = saveRDRestoreToken
	backend.extent = linuxX11ScreenSize
	return backend
}
