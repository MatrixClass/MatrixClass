//go:build linux

package control

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

var (
	ErrLinuxProcessUnavailable = errors.New("control: Linux process is unavailable")
	ErrLinuxProcessZombie      = errors.New("control: refusing to terminate zombie Linux process")
	ErrLinuxProcessProtected   = errors.New("control: refusing to terminate protected Linux process")
)

type linuxProcessInspector struct {
	procRoot string
	selfPID  uint32
	// kill overrides signal delivery (tests).
	kill func(pid uint32) error
}

func newPlatformProcessInspector() ProcessInspector {
	return &linuxProcessInspector{procRoot: "/proc", selfPID: uint32(os.Getpid())}
}

// platformProtectedProcesses are Linux desktop-session/system processes that
// policy enforcement (especially whitelist mode) must never terminate. Names are
// /proc/<pid>/comm values, which the kernel truncates to 15 bytes.
var platformProtectedProcesses = []string{
	"init", "systemd", "systemd-*", "(sd-pam)", "dbus-daemon", "dbus-broker", "dbus-broker-lau",
	"polkitd", "polkit-*", "login", "agetty", "sshd", "networkmanager", "wpa_supplicant",
	"x", "xorg", "xwayland", "xdg-*", "at-spi*", "gvfs*", "dconf-service",
	"pipewire", "pipewire-pulse", "wireplumber", "pulseaudio",
	"gdm", "gdm3", "gdm-*", "lightdm", "sddm", "sddm-helper",
	"gnome-shell", "gnome-session*", "gnome-keyring-d", "gsd-*", "mutter*",
	"plasmashell", "kwin_x11", "kwin_wayland", "ksmserver", "kded5", "kded6", "kdeinit5",
	"xfce4-session", "xfwm4", "xfce4-panel", "xfdesktop", "xfsettingsd",
	"cinnamon", "cinnamon-session", "muffin", "mate-session", "marco", "mate-panel",
	"budgie-wm", "budgie-panel", "lxsession", "openbox", "lxqt-session",
	"ibus-daemon", "ibus-*", "fcitx", "fcitx5", "fcitx*", "sogou*",
	// Our own binaries: comm is truncated to 15 bytes ("matrixclass-stu"),
	// so the exact os.Executable() name added by policy.go does not match.
	"matrixclass*",
	// GNOME helpers whose truncated comm does not match "gnome-shell".
	"gnome-shell*",
	// KDE Plasma session helpers.
	"kglobalaccel*", "kwalletd*", "plasma_session", "startplasma-*", "ksplash*",
	// UKUI (Kylin).
	"ukui-session*", "ukui-kwin*", "ukui-panel", "ukui-menu", "ukui-sidebar", "ukui-settings-*",
	"ukui-screensave*", "peony-qt-deskto", "peony-qt-desktop",
	// Deepin DDE / UOS.
	"startdde", "dde-*", "deepin-kwin*", "deepin-wm", "kwin_wayland_wr",
	// Screen lockers: killing them could leave a locked session without UI.
	"gnome-screensav*", "kscreenlocker*", "light-locker", "xscreensaver",
}

func (p *linuxProcessInspector) ListProcesses() ([]ProcessInfo, error) {
	entries, err := os.ReadDir(p.procRoot)
	if err != nil {
		return nil, err
	}
	euid := uint32(os.Geteuid())
	type candidate struct {
		pid  uint32
		name string
	}
	candidates := make([]candidate, 0, len(entries))
	parents := make(map[uint32]uint32, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid64, err := strconv.ParseUint(entry.Name(), 10, 32)
		if err != nil || pid64 == 0 {
			continue
		}
		pid := uint32(pid64)
		procDir := filepath.Join(p.procRoot, entry.Name())
		ppid, hasParent := readLinuxParentPID(procDir)
		if hasParent {
			parents[pid] = ppid
		}
		// Only processes owned by the current user are candidates; other users'
		// sessions and system daemons must never be signalled. Entries whose
		// owner cannot be determined (vanished, restricted /proc) are skipped.
		if uid, ok := linuxProcessOwner(procDir); !ok || uid != euid {
			continue
		}
		// Kernel threads (kthreadd and its children) are never candidates,
		// which matters when the student client runs as root.
		if linuxIsKernelThread(pid, ppid, hasParent) {
			continue
		}
		name := readLinuxProcessName(procDir)
		if name != "" {
			candidates = append(candidates, candidate{pid: pid, name: name})
		}
	}
	// Exclude this process, its parent chain (session leader, terminal, desktop
	// launcher) and its own children (pw-cat, helpers, remote-exec jobs).
	lineage := processLineage(p.self(), parents)
	processes := make([]ProcessInfo, 0, len(candidates))
	for _, c := range candidates {
		if lineage[c.pid] {
			continue
		}
		processes = append(processes, ProcessInfo{PID: c.pid, Name: c.name})
	}
	return processes, nil
}

