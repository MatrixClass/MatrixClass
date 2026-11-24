package student

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"nexlab/internal/transport"
	"nexlab/internal/ui/theme"
)

func (a *App) loadNetworkConfig() {
	if a.networkConfigPath != "" {
		if data, err := os.ReadFile(a.networkConfigPath); err == nil {
			_ = json.Unmarshal(data, &a.networkConfig)
		}
	}
	a.networkMode = networkModeLabel(a.networkConfig.Mode)
	a.networkInterface = a.networkConfig.InterfaceName
	a.ignoreVirtual = a.networkConfig.IgnoreVirtual
	a.ignoreVPN = a.networkConfig.IgnoreVPN
}

func (a *App) NetworkConfig() transport.NetworkConfig { return a.networkConfig }

func (a *App) ApplyNetworkConfig(cfg transport.NetworkConfig) error {
	if cfg.Mode != transport.NetworkModeSingle && cfg.Mode != transport.NetworkModeMulti {
		cfg.Mode = transport.NetworkModeMulti
	}
	if cfg.Mode == transport.NetworkModeSingle && cfg.InterfaceName == "" {
		return errors.New("单网卡模式必须选择网卡")
	}
	if a.networkConfigPath != "" {
		if err := os.MkdirAll(filepath.Dir(a.networkConfigPath), 0o755); err != nil {
			return err
		}
		data, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(a.networkConfigPath, append(data, '\n'), 0o600); err != nil {
			return err
		}
	}
	a.networkConfig = cfg
	a.networkMode = networkModeLabel(cfg.Mode)
	a.networkInterface = cfg.InterfaceName
	a.ignoreVirtual = cfg.IgnoreVirtual
	a.ignoreVPN = cfg.IgnoreVPN
	if a.onNetworkConfig != nil {
		a.onNetworkConfig(cfg)
	}
	a.statusNote = "网络参数已保存，正在重新连接"
	return nil
}

func localInterfaceOptions() []string {
	ifaces, _ := net.Interfaces()
	options := []string{""}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagLoopback == 0 {
			options = append(options, iface.Name)
		}
	}
	return options
}

func networkModeOptions() []string {
	return []string{"多网卡（自动）", "单网卡（手动选择）"}
}

func networkModeLabel(mode transport.NetworkMode) string {
	if mode == transport.NetworkModeSingle {
		return "单网卡（手动选择）"
	}
	return "多网卡（自动）"
}

func networkModeValue(label string) transport.NetworkMode {
	if label == "单网卡（手动选择）" {
		return transport.NetworkModeSingle
	}
	return transport.NetworkModeMulti
}

const (
	passwordModeSetup    = "setup"
	passwordModeExit     = "exit"
	passwordModeSettings = "settings-auth"
)

