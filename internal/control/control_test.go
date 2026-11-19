package control

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"nexlab/internal/core"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type mockInputBackend struct {
	mu      sync.Mutex
	batches [][]WinInput
}

func (m *mockInputBackend) SendInputs(inputs []WinInput) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := append([]WinInput(nil), inputs...)
	m.batches = append(m.batches, cp)
	return nil
}

func (m *mockInputBackend) allInputs() []WinInput {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []WinInput
	for _, b := range m.batches {
		out = append(out, b...)
	}
	return out
}

type mockLockBackend struct {
	mu       sync.Mutex
	enabled  bool
	messages []string
	disables int
	failNext error
}

func (m *mockLockBackend) EnableLock(message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failNext != nil {
		err := m.failNext
		m.failNext = nil
		return err
	}
	m.enabled = true
	m.messages = append(m.messages, message)
	return nil
}

func (m *mockLockBackend) DisableLock() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled = false
	m.disables++
	return nil
}

func (m *mockLockBackend) UpdateMessage(message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, message)
	return nil
}

type mockProcessInspector struct {
	mu         sync.Mutex
	procs      []ProcessInfo
	terminated []uint32
	failPIDs   map[uint32]error
}

func (m *mockProcessInspector) ListProcesses() ([]ProcessInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]ProcessInfo(nil), m.procs...)
	return out, nil
}

func (m *mockProcessInspector) TerminateProcess(pid uint32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err, ok := m.failPIDs[pid]; ok {
		return err
	}
	m.terminated = append(m.terminated, pid)
	return nil
}

type mockPowerExecutor struct {
	mu        sync.Mutex
	shutdowns int
	reboots   int
	logoffs   int
	lastDelay uint32
	lastMsg   string
}

func (m *mockPowerExecutor) Shutdown(timeoutSec uint32, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.shutdowns++
	m.lastDelay = timeoutSec
	m.lastMsg = reason
	return nil
}

func (m *mockPowerExecutor) Reboot(timeoutSec uint32, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reboots++
	m.lastDelay = timeoutSec
	m.lastMsg = reason
	return nil
}

func (m *mockPowerExecutor) Logoff() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logoffs++
	return nil
}

