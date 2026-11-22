//go:build linux

package control

// Linux power control.
//
// Shutdown/reboot go through org.freedesktop.login1 (systemd-logind or
// elogind) with interactive=false, after asking CanPowerOff/CanReboot so a
// polkit refusal is reported clearly instead of hanging on a password prompt.
// Without a reachable login1 the classic commands are used.
//
// Logoff asks the desktop session manager (GNOME/UKUI/MATE/Cinnamon/Budgie via
// org.gnome.SessionManager, KDE, DDE, XFCE, LXQt) to end the session, then
// falls back to terminating only this graphical logind session. Other
// sessions of the same user (SSH, other seats) are never killed.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

var (
	ErrPowerUnsupportedPlatform = errors.New("control: Linux power action is unavailable")
	// ErrLinuxPowerNotAuthorized means polkit/logind refused the action for
	// this (non-administrator) user without interactive authentication.
	ErrLinuxPowerNotAuthorized = errors.New("control: Linux power action requires administrator (polkit) authorization; add a polkit rule for org.freedesktop.login1.power-off / reboot for the student account")
	// ErrLinuxPowerNotPermitted means logind reports the action as not
	// available (CanPowerOff/CanReboot = "no" or "na").
	ErrLinuxPowerNotPermitted = errors.New("control: Linux power action is not permitted on this system")
)

var linuxPowerLookPath = exec.LookPath

const linuxPowerDBusTimeout = 10 * time.Second

// linuxDBusCaller performs one D-Bus method call and returns the reply body.
type linuxDBusCaller interface {
	Call(system bool, dest string, path dbus.ObjectPath, method string, args ...any) ([]any, error)
}

type linuxPowerExecutor struct {
	command func(context.Context, string, ...string) error
	bus     linuxDBusCaller
	getenv  func(string) string
	pid     int
	uid     int
}

func newPlatformPowerExecutor() PowerExecutor {
	return &linuxPowerExecutor{command: runLinuxPowerCommand, bus: linuxSharedDBus{}, getenv: os.Getenv, pid: os.Getpid(), uid: os.Getuid()}
}

func (e *linuxPowerExecutor) Shutdown(timeoutSec uint32, reason string) error {
	return e.powerAction("shutdown", timeoutSec, reason)
}

func (e *linuxPowerExecutor) Reboot(timeoutSec uint32, reason string) error {
	return e.powerAction("reboot", timeoutSec, reason)
}

func (e *linuxPowerExecutor) powerAction(action string, timeoutSec uint32, reason string) error {
	if e.bus != nil {
		handled, err := e.login1PowerAction(action, timeoutSec)
		if handled {
			return err
		}
	}
	return e.runLinuxAction(action, timeoutSec, reason)
}

// login1PowerAction returns handled=false when login1 is unreachable so the
// caller can fall back to commands; any answer from logind is final.
func (e *linuxPowerExecutor) login1PowerAction(action string, timeoutSec uint32) (bool, error) {
	method, scheduleType := "PowerOff", "poweroff"
	if action == "reboot" {
		method, scheduleType = "Reboot", "reboot"
	}
	const dest, path = "org.freedesktop.login1", dbus.ObjectPath("/org/freedesktop/login1")
	body, err := e.bus.Call(true, dest, path, "org.freedesktop.login1.Manager.Can"+method)
	if err != nil {
		if linuxDBusUnreachable(err) {
			return false, nil
		}
		return true, linuxPowerDBusError(action, err)
	}
	answer := ""
	if len(body) > 0 {
		answer, _ = body[0].(string)
	}
	if err := linuxPowerCanAnswerError(action, answer); err != nil {
		return true, err
	}
	if timeoutSec > 0 {
		when := uint64(time.Now().Add(time.Duration(timeoutSec) * time.Second).UnixMicro())
		_, err = e.bus.Call(true, dest, path, "org.freedesktop.login1.Manager.ScheduleShutdown", scheduleType, when)
	} else {
		_, err = e.bus.Call(true, dest, path, "org.freedesktop.login1.Manager."+method, false)
	}
	if err != nil {
		return true, linuxPowerDBusError(action, err)
	}
	return true, nil
}

