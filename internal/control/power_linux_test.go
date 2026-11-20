//go:build linux

package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

type fakeLinuxBus struct {
	calls   []string
	replies map[string][]any
	errs    map[string]error
}

func (f *fakeLinuxBus) Call(system bool, dest string, path dbus.ObjectPath, method string, args ...any) ([]any, error) {
	key := method
	if method == "org.freedesktop.DBus.NameHasOwner" && len(args) == 1 {
		key = method + " " + fmt.Sprint(args[0])
	}
	f.calls = append(f.calls, strings.TrimSpace(fmt.Sprintf("%s %s %v", method, path, args)))
	if err, ok := f.errs[key]; ok {
		return nil, err
	}
	if reply, ok := f.replies[key]; ok {
		return reply, nil
	}
	if strings.HasPrefix(method, "org.freedesktop.DBus.NameHasOwner") {
		return []any{false}, nil
	}
	return nil, nil
}

func (f *fakeLinuxBus) called(prefix string) bool {
	for _, call := range f.calls {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}
	return false
}

func noCommand(context.Context, string, ...string) error {
	return errors.New("command must not run")
}

func TestLinuxPowerUsesLogin1WhenAllowed(t *testing.T) {
	bus := &fakeLinuxBus{replies: map[string][]any{"org.freedesktop.login1.Manager.CanPowerOff": {"yes"}}}
	executor := &linuxPowerExecutor{command: noCommand, bus: bus}
	if err := executor.Shutdown(0, "lesson ended"); err != nil {
		t.Fatal(err)
	}
	if !bus.called("org.freedesktop.login1.Manager.PowerOff /org/freedesktop/login1 [false]") {
		t.Fatalf("PowerOff not called non-interactively: %v", bus.calls)
	}
}

func TestLinuxPowerDelayedRebootSchedules(t *testing.T) {
	bus := &fakeLinuxBus{replies: map[string][]any{"org.freedesktop.login1.Manager.CanReboot": {"yes"}}}
	executor := &linuxPowerExecutor{command: noCommand, bus: bus}
	if err := executor.Reboot(60, ""); err != nil {
		t.Fatal(err)
	}
	if !bus.called("org.freedesktop.login1.Manager.ScheduleShutdown /org/freedesktop/login1 [reboot ") {
		t.Fatalf("ScheduleShutdown not called: %v", bus.calls)
	}
}

func TestLinuxPowerPolkitChallengeIsClearError(t *testing.T) {
	bus := &fakeLinuxBus{replies: map[string][]any{"org.freedesktop.login1.Manager.CanPowerOff": {"challenge"}}}
	executor := &linuxPowerExecutor{command: noCommand, bus: bus}
	if err := executor.Shutdown(0, ""); !errors.Is(err, ErrLinuxPowerNotAuthorized) {
		t.Fatalf("err = %v, want ErrLinuxPowerNotAuthorized", err)
	}
	if bus.called("org.freedesktop.login1.Manager.PowerOff") {
		t.Fatal("PowerOff attempted despite challenge")
	}
}

