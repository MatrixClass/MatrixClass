package teacher

import (
	"fmt"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"nexlab/internal/protocol"
	"nexlab/internal/transport"
	"nexlab/internal/ui/theme"
)

// openSettings shows the settings modal with the channel draft reset.
func (a *App) openSettings() {
	a.channelInput = a.persistedChannel()
	a.settingsTab = settingsTabClassroom
	a.settingsSnapshot = settingsDraft{
		collectSaveDir: a.collectSaveDir,
		useMulticast:   a.useMulticast,
		monitorQual:    a.monitorQual,
		monitorFPS:     a.monitorFPS,
	}
	a.settingsOpen = true
}

// settingsDraft holds the values the settings modal can change, so closing it
// without saving restores them.
type settingsDraft struct {
	collectSaveDir string
	useMulticast   bool
	monitorQual    float64
	monitorFPS     float64
}

// cancelSettings closes the settings modal and reverts unsaved edits.
func (a *App) cancelSettings() {
	snap := a.settingsSnapshot
	monitorChanged := snap.monitorQual != a.monitorQual || snap.monitorFPS != a.monitorFPS
	a.collectSaveDir = snap.collectSaveDir
	a.useMulticast = snap.useMulticast
	a.monitorQual = snap.monitorQual
	a.monitorFPS = snap.monitorFPS
	a.settingsOpen = false
	a.syncRuntimeFlags()
	if monitorChanged && a.monitoring {
		a.sendMonitorConfig()
	}
}

// Settings modal navigation pages.
const (
	settingsTabClassroom = "课堂"
	settingsTabNetwork   = "网络"
	settingsTabMonitor   = "监控"
	settingsTabAbout     = "关于"
)

// AppVersion is the teacher build version shown on the about page; the
// teacher binary overrides it with the version stamped at link time.
var AppVersion = "0.1.0"

var settingsTabs = []string{settingsTabClassroom, settingsTabNetwork, settingsTabMonitor, settingsTabAbout}

func (a *App) buildSettingsModal(c *ui.Context, pal theme.Palette) {
	if a.settingsTab == "" {
		a.settingsTab = settingsTabClassroom
	}
	ui.Column(c).Width(680).Height(440).Background(pal.Canvas).Border(1, pal.CardBorder).Radius(4).Clip().Children(func() {
		ui.Row(c).Grow(1).FillWidth().Children(func() {
			ui.Column(c).Width(150).FillHeight().Background(pal.ChromeRail).Padding(12, 8).Gap(4).Children(func() {
				ui.Text(c, "设置").FontSize(15).Bold().Padding(4, 8)
				for _, id := range settingsTabs {
					id := id
					btn := ui.Button(c, id).Label("设置-"+id).FillWidth().Padding(7, 10).Radius(4).Justify(ui.Start)
					if a.settingsTab == id {
						btn.Background(pal.Selection).Border(1, pal.CardBorder)
					}
					if btn.Clicked() {
						a.settingsTab = id
					}
				}
			})
			ui.Column(c).Grow(1).FillHeight().Padding(16).Gap(10).Children(func() {
				switch a.settingsTab {
				case settingsTabNetwork:
					a.buildSettingsNetworkPage(c, pal)
				case settingsTabMonitor:
					a.buildSettingsMonitorPage(c, pal)
				case settingsTabAbout:
					a.buildSettingsAboutPage(c, pal)
				default:
					a.buildSettingsClassroomPage(c, pal)
				}
				ui.Spacer(c)
				ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
					if ui.Button(c, "取消").Clicked() {
						a.cancelSettings()
					}
					if ui.PrimaryButton(c, "保存并应用").Clicked() {
						a.applySettings()
					}
				})
			})
		})
	})
}

// applySettings commits the settings drafts and closes the modal.
func (a *App) applySettings() {
	a.settingsOpen = false
	a.syncRuntimeFlags()
	a.applyArchiveDir(a.collectSaveDir)
	if a.monitoring {
		a.sendMonitorConfig()
	}
	if draft := normalizeChannelID(a.channelInput); draft != a.channel {
		a.pendingChannel = draft
		a.statusNote = fmt.Sprintf("频道已改为 %s，重启教师端后生效（当前仍为 %s）", draft, a.channel)
	} else {
		a.pendingChannel = ""
		a.statusNote = "课堂设置已保存"
	}
	a.savePersistedSettings()
}

