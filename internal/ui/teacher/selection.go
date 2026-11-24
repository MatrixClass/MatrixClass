package teacher

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"nexlab/internal/core"
	"nexlab/internal/protocol"
	"nexlab/internal/ui/theme"
)

func (a *App) isSelected(id string) bool {
	if id == "" {
		return false
	}
	if len(a.selectedIDs) > 0 {
		return a.selectedIDs[id]
	}
	return a.selectedID == id
}

func (a *App) selectedCount() int {
	if len(a.selectedIDs) > 0 {
		return len(a.selectedIDs)
	}
	if a.selectedID != "" {
		return 1
	}
	return 0
}

func (a *App) selectSingle(id string) {
	if a.selectedIDs == nil {
		a.selectedIDs = make(map[string]bool)
	} else {
		clear(a.selectedIDs)
	}
	a.selectedID = id
	if id != "" {
		a.selectedIDs[id] = true
	}
}

func (a *App) toggleSelect(id string) {
	if id == "" {
		return
	}
	if a.selectedIDs == nil {
		a.selectedIDs = make(map[string]bool)
		if a.selectedID != "" {
			a.selectedIDs[a.selectedID] = true
		}
	}
	if a.selectedIDs[id] {
		delete(a.selectedIDs, id)
		if a.selectedID == id {
			a.selectedID = ""
			for k := range a.selectedIDs {
				a.selectedID = k
				break
			}
		}
	} else {
		a.selectedIDs[id] = true
		a.selectedID = id
	}
}

func (a *App) selectAllStudents(students []core.StudentInfo) {
	if a.selectedIDs == nil {
		a.selectedIDs = make(map[string]bool)
	} else {
		clear(a.selectedIDs)
	}
	a.selectedID = ""
	for i, st := range students {
		a.selectedIDs[st.ID] = true
		if i == 0 {
			a.selectedID = st.ID
		}
	}
	if len(students) > 0 {
		a.statusNote = fmt.Sprintf("已全选 %d 台学生机 (可右键批量操作)", len(students))
	}
}

func (a *App) clearSelection() {
	if a.selectedIDs != nil {
		clear(a.selectedIDs)
	}
	a.selectedID = ""
}