func TestLinuxPowerCanAnswers(t *testing.T) {
	if err := linuxPowerCanAnswerError("shutdown", "na"); !errors.Is(err, ErrLinuxPowerNotPermitted) {
		t.Fatalf("na err = %v", err)
	}
	if err := linuxPowerCanAnswerError("shutdown", "no"); !errors.Is(err, ErrLinuxPowerNotPermitted) {
		t.Fatalf("no err = %v", err)
	}
	if err := linuxPowerCanAnswerError("shutdown", "yes"); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxPowerAccessDeniedFromLogin1(t *testing.T) {
	bus := &fakeLinuxBus{
		replies: map[string][]any{"org.freedesktop.login1.Manager.CanReboot": {"yes"}},
		errs:    map[string]error{"org.freedesktop.login1.Manager.Reboot": dbus.Error{Name: "org.freedesktop.DBus.Error.InteractiveAuthorizationRequired"}},
	}
	executor := &linuxPowerExecutor{command: noCommand, bus: bus}
	if err := executor.Reboot(0, ""); !errors.Is(err, ErrLinuxPowerNotAuthorized) {
		t.Fatalf("err = %v", err)
	}
}

func TestLinuxPowerFallsBackToCommandsWithoutLogin1(t *testing.T) {
	original := linuxPowerLookPath
	defer func() { linuxPowerLookPath = original }()
	linuxPowerLookPath = func(name string) (string, error) {
		if name == "systemctl" {
			return "/usr/bin/systemctl", nil
		}
		return "", errors.New("not found")
	}
	bus := &fakeLinuxBus{errs: map[string]error{"org.freedesktop.login1.Manager.CanPowerOff": dbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown"}}}
	var got string
	executor := &linuxPowerExecutor{bus: bus, command: func(_ context.Context, name string, args ...string) error {
		got = name + " " + strings.Join(args, " ")
		return nil
	}}
	if err := executor.Shutdown(0, "bye"); err != nil {
		t.Fatal(err)
	}
	// Regression: "systemctl shutdown" is not a systemctl verb.
	if got != "/usr/bin/systemctl poweroff --no-ask-password --message=bye" {
		t.Fatalf("command = %q", got)
	}
}

func TestLinuxPowerCommandFallsBackWithoutSystemctl(t *testing.T) {
	original := linuxPowerLookPath
	defer func() { linuxPowerLookPath = original }()
	linuxPowerLookPath = func(name string) (string, error) {
		if name == "shutdown" {
			return "/sbin/shutdown", nil
		}
		return "", errors.New("not found")
	}
	name, args, ok := linuxPowerCommand("shutdown", 61, "lesson ended")
	if !ok || name != "/sbin/shutdown" || fmt.Sprint(args) != "[-P +2 lesson ended]" {
		t.Fatalf("unexpected fallback command: name=%q args=%v ok=%v", name, args, ok)
	}
	name, args, ok = linuxPowerCommand("reboot", 0, "")
	if !ok || name != "/sbin/shutdown" || fmt.Sprint(args) != "[-r now]" {
		t.Fatalf("unexpected reboot command: name=%q args=%v ok=%v", name, args, ok)
	}
}

func TestLinuxPowerCommandPrefersSystemctlOnlyWithoutDelay(t *testing.T) {
	original := linuxPowerLookPath
	defer func() { linuxPowerLookPath = original }()
	linuxPowerLookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	name, args, _ := linuxPowerCommand("reboot", 0, "")
	if name != "/usr/bin/systemctl" || fmt.Sprint(args) != "[reboot --no-ask-password]" {
		t.Fatalf("name=%q args=%v", name, args)
	}
	name, args, _ = linuxPowerCommand("shutdown", 120, "")
	if name != "/usr/bin/shutdown" || fmt.Sprint(args) != "[-P +2]" {
		t.Fatalf("delayed name=%q args=%v", name, args)
	}
	if _, _, ok := linuxPowerCommand("hibernate", 0, ""); ok {
		t.Fatal("unknown action accepted")
	}
}

func TestLinuxPowerCommandPermissionErrorIsClassified(t *testing.T) {
	err := linuxPowerCommandError("shutdown", errors.New("exit status 1: Interactive authentication required."))
	if !errors.Is(err, ErrLinuxPowerNotAuthorized) {
		t.Fatalf("err = %v", err)
	}
	if err := linuxPowerCommandError("shutdown", errors.New("boom")); errors.Is(err, ErrLinuxPowerNotAuthorized) {
		t.Fatalf("generic failure classified as polkit: %v", err)
	}
}

func TestLinuxPowerExecutorReportsUnavailableCommand(t *testing.T) {
	original := linuxPowerLookPath
	defer func() { linuxPowerLookPath = original }()
	linuxPowerLookPath = func(string) (string, error) { return "", errors.New("not found") }
	executor := &linuxPowerExecutor{command: func(context.Context, string, ...string) error { return nil }}
	if err := executor.Shutdown(0, ""); !errors.Is(err, ErrPowerUnsupportedPlatform) {
		t.Fatalf("Shutdown error = %v, want ErrPowerUnsupportedPlatform", err)
	}
}

func TestLinuxLogoffPrefersDesktopSessionManager(t *testing.T) {
	bus := &fakeLinuxBus{replies: map[string][]any{"org.freedesktop.DBus.NameHasOwner org.kde.Shutdown": {true}}}
	executor := &linuxPowerExecutor{command: noCommand, bus: bus, getenv: func(string) string { return "" }}
	if err := executor.Logoff(); err != nil {
		t.Fatal(err)
	}
	if !bus.called("org.kde.Shutdown.logout /Shutdown") {
		t.Fatalf("KDE logout not called: %v", bus.calls)
	}
	if bus.called("org.freedesktop.login1.Session.Terminate") {
		t.Fatal("session terminated although the DE logout succeeded")
	}
}

func TestLinuxLogoffGnomeNoConfirmation(t *testing.T) {
	bus := &fakeLinuxBus{replies: map[string][]any{"org.freedesktop.DBus.NameHasOwner org.gnome.SessionManager": {true}}}
	executor := &linuxPowerExecutor{command: noCommand, bus: bus, getenv: func(string) string { return "" }}
	if err := executor.Logoff(); err != nil {
		t.Fatal(err)
	}
	if !bus.called("org.gnome.SessionManager.Logout /org/gnome/SessionManager [1]") {
		t.Fatalf("GNOME logout not called with mode 1: %v", bus.calls)
	}
}

func TestLinuxLogoffFallsBackToOwnLogindSession(t *testing.T) {
	bus := &fakeLinuxBus{replies: map[string][]any{
		"org.freedesktop.login1.Manager.GetSession": {dbus.ObjectPath("/org/freedesktop/login1/session/_34")},
	}}
	executor := &linuxPowerExecutor{command: noCommand, bus: bus, getenv: func(key string) string {
		if key == "XDG_SESSION_ID" {
			return "4"
		}
		return ""
	}}
	if err := executor.Logoff(); err != nil {
		t.Fatal(err)
	}
	if !bus.called("org.freedesktop.login1.Session.Terminate /org/freedesktop/login1/session/_34") {
		t.Fatalf("own session not terminated: %v", bus.calls)
	}
	for _, call := range bus.calls {
		if strings.Contains(call, "TerminateUser") {
			t.Fatalf("whole user terminated: %v", bus.calls)
		}
	}
}

func TestLinuxLogoffUsesDisplaySessionOutsideSession(t *testing.T) {
	bus := &fakeLinuxBus{
		replies: map[string][]any{
			"org.freedesktop.login1.Manager.GetUser":   {dbus.ObjectPath("/org/freedesktop/login1/user/_1000")},
			"org.freedesktop.DBus.Properties.Get":      {dbus.MakeVariant([]any{"c2", dbus.ObjectPath("/org/freedesktop/login1/session/c2")})},
			"org.freedesktop.login1.Session.Terminate": nil,
		},
		errs: map[string]error{"org.freedesktop.login1.Manager.GetSessionByPID": dbus.Error{Name: "org.freedesktop.login1.NoSessionForPID"}},
	}
	executor := &linuxPowerExecutor{command: noCommand, bus: bus, getenv: func(string) string { return "" }, uid: 1000, pid: 42}
	if err := executor.Logoff(); err != nil {
		t.Fatal(err)
	}
	if !bus.called("org.freedesktop.login1.Session.Terminate /org/freedesktop/login1/session/c2") {
		t.Fatalf("display session not terminated: %v", bus.calls)
	}
}

func TestLinuxLogoffReportsFailure(t *testing.T) {
	original := linuxPowerLookPath
	defer func() { linuxPowerLookPath = original }()
	linuxPowerLookPath = func(string) (string, error) { return "", errors.New("not found") }
	bus := &fakeLinuxBus{errs: map[string]error{
		"org.freedesktop.login1.Manager.GetSessionByPID": dbus.Error{Name: "org.freedesktop.login1.NoSessionForPID"},
		"org.freedesktop.login1.Manager.GetUser":         dbus.Error{Name: "org.freedesktop.login1.NoSuchUser"},
	}}
	executor := &linuxPowerExecutor{command: noCommand, bus: bus, getenv: func(string) string { return "" }}
	if err := executor.Logoff(); !errors.Is(err, ErrPowerUnsupportedPlatform) {
		t.Fatalf("err = %v", err)
	}
}

func TestLinuxDisplaySessionPath(t *testing.T) {
	if got := linuxDisplaySessionPath([]any{"", dbus.ObjectPath("/")}); got != "" {
		t.Fatalf("no display session -> %q", got)
	}
	if got := linuxDisplaySessionPath("bad"); got != "" {
		t.Fatalf("bad value -> %q", got)
	}
}