func (a *App) buildSettingsClassroomPage(c *ui.Context, pal theme.Palette) {
	ui.Text(c, "课堂设置").FontSize(16).Bold()
	ui.Text(c, "频道 ID (数字)").FontSize(12).TextColor(pal.TextMuted)
	// Edit a draft: the running transport and discovery beacon keep the
	// channel chosen at startup, so a change only applies after restart.
	ui.Select(c, &a.channelInput, numericChannelOptions())
	ui.Text(c, "频道修改后需重启教师端生效，学生端按频道 ID 自动加入。").FontSize(11).TextColor(pal.TextMuted)
	ui.Text(c, "默认作业存放目录").FontSize(12).TextColor(pal.TextMuted)
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.TextInput(c, &a.collectSaveDir).Placeholder("默认: 系统下载目录 (Downloads)").Grow(1)
		if ui.Button(c, "浏览...").Padding(4, 10).Clicked() {
			a.openCollectDirPicker()
		}
	})
}

func (a *App) buildSettingsNetworkPage(c *ui.Context, pal theme.Palette) {
	ui.Text(c, "网络设置").FontSize(16).Bold()
	a.buildIdentitySection(c, pal)
	ui.Divider(c)
	ui.Checkbox(c, &a.useMulticast, "优先使用 UDP 组播 + FEC 广播画面")
	ui.Text(c, "组播不可用时自动回退到 KCP 单播。").FontSize(11).TextColor(pal.TextMuted)
	ui.Divider(c)
	ui.Text(c, "学生端网络参数").FontSize(12).Bold()
	ui.Text(c, "向当前选中的学生端批量下发监听网卡模式，学生端保存后自动重连。").FontSize(11).TextColor(pal.TextMuted)
	if ui.Button(c, "批量设置学生端网络参数").Clicked() {
		a.settingsOpen = false
		a.networkModeInput = string(transport.NetworkModeMulti)
		a.networkInterfaceInput = ""
		a.networkIgnoreVirtual = true
		a.networkIgnoreVPN = true
		a.networkConfigOpen = true
	}
}

func (a *App) buildSettingsMonitorPage(c *ui.Context, pal theme.Palette) {
	ui.Text(c, "监控设置").FontSize(16).Bold()
	ui.Row(c).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "监控画质").FontSize(12).Bold()
		ui.Spacer(c)
		ui.Text(c, fmt.Sprintf("%d%%", int(a.monitorQual))).FontSize(12).TextColor(pal.Accent)
	})
	if ui.Slider(c, &a.monitorQual, 30, 95).Label("监控画质滑块").Changed() && a.monitoring {
		a.sendMonitorConfig()
	}
	ui.Row(c).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "监控帧率").FontSize(12).Bold()
		ui.Spacer(c)
		ui.Text(c, fmt.Sprintf("%d FPS", int(a.monitorFPS))).FontSize(12).TextColor(pal.Accent)
	})
	if ui.Slider(c, &a.monitorFPS, 5, 30).Label("监控帧率滑块").Changed() && a.monitoring {
		a.sendMonitorConfig()
	}
	ui.Text(c, "监控进行中调整会立即下发到学生端。").FontSize(11).TextColor(pal.TextMuted)
}

func (a *App) buildSettingsAboutPage(c *ui.Context, pal theme.Palette) {
	ui.Text(c, "关于").FontSize(16).Bold()
	ui.Row(c).AlignItems(ui.Center).Gap(12).Children(func() {
		ui.Image(c, theme.IconMonitor).Size(40, 40)
		ui.Column(c).Gap(2).Children(func() {
			ui.Text(c, "矩联电子教室 MatrixClass").FontSize(15).Bold()
			ui.Text(c, "教师端 · 版本 "+AppVersion).FontSize(12).TextColor(pal.TextMuted)
		})
	})
	ui.Divider(c)
	aboutRow := func(key, val string) {
		ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
			ui.Text(c, key).FontSize(12).TextColor(pal.TextMuted).Width(80)
			ui.Text(c, val).FontSize(12).Grow(1)
		})
	}
	aboutRow("支持平台", "Windows / Linux (x64, arm64, loong64) / macOS")
	aboutRow("项目主页", "https://github.com/MatrixClass/MatrixClass")
	if a.fingerprint != "" {
		aboutRow("身份指纹", a.fingerprint)
	}
	ui.Text(c, "局域网课堂管理：屏幕广播、屏幕监控、远程控制、文件分发、作业收取、随堂测验、网络影院、远程命令。").FontSize(11).TextColor(pal.TextMuted)
}

