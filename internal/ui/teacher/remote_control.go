package teacher

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"nexlab/internal/core"
	"nexlab/internal/protocol"
	"nexlab/internal/ui/theme"
)

func (a *App) buildRemoteControlModal(c *ui.Context, pal theme.Palette) {
	targetLabel := a.remoteControlID
	for _, st := range a.listStudents() {
		if st.ID == a.remoteControlID {
			targetLabel = fmt.Sprintf("%s (%s · %s)", st.MachineName, st.StudentNo, st.IP)
			break
		}
	}

	ui.Column(c).Width(880).Height(580).Background(pal.Canvas).Border(1, pal.CardBorder).Radius(4).Padding(12).Gap(8).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "远程控制: "+targetLabel).FontSize(14).Bold()
			ui.Badge(c, "实时交互").Background(pal.Selection).TextColor(pal.Text)
			ui.Spacer(c)
			if ui.Button(c, "发送 Esc").Padding(3, 10).Clicked() {
				a.sendRemoteInput(core.RemoteInputEvent{Kind: core.InputKeyDown, VKCode: 0x1B})
				a.sendRemoteInput(core.RemoteInputEvent{Kind: core.InputKeyUp, VKCode: 0x1B})
			}
			if ui.PrimaryButton(c, "退出远程控制").Padding(4, 12).Clicked() {
				a.StopRemoteControl()
			}
		})

		a.mu.Lock()
		bmp := a.remoteBitmap
		if bmp == nil {
			bmp = a.monitorTiles[a.remoteControlID]
		}
		a.mu.Unlock()

		stage := ui.Box(c).Grow(1).FillWidth().Background(pal.TheatreBg).Border(1, pal.CardBorder).Radius(2).Center().Focusable().Clip()
		const stageW, stageH = 856.0, 490.0
		stage.HandleInput(func(ev ui.InputEvent) bool {
			nx := float64(ev.X) / stageW
			ny := float64(ev.Y) / stageH
			if b := stage.Bounds(); b.W > 0 && b.H > 0 {
				nx = float64(ev.X) / float64(b.W)
				ny = float64(ev.Y) / float64(b.H)
			}
			if nx < 0 {
				nx = 0
			} else if nx > 1 {
				nx = 1
			}
			if ny < 0 {
				ny = 0
			} else if ny > 1 {
				ny = 1
			}

			switch ev.Kind {
			case ui.InputPointerMove:
				a.sendRemoteInput(core.RemoteInputEvent{Kind: core.InputMouseMove, X: nx, Y: ny})
				return true
			case ui.InputPointerDown:
				a.sendRemoteInput(core.RemoteInputEvent{Kind: core.InputMouseDown, X: nx, Y: ny, Button: mapUIButton(ev.Button)})
				return true
			case ui.InputPointerUp:
				a.sendRemoteInput(core.RemoteInputEvent{Kind: core.InputMouseUp, X: nx, Y: ny, Button: mapUIButton(ev.Button)})
				return true
			case ui.InputScroll:
				a.sendRemoteInput(core.RemoteInputEvent{Kind: core.InputMouseWheel, Delta: int(-ev.DY * 120)})
				return true
			case ui.InputKeyDown:
				if vk := uiKeyToVK(ev.Key); vk != 0 {
					a.sendRemoteInput(core.RemoteInputEvent{Kind: core.InputKeyDown, VKCode: vk})
					return true
				}
			case ui.InputKeyUp:
				if vk := uiKeyToVK(ev.Key); vk != 0 {
					a.sendRemoteInput(core.RemoteInputEvent{Kind: core.InputKeyUp, VKCode: vk})
					return true
				}
			}
			return false
		})
		stage.Children(func() {
			if bmp != nil {
				ui.Image(c, bmp).Fill().Fit(ui.Contain)
			} else {
				ui.Text(c, "正在请求学生机实时画面...").FontSize(13).TextColor(ui.Hex("#D4D4D4"))
			}
		})
	})
}

