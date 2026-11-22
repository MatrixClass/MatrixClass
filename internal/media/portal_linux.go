//go:build linux

package media

// xdg-desktop-portal client (session bus, godbus): ScreenCast
// CreateSession -> SelectSources -> Start -> OpenPipeWireRemote, plus the
// non-interactive Screenshot fallback.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	// portalSetupTimeout bounds non-interactive portal requests.
	portalSetupTimeout = 30 * time.Second
	// portalScreenshotTimeout bounds one Screenshot request (may show a
	// one-time permission prompt).
	portalScreenshotTimeout = 2 * time.Minute
	portalMaxScreenshotSize = 256 << 20
)

type portalClient struct {
	conn    *dbus.Conn
	unique  string
	desktop dbus.BusObject
	signals chan *dbus.Signal

	mu            sync.Mutex
	waiters       map[dbus.ObjectPath]chan *dbus.Signal
	session       dbus.ObjectPath
	sessionClosed chan struct{}
	closedOnce    sync.Once
	closeOnce     sync.Once
}

func portalConnect() (*portalClient, error) {
	conn, err := dbus.SessionBusPrivateNoAutoStartup()
	if err != nil {
		return nil, fmt.Errorf("%w: session bus: %v", ErrWaylandPortalUnavailable, err)
	}
	if err := conn.Auth(nil); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: session bus auth: %v", ErrWaylandPortalUnavailable, err)
	}
	if err := conn.Hello(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: session bus hello: %v", ErrWaylandPortalUnavailable, err)
	}
	names := conn.Names()
	if len(names) == 0 {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: no unique bus name", ErrWaylandPortalUnavailable)
	}
	p := &portalClient{
		conn:          conn,
		unique:        names[0],
		desktop:       conn.Object(portalBusName, portalObjectPath),
		signals:       make(chan *dbus.Signal, 16),
		waiters:       make(map[dbus.ObjectPath]chan *dbus.Signal),
		sessionClosed: make(chan struct{}),
	}
	conn.Signal(p.signals)
	go p.dispatch()
	return p, nil
}

func (p *portalClient) dispatch() {
	for sig := range p.signals {
		switch sig.Name {
		case portalRequestIface + ".Response":
			p.mu.Lock()
			ch := p.waiters[sig.Path]
			p.mu.Unlock()
			if ch != nil {
				select {
				case ch <- sig:
				default:
				}
			}
		case portalSessionIface + ".Closed":
			p.mu.Lock()
			match := sig.Path == p.session
			p.mu.Unlock()
			if match {
				p.closedOnce.Do(func() { close(p.sessionClosed) })
			}
		}
	}
	// The connection ended (Close, bus or portal restart): the portal session
	// cannot outlive its D-Bus connection, so report it closed.
	p.closedOnce.Do(func() { close(p.sessionClosed) })
}

func (p *portalClient) close() {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		session := p.session
		p.mu.Unlock()
		if session != "" {
			_ = p.conn.Object(portalBusName, session).Call(portalSessionIface+".Close", 0).Err
		}
		_ = p.conn.Close()
	})
}

func (p *portalClient) property(iface, name string) (dbus.Variant, error) {
	v, err := p.desktop.GetProperty(iface + "." + name)
	return v, portalMapCallError(err)
}

func (p *portalClient) uint32Property(iface, name string) (uint32, error) {
	v, err := p.property(iface, name)
	if err != nil {
		return 0, err
	}
	n, ok := portalUint32(v)
	if !ok {
		return 0, fmt.Errorf("media: portal property %s.%s has type %s", iface, name, v.Signature())
	}
	return n, nil
}

func (p *portalClient) watch(path dbus.ObjectPath) (chan *dbus.Signal, func(), error) {
	ch := make(chan *dbus.Signal, 1)
	opts := []dbus.MatchOption{dbus.WithMatchObjectPath(path), dbus.WithMatchInterface(portalRequestIface), dbus.WithMatchMember("Response")}
	if err := p.conn.AddMatchSignal(opts...); err != nil {
		return nil, nil, fmt.Errorf("%w: add match: %v", ErrWaylandPortalUnavailable, err)
	}
	p.mu.Lock()
	p.waiters[path] = ch
	p.mu.Unlock()
	return ch, func() {
		p.mu.Lock()
		delete(p.waiters, path)
		p.mu.Unlock()
		_ = p.conn.RemoveMatchSignal(opts...)
	}, nil
}