func (a *App) targetStudentIDs() []string {
	if len(a.selectedIDs) > 0 {
		var out []string
		for _, st := range a.listStudents() {
			if a.selectedIDs[st.ID] {
				out = append(out, st.ID)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if a.selectedID != "" {
		return []string{a.selectedID}
	}
	return nil
}

func (a *App) attachBlankAreaContextMenu(elem ui.Element, students []core.StudentInfo) {
	elem.ContextMenu(func(m *ui.Menu) {
		offlineCount := 0
		for _, st := range a.listStudents() {
			if st.Status == core.StatusOffline {
				offlineCount++
			}
		}
		clearLbl := "清除离线电脑"
		if offlineCount > 0 {
			clearLbl = fmt.Sprintf("清除离线电脑 (%d)", offlineCount)
		}
		if m.Item(clearLbl).Chosen() {
			a.ClearOfflineStudents()
		}
		if len(students) > 0 {
			if m.Item("全选 (Ctrl+A)").Chosen() {
				a.selectAllStudents(students)
			}
		}
		if a.selectedCount() > 0 {
			if m.Item("取消选择").Chosen() {
				a.clearSelection()
			}
		}
	})
}

func (a *App) attachMarqueeElement(elem ui.Element, itemID string, students []core.StudentInfo) {
	a.itemBounds = ensureBoundsMap(a.itemBounds)
	eb := elem.Bounds()
	elem.HandleInput(func(ev ui.InputEvent) bool {
		wx := eb.X + ev.X
		wy := eb.Y + ev.Y
		switch ev.Kind {
		case ui.InputPointerDown:
			if ev.Button != 0 {
				return false
			}
			a.marqueeActive = true
			a.marqueeMoved = false
			a.marqueeAdditive = (ev.Mods & (ui.Ctrl | ui.Cmd)) != 0
			a.marqueeStartX = wx
			a.marqueeStartY = wy
			a.marqueeCurX = wx
			a.marqueeCurY = wy
			clear(a.marqueeBase)
			if a.marqueeAdditive {
				for k, v := range a.selectedIDs {
					if v {
						a.marqueeBase[k] = true
					}
				}
				if a.selectedID != "" {
					a.marqueeBase[a.selectedID] = true
				}
			}
			return true
		case ui.InputPointerMove:
			if !a.marqueeActive {
				return false
			}
			a.marqueeCurX = wx
			a.marqueeCurY = wy
			dx := a.marqueeCurX - a.marqueeStartX
			dy := a.marqueeCurY - a.marqueeStartY
			if dx*dx+dy*dy >= 36 {
				a.marqueeMoved = true
				a.applyMarqueeSelection(students)
				return true
			}
			return true
		case ui.InputPointerUp:
			if !a.marqueeActive {
				return false
			}
			wasMoved := a.marqueeMoved
			if wasMoved && !ev.Canceled {
				a.marqueeCurX = wx
				a.marqueeCurY = wy
				a.applyMarqueeSelection(students)
				a.suppressClear = true
			}
			additive := a.marqueeAdditive || (ev.Mods&(ui.Ctrl|ui.Cmd)) != 0
			a.marqueeActive = false
			a.marqueeMoved = false
			if !wasMoved && !ev.Canceled {
				if itemID != "" {
					if additive {
						a.toggleSelect(itemID)
					} else {
						a.selectSingle(itemID)
					}
				} else if !additive {
					a.clearSelection()
				}
			}
			return true
		}
		return false
	})
}

func ensureBoundsMap(m map[string]ui.Rect) map[string]ui.Rect {
	if m == nil {
		return make(map[string]ui.Rect)
	}
	return m
}

func (a *App) applyMarqueeSelection(students []core.StudentInfo) {
	minX, maxX := a.marqueeStartX, a.marqueeCurX
	if minX > maxX {
		minX, maxX = maxX, minX
	}
	minY, maxY := a.marqueeStartY, a.marqueeCurY
	if minY > maxY {
		minY, maxY = maxY, minY
	}
	if a.selectedIDs == nil {
		a.selectedIDs = make(map[string]bool)
	} else {
		clear(a.selectedIDs)
	}
	if a.marqueeAdditive {
		for k, v := range a.marqueeBase {
			if v {
				a.selectedIDs[k] = true
			}
		}
	}
	a.selectedID = ""
	for _, st := range students {
		b, ok := a.itemBounds[st.ID]
		if !ok || b.W <= 0 || b.H <= 0 {
			continue
		}
		if b.X < maxX && b.X+b.W > minX && b.Y < maxY && b.Y+b.H > minY {
			a.selectedIDs[st.ID] = true
		}
	}
	for _, st := range students {
		if a.selectedIDs[st.ID] {
			a.selectedID = st.ID
			break
		}
	}
}

func (a *App) drawMarqueeOverlay(c *ui.Context, pal theme.Palette, scrollBounds ui.Rect) {
	if !a.marqueeActive || !a.marqueeMoved || scrollBounds.W <= 0 || scrollBounds.H <= 0 {
		return
	}
	minX, maxX := a.marqueeStartX, a.marqueeCurX
	if minX > maxX {
		minX, maxX = maxX, minX
	}
	minY, maxY := a.marqueeStartY, a.marqueeCurY
	if minY > maxY {
		minY, maxY = maxY, minY
	}
	ui.Box(c).Absolute().
		Left(minX-scrollBounds.X).Top(minY-scrollBounds.Y).
		Width(maxX-minX).Height(maxY-minY).
		Background(ui.RGBA(0, 0, 0, 0.08)).
		Border(1, pal.Accent).Radius(2)
}

func (a *App) attachStudentContextMenu(elem ui.Element, st core.StudentInfo) {
	elem.ContextMenu(func(m *ui.Menu) {
		if !a.isSelected(st.ID) {
			a.selectSingle(st.ID)
		}
		targets := a.targetStudentIDs()
		if len(targets) == 0 {
			targets = []string{st.ID}
		}
		batch := len(targets) > 1
		prefix := ""
		if batch {
			prefix = fmt.Sprintf("批量(%d台) ", len(targets))
		}

		if m.Item(prefix + "监控屏幕").Chosen() {
			a.monitoring = true
			a.mainTab = TabMonitorWall
			a.activeModeLabel = "屏幕监控"
			a.statusNote = fmt.Sprintf("正在监控 %d 台学生屏幕", len(targets))
			a.sendControlEnvelope(protocol.MsgMonitorControl, struct {
				Active bool `json:"active"`
				core.MonitorConfig
			}{
				Active: true,
				MonitorConfig: core.MonitorConfig{
					FPS:       int(a.monitorFPS),
					Quality:   int(a.monitorQual),
					Width:     320,
					Height:    180,
					TargetIDs: targets,
				},
			}, targets)
		}
		if m.Item(prefix + "屏幕广播").Chosen() {
			a.broadcasting = true
			a.syncRuntimeFlags()
			cfg := a.CurrentBroadcastConfig()
			a.activeModeLabel = "屏幕广播"
			a.statusNote = fmt.Sprintf("正在向 %d 台学生广播屏幕", len(targets))
			a.sendControlEnvelope(protocol.MsgBroadcastStart, cfg, targets)
		}
		if !batch {
			if m.Item("远程控制").Chosen() {
				a.StartRemoteControl(st.ID)
			}
			if m.Item("指定为学生演示").Chosen() {
				a.ToggleStudentDemo(st.ID)
			}
		}
		m.Separator()
		if m.Item(prefix + "黑屏锁定").Chosen() {
			a.setScreenLockTargets(true, targets)
		}
		if m.Item(prefix + "解除锁屏").Chosen() {
			a.setScreenLockTargets(false, targets)
		}
		if m.Item(prefix + "发送文件...").Chosen() {
			a.fileTargetAll = false
			a.OpenFileDistributeWindow()
			a.statusNote = fmt.Sprintf("已选定 %d 台学生作为文件分发目标", len(targets))
		}
		if groups := a.listGroups(); len(groups) > 0 {
			m.Separator()
			for _, g := range groups {
				g := g
				if m.Item(prefix + "移入「" + g.Name + "」").Chosen() {
					a.assignTargetsToGroup(g.ID, targets)
				}
			}
		}
		m.Separator()
		if m.Item(prefix + "远程唤醒 (WOL)").Chosen() {
			a.ExecutePowerTargets(core.PowerWake, targets)
		}
		if m.Item(prefix + "远程重启").Chosen() {
			a.ExecutePowerTargets(core.PowerReboot, targets)
		}
		if m.Item(prefix + "远程关机").Chosen() {
			a.ExecutePowerTargets(core.PowerShutdown, targets)
		}
		if batch {
			m.Separator()
			if m.Item("取消全选").Chosen() {
				a.clearSelection()
			}
		}
	})
}