// linuxPowerCanAnswerError maps logind CanPowerOff/CanReboot answers.
func linuxPowerCanAnswerError(action, answer string) error {
	switch answer {
	case "yes":
		return nil
	case "challenge", "auth-required":
		return fmt.Errorf("%w (%s)", ErrLinuxPowerNotAuthorized, action)
	case "no", "na", "":
		return fmt.Errorf("%w: logind answered %q for %s", ErrLinuxPowerNotPermitted, answer, action)
	default:
		return fmt.Errorf("%w: unexpected logind answer %q for %s", ErrLinuxPowerNotPermitted, answer, action)
	}
}

func linuxPowerDBusError(action string, err error) error {
	if name, ok := linuxDBusErrorName(err); ok {
		switch name {
		case "org.freedesktop.DBus.Error.InteractiveAuthorizationRequired", "org.freedesktop.DBus.Error.AccessDenied", "org.freedesktop.PolicyKit1.Error.NotAuthorized":
			return fmt.Errorf("%w (%s): %v", ErrLinuxPowerNotAuthorized, action, err)
		}
	}
	return fmt.Errorf("control: Linux %s failed: %w", action, err)
}

// linuxDBusUnreachable reports errors meaning "the service/bus is not there"
// (as opposed to a refusal by the service).
func linuxDBusUnreachable(err error) bool {
	if name, ok := linuxDBusErrorName(err); ok {
		switch name {
		case "org.freedesktop.DBus.Error.ServiceUnknown", "org.freedesktop.DBus.Error.NameHasNoOwner",
			"org.freedesktop.DBus.Error.UnknownMethod", "org.freedesktop.DBus.Error.UnknownObject",
			"org.freedesktop.DBus.Error.UnknownInterface", "org.freedesktop.DBus.Error.Spawn.ServiceNotFound":
			return true
		}
		return false
	}
	var unreachable linuxBusUnavailableError
	return errors.As(err, &unreachable)
}

// linuxDBusErrorName returns the D-Bus error name of err (godbus reports
// method errors as dbus.Error values, and some APIs as *dbus.Error).
func linuxDBusErrorName(err error) (string, bool) {
	var value dbus.Error
	if errors.As(err, &value) {
		return value.Name, true
	}
	var ptr *dbus.Error
	if errors.As(err, &ptr) && ptr != nil {
		return ptr.Name, true
	}
	return "", false
}

type linuxBusUnavailableError struct{ err error }

func (e linuxBusUnavailableError) Error() string {
	return "control: D-Bus unavailable: " + e.err.Error()
}
func (e linuxBusUnavailableError) Unwrap() error { return e.err }

// linuxLogoutTarget is a desktop session-manager logout method.
type linuxLogoutTarget struct {
	dest   string
	path   dbus.ObjectPath
	method string
	args   []any
}

// linuxLogoutTargets lists session-manager logout calls in preference order.
// Each is only attempted when its bus name currently has an owner.
var linuxLogoutTargets = []linuxLogoutTarget{
	// GNOME, and session managers implementing the GNOME API (MATE,
	// Cinnamon, Budgie, UKUI). Mode 1 = no confirmation dialog.
	{dest: "org.gnome.SessionManager", path: "/org/gnome/SessionManager", method: "org.gnome.SessionManager.Logout", args: []any{uint32(1)}},
	// UKUI native interface.
	{dest: "org.ukui.SessionManager", path: "/org/ukui/SessionManager", method: "org.ukui.SessionManager.logout"},
	// KDE Plasma 5.25+ / 6.
	{dest: "org.kde.Shutdown", path: "/Shutdown", method: "org.kde.Shutdown.logout"},
	// KDE Plasma 5 (confirm=0 none, sdtype=0 logout, sdmode=0 default).
	{dest: "org.kde.ksmserver", path: "/KSMServer", method: "org.kde.KSMServerInterface.logout", args: []any{int32(0), int32(0), int32(0)}},
	// Deepin DDE v23 and v20.
	{dest: "org.deepin.dde.SessionManager1", path: "/org/deepin/dde/SessionManager1", method: "org.deepin.dde.SessionManager1.ForceLogout"},
	{dest: "com.deepin.SessionManager", path: "/com/deepin/SessionManager", method: "com.deepin.SessionManager.ForceLogout"},
	// XFCE (show_dialog=false, allow_save=true).
	{dest: "org.xfce.SessionManager", path: "/org/xfce/SessionManager", method: "org.xfce.Session.Manager.Logout", args: []any{false, true}},
	// LXQt.
	{dest: "org.lxqt.session", path: "/LXQtSession", method: "org.lxqt.session.logout"},
}