// request invokes a portal method that answers through a Request object and
// waits for its Response. options must not contain handle_token yet. The
// Response match is installed before the call to avoid racing the reply.
func (p *portalClient) request(ctx context.Context, timeout time.Duration, method string, options map[string]dbus.Variant, args ...any) (map[string]dbus.Variant, error) {
	token := portalNewToken()
	if options == nil {
		options = map[string]dbus.Variant{}
	}
	options["handle_token"] = dbus.MakeVariant(token)
	expected := portalRequestPath(p.unique, token)
	ch, unwatch, err := p.watch(expected)
	if err != nil {
		return nil, err
	}
	defer unwatch()
	var handle dbus.ObjectPath
	args = append(args, options)
	if err := p.desktop.CallWithContext(ctx, method, 0, args...).Store(&handle); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, portalMapCallError(err)
	}
	if handle != expected && handle.IsValid() {
		// Portals older than 0.9 ignore handle_token.
		other, unwatchOther, err := p.watch(handle)
		if err == nil {
			defer unwatchOther()
			ch = other
		}
	}
	var timer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timer = t.C
	}
	select {
	case sig := <-ch:
		if len(sig.Body) < 2 {
			return nil, fmt.Errorf("media: malformed portal %s response", method)
		}
		code, ok := portalUint32(sig.Body[0])
		if !ok {
			return nil, fmt.Errorf("media: malformed portal %s response code", method)
		}
		if err := portalResponseError(code); err != nil {
			return nil, err
		}
		results, _ := portalVariantMap(sig.Body[1])
		return results, nil
	case <-ctx.Done():
		_ = p.conn.Object(portalBusName, handle).Call(portalRequestIface+".Close", 0).Err
		return nil, ctx.Err()
	case <-timer:
		_ = p.conn.Object(portalBusName, handle).Call(portalRequestIface+".Close", 0).Err
		return nil, fmt.Errorf("%w: portal %s timed out", ErrWaylandPortalUnavailable, method)
	}
}

func (p *portalClient) screenCastConfig() (portalScreenCastConfig, error) {
	var cfg portalScreenCastConfig
	version, err := p.uint32Property(portalScreenCastIface, "version")
	if err != nil {
		return cfg, err
	}
	cfg.Version = version
	cfg.AvailableSourceTypes, _ = p.uint32Property(portalScreenCastIface, "AvailableSourceTypes")
	if cfg.AvailableSourceTypes != 0 && cfg.AvailableSourceTypes&portalSourceMonitor == 0 {
		return cfg, fmt.Errorf("%w (monitor capture not offered, types=%d)", errPortalInterfaceMissing, cfg.AvailableSourceTypes)
	}
	if version >= 2 {
		cfg.AvailableCursorModes, _ = p.uint32Property(portalScreenCastIface, "AvailableCursorModes")
	}
	return cfg, nil
}

// portalGrant is an authorized ScreenCast session.
type portalGrant struct {
	client  *portalClient
	session dbus.ObjectPath
	src     portalStream
	token   string
	version uint32
}

func (g *portalGrant) stream() portalStream    { return g.src }
func (g *portalGrant) restoreToken() string    { return g.token }
func (g *portalGrant) portalVersion() uint32   { return g.version }
func (g *portalGrant) closed() <-chan struct{} { return g.client.sessionClosed }
func (g *portalGrant) close()                  { g.client.close() }

func (g *portalGrant) openPipeWire(ctx context.Context) (int, error) {
	var fd dbus.UnixFD
	call := g.client.desktop.CallWithContext(ctx, portalScreenCastIface+".OpenPipeWireRemote", 0, g.session, map[string]dbus.Variant{})
	if err := call.Store(&fd); err != nil {
		return -1, fmt.Errorf("%w: OpenPipeWireRemote: %v", ErrWaylandPipeWireDisconnected, portalMapCallError(err))
	}
	if fd < 0 {
		return -1, fmt.Errorf("%w: OpenPipeWireRemote returned no fd", ErrWaylandPipeWireDisconnected)
	}
	return int(fd), nil
}

func portalStartScreenCast(ctx context.Context, restoreToken string) (*portalGrant, error) {
	client, err := portalConnect()
	if err != nil {
		return nil, err
	}
	grant, err := client.startScreenCast(ctx, restoreToken)
	if err != nil {
		client.close()
		return nil, err
	}
	return grant, nil
}

