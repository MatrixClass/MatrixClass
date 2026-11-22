package control

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"nexlab/internal/core"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	PolicyModeBlacklist = "blacklist"
	PolicyModeWhitelist = "whitelist"
	PolicyModeOff       = "off"

	hostsMarkerBegin = "# BEGIN NEXLAB POLICY"
	hostsMarkerEnd   = "# END NEXLAB POLICY"
)

var (
	ErrDomainBlocked = errors.New("control: domain blocked by classroom policy")
)

type ProcessInfo struct {
	PID  uint32
	Name string
}

type ProcessInspector interface {
	ListProcesses() ([]ProcessInfo, error)
	TerminateProcess(pid uint32) error
}

type PolicyViolation struct {
	Kind      string    `json:"kind"`
	Target    string    `json:"target"`
	PID       uint32    `json:"pid,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

type PolicyOptions struct {
	Inspector    ProcessInspector
	PollInterval time.Duration
	SelfPID      uint32
	Protected    []string
	HostsPath    string
	HostsDryRun  bool
	OnViolation  func(PolicyViolation)
	DialTimeout  time.Duration
}

type PolicyEnforcer struct {
	mu           sync.RWMutex
	rule         core.PolicyRule
	inspector    ProcessInspector
	pollInterval time.Duration
	selfPID      uint32
	protected    map[string]struct{}
	protGlobs    []string
	hostsPath    string
	hostsDryRun  bool
	onViolation  func(PolicyViolation)
	dialTimeout  time.Duration

	cancelMonitor context.CancelFunc
	monitorWG     sync.WaitGroup

	proxyMu     sync.Mutex
	proxyServer *http.Server
	proxyLn     net.Listener
}

// defaultProtectedProcesses combines the cross-platform list with the
// per-platform session/system process list (platformProtectedProcesses).
// Entries may be glob patterns (e.g. "gsd-*").
var defaultProtectedProcesses = append(append([]string(nil), commonProtectedProcesses...), platformProtectedProcesses...)

func NewPolicyEnforcer(opts PolicyOptions) *PolicyEnforcer {
	inspector := opts.Inspector
	if inspector == nil {
		inspector = newPlatformProcessInspector()
	}
	pollInterval := opts.PollInterval
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	selfPID := opts.SelfPID
	if selfPID == 0 {
		selfPID = uint32(os.Getpid())
	}
	dialTimeout := opts.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = 10 * time.Second
	}
	protected := make(map[string]struct{}, len(defaultProtectedProcesses)+len(opts.Protected)+1)
	var protGlobs []string
	addProtected := func(name string) {
		norm := normalizeAppName(name)
		if norm == "" || norm == "." {
			return
		}
		if strings.ContainsAny(norm, "*?[") {
			protGlobs = append(protGlobs, norm)
			return
		}
		protected[norm] = struct{}{}
	}
	for _, name := range defaultProtectedProcesses {
		addProtected(name)
	}
	if exe, err := os.Executable(); err == nil {
		addProtected(exe)
	}
	for _, name := range opts.Protected {
		addProtected(name)
	}
	return &PolicyEnforcer{
		inspector:    inspector,
		pollInterval: pollInterval,
		selfPID:      selfPID,
		protected:    protected,
		protGlobs:    protGlobs,
		hostsPath:    opts.HostsPath,
		hostsDryRun:  opts.HostsDryRun,
		onViolation:  opts.OnViolation,
		dialTimeout:  dialTimeout,
	}
}

func (p *PolicyEnforcer) ApplyRule(rule core.PolicyRule) error {
	normalized := normalizeRule(rule)
	p.mu.Lock()
	p.rule = normalized
	hostsPath := p.hostsPath
	dryRun := p.hostsDryRun
	p.mu.Unlock()

	if hostsPath != "" {
		if _, err := ApplyHostsBlock(hostsPath, normalized.BlockedDomains, dryRun); err != nil {
			return err
		}
	}
	return nil
}

func (p *PolicyEnforcer) Rule() core.PolicyRule {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return cloneRule(p.rule)
}

func (p *PolicyEnforcer) EnforceProcessesOnce() ([]ProcessInfo, error) {
	p.mu.RLock()
	rule := cloneRule(p.rule)
	inspector := p.inspector
	selfPID := p.selfPID
	protected := p.protected
	protGlobs := p.protGlobs
	onViolation := p.onViolation
	p.mu.RUnlock()

	if rule.Mode == "" || rule.Mode == PolicyModeOff {
		return nil, nil
	}
	if rule.Mode == PolicyModeBlacklist && len(rule.BlockedApps) == 0 {
		return nil, nil
	}
	if rule.Mode == PolicyModeWhitelist && len(rule.AllowedApps) == 0 {
		return nil, nil
	}

	procs, err := inspector.ListProcesses()
	if err != nil {
		return nil, err
	}

	var terminated []ProcessInfo
	var errs []error
	for _, proc := range procs {
		if proc.PID == 0 || proc.PID == selfPID {
			continue
		}
		if isProtectedProcessName(proc.Name, protected, protGlobs) {
			continue
		}
		if !IsProcessBlocked(proc.Name, rule) {
			continue
		}
		if err := inspector.TerminateProcess(proc.PID); err != nil {
			errs = append(errs, fmt.Errorf("terminate %s (pid=%d): %w", proc.Name, proc.PID, err))
			continue
		}
		terminated = append(terminated, proc)
		if onViolation != nil {
			onViolation(PolicyViolation{
				Kind:      "process",
				Target:    proc.Name,
				PID:       proc.PID,
				Timestamp: time.Now(),
			})
		}
	}
	return terminated, errors.Join(errs...)
}

func (p *PolicyEnforcer) StartMonitor(ctx context.Context) {
	p.StopMonitor()

	monCtx, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	p.cancelMonitor = cancel
	interval := p.pollInterval
	p.mu.Unlock()

	p.monitorWG.Add(1)
	go func() {
		defer p.monitorWG.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-monCtx.Done():
				return
			case <-ticker.C:
				_, _ = p.EnforceProcessesOnce()
			}
		}
	}()
}

func (p *PolicyEnforcer) StopMonitor() {
	p.mu.Lock()
	cancel := p.cancelMonitor
	p.cancelMonitor = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
		p.monitorWG.Wait()
	}
}

func (p *PolicyEnforcer) StartDomainProxy(addr string) (string, error) {
	p.proxyMu.Lock()
	defer p.proxyMu.Unlock()
	if p.proxyServer != nil {
		return p.proxyLn.Addr().String(), nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	srv := &http.Server{
		Handler:           p.ProxyHandler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	p.proxyLn = ln
	p.proxyServer = srv
	go func() {
		_ = srv.Serve(ln)
	}()
	return ln.Addr().String(), nil
}

func (p *PolicyEnforcer) StopDomainProxy(ctx context.Context) error {
	p.proxyMu.Lock()
	srv := p.proxyServer
	p.proxyServer = nil
	p.proxyLn = nil
	p.proxyMu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

func (p *PolicyEnforcer) Close() error {
	p.StopMonitor()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return p.StopDomainProxy(ctx)
}

func (p *PolicyEnforcer) IsDomainAllowed(host string) bool {
	p.mu.RLock()
	rule := p.rule
	p.mu.RUnlock()
	return CheckDomainAllowed(host, rule)
}

func (p *PolicyEnforcer) ProxyHandler() http.Handler {
	transport := &http.Transport{
		DialContext: (&net.Dialer{Timeout: p.dialTimeout}).DialContext,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHost := extractTargetHost(r)
		if !p.IsDomainAllowed(targetHost) {
			if p.onViolation != nil {
				p.onViolation(PolicyViolation{
					Kind:      "domain",
					Target:    targetHost,
					Timestamp: time.Now(),
				})
			}
			http.Error(w, "Forbidden by classroom policy", http.StatusForbidden)
			return
		}

		if r.Method == http.MethodConnect {
			p.handleConnect(w, r)
			return
		}

		outReq := r.Clone(r.Context())
		outReq.RequestURI = ""
		if outReq.URL.Scheme == "" {
			outReq.URL.Scheme = "http"
		}
		if outReq.URL.Host == "" {
			outReq.URL.Host = r.Host
		}
		removeHopByHopHeaders(outReq.Header)

		resp, err := transport.RoundTrip(outReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		removeHopByHopHeaders(resp.Header)
		for k, vals := range resp.Header {
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})
}

func (p *PolicyEnforcer) handleConnect(w http.ResponseWriter, r *http.Request) {
	targetAddr := r.Host
	if targetAddr == "" {
		targetAddr = r.URL.Host
	}
	if !strings.Contains(targetAddr, ":") {
		targetAddr += ":443"
	}
	upstream, err := net.DialTimeout("tcp", targetAddr, p.dialTimeout)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	clientConn, bufRW, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		clientConn.Close()
		upstream.Close()
		return
	}
	if bufRW != nil && bufRW.Reader.Buffered() > 0 {
		buffered, _ := bufRW.Peek(bufRW.Reader.Buffered())
		if len(buffered) > 0 {
			_, _ = upstream.Write(buffered)
		}
	}
	go func() {
		_, _ = io.Copy(upstream, clientConn)
		upstream.Close()
	}()
	go func() {
		_, _ = io.Copy(clientConn, upstream)
		clientConn.Close()
	}()
}

func isProtectedProcessName(name string, protected map[string]struct{}, globs []string) bool {
	norm := normalizeAppName(name)
	if _, ok := protected[norm]; ok {
		return true
	}
	if _, ok := protected[strings.TrimSuffix(norm, ".exe")]; ok {
		return true
	}
	for _, pattern := range globs {
		if matched, err := filepath.Match(pattern, norm); err == nil && matched {
			return true
		}
	}
	return false
}

func IsProcessBlocked(procName string, rule core.PolicyRule) bool {
	norm := normalizeAppName(procName)
	if norm == "" {
		return false
	}
	mode := strings.ToLower(strings.TrimSpace(rule.Mode))
	switch mode {
	case "", PolicyModeOff:
		return false
	case PolicyModeBlacklist:
		for _, blocked := range rule.BlockedApps {
			if matchAppPattern(norm, blocked) {
				return true
			}
		}
		return false
	case PolicyModeWhitelist:
		if len(rule.AllowedApps) == 0 {
			return false
		}
		for _, allowed := range rule.AllowedApps {
			if matchAppPattern(norm, allowed) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func CheckDomainAllowed(host string, rule core.PolicyRule) bool {
	norm := normalizeDomain(host)
	if norm == "" {
		return false
	}
	mode := strings.ToLower(strings.TrimSpace(rule.Mode))
	switch mode {
	case "", PolicyModeOff:
		return true
	case PolicyModeBlacklist:
		for _, blocked := range rule.BlockedDomains {
			if matchDomainPattern(norm, blocked) {
				return false
			}
		}
		return true
	case PolicyModeWhitelist:
		for _, blocked := range rule.BlockedDomains {
			if matchDomainPattern(norm, blocked) {
				return false
			}
		}
		if len(rule.AllowedDomains) == 0 {
			return true
		}
		for _, allowed := range rule.AllowedDomains {
			if matchDomainPattern(norm, allowed) {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func RenderHostsBlock(existingContent string, blockedDomains []string) string {
	var kept []string
	scanner := bufio.NewScanner(strings.NewReader(existingContent))
	inBlock := false
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == hostsMarkerBegin {
			inBlock = true
			continue
		}
		if trimmed == hostsMarkerEnd {
			inBlock = false
			continue
		}
		if !inBlock {
			kept = append(kept, line)
		}
	}
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}

	cleanedDomains := make([]string, 0, len(blockedDomains))
	seen := make(map[string]struct{}, len(blockedDomains))
	for _, d := range blockedDomains {
		nd := normalizeDomain(d)
		nd = strings.TrimPrefix(nd, "*.")
		if nd == "" {
			continue
		}
		if _, ok := seen[nd]; ok {
			continue
		}
		seen[nd] = struct{}{}
		cleanedDomains = append(cleanedDomains, nd)
	}

	var sb strings.Builder
	for _, line := range kept {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	if len(cleanedDomains) > 0 {
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(hostsMarkerBegin)
		sb.WriteByte('\n')
		for _, d := range cleanedDomains {
			sb.WriteString("0.0.0.0 ")
			sb.WriteString(d)
			sb.WriteByte('\n')
		}
		sb.WriteString(hostsMarkerEnd)
		sb.WriteByte('\n')
	}
	return sb.String()
}

func ApplyHostsBlock(hostsPath string, blockedDomains []string, dryRun bool) (string, error) {
	var existing string
	raw, err := os.ReadFile(hostsPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err == nil {
		existing = string(raw)
	}
	rendered := RenderHostsBlock(existing, blockedDomains)
	if dryRun {
		return rendered, nil
	}
	if err := os.MkdirAll(filepath.Dir(hostsPath), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(hostsPath, []byte(rendered), 0o644); err != nil {
		return "", err
	}
	return rendered, nil
}

func normalizeAppName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = filepath.Base(n)
	return n
}

func matchAppPattern(procNorm, pattern string) bool {
	pat := normalizeAppName(pattern)
	if pat == "" {
		return false
	}
	if procNorm == pat {
		return true
	}
	if strings.TrimSuffix(procNorm, ".exe") == strings.TrimSuffix(pat, ".exe") {
		return true
	}
	if matched, err := filepath.Match(pat, procNorm); err == nil && matched {
		return true
	}
	return false
}

func normalizeDomain(raw string) string {
	d := strings.ToLower(strings.TrimSpace(raw))
	if d == "" {
		return ""
	}
	if strings.Contains(d, "://") {
		if idx := strings.Index(d, "://"); idx >= 0 {
			d = d[idx+3:]
		}
	}
	if idx := strings.IndexByte(d, '/'); idx >= 0 {
		d = d[:idx]
	}
	if host, _, err := net.SplitHostPort(d); err == nil {
		d = host
	}
	d = strings.Trim(d, "[]")
	d = strings.TrimSuffix(d, ".")
	return d
}

func matchDomainPattern(hostNorm, pattern string) bool {
	pat := normalizeDomain(pattern)
	if pat == "" {
		return false
	}
	if strings.HasPrefix(pat, "*.") {
		base := strings.TrimPrefix(pat, "*.")
		return hostNorm == base || strings.HasSuffix(hostNorm, "."+base)
	}
	return hostNorm == pat || strings.HasSuffix(hostNorm, "."+pat)
}

func extractTargetHost(r *http.Request) string {
	if r.URL != nil && r.URL.Host != "" {
		return r.URL.Host
	}
	return r.Host
}

func removeHopByHopHeaders(h http.Header) {
	for _, k := range []string{
		"Connection",
		"Proxy-Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailers",
		"Transfer-Encoding",
		"Upgrade",
	} {
		h.Del(k)
	}
}

func normalizeRule(rule core.PolicyRule) core.PolicyRule {
	out := cloneRule(rule)
	out.Mode = strings.ToLower(strings.TrimSpace(out.Mode))
	if out.Mode == "" {
		out.Mode = PolicyModeBlacklist
	}
	return out
}

func cloneRule(rule core.PolicyRule) core.PolicyRule {
	out := rule
	if rule.BlockedApps != nil {
		out.BlockedApps = append([]string(nil), rule.BlockedApps...)
	}
	if rule.AllowedApps != nil {
		out.AllowedApps = append([]string(nil), rule.AllowedApps...)
	}
	if rule.BlockedDomains != nil {
		out.BlockedDomains = append([]string(nil), rule.BlockedDomains...)
	}
	if rule.AllowedDomains != nil {
		out.AllowedDomains = append([]string(nil), rule.AllowedDomains...)
	}
	return out
}