func (a *App) StartRemoteControl(studentID string) {
	if studentID == "" {
		a.statusNote = "请先在座位图或列表中选择一台学生机"
		return
	}
	if a.remoteControlID != "" && a.remoteControlID != studentID {
		a.sendControlEnvelope(protocol.MsgRemoteControlReq, map[string]any{"student_id": a.remoteControlID, "enabled": false}, []string{a.remoteControlID})
	}
	a.remoteControlID = studentID
	a.remoteControlOpen = true
	a.resetRemoteView(studentID)
	a.activeModeLabel = "远程控制"
	a.statusNote = "已发起远程控制请求 -> " + studentID
	a.sendControlEnvelope(protocol.MsgRemoteControlReq, map[string]any{"student_id": studentID, "enabled": true}, []string{studentID})
}

func (a *App) StopRemoteControl() {
	if a.remoteControlID != "" {
		a.sendControlEnvelope(protocol.MsgRemoteControlReq, map[string]any{"student_id": a.remoteControlID, "enabled": false}, []string{a.remoteControlID})
	}
	a.remoteControlID = ""
	a.remoteControlOpen = false
	a.resetRemoteView("")
	a.activeModeLabel = "待机监听"
	a.statusNote = "已结束远程控制"
}

// resetRemoteView drops the previous target's frames so the modal never
// shows one student's screen while input goes to another.
func (a *App) resetRemoteView(target string) {
	a.mu.Lock()
	a.remoteTargetSnap = target
	a.remoteBitmap = nil
	a.pendingRemote = nil
	a.mu.Unlock()
}

func (a *App) sendRemoteInput(ev core.RemoteInputEvent) {
	if a.remoteControlID == "" {
		return
	}
	a.sendControlEnvelope(protocol.MsgRemoteInputEvent, ev, []string{a.remoteControlID})
}

// maxQueuedRemoteInput bounds how many remote input events may wait in the
// control queue; older input is worthless, so newer input is dropped beyond it.
const maxQueuedRemoteInput = 64

func mapUIButton(btn int) int {
	switch btn {
	case 1:
		return 2 // UI secondary -> Win32 right
	case 2:
		return 1 // UI middle -> Win32 middle
	default:
		return 0
	}
}

func uiKeyToVK(k ui.Key) uint16 {
	switch {
	case k >= ui.KeyA && k <= ui.KeyZ:
		return uint16('A' + (k - ui.KeyA))
	case k >= ui.Key0 && k <= ui.Key9:
		return uint16('0' + (k - ui.Key0))
	case k >= ui.KeyF1 && k <= ui.KeyF12:
		return uint16(0x70 + (k - ui.KeyF1))
	}
	switch k {
	case ui.KeyEnter:
		return 0x0D
	case ui.KeyEscape:
		return 0x1B
	case ui.KeyBackspace:
		return 0x08
	case ui.KeyTab:
		return 0x09
	case ui.KeySpace:
		return 0x20
	case ui.KeyDelete:
		return 0x2E
	case ui.KeyInsert:
		return 0x2D
	case ui.KeyHome:
		return 0x24
	case ui.KeyEnd:
		return 0x23
	case ui.KeyPageUp:
		return 0x21
	case ui.KeyPageDown:
		return 0x22
	case ui.KeyLeft:
		return 0x25
	case ui.KeyUp:
		return 0x26
	case ui.KeyRight:
		return 0x27
	case ui.KeyDown:
		return 0x28
	case ui.KeyMinus:
		return 0xBD
	case ui.KeyEqual:
		return 0xBB
	case ui.KeyComma:
		return 0xBC
	case ui.KeyPeriod:
		return 0xBE
	case ui.KeySlash:
		return 0xBF
	case ui.KeySemicolon:
		return 0xBA
	case ui.KeyQuote:
		return 0xDE
	case ui.KeyBracketLeft:
		return 0xDB
	case ui.KeyBracketRight:
		return 0xDD
	case ui.KeyBackslash:
		return 0xDC
	case ui.KeyBackquote:
		return 0xC0
	default:
		return 0
	}
}
