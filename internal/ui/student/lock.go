package student

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"nexlab/internal/ui/theme"
)

func (a *App) buildLockView(c *ui.Context, pal theme.Palette) {
	ui.Column(c).Fill().Background(pal.LockBg).Center().Gap(14).Padding(24).Children(func() {
		ui.Badge(c, "黑屏锁屏中").Background(ui.Hex("#262626")).TextColor(ui.Hex("#FFFFFF"))
		ui.Text(c, a.lockMessage).FontSize(20).Bold().TextColor(ui.Hex("#F5F5F5"))
		ui.Text(c, "键盘与鼠标已被教师端锁定，断网超时或教师解锁后自动恢复").FontSize(12).TextColor(ui.Hex("#8E8E8E"))

		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.TextInput(c, &a.unlockPassInput).Password().Placeholder("紧急本地解锁口令...").Width(220)
			if ui.Button(c, "验证解锁").Padding(6, 14).Clicked() {
				a.tryEmergencyUnlock()
			}
		})
		if a.unlockStatus != "" {
			ui.Text(c, a.unlockStatus).FontSize(12).TextColor(ui.Hex("#D4D4D4"))
		}
	})
}

func (a *App) SetLocked(locked bool, message string) {
	a.mu.Lock()
	a.screenLocked = locked
	if message != "" {
		a.lockMessage = message
	}
	if locked {
		a.mode = ModeLock
	} else if a.mode == ModeLock {
		a.mode = ModeBar
	}
	w := a.win
	a.mu.Unlock()
	if a.opts.Control != nil {
		_ = a.opts.Control.SetScreenLock(locked, message)
	}
	if w != nil {
		w.Update(func() {
			a.syncWindowOnMain(w, true, locked)
		})
	}
}

func (a *App) tryEmergencyUnlock() {
	if strings.TrimSpace(a.unlockPassInput) == "" {
		a.unlockStatus = "请输入解锁口令"
		return
	}
	type boolUnlocker interface {
		EmergencyUnlock(pass string) (bool, error)
	}
	type errUnlocker interface {
		EmergencyUnlock(pass string) error
	}
	switch u := a.opts.Control.(type) {
	case boolUnlocker:
		ok, err := u.EmergencyUnlock(a.unlockPassInput)
		if err != nil {
			a.unlockStatus = "口令错误: " + err.Error()
			return
		}
		if !ok {
			a.unlockStatus = "口令错误"
			return
		}
	case errUnlocker:
		if err := u.EmergencyUnlock(a.unlockPassInput); err != nil {
			a.unlockStatus = "口令错误: " + err.Error()
			return
		}
	default:
		if a.unlockPassInput != "nexlab-unlock" {
			a.unlockStatus = "口令错误"
			return
		}
	}
	if a.opts.Control == nil && a.unlockPassInput != "nexlab-unlock" {
		a.unlockStatus = "口令错误"
		return
	}
	a.unlockPassInput = ""
	a.unlockStatus = ""
	a.SetLocked(false, "")
}