func hashExitPassword(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (a *App) loadExitPasswordHash() string {
	if a.exitPasswordPath == "" {
		return ""
	}
	b, err := os.ReadFile(a.exitPasswordPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (a *App) loadChannel() string {
	if a.channelPath == "" {
		return ""
	}
	b, err := os.ReadFile(a.channelPath)
	if err != nil {
		return ""
	}
	channel := strings.TrimSpace(string(b))
	if channel == "" {
		return ""
	}
	for _, r := range channel {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return channel
}

func (a *App) saveChannel(raw string) error {
	channel := strings.TrimSpace(raw)
	if channel == "" {
		return errors.New("频道 ID 不能为空")
	}
	for _, r := range channel {
		if r < '0' || r > '9' {
			return errors.New("频道 ID 必须是数字")
		}
	}
	if a.channelPath != "" {
		if err := os.MkdirAll(filepath.Dir(a.channelPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(a.channelPath, []byte(channel+"\n"), 0o600); err != nil {
			return err
		}
	}
	a.channel = channel
	return nil
}

func (a *App) InitialSetupRequired() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.passwordMode == passwordModeSetup
}

func (a *App) Channel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.channel
}

func (a *App) saveExitPassword(raw string) error {
	hash := hashExitPassword(raw)
	if a.exitPasswordPath != "" {
		if err := os.MkdirAll(filepath.Dir(a.exitPasswordPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(a.exitPasswordPath, []byte(hash+"\n"), 0o600); err != nil {
			return err
		}
	}
	a.exitPasswordHash = hash
	return nil
}

func (a *App) clearExitPassword() {
	a.exitPasswordHash = ""
	if a.exitPasswordPath != "" {
		_ = os.Remove(a.exitPasswordPath)
	}
}

func (a *App) EnsureExitPasswordConfigured() {
	if a.InitialSetupRequired() {
		a.showExitPasswordWindow(true)
	}
}

// ExitAuthorized reports whether the exit password was accepted.
func (a *App) ExitAuthorized() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.exitAuthorized
}

// ExitPasswordConfigured reports whether a password exists for operations
// that must authenticate outside the normal student window, such as uninstall.
func (a *App) ExitPasswordConfigured() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.exitPasswordHash != ""
}

func (a *App) RequestExit() {
	a.mu.Lock()
	configured := a.exitPasswordHash != ""
	a.mu.Unlock()
	if !configured {
		a.showPasswordWindow(passwordModeSetup)
		return
	}
	a.showPasswordWindow(passwordModeExit)
}

func (a *App) OpenParameterSettings() {
	a.mu.Lock()
	configured := a.exitPasswordHash != ""
	a.mu.Unlock()
	if !configured {
		a.showPasswordWindow(passwordModeSetup)
		return
	}
	a.showPasswordWindow(passwordModeSettings)
}

func (a *App) showExitPasswordWindow(setup bool) {
	if setup {
		a.showPasswordWindow(passwordModeSetup)
		return
	}
	a.showPasswordWindow(passwordModeExit)
}

func passwordWindowTitle(mode string) string {
	switch mode {
	case passwordModeSetup:
		return "学生端首次设置"
	case passwordModeSettings:
		return "学生端参数设置验证"
	default:
		return "退出学生端"
	}
}

func (a *App) showPasswordWindow(mode string) {
	setup := mode == passwordModeSetup
	a.mu.Lock()
	mainWin := a.win
	w := a.exitPasswordWin
	a.exitPasswordSetup = setup
	a.passwordMode = mode
	a.exitPasswordInput = ""
	a.exitPasswordConfirm = ""
	a.exitPasswordError = ""
	a.mu.Unlock()
	if mainWin == nil {
		return
	}
	mainWin.Update(func() {
		windowHeight := 250
		minWindowHeight := 220
		if setup {
			windowHeight = 380
			minWindowHeight = 340
		}
		if w == nil {
			w = mygo.NewWindow(mygo.WindowOptions{
				Title:       passwordWindowTitle(mode),
				Width:       420,
				Height:      windowHeight,
				MinWidth:    380,
				MinHeight:   minWindowHeight,
				AlwaysOnTop: true,
				Frameless:   false,
				Content:     ui.View(a.ExitPasswordView),
			})
			w.SetClosable(false)
			w.OnClosed(func() {
				a.mu.Lock()
				if a.exitPasswordWin == w {
					a.exitPasswordWin = nil
				}
				a.mu.Unlock()
			})
			a.mu.Lock()
			a.exitPasswordWin = w
			a.mu.Unlock()
		} else {
			w.SetTitle(passwordWindowTitle(mode))
			w.SetMinimumSize(380, minWindowHeight)
			w.SetSize(420, windowHeight)
		}
		w.SetAlwaysOnTop(true)
		w.Show()
		w.Focus()
		w.Invalidate()
	})
}

func (a *App) ExitPasswordView(c *ui.Context) {
	a.mu.Lock()
	setup := a.exitPasswordSetup
	mode := a.passwordMode
	errText := a.exitPasswordError
	a.mu.Unlock()
	ui.Column(c).Fill().Padding(20).Gap(12).Background(theme.GetPalette(false).Canvas).Children(func() {
		if setup {
			ui.Text(c, "首次启动：设置学生端参数").FontSize(17).Bold()
			ui.Text(c, "设置频道 ID 和学生端退出密码后才会连接教师端。").FontSize(12).TextColor(theme.GetPalette(false).TextMuted)
			ui.TextInput(c, &a.channelInput).Placeholder("频道 ID（仅数字）").FillWidth().AutoFocus()
			ui.TextInput(c, &a.exitPasswordInput).Password().Placeholder("设置退出密码").FillWidth()
			ui.TextInput(c, &a.exitPasswordConfirm).Password().Placeholder("再次输入退出密码").FillWidth()
			if a.trust != nil {
				ui.TextInput(c, &a.trustInput).Placeholder("教师端指纹（可选，留空则信任首次连接的教师端）").FillWidth()
			}
			if ui.PrimaryButton(c, "保存并连接").Clicked() {
				if strings.TrimSpace(a.exitPasswordInput) == "" || a.exitPasswordInput != a.exitPasswordConfirm {
					a.exitPasswordError = "两次输入的密码不一致或为空"
				} else if err := a.saveTrustedFingerprint(a.trustInput); err != nil {
					a.exitPasswordError = err.Error()
				} else if err := a.saveChannel(a.channelInput); err != nil {
					a.exitPasswordError = err.Error()
				} else if err := a.saveExitPassword(a.exitPasswordInput); err != nil {
					a.exitPasswordError = "保存失败: " + err.Error()
				} else {
					a.passwordMode = passwordModeExit
					if a.onSetupComplete != nil {
						a.onSetupComplete(a.channel)
					}
					a.closeExitPasswordWindow()
				}
			}
		} else if mode == passwordModeSettings {
			ui.Text(c, "参数设置需要密码").FontSize(17).Bold()
			ui.Text(c, "请输入学生端密码后进入参数设置。").FontSize(12).TextColor(theme.GetPalette(false).TextMuted)
			ui.TextInput(c, &a.exitPasswordInput).Password().Placeholder("学生端密码").FillWidth().AutoFocus()
			if ui.PrimaryButton(c, "验证并继续").Clicked() {
				if hashExitPassword(a.exitPasswordInput) != a.exitPasswordHash {
					a.exitPasswordError = "密码错误"
				} else {
					a.mu.Lock()
					a.passwordMode = passwordModeExit
					a.mu.Unlock()
					a.closeExitPasswordWindow()
					a.showSettingsWindow()
				}
			}
			if ui.Button(c, "取消").Clicked() {
				a.mu.Lock()
				a.passwordMode = passwordModeExit
				a.mu.Unlock()
				a.closeExitPasswordWindow()
			}
		} else {
			ui.Text(c, "退出学生端").FontSize(17).Bold()
			ui.Text(c, "请输入学生端密码后关闭学生端。").FontSize(12).TextColor(theme.GetPalette(false).TextMuted)
			ui.TextInput(c, &a.exitPasswordInput).Password().Placeholder("学生端密码").FillWidth().AutoFocus()
			if ui.PrimaryButton(c, "确认退出").Clicked() {
				if hashExitPassword(a.exitPasswordInput) != a.exitPasswordHash {
					a.exitPasswordError = "密码错误"
				} else {
					a.mu.Lock()
					a.exitAuthorized = true
					a.mu.Unlock()
					a.closeExitPasswordWindow()
					mygo.App.Quit()
				}
			}
			if ui.Button(c, "取消").Clicked() {
				a.closeExitPasswordWindow()
			}
		}
		if errText != "" {
			ui.Text(c, errText).FontSize(12).TextColor(ui.Hex("#B42318"))
		}
	})
}

func (a *App) showSettingsWindow() {
	a.mu.Lock()
	mainWin := a.win
	a.channelInput = a.channel
	a.networkMode = networkModeLabel(a.networkConfig.Mode)
	a.networkInterface = a.networkConfig.InterfaceName
	a.ignoreVirtual = a.networkConfig.IgnoreVirtual
	a.ignoreVPN = a.networkConfig.IgnoreVPN
	a.settingsError = ""
	if a.trust != nil {
		a.trustInput = a.trust.Pinned()
	}
	w := a.settingsWin
	a.mu.Unlock()
	if mainWin == nil {
		return
	}
	mainWin.Update(func() {
		if w == nil {
			w = mygo.NewWindow(mygo.WindowOptions{Title: "学生端参数设置", Width: 480, Height: 540, MinWidth: 420, MinHeight: 480, AlwaysOnTop: true, Frameless: false, Content: ui.View(a.SettingsView)})
			w.OnClosed(func() {
				a.mu.Lock()
				if a.settingsWin == w {
					a.settingsWin = nil
				}
				a.mu.Unlock()
			})
			a.mu.Lock()
			a.settingsWin = w
			a.mu.Unlock()
		}
		w.SetAlwaysOnTop(true)
		w.Show()
		w.Focus()
		w.Invalidate()
	})
}

func (a *App) SettingsView(c *ui.Context) {
	a.mu.Lock()
	errText := a.settingsError
	a.mu.Unlock()
	ui.Column(c).Fill().Padding(20).Gap(12).Background(theme.GetPalette(false).Canvas).Children(func() {
		ui.Text(c, "学生端参数设置").FontSize(17).Bold()
		ui.Text(c, "修改频道后将按新频道重新连接。").FontSize(12).TextColor(theme.GetPalette(false).TextMuted)
		ui.TextInput(c, &a.channelInput).Placeholder("频道 ID（仅数字）").FillWidth().AutoFocus()
		ui.Text(c, "网络连接模式").FontSize(12).Bold()
		if ui.Select(c, &a.networkMode, networkModeOptions()).Changed() {
			a.Invalidate()
		}
		if networkModeValue(a.networkMode) == transport.NetworkModeSingle {
			ui.Text(c, "手动选择监听网卡").FontSize(12).TextColor(theme.GetPalette(false).TextMuted)
			ui.Select(c, &a.networkInterface, localInterfaceOptions())
		} else {
			ui.Checkbox(c, &a.ignoreVirtual, "忽略虚拟机网卡")
			ui.Checkbox(c, &a.ignoreVPN, "忽略 VPN 虚拟网卡")
		}
		if a.trust != nil {
			ui.Text(c, "信任的教师端指纹").FontSize(12).Bold()
			ui.TextInput(c, &a.trustInput).Placeholder("留空则信任下一次连接的教师端").FillWidth()
			ui.Text(c, "只接受此指纹的教师端控制本机。更换教师机密钥后需在此清空或更新。").FontSize(11).TextColor(theme.GetPalette(false).TextMuted)
		}
		ui.Spacer(c)
		ui.Row(c).FillWidth().Justify(ui.End).Gap(8).Children(func() {
			if ui.Button(c, "取消").Clicked() {
				if a.settingsWin != nil {
					a.settingsWin.Hide()
				}
			}
			if ui.PrimaryButton(c, "保存并重连").Clicked() {
				cfg := transport.NetworkConfig{Mode: networkModeValue(a.networkMode), InterfaceName: a.networkInterface, IgnoreVirtual: a.ignoreVirtual, IgnoreVPN: a.ignoreVPN}
				if err := a.saveTrustedFingerprint(a.trustInput); err != nil {
					a.settingsError = err.Error()
				} else if err := a.saveChannel(a.channelInput); err != nil {
					a.settingsError = err.Error()
				} else if err := a.ApplyNetworkConfig(cfg); err != nil {
					a.settingsError = err.Error()
				} else {
					a.statusNote = "正在切换频道 ID: " + a.channel
					if a.onSetupComplete != nil {
						a.onSetupComplete(a.channel)
					}
					if a.settingsWin != nil {
						a.settingsWin.Hide()
					}
				}
			}
		})
		if errText != "" {
			ui.Text(c, errText).FontSize(12).TextColor(ui.Hex("#B42318"))
		}
	})
}

func (a *App) closeExitPasswordWindow() {
	a.mu.Lock()
	w := a.exitPasswordWin
	a.exitPasswordWin = nil
	a.mu.Unlock()
	if w != nil {
		w.Hide()
	}
}

func (a *App) ResetExitPassword() {
	a.clearExitPassword()
	a.showExitPasswordWindow(true)
}

// saveTrustedFingerprint pins the teacher fingerprint; empty clears the pin
// so the next authenticated teacher is trusted on first use.
func (a *App) saveTrustedFingerprint(raw string) error {
	if a.trust == nil {
		return nil
	}
	if strings.TrimSpace(raw) == a.trust.Pinned() {
		return nil
	}
	if err := a.trust.SetPinned(raw); err != nil {
		if errors.Is(err, transport.ErrInvalidFingerprnt) {
			return errors.New("教师端指纹格式不正确，应为 32 位十六进制字符")
		}
		return err
	}
	a.trustInput = a.trust.Pinned()
	return nil
}