func (p *portalClient) startScreenCast(ctx context.Context, restoreToken string) (*portalGrant, error) {
	cfg, err := p.screenCastConfig()
	if err != nil {
		return nil, err
	}
	cfg.RestoreToken = restoreToken

	sessionToken := portalNewToken()
	// Register the expected session path first so an early Closed is seen.
	p.mu.Lock()
	p.session = portalSessionPath(p.unique, sessionToken)
	p.mu.Unlock()
	closedMatch := []dbus.MatchOption{dbus.WithMatchInterface(portalSessionIface), dbus.WithMatchMember("Closed")}
	if err := p.conn.AddMatchSignal(closedMatch...); err != nil {
		return nil, fmt.Errorf("%w: add match: %v", ErrWaylandPortalUnavailable, err)
	}

	setupCtx, cancel := context.WithTimeout(ctx, portalSetupTimeout)
	results, err := p.request(setupCtx, 0, portalScreenCastIface+".CreateSession",
		map[string]dbus.Variant{"session_handle_token": dbus.MakeVariant(sessionToken)})
	cancel()
	if err != nil {
		return nil, err
	}
	session, err := portalParseSessionHandle(results)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.session = session
	p.mu.Unlock()

	selectOpts := portalSelectSourcesOptions(cfg, "")
	delete(selectOpts, "handle_token")
	// Some backends show their chooser already on SelectSources; no timeout
	// beyond ctx for this and Start (the user may take a while).
	if _, err := p.request(ctx, 0, portalScreenCastIface+".SelectSources", selectOpts, session); err != nil {
		return nil, err
	}
	results, err = p.request(ctx, 0, portalScreenCastIface+".Start", nil, session, "")
	if err != nil {
		return nil, err
	}
	streams, newToken, err := portalParseStartResults(results)
	if err != nil {
		return nil, err
	}
	src, err := portalChooseStream(streams)
	if err != nil {
		return nil, err
	}
	return &portalGrant{client: p, session: session, src: src, token: newToken, version: cfg.Version}, nil
}

// --- driver ------------------------------------------------------------------

type waylandPortalDriver struct{}

func (waylandPortalDriver) streamAvailable() error { return pwAvailable() }

func (waylandPortalDriver) authorize(ctx context.Context, restoreToken string) (waylandGrant, error) {
	grant, err := portalStartScreenCast(ctx, restoreToken)
	if err != nil {
		return nil, err
	}
	return grant, nil
}

func (waylandPortalDriver) stream(ctx context.Context, fd int, nodeID uint32, sink *pwStreamSink) error {
	return pwRunStream(ctx, fd, nodeID, sink)
}

func (waylandPortalDriver) screenshotAvailable(context.Context) error {
	client, err := portalConnect()
	if err != nil {
		return err
	}
	defer client.close()
	_, err = client.uint32Property(portalScreenshotIface, "version")
	return err
}

// screenshot takes one non-interactive portal screenshot, decodes it and
// deletes the temporary file the portal wrote.
func (waylandPortalDriver) screenshot(ctx context.Context) ([]byte, int, int, error) {
	client, err := portalConnect()
	if err != nil {
		return nil, 0, 0, err
	}
	defer client.close()
	results, err := client.request(ctx, portalScreenshotTimeout, portalScreenshotIface+".Screenshot",
		map[string]dbus.Variant{"interactive": dbus.MakeVariant(false), "modal": dbus.MakeVariant(false)}, "")
	if err != nil {
		return nil, 0, 0, err
	}
	uri, _ := portalVariantValue(results["uri"]).(string)
	path, err := portalScreenshotPath(uri)
	if err != nil {
		return nil, 0, 0, err
	}
	defer func() { _ = os.Remove(path) }()
	return portalDecodeScreenshot(path)
}

func portalScreenshotPath(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" || u.Path == "" {
		return "", fmt.Errorf("media: unexpected screenshot uri %q", uri)
	}
	info, err := os.Lstat(u.Path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("media: screenshot %q is not a regular file", u.Path)
	}
	return u.Path, nil
}

func portalDecodeScreenshot(path string) ([]byte, int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, portalMaxScreenshotSize+1))
	if err != nil {
		return nil, 0, 0, err
	}
	if len(data) > portalMaxScreenshotSize {
		return nil, 0, 0, errors.New("media: screenshot file too large")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > pwMaxVideoDimension || cfg.Height > pwMaxVideoDimension {
		return nil, 0, 0, fmt.Errorf("%w: screenshot %dx%d", errPwBadGeometry, cfg.Width, cfg.Height)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, err
	}
	b := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)
	for i := 3; i < len(rgba.Pix); i += 4 {
		rgba.Pix[i] = 255
	}
	return rgba.Pix, b.Dx(), b.Dy(), nil
}