func TestInputMappingAndInjection(t *testing.T) {
	backend := &mockInputBackend{}
	inj := NewInputInjector(backend)

	if got := NormalizeCoord(-0.2); got != 0 {
		t.Fatalf("NormalizeCoord(-0.2) = %d, want 0", got)
	}
	if got := NormalizeCoord(1.5); got != 65535 {
		t.Fatalf("NormalizeCoord(1.5) = %d, want 65535", got)
	}
	if got := NormalizeCoord(0.5); got != 32768 {
		t.Fatalf("NormalizeCoord(0.5) = %d, want 32768", got)
	}

	events := []core.RemoteInputEvent{
		{Kind: core.InputMouseMove, X: 0.25, Y: 0.75},
		{Kind: core.InputMouseDown, X: 0.5, Y: 0.5, Button: 0},
		{Kind: core.InputMouseUp, X: -1, Y: -1, Button: 2},
		{Kind: core.InputMouseDown, X: -1, Y: -1, Button: 3},
		{Kind: core.InputMouseWheel, Delta: -120},
		{Kind: core.InputKeyDown, VKCode: 0x41},
		{Kind: core.InputKeyUp, VKCode: 0x26}, // Up arrow (extended key)
	}

	for _, ev := range events {
		if err := inj.Inject(ev); err != nil {
			t.Fatalf("Inject(%+v) error = %v", ev, err)
		}
	}

	got := backend.allInputs()
	if len(got) != len(events) {
		t.Fatalf("sent %d inputs, want %d", len(got), len(events))
	}

	// 0: mouse move
	if got[0].Type != InputTypeMouse || got[0].Mi.Dx != NormalizeCoord(0.25) || got[0].Mi.Dy != NormalizeCoord(0.75) {
		t.Fatalf("mouse move mismatch: %+v", got[0].Mi)
	}
	wantMoveFlags := MouseEventfMove | MouseEventfAbsolute | MouseEventfVirtualDesk
	if got[0].Mi.DwFlags != wantMoveFlags {
		t.Fatalf("mouse move flags = %#x, want %#x", got[0].Mi.DwFlags, wantMoveFlags)
	}

	// 1: left mouse down with position
	if got[1].Mi.DwFlags != (wantMoveFlags | MouseEventfLeftDown) {
		t.Fatalf("mouse down flags = %#x", got[1].Mi.DwFlags)
	}

	// 2: right mouse up without position
	if got[2].Mi.DwFlags != MouseEventfRightUp {
		t.Fatalf("right mouse up flags = %#x, want %#x", got[2].Mi.DwFlags, MouseEventfRightUp)
	}

	// 3: XButton1 down
	if got[3].Mi.DwFlags != MouseEventfXDown || got[3].Mi.MouseData != XButton1 {
		t.Fatalf("xbutton1 mismatch: %+v", got[3].Mi)
	}

	// 4: wheel negative delta
	if got[4].Mi.DwFlags != MouseEventfWheel || int32(got[4].Mi.MouseData) != -120 {
		t.Fatalf("wheel mismatch: %+v", got[4].Mi)
	}

	// 5: key down 'A' (non-extended)
	if got[5].Type != InputTypeKeyboard || got[5].Ki.WVk != 0x41 || got[5].Ki.DwFlags != 0 {
		t.Fatalf("key down mismatch: %+v", got[5].Ki)
	}

	// 6: key up VK_UP (extended)
	if got[6].Type != InputTypeKeyboard || got[6].Ki.WVk != 0x26 || got[6].Ki.DwFlags != (KeyEventfKeyUp|KeyEventfExtendedKey) {
		t.Fatalf("extended key up mismatch: %+v", got[6].Ki)
	}

	// Error cases
	if err := inj.Inject(core.RemoteInputEvent{Kind: "bogus"}); !errors.Is(err, ErrInvalidInputKind) {
		t.Fatalf("invalid kind err = %v, want %v", err, ErrInvalidInputKind)
	}
	if err := inj.Inject(core.RemoteInputEvent{Kind: core.InputMouseDown, Button: 9}); !errors.Is(err, ErrUnsupportedButton) {
		t.Fatalf("invalid button err = %v, want %v", err, ErrUnsupportedButton)
	}
	if err := inj.Inject(core.RemoteInputEvent{Kind: core.InputKeyDown, VKCode: 0}); !errors.Is(err, ErrInvalidVKCode) {
		t.Fatalf("zero vk err = %v, want %v", err, ErrInvalidVKCode)
	}
}

