package student

import (
	"crypto/subtle"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"nexlab/internal/ui/theme"
)

const (
	lockRevealTaps   = 5
	lockRevealWindow = 3 * time.Second
)

func (a *App) buildLockView(c *ui.Context, pal theme.Palette) {
	ui.Column(c).Fill().Background(pal.LockBg).Children(func() {
		ui.Column(c).Grow(1).Center().Gap(14).Padding(24).Children(func() {
			ui.Badge(c, "黑屏锁屏中").Background(ui.Hex("#262626")).TextColor(ui.Hex("#FFFFFF"))
			ui.Text(c, a.lockMessage).FontSize(20).Bold().TextColor(ui.Hex("#F5F5F5")).OnClick(a.registerLockTap)
			ui.Text(c, "键盘与鼠标已被教师端锁定，断网超时或教师解锁后自动恢复").FontSize(12).TextColor(ui.Hex("#8E8E8E"))

			if a.unlockRevealed {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.TextInput(c, &a.unlockPassInput).Password().Placeholder("输入学生端密码").Width(220).OnSubmit(a.tryEmergencyUnlock)
					if ui.Button(c, "验证解锁").Padding(6, 14).Clicked() {
						a.tryEmergencyUnlock()
					}
				})
			}
			if a.unlockStatus != "" {
				ui.Text(c, a.unlockStatus).FontSize(12).TextColor(ui.Hex("#D4D4D4"))
			}
		})
		ui.Row(c).Padding(0, 24, 24, 24).AlignItems(ui.Center).Gap(10).Children(func() {
			label := "举手"
			if a.handRaised {
				label = "取消举手"
			}
			if ui.Button(c, label).Padding(8, 18).Clicked() {
				a.ToggleHandRaise()
			}
			if a.handRaised {
				ui.Text(c, "已举手，等待教师处理").FontSize(12).TextColor(ui.Hex("#8E8E8E"))
			}
		})
	})
}

func (a *App) registerLockTap() {
	now := time.Now()
	if a.lockTapAt.IsZero() || now.Sub(a.lockTapAt) > lockRevealWindow {
		a.lockTapCount = 0
	}
	a.lockTapAt = now
	a.lockTapCount++
	if a.lockTapCount >= lockRevealTaps {
		a.lockTapCount = 0
		a.unlockRevealed = true
	}
}

func (a *App) SetLocked(locked bool, message string) {
	a.mu.Lock()
	a.screenLocked = locked
	if message != "" {
		a.lockMessage = message
	}
	if locked {
		a.mode = ModeLock
	} else {
		a.unlockRevealed = false
		a.unlockPassInput = ""
		a.unlockStatus = ""
		a.lockTapCount = 0
		if a.mode == ModeLock {
			a.mode = ModeBar
		}
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
		a.unlockStatus = "请输入学生端密码"
		return
	}
	a.mu.Lock()
	expected := a.exitPasswordHash
	a.mu.Unlock()
	if expected == "" {
		a.unlockStatus = "未设置学生端密码，请联系教师解锁"
		a.unlockPassInput = ""
		return
	}
	actual := hashExitPassword(a.unlockPassInput)
	if subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) != 1 {
		a.unlockStatus = "密码错误"
		a.unlockPassInput = ""
		return
	}
	a.SetLocked(false, "")
}
