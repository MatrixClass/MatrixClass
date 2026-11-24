package teacher

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"nexlab/internal/protocol"
	"nexlab/internal/remoteexec"
	"nexlab/internal/ui/theme"
)

func (a *App) openRemoteExecWindow() {
	a.remoteExecOpen = true
	a.remoteResult = nil
	a.remoteOutput = ""
}

func (a *App) remoteTargetIDs() []string {
	if len(a.selectedIDs) > 0 {
		ids := make([]string, 0, len(a.selectedIDs))
		for id, selected := range a.selectedIDs {
			if selected {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			return ids
		}
	}
	if a.selectedID != "" {
		return []string{a.selectedID}
	}
	return nil
}

func (a *App) requestRemoteApps() {
	a.remoteRequestID = fmt.Sprintf("apps-%d", time.Now().UnixNano())
	a.remoteApps = nil
	a.sendControlEnvelope(protocol.MsgRemoteAppListReq, nil, a.remoteTargetIDs())
	a.statusNote = "正在获取学生端应用列表"
}

func (a *App) launchRemoteApp() {
	targets := a.remoteTargetIDs()
	if len(targets) == 0 {
		a.statusNote = "请先选择学生设备"
		return
	}
	a.remoteRequestID = fmt.Sprintf("app-%d", time.Now().UnixNano())
	a.sendControlEnvelope(protocol.MsgRemoteAppLaunch, remoteexec.AppLaunchRequest{
		RequestID:  a.remoteRequestID,
		Executable: strings.TrimSpace(a.remoteExecutable),
		Arguments:  a.remoteArguments,
		WorkingDir: strings.TrimSpace(a.remoteWorkDir),
		Wait:       false,
	}, targets)
	a.statusNote = "已发送远程启动请求"
}

func (a *App) runRemoteCommand() {
	targets := a.remoteTargetIDs()
	command := strings.TrimSpace(a.remoteCommand)
	if len(targets) == 0 {
		a.statusNote = "请先选择学生设备"
		return
	}
	if command == "" {
		a.statusNote = "请输入要执行的命令"
		return
	}
	a.remoteRequestID = fmt.Sprintf("cmd-%d", time.Now().UnixNano())
	shell := remoteexec.Shell(a.remoteShell)
	a.sendControlEnvelope(protocol.MsgRemoteCommandStart, remoteexec.CommandRequest{
		RequestID:  a.remoteRequestID,
		Shell:      shell,
		Command:    command,
		WorkingDir: strings.TrimSpace(a.remoteCmdWorkDir),
		TimeoutSec: int(a.remoteTimeout),
	}, targets)
	a.remoteResult = &remoteexec.CommandResult{RequestID: a.remoteRequestID, Status: "running"}
	a.remoteOutput = ""
	a.statusNote = "远程命令执行中"
}

func (a *App) cancelRemoteCommand() {
	if a.remoteRequestID == "" {
		return
	}
	a.sendControlEnvelopeWithRequest(protocol.MsgRemoteCommandCancel, map[string]string{"request_id": a.remoteRequestID}, a.remoteTargetIDs(), a.remoteRequestID)
	a.statusNote = "已发送取消远程命令请求"
}

func (a *App) handleRemoteEnvelope(env protocol.Envelope) {
	switch env.Type {
	case protocol.MsgRemoteAppListResp:
		var apps []remoteexec.Application
		if json.Unmarshal(env.Payload, &apps) == nil {
			a.remoteApps = apps
			a.statusNote = fmt.Sprintf("已获取 %d 个可用应用", len(apps))
		}
	case protocol.MsgRemoteCommandOutput:
		var output remoteexec.CommandOutput
		if json.Unmarshal(env.Payload, &output) == nil {
			if a.remoteRequestID == "" || output.RequestID == a.remoteRequestID {
				a.remoteOutput += output.Data
			}
		}
	case protocol.MsgRemoteCommandResult, protocol.MsgRemoteAppResult:
		var result remoteexec.CommandResult
		if json.Unmarshal(env.Payload, &result) == nil {
			if a.remoteRequestID != "" && result.RequestID != "" && result.RequestID != a.remoteRequestID {
				return
			}
			a.remoteResult = &result
			a.statusNote = "远程操作" + remoteStatusLabel(result.Status)
		}
	}
}

// HandleRemoteEnvelope is called from transport goroutines; state changes are
// applied on the UI thread.
func (a *App) HandleRemoteEnvelope(env protocol.Envelope) {
	a.runOnUI(func() { a.handleRemoteEnvelope(env) })
	a.Invalidate()
}

func (a *App) buildRemoteExecModal(c *ui.Context, pal theme.Palette) {
	ui.Column(c).Width(820).Height(620).Background(pal.Canvas).Border(1, pal.CardBorder).Radius(4).Padding(14).Gap(8).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Text(c, "远程操作").FontSize(16).Bold()
			ui.Spacer(c)
			ui.Text(c, fmt.Sprintf("已选 %d 台设备", len(a.remoteTargetIDs()))).FontSize(11).TextColor(pal.TextMuted)
			if ui.Button(c, "关闭").Clicked() {
				a.remoteExecOpen = false
			}
		})
		ui.Row(c).FillWidth().Grow(1).Gap(10).Children(func() {
			ui.Column(c).Width(150).FillHeight().Background(pal.Card).Border(1, pal.CardBorder).Radius(4).Padding(8).Gap(6).Children(func() {
				ui.Text(c, "操作类型").FontSize(12).Bold().TextColor(pal.TextMuted)
				launchButton := ui.Button(c, "启动程序").FillWidth().Padding(6, 8).Radius(4)
				if a.remoteExecPanel == remoteExecPanelLaunch {
					launchButton.Background(pal.Selection).Border(1, pal.CardBorder)
				}
				if launchButton.Clicked() {
					a.remoteExecPanel = remoteExecPanelLaunch
				}
				commandButton := ui.Button(c, "执行命令").FillWidth().Padding(6, 8).Radius(4)
				if a.remoteExecPanel == remoteExecPanelCommand {
					commandButton.Background(pal.Selection).Border(1, pal.CardBorder)
				}
				if commandButton.Clicked() {
					a.remoteExecPanel = remoteExecPanelCommand
				}
			})
			ui.Column(c).Grow(1).FillHeight().Gap(8).Children(func() {
				if a.remoteExecPanel == remoteExecPanelLaunch {
					a.buildRemoteLaunchPanel(c, pal)
				} else {
					a.buildRemoteCommandPanel(c, pal)
				}
			})
		})
	})
}