func TestScreenLockStateEmergencyUnlockAndTimeout(t *testing.T) {
	backend := &mockLockBackend{}
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	var states []LockState

	ctrl := NewScreenLockController(LockOptions{
		Backend:           backend,
		EmergencyPassword: "teach-override-123",
		DisconnectTimeout: 30 * time.Second,
		Now: func() time.Time {
			return now
		},
		OnStateChange: func(s LockState) {
			states = append(states, s)
		},
	})
	defer ctrl.Close()

	if st := ctrl.State(); st.Locked || !st.EmergencyActive {
		t.Fatalf("unexpected initial state: %+v", st)
	}

	if err := ctrl.SetLocked(true, "Eyes on the projector"); err != nil {
		t.Fatalf("SetLocked(true) error = %v", err)
	}
	if !backend.enabled {
		t.Fatalf("expected backend lock enabled")
	}
	if st := ctrl.State(); !st.Locked || st.Message != "Eyes on the projector" {
		t.Fatalf("unexpected locked state: %+v", st)
	}

	// Update message while locked.
	if err := ctrl.SetLocked(true, "Quiz starts in 1 minute"); err != nil {
		t.Fatalf("SetLocked(true, update) error = %v", err)
	}
	if len(backend.messages) != 2 || backend.messages[1] != "Quiz starts in 1 minute" {
		t.Fatalf("unexpected backend messages: %v", backend.messages)
	}

	// Wrong emergency password keeps screen locked.
	ok, err := ctrl.EmergencyUnlock("wrong-password")
	if ok || !errors.Is(err, ErrInvalidEmergencyPassword) {
		t.Fatalf("wrong password result = (%v, %v)", ok, err)
	}
	if !ctrl.State().Locked {
		t.Fatalf("screen should stay locked after wrong emergency password")
	}

	// Correct emergency password unlocks immediately.
	ok, err = ctrl.EmergencyUnlock("teach-override-123")
	if !ok || err != nil {
		t.Fatalf("EmergencyUnlock valid password failed: (%v, %v)", ok, err)
	}
	if st := ctrl.State(); st.Locked || st.UnlockReason != "emergency_password" {
		t.Fatalf("unexpected unlocked state: %+v", st)
	}
	if backend.enabled {
		t.Fatalf("expected backend disabled after emergency unlock")
	}

	// Re-lock and test disconnect timeout with heartbeat reset.
	if err := ctrl.SetLocked(true, "Locked again"); err != nil {
		t.Fatalf("SetLocked error = %v", err)
	}
	now = now.Add(20 * time.Second)
	if ctrl.CheckDisconnectTimeout() {
		t.Fatalf("should not timeout before 30s")
	}
	ctrl.NotifyTeacherHeartbeat()
	now = now.Add(20 * time.Second)
	if ctrl.CheckDisconnectTimeout() {
		t.Fatalf("heartbeat should have reset timeout")
	}
	now = now.Add(11 * time.Second)
	if !ctrl.CheckDisconnectTimeout() {
		t.Fatalf("expected disconnect timeout unlock")
	}
	if st := ctrl.State(); st.Locked || st.UnlockReason != "disconnect_timeout" {
		t.Fatalf("unexpected timeout state: %+v", st)
	}

	// Backend failure on EnableLock should not leave state stuck as locked.
	backend.failNext = errors.New("hook failed")
	if err := ctrl.SetLocked(true, "fail"); err == nil {
		t.Fatalf("expected EnableLock failure")
	}
	if ctrl.State().Locked {
		t.Fatalf("expected state to roll back to unlocked on EnableLock failure")
	}
}

func TestProcessBlacklistAndWhitelistMatching(t *testing.T) {
	inspector := &mockProcessInspector{
		procs: []ProcessInfo{
			{PID: 4, Name: "System"},
			{PID: 100, Name: "explorer.exe"},
			{PID: 200, Name: "nexlab.exe"},
			{PID: 300, Name: "self.exe"},
			{PID: 401, Name: "steam.exe"},
			{PID: 402, Name: "WeChat.exe"},
			{PID: 403, Name: "notepad.exe"},
			{PID: 404, Name: "game_launcher_v2.exe"},
		},
	}

	var violations []PolicyViolation
	enforcer := NewPolicyEnforcer(PolicyOptions{
		Inspector:   inspector,
		SelfPID:     300,
		OnViolation: func(v PolicyViolation) { violations = append(violations, v) },
	})

	// Blacklist mode: exact (case-insensitive, optional .exe) + wildcard.
	err := enforcer.ApplyRule(core.PolicyRule{
		Mode:        PolicyModeBlacklist,
		BlockedApps: []string{"Steam", "game_launcher_*.exe", "explorer.exe"},
	})
	if err != nil {
		t.Fatalf("ApplyRule blacklist error = %v", err)
	}

	killed, err := enforcer.EnforceProcessesOnce()
	if err != nil {
		t.Fatalf("EnforceProcessesOnce blacklist error = %v", err)
	}
	if len(killed) != 2 {
		t.Fatalf("blacklist killed %d procs (%+v), want 2 (steam.exe, game_launcher_v2.exe)", len(killed), killed)
	}
	if killed[0].PID != 401 || killed[1].PID != 404 {
		t.Fatalf("unexpected blacklist killed PIDs: %+v", killed)
	}

	// Whitelist mode: kills non-protected, non-self apps not in AllowedApps.
	inspector.terminated = nil
	violations = nil
	err = enforcer.ApplyRule(core.PolicyRule{
		Mode:        PolicyModeWhitelist,
		AllowedApps: []string{"notepad"},
	})
	if err != nil {
		t.Fatalf("ApplyRule whitelist error = %v", err)
	}
	killed, err = enforcer.EnforceProcessesOnce()
	if err != nil {
		t.Fatalf("EnforceProcessesOnce whitelist error = %v", err)
	}
	gotPIDs := map[uint32]bool{}
	for _, p := range killed {
		gotPIDs[p.PID] = true
	}
	// Protected (System, explorer.exe, nexlab.exe) and self (PID 300) and allowed (notepad.exe PID 403) must survive.
	for _, survive := range []uint32{4, 100, 200, 300, 403} {
		if gotPIDs[survive] {
			t.Fatalf("protected/allowed PID %d was terminated", survive)
		}
	}
	for _, mustKill := range []uint32{401, 402, 404} {
		if !gotPIDs[mustKill] {
			t.Fatalf("expected PID %d to be terminated under whitelist", mustKill)
		}
	}
	if len(violations) != 3 {
		t.Fatalf("recorded %d violations, want 3", len(violations))
	}
}