func linuxIsKernelThread(pid, ppid uint32, hasParent bool) bool {
	return pid == 2 || (hasParent && ppid == 2)
}

func (p *linuxProcessInspector) self() uint32 {
	if p.selfPID != 0 {
		return p.selfPID
	}
	return uint32(os.Getpid())
}

func linuxProcessOwner(procDir string) (uint32, bool) {
	info, err := os.Stat(procDir)
	if err != nil {
		return 0, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}

func readLinuxParentPID(procDir string) (uint32, bool) {
	data, err := os.ReadFile(filepath.Join(procDir, "stat"))
	if err != nil {
		return 0, false
	}
	return linuxParentPIDFromStat(string(data))
}

// linuxParentPIDFromStat parses field 4 (ppid) of /proc/<pid>/stat.
func linuxParentPIDFromStat(stat string) (uint32, bool) {
	closeParen := strings.LastIndex(stat, ")")
	if closeParen < 0 {
		return 0, false
	}
	fields := strings.Fields(stat[closeParen+1:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.ParseUint(fields[1], 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(ppid), true
}

func readLinuxProcessName(procDir string) string {
	if data, err := os.ReadFile(filepath.Join(procDir, "comm")); err == nil {
		if name := strings.TrimSpace(string(data)); name != "" {
			return name
		}
	}
	data, err := os.ReadFile(filepath.Join(procDir, "cmdline"))
	if err != nil {
		return ""
	}
	value := strings.TrimSpace(strings.ReplaceAll(string(data), "\x00", " "))
	if value == "" {
		return ""
	}
	return filepath.Base(strings.Fields(value)[0])
}

func (p *linuxProcessInspector) TerminateProcess(pid uint32) error {
	if pid == 0 || pid == uint32(os.Getpid()) || (p.selfPID != 0 && pid == p.selfPID) {
		return ErrLinuxProcessProtected
	}
	// Pin the process with a pidfd before inspecting it, so that the checks
	// below and the signal refer to the same process even if the PID is
	// recycled in between (Linux 5.3+; older kernels fall back to kill(2)).
	pidfd := p.openPidfd(pid)
	if pidfd >= 0 {
		defer unix.Close(pidfd)
	}
	procDir := filepath.Join(p.procRoot, strconv.FormatUint(uint64(pid), 10))
	stat, err := os.ReadFile(filepath.Join(procDir, "stat"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %d", ErrLinuxProcessUnavailable, pid)
		}
		return fmt.Errorf("control: inspect Linux process %d: %w", pid, err)
	}
	if linuxProcessState(string(stat)) == 'Z' {
		return fmt.Errorf("%w: %d", ErrLinuxProcessZombie, pid)
	}
	if uid, ok := linuxProcessOwner(procDir); !ok {
		return fmt.Errorf("%w: %d", ErrLinuxProcessUnavailable, pid)
	} else if uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%w: %d is owned by another user", ErrLinuxProcessProtected, pid)
	}
	if ppid, ok := linuxParentPIDFromStat(string(stat)); linuxIsKernelThread(pid, ppid, ok) {
		return fmt.Errorf("%w: %d is a kernel thread", ErrLinuxProcessProtected, pid)
	}
	parentOf := func(id uint32) (uint32, bool) {
		return readLinuxParentPID(filepath.Join(p.procRoot, strconv.FormatUint(uint64(id), 10)))
	}
	if isAncestorOrDescendant(p.self(), pid, parentOf) {
		return fmt.Errorf("%w: %d is related to this process", ErrLinuxProcessProtected, pid)
	}
	if err := p.signal(pid, pidfd); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("%w: %d", ErrLinuxProcessUnavailable, pid)
		}
		return fmt.Errorf("control: terminate linux process %d: %w", pid, err)
	}
	return nil
}

// openPidfd returns a pidfd for pid, or -1 when unsupported or when the
// inspector reads a fake /proc root (tests).
func (p *linuxProcessInspector) openPidfd(pid uint32) int {
	if p.procRoot != "/proc" {
		return -1
	}
	fd, err := unix.PidfdOpen(int(pid), 0)
	if err != nil {
		return -1
	}
	return fd
}

func (p *linuxProcessInspector) signal(pid uint32, pidfd int) error {
	if p.kill != nil {
		return p.kill(pid)
	}
	if pidfd >= 0 {
		err := unix.PidfdSendSignal(pidfd, unix.SIGTERM, nil, 0)
		if !errors.Is(err, unix.ENOSYS) {
			return err
		}
	}
	return syscall.Kill(int(pid), syscall.SIGTERM)
}

func linuxProcessState(stat string) rune {
	closeParen := strings.LastIndex(stat, ")")
	if closeParen < 0 || closeParen+2 >= len(stat) {
		return 0
	}
	return rune(stat[closeParen+2])
}