func (e *linuxPowerExecutor) Logoff() error {
	var errs []error
	if e.bus != nil {
		if handled, err := e.sessionManagerLogout(); handled && err == nil {
			return nil
		} else if err != nil {
			errs = append(errs, err)
		}
		if err := e.login1TerminateSession(); err == nil {
			return nil
		} else {
			errs = append(errs, err)
		}
	}
	if path, err := linuxPowerLookPath("loginctl"); err == nil {
		if id := e.sessionID(); id != "" {
			if err := e.command(context.Background(), path, "terminate-session", id); err != nil {
				return errors.Join(append(errs, fmt.Errorf("control: loginctl terminate-session %s: %w", id, err))...)
			}
			return nil
		}
	}
	if len(errs) == 0 {
		return fmt.Errorf("%w: no desktop session manager or logind session found for logoff", ErrPowerUnsupportedPlatform)
	}
	return errors.Join(append([]error{fmt.Errorf("%w: logoff failed", ErrPowerUnsupportedPlatform)}, errs...)...)
}

func (e *linuxPowerExecutor) sessionManagerLogout() (bool, error) {
	var errs []error
	for _, target := range linuxLogoutTargets {
		body, err := e.bus.Call(false, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus.NameHasOwner", target.dest)
		if err != nil {
			if linuxDBusUnreachable(err) {
				return false, nil
			}
			return false, fmt.Errorf("control: query session bus: %w", err)
		}
		if len(body) == 0 {
			continue
		}
		if owned, _ := body[0].(bool); !owned {
			continue
		}
		if _, err := e.bus.Call(false, target.dest, target.path, target.method, target.args...); err != nil {
			errs = append(errs, fmt.Errorf("control: %s: %w", target.method, err))
			continue
		}
		return true, nil
	}
	if len(errs) > 0 {
		return true, errors.Join(errs...)
	}
	return false, nil
}

// login1TerminateSession terminates only this app's graphical session.
func (e *linuxPowerExecutor) login1TerminateSession() error {
	const dest = "org.freedesktop.login1"
	var session dbus.ObjectPath
	if id := linuxValidSessionID(e.env("XDG_SESSION_ID")); id != "" {
		body, err := e.bus.Call(true, dest, "/org/freedesktop/login1", "org.freedesktop.login1.Manager.GetSession", id)
		if err == nil && len(body) > 0 {
			session, _ = body[0].(dbus.ObjectPath)
		}
	}
	if session == "" {
		body, err := e.bus.Call(true, dest, "/org/freedesktop/login1", "org.freedesktop.login1.Manager.GetSessionByPID", uint32(e.pid))
		if err == nil && len(body) > 0 {
			session, _ = body[0].(dbus.ObjectPath)
		}
	}
	if session == "" {
		session = e.userDisplaySession()
	}
	if session == "" || !session.IsValid() {
		return errors.New("control: no logind session found for this user")
	}
	if _, err := e.bus.Call(true, dest, session, "org.freedesktop.login1.Session.Terminate"); err != nil {
		return linuxPowerDBusError("logoff", err)
	}
	return nil
}

// userDisplaySession returns the user's primary graphical session (User.Display),
// used when the app runs outside a session (systemd user service).
func (e *linuxPowerExecutor) userDisplaySession() dbus.ObjectPath {
	const dest = "org.freedesktop.login1"
	body, err := e.bus.Call(true, dest, "/org/freedesktop/login1", "org.freedesktop.login1.Manager.GetUser", uint32(e.uid))
	if err != nil || len(body) == 0 {
		return ""
	}
	user, _ := body[0].(dbus.ObjectPath)
	if user == "" {
		return ""
	}
	body, err = e.bus.Call(true, dest, user, "org.freedesktop.DBus.Properties.Get", "org.freedesktop.login1.User", "Display")
	if err != nil || len(body) == 0 {
		return ""
	}
	variant, ok := body[0].(dbus.Variant)
	if !ok {
		return ""
	}
	return linuxDisplaySessionPath(variant.Value())
}

// linuxDisplaySessionPath decodes the (so) User.Display property.
func linuxDisplaySessionPath(value any) dbus.ObjectPath {
	fields, ok := value.([]any)
	if !ok || len(fields) < 2 {
		return ""
	}
	path, _ := fields[1].(dbus.ObjectPath)
	if path == "/" {
		return ""
	}
	return path
}

func (e *linuxPowerExecutor) env(key string) string {
	if e.getenv == nil {
		return os.Getenv(key)
	}
	return e.getenv(key)
}

func (e *linuxPowerExecutor) sessionID() string {
	return linuxValidSessionID(e.env("XDG_SESSION_ID"))
}

func (e *linuxPowerExecutor) runLinuxAction(action string, timeoutSec uint32, reason string) error {
	if name, args, ok := linuxPowerCommand(action, timeoutSec, reason); ok {
		if err := e.command(context.Background(), name, args...); err != nil {
			return linuxPowerCommandError(action, err)
		}
		return nil
	}
	return fmt.Errorf("%w: no command for %s", ErrPowerUnsupportedPlatform, action)
}

// linuxPowerCommandError marks polkit/permission failures of the fallback
// commands so the UI can show an actionable message.
func linuxPowerCommandError(action string, err error) error {
	text := strings.ToLower(err.Error())
	for _, marker := range []string{"interactive authentication required", "access denied", "not authorized", "permission denied", "must be root", "operation not permitted"} {
		if strings.Contains(text, marker) {
			return fmt.Errorf("%w (%s): %v", ErrLinuxPowerNotAuthorized, action, err)
		}
	}
	return fmt.Errorf("control: Linux %s failed: %w", action, err)
}

// linuxPowerCommand builds the fallback command. systemctl only knows
// "poweroff"/"reboot" (there is no "systemctl shutdown") and has no delay, so
// delayed actions use shutdown(8) with whole minutes.
func linuxPowerCommand(action string, timeoutSec uint32, reason string) (string, []string, bool) {
	verb, flag := "poweroff", "-P"
	if action == "reboot" {
		verb, flag = "reboot", "-r"
	} else if action != "shutdown" {
		return "", nil, false
	}
	if timeoutSec == 0 {
		if path, err := linuxPowerLookPath("systemctl"); err == nil {
			args := []string{verb, "--no-ask-password"}
			if reason != "" {
				args = append(args, "--message="+reason)
			}
			return path, args, true
		}
	}
	if path, err := linuxPowerLookPath("shutdown"); err == nil {
		delay := "now"
		if timeoutSec > 0 {
			delay = "+" + strconv.FormatUint(uint64((timeoutSec+59)/60), 10)
		}
		args := []string{flag, delay}
		if reason != "" {
			args = append(args, reason)
		}
		return path, args, true
	}
	if timeoutSec == 0 {
		if path, err := linuxPowerLookPath(verb); err == nil {
			return path, []string{}, true
		}
	}
	return "", nil, false
}

func runLinuxPowerCommand(parent context.Context, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		if len(output) > 0 {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
		}
		return err
	}
	return nil
}

// linuxSharedDBus calls through the process-wide shared system/session bus
// connections (never closed here; other components may share them).
type linuxSharedDBus struct{}

func (linuxSharedDBus) Call(system bool, dest string, path dbus.ObjectPath, method string, args ...any) ([]any, error) {
	var conn *dbus.Conn
	var err error
	if system {
		conn, err = dbus.SystemBus()
	} else {
		conn, err = dbus.SessionBus()
	}
	if err != nil {
		return nil, linuxBusUnavailableError{err: err}
	}
	ctx, cancel := context.WithTimeout(context.Background(), linuxPowerDBusTimeout)
	defer cancel()
	call := conn.Object(dest, path).CallWithContext(ctx, method, 0, args...)
	if call.Err != nil {
		return nil, call.Err
	}
	return call.Body, nil
}