func TestDomainFilterProxyAndHostsBlock(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "ok")
		_, _ = io.WriteString(w, "hello from "+r.Host)
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	_, upstreamPort, _ := net.SplitHostPort(upstreamURL.Host)

	var violations []PolicyViolation
	enforcer := NewPolicyEnforcer(PolicyOptions{
		OnViolation: func(v PolicyViolation) {
			violations = append(violations, v)
		},
	})
	if err := enforcer.ApplyRule(core.PolicyRule{
		Mode:           PolicyModeBlacklist,
		BlockedDomains: []string{"*.blocked.example", "bad.local"},
	}); err != nil {
		t.Fatalf("ApplyRule error = %v", err)
	}

	proxyAddr, err := enforcer.StartDomainProxy("127.0.0.1:0")
	if err != nil {
		t.Fatalf("StartDomainProxy error = %v", err)
	}
	defer enforcer.Close()

	proxyURL, err := url.Parse("http://" + proxyAddr)
	if err != nil {
		t.Fatalf("parse proxy url: %v", err)
	}
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
		Timeout: 5 * time.Second,
	}

	// Allowed request passes through to local upstream test server.
	resp, err := client.Get("http://127.0.0.1:" + upstreamPort + "/ok")
	if err != nil {
		t.Fatalf("GET allowed upstream via proxy error = %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "hello from") {
		t.Fatalf("unexpected allowed response: status=%d body=%q", resp.StatusCode, string(body))
	}

	// Blocked HTTP request gets 403 without leaving localhost.
	respBlocked, err := client.Get("http://sub.blocked.example/game")
	if err != nil {
		t.Fatalf("GET blocked domain via proxy error = %v", err)
	}
	respBlocked.Body.Close()
	if respBlocked.StatusCode != http.StatusForbidden {
		t.Fatalf("blocked HTTP status = %d, want 403", respBlocked.StatusCode)
	}

	// Blocked HTTPS CONNECT request gets 403 before dialing upstream.
	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial proxy error = %v", err)
	}
	_, _ = io.WriteString(conn, "CONNECT bad.local:443 HTTP/1.1\r\nHost: bad.local:443\r\n\r\n")
	buf := make([]byte, 256)
	n, _ := conn.Read(buf)
	conn.Close()
	if !strings.HasPrefix(string(buf[:n]), "HTTP/1.1 403") {
		t.Fatalf("blocked CONNECT response = %q, want 403", string(buf[:n]))
	}

	// Switch to whitelist mode and verify CheckDomainAllowed.
	if err := enforcer.ApplyRule(core.PolicyRule{
		Mode:           PolicyModeWhitelist,
		AllowedDomains: []string{"edu.example", "*.docs.example"},
		BlockedDomains: []string{"ads.docs.example"},
	}); err != nil {
		t.Fatalf("ApplyRule whitelist error = %v", err)
	}
	if !enforcer.IsDomainAllowed("portal.edu.example") {
		t.Fatalf("expected subdomain of allowed domain to pass")
	}
	if enforcer.IsDomainAllowed("ads.docs.example") {
		t.Fatalf("expected explicit BlockedDomains entry to override whitelist")
	}
	if enforcer.IsDomainAllowed("random.example") {
		t.Fatalf("expected unlisted domain to be blocked in whitelist mode")
	}

	// Verify hosts file block rendering and idempotent replacement.
	tmpHosts := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(tmpHosts, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatalf("write temp hosts: %v", err)
	}
	rendered, err := ApplyHostsBlock(tmpHosts, []string{"*.game.com", "https://video.example:443/watch", "game.com"}, false)
	if err != nil {
		t.Fatalf("ApplyHostsBlock error = %v", err)
	}
	if !strings.Contains(rendered, "127.0.0.1 localhost") ||
		!strings.Contains(rendered, "0.0.0.0 game.com") ||
		!strings.Contains(rendered, "0.0.0.0 video.example") {
		t.Fatalf("unexpected rendered hosts:\n%s", rendered)
	}
	if strings.Count(rendered, "0.0.0.0 game.com") != 1 {
		t.Fatalf("expected deduplicated game.com in hosts:\n%s", rendered)
	}
	// Clearing domains removes managed block while preserving localhost.
	cleared, err := ApplyHostsBlock(tmpHosts, nil, false)
	if err != nil {
		t.Fatalf("ApplyHostsBlock clear error = %v", err)
	}
	if strings.Contains(cleared, hostsMarkerBegin) || !strings.Contains(cleared, "127.0.0.1 localhost") {
		t.Fatalf("unexpected cleared hosts:\n%s", cleared)
	}
}