func (a *App) buildRemoteLaunchPanel(c *ui.Context, pal theme.Palette) {
	ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
		ui.Text(c, "启动程序").FontSize(14).Bold()
		ui.Spacer(c)
		if ui.Button(c, "获取应用列表").Padding(3, 10).Clicked() {
			a.requestRemoteApps()
		}
	})
	ui.Text(c, "应用列表来自选中学生机控制面板中的“程序和功能”，也可直接填写程序路径").FontSize(11).TextColor(pal.TextMuted)
	ui.Scroll(c).Grow(1).FillWidth().ScrollbarAxes(false, true).Scrollbars(true).KeepScrollbars(true).Background(pal.Card).Border(1, pal.CardBorder).Padding(8).Children(func() {
		if len(a.remoteApps) == 0 {
			ui.Box(c).FillWidth().Padding(20).Center().Children(func() {
				ui.Text(c, "暂无应用，请先选择学生机并点击「获取应用列表」").FontSize(12).TextColor(pal.TextMuted)
			})
			return
		}
		for _, app := range a.remoteApps {
			appCopy := app
			row := ui.Row(c).Key("remote-app-"+app.ID).FillWidth().AlignItems(ui.Center).Gap(6).Padding(4, 6).Radius(3)
			picked := a.remoteAppPicked == app.ID
			if picked {
				row.Background(pal.Selection).Border(1, pal.CardBorder)
			}
			row.Children(func() {
				ui.Column(c).Grow(1).Gap(2).Children(func() {
					ui.Text(c, appCopy.Name).FontSize(12).SingleLine()
					ui.Text(c, appCopy.Executable).FontSize(10).SingleLine().TextColor(pal.TextMuted)
				})
				label := "选择"
				if picked {
					label = "已选择"
				}
				if ui.Button(c, label).Padding(2, 8).Clicked() {
					a.remoteAppPicked = appCopy.ID
					a.remoteExecutable = appCopy.Executable
					a.remoteWorkDir = appCopy.WorkingDir
				}
			})
		}
	})
	ui.TextInput(c, &a.remoteExecutable).Placeholder("可执行文件路径，例如 C:\\Program Files\\App\\App.exe")
	ui.TextInput(c, &a.remoteArguments).Placeholder("启动参数，可选")
	ui.TextInput(c, &a.remoteWorkDir).Placeholder("工作目录，可选")
	if ui.PrimaryButton(c, "启动").Clicked() {
		a.launchRemoteApp()
	}
}

func (a *App) buildRemoteCommandPanel(c *ui.Context, pal theme.Palette) {
	ui.Text(c, "执行命令").FontSize(14).Bold()
	ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
		ui.Text(c, "Shell").FontSize(12).Bold()
		ui.Select(c, &a.remoteShell, remoteexec.ShellOptions()).Width(130)
		ui.Text(c, "超时秒数").FontSize(12).Bold()
		ui.NumberInput(c, &a.remoteTimeout, 1, 600, 1).Width(90)
	})
	ui.TextInput(c, &a.remoteCmdWorkDir).Placeholder("工作目录，可选")
	ui.TextArea(c, &a.remoteCommand).Placeholder("输入 CMD 或 PowerShell 命令，可多行").FillWidth().Lines(3, 6)
	ui.Row(c).Gap(8).Children(func() {
		if ui.PrimaryButton(c, "开始执行").Clicked() {
			a.runRemoteCommand()
		}
		if ui.Button(c, "取消命令").Clicked() {
			a.cancelRemoteCommand()
		}
	})
	ui.Text(c, "输出").FontSize(12).Bold()
	ui.Scroll(c).Grow(1).FillWidth().ScrollbarAxes(false, true).Scrollbars(true).KeepScrollbars(true).Background(pal.Card).Border(1, pal.CardBorder).Padding(8).Children(func() {
		ui.Text(c, a.remoteOutput).FontSize(11).TextColor(pal.Text)
		if a.remoteResult != nil {
			ui.Divider(c)
			ui.Text(c, fmt.Sprintf("状态：%s  退出码：%d  %s", remoteStatusLabel(a.remoteResult.Status), a.remoteResult.ExitCode, a.remoteResult.Error)).FontSize(11).TextColor(pal.TextMuted)
		}
	})
}