func (a *App) buildNetworkConfigModal(c *ui.Context, pal theme.Palette) {
	ui.Column(c).Width(500).Background(pal.Canvas).Border(1, pal.CardBorder).Radius(4).Padding(16).Gap(10).Children(func() {
		ui.Text(c, "批量设置学生端网络参数").FontSize(16).Bold()
		ui.Text(c, "当前选中的学生端将保存配置并自动重连。未选择设备时不会下发。").FontSize(12).TextColor(pal.TextMuted)
		ui.Text(c, "网络模式").FontSize(12).Bold()
		modeLabel := "多网卡（自动）"
		if a.networkModeInput == string(transport.NetworkModeSingle) {
			modeLabel = "单网卡（手动指定）"
		}
		if ui.Select(c, &modeLabel, []string{"多网卡（自动）", "单网卡（手动指定）"}).Changed() {
			if modeLabel == "单网卡（手动指定）" {
				a.networkModeInput = string(transport.NetworkModeSingle)
			} else {
				a.networkModeInput = string(transport.NetworkModeMulti)
			}
		}
		if a.networkModeInput == string(transport.NetworkModeSingle) {
			ui.TextInput(c, &a.networkInterfaceInput).Placeholder("网卡名称，例如 Ethernet 或 eth0").FillWidth()
		} else {
			ui.Checkbox(c, &a.networkIgnoreVirtual, "忽略虚拟机网卡")
			ui.Checkbox(c, &a.networkIgnoreVPN, "忽略 VPN 虚拟网卡")
		}
		ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
			if ui.Button(c, "取消").Clicked() {
				a.networkConfigOpen = false
			}
			if ui.PrimaryButton(c, "下发到选中学生").Clicked() {
				targets := a.remoteTargetIDs()
				if len(targets) == 0 {
					a.statusNote = "请先在设备视图中选择要设置的学生机"
					return
				}
				cfg := transport.NetworkConfig{Mode: transport.NetworkMode(a.networkModeInput), InterfaceName: a.networkInterfaceInput, IgnoreVirtual: a.networkIgnoreVirtual, IgnoreVPN: a.networkIgnoreVPN}
				a.sendControlEnvelope(protocol.MsgStudentNetworkConfig, cfg, targets)
				a.networkConfigOpen = false
				a.statusNote = fmt.Sprintf("已向 %d 台学生机下发网络参数", len(targets))
			}
		})
	})
}

// buildIdentitySection shows the teacher identity fingerprint. Students pin
// the first teacher they connect to; on a shared LAN the fingerprint can be
// entered on each student so an impostor console is never trusted.
func (a *App) buildIdentitySection(c *ui.Context, pal theme.Palette) {
	ui.Text(c, "教师端身份").FontSize(12).Bold()
	if a.fingerprint == "" {
		ui.Text(c, "演示模式未启用身份认证。").FontSize(11).TextColor(pal.TextMuted)
		return
	}
	ui.Text(c, "学生端只信任此指纹的教师端，连接全程加密。冒充的教师端无法控制学生机。").FontSize(11).TextColor(pal.TextMuted)
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, a.fingerprint).FontSize(13).Bold().Grow(1)
		if ui.Button(c, "复制").Clicked() {
			if err := mygo.Clipboard.Write(transfer.TextData(a.fingerprint)); err != nil {
				a.statusNote = "复制失败: " + err.Error()
			} else {
				a.statusNote = "已复制教师端指纹"
			}
		}
	})
	ui.Text(c, "多台教师机共用同一课堂时，把数据目录中的 teacher-identity.key 复制到其他教师机即可。").FontSize(11).TextColor(pal.TextMuted)
}