func TestWOLMagicPacketEncodingAndPowerDispatch(t *testing.T) {
	pkt, err := BuildWOLMagicPacket("AA:BB:CC:DD:EE:FF")
	if err != nil {
		t.Fatalf("BuildWOLMagicPacket error = %v", err)
	}
	if len(pkt) != WOLMagicPacketSize {
		t.Fatalf("packet len = %d, want %d", len(pkt), WOLMagicPacketSize)
	}
	if !bytes.Equal(pkt[:6], []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}) {
		t.Fatalf("invalid WOL sync header: %x", pkt[:6])
	}
	wantMAC := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}
	for i := 0; i < 16; i++ {
		off := 6 + i*6
		if !bytes.Equal(pkt[off:off+6], wantMAC) {
			t.Fatalf("WOL MAC repetition %d = %x, want %x", i, pkt[off:off+6], wantMAC)
		}
	}

	// Also supports hyphen-delimited MAC addresses.
	pktHyphen, err := BuildWOLMagicPacket("aa-bb-cc-dd-ee-ff")
	if err != nil || !bytes.Equal(pktHyphen, pkt) {
		t.Fatalf("hyphen MAC mismatch: err=%v", err)
	}

	if _, err := BuildWOLMagicPacket("not-a-mac"); !errors.Is(err, ErrInvalidMACAddress) {
		t.Fatalf("invalid MAC err = %v, want %v", err, ErrInvalidMACAddress)
	}

	exec := &mockPowerExecutor{}
	var sentAddr string
	var sentPkt []byte
	power := NewPowerController(PowerOptions{
		Executor:      exec,
		BroadcastAddr: "192.168.1.255:9",
		ShutdownDelay: 15,
		UDPSender: func(addr string, packet []byte) error {
			sentAddr = addr
			sentPkt = append([]byte(nil), packet...)
			return nil
		},
	})

	if err := power.Execute(core.PowerWake, "11:22:33:44:55:66"); err != nil {
		t.Fatalf("Execute(wake) error = %v", err)
	}
	if sentAddr != "192.168.1.255:9" || len(sentPkt) != WOLMagicPacketSize {
		t.Fatalf("unexpected WOL dispatch: addr=%q len=%d", sentAddr, len(sentPkt))
	}
	if err := power.Execute(core.PowerShutdown, ""); err != nil || exec.shutdowns != 1 || exec.lastDelay != 15 {
		t.Fatalf("Execute(shutdown) failed: err=%v exec=%+v", err, exec)
	}
	if err := power.Execute(core.PowerReboot, ""); err != nil || exec.reboots != 1 {
		t.Fatalf("Execute(reboot) failed: err=%v exec=%+v", err, exec)
	}
	if err := power.Execute(core.PowerLogoff, ""); err != nil || exec.logoffs != 1 {
		t.Fatalf("Execute(logoff) failed: err=%v exec=%+v", err, exec)
	}
	if err := power.Execute("hibernate", ""); !errors.Is(err, ErrUnsupportedPowerAct) {
		t.Fatalf("unsupported action err = %v, want %v", err, ErrUnsupportedPowerAct)
	}
}

