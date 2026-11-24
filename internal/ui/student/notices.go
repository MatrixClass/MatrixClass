package student

import (
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"nexlab/internal/ui/theme"
)

func (a *App) SetStatusNote(note string) {
	a.mu.Lock()
	a.statusNote = note
	w := a.win
	a.mu.Unlock()
	if w != nil {
		w.Invalidate()
	}
}

// ShowTeacherNotice pops up a bottom-right desktop notification toast for incoming teacher messages.
func (a *App) ShowTeacherNotice(sender, text string) {
	if strings.TrimSpace(sender) == "" {
		sender = "教师端"
	}
	a.mu.Lock()
	a.toastSender = sender
	a.toastText = text
	a.toastVisible = true
	a.toastSeq++
	seq := a.toastSeq
	a.statusNote = "新消息: " + text
	w := a.win
	a.mu.Unlock()

	if w == nil {
		return
	}

	w.Update(func() {
		const toastW, toastH = 320, 116
		wa := mygo.Screen.PrimaryDisplay().WorkArea
		x := wa.X + wa.Width - toastW - 16
		y := wa.Y + wa.Height - toastH - 16
		if x < 0 {
			x = 20
		}
		if y < 0 {
			y = 20
		}

		a.mu.Lock()
		tw := a.toastWin
		a.mu.Unlock()
		if tw == nil {
			tw = mygo.NewWindow(mygo.WindowOptions{
				Title:         "课堂消息提醒",
				Width:         toastW,
				Height:        toastH,
				X:             x,
				Y:             y,
				Frameless:     false,
				AlwaysOnTop:   true,
				SkipTaskbar:   true,
				DisableResize: true,
				Content:       ui.View(a.ToastView),
			})
			a.mu.Lock()
			a.toastWin = tw
			a.mu.Unlock()
		} else {
			tw.SetPosition(x, y)
			tw.SetAlwaysOnTop(true)
			tw.Invalidate()
		}
		tw.ShowInactive()
	})

	time.AfterFunc(6*time.Second, func() {
		a.mu.Lock()
		stillLatest := a.toastSeq == seq
		tw := a.toastWin
		if stillLatest {
			a.toastVisible = false
		}
		a.mu.Unlock()
		if stillLatest && tw != nil {
			tw.Hide()
		}
	})
}

// ToastView renders the bottom-right popup notice card.
func (a *App) ToastView(c *ui.Context) {
	a.mu.Lock()
	dark := a.dark
	sender := a.toastSender
	text := a.toastText
	a.mu.Unlock()

	pal := theme.GetPalette(dark)
	c.SetTheme(theme.Theme(dark))
	ui.Column(c).Fill().Background(pal.ChromeBar).Border(1, pal.Accent).Radius(4).Padding(10, 12).Gap(6).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Badge(c, "课堂通知").Background(pal.Accent).TextColor(pal.AccentText)
			ui.Text(c, sender).FontSize(12).Bold().TextColor(pal.Text).Grow(1).SingleLine()
			if ui.Button(c, "关闭").Padding(2, 8).Clicked() {
				a.hideToast()
			}
		})
		ui.Text(c, text).FontSize(12).TextColor(pal.Text).Grow(1)
		ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
			if ui.PrimaryButton(c, "查看与回复").Padding(3, 10).Clicked() {
				a.hideToast()
				a.OpenMode(ModeChat)
			}
		})
	})
}

func (a *App) hideToast() {
	a.mu.Lock()
	a.toastVisible = false
	tw := a.toastWin
	a.mu.Unlock()
	if tw != nil {
		tw.Hide()
	}
}