func TestManagerControlEngineIntegration(t *testing.T) {
	inBackend := &mockInputBackend{}
	lockBackend := &mockLockBackend{}
	inspector := &mockProcessInspector{
		procs: []ProcessInfo{
			{PID: 501, Name: "discord.exe"},
			{PID: 502, Name: "calc.exe"},
		},
	}
	powerExec := &mockPowerExecutor{}

	mgr := NewManager(ManagerOptions{
		InputBackend: inBackend,
		Lock: LockOptions{
			Backend:           lockBackend,
			EmergencyPassword: "admin",
		},
		Policy: PolicyOptions{
			Inspector: inspector,
			SelfPID:   999,
		},
		Power: PowerOptions{
			Executor: powerExec,
		},
		ProxyAddr:      "127.0.0.1:0",
		EnforceOnApply: true,
	})

	var engine core.ControlEngine = mgr

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proxyAddr, err := mgr.Start(ctx)
	if err != nil {
		t.Fatalf("mgr.Start error = %v", err)
	}
	defer mgr.Close()
	if proxyAddr == "" {
		t.Fatalf("expected non-empty proxy address")
	}

	if err := engine.InjectInput(core.RemoteInputEvent{Kind: core.InputKeyDown, VKCode: 0x0D}); err != nil {
		t.Fatalf("engine.InjectInput error = %v", err)
	}
	if len(inBackend.allInputs()) != 1 {
		t.Fatalf("expected 1 injected input")
	}

	// ApplyPolicy with LockScreen=true locks the screen and enforces process rules.
	if err := engine.ApplyPolicy(core.PolicyRule{
		Mode:        PolicyModeBlacklist,
		BlockedApps: []string{"discord.exe"},
		LockScreen:  true,
		LockMessage: "Exam in progress",
	}); err != nil {
		t.Fatalf("engine.ApplyPolicy error = %v", err)
	}
	if !mgr.LockState().Locked || mgr.LockState().Message != "Exam in progress" {
		t.Fatalf("expected policy to lock screen, got %+v", mgr.LockState())
	}
	if len(inspector.terminated) != 1 || inspector.terminated[0] != 501 {
		t.Fatalf("expected discord.exe (501) terminated, got %v", inspector.terminated)
	}

	// Applying a follow-up policy with LockScreen=false unlocks a policy-triggered lock.
	if err := engine.ApplyPolicy(core.PolicyRule{
		Mode:       PolicyModeOff,
		LockScreen: false,
	}); err != nil {
		t.Fatalf("engine.ApplyPolicy unlock error = %v", err)
	}
	if mgr.LockState().Locked {
		t.Fatalf("expected policy LockScreen=false to release policy-initiated lock")
	}

	if err := engine.ExecutePower(core.PowerLogoff, ""); err != nil || powerExec.logoffs != 1 {
		t.Fatalf("engine.ExecutePower error = %v, logoffs = %d", err, powerExec.logoffs)
	}
}
