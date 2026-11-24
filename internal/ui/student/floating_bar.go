package student

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"nexlab/internal/ui/theme"
)

func (a *App) positionBarTopCenter(w *mygo.Window) {
	if w == nil {
		return
	}
	wa := mygo.Screen.PrimaryDisplay().WorkArea
	const barW = 740
	x := wa.X + (wa.Width-barW)/2
	if x < wa.X {
		x = wa.X
	}
	w.SetPosition(x, wa.Y)
}

func (a *App) scheduleBarAutoDock() {
	a.mu.Lock()
	a.barAutoDockSeq++
	seq := a.barAutoDockSeq
	a.mu.Unlock()

	time.AfterFunc(3*time.Second, func() {
		a.mu.Lock()
		if a.barAutoDockSeq != seq || a.mode != ModeBar || a.barHovered || a.barFocused || a.barDocked {
			a.mu.Unlock()
			return
		}
		a.barDocked = true
		a.mu.Unlock()
		a.syncWindow()
		a.Invalidate()
	})
}

func (a *App) wakeFloatingBar() {
	a.mu.Lock()
	a.barAutoDockSeq++
	wasDocked := a.barDocked
	a.barDocked = false
	a.mu.Unlock()
	if wasDocked {
		a.syncWindow()
		a.Invalidate()
	}
}

func (a *App) setBarFocused(focused bool) {
	a.mu.Lock()
	if a.barFocused == focused {
		a.mu.Unlock()
		return
	}
	a.barFocused = focused
	mode := a.mode
	hovered := a.barHovered
	wasDocked := a.barDocked
	if focused && mode == ModeBar {
		a.barAutoDockSeq++
		a.barDocked = false
	}
	a.mu.Unlock()

	if focused && mode == ModeBar && wasDocked {
		a.syncWindow()
		a.Invalidate()
		return
	}
	if !focused && !hovered && mode == ModeBar {
		a.scheduleBarAutoDock()
	}
}

func (a *App) setBarHovered(hovered bool) {
	a.mu.Lock()
	if a.barHovered == hovered {
		a.mu.Unlock()
		return
	}
	a.barHovered = hovered
	mode := a.mode
	focused := a.barFocused
	wasDocked := a.barDocked
	if hovered && mode == ModeBar {
		a.barAutoDockSeq++
		a.barDocked = false
	}
	a.mu.Unlock()

	if hovered && mode == ModeBar && wasDocked {
		a.syncWindow()
		a.Invalidate()
		return
	}
	if !hovered && !focused && mode == ModeBar {
		a.scheduleBarAutoDock()
	}
}

func (a *App) ShowFloatingBar() {
	a.mu.Lock()
	a.mode = ModeBar
	a.barCollapsed = false
	a.barDocked = false
	a.barAutoDockSeq++
	w := a.win
	a.mu.Unlock()
	if w != nil {
		w.Update(func() {
			a.syncWindowOnMain(w, true, true)
		})
	}
	a.scheduleBarAutoDock()
}

func (a *App) buildFloatingBarView(c *ui.Context, pal theme.Palette) {
	a.mu.Lock()
	docked := a.barDocked
	wasHovered := a.barHovered
	a.mu.Unlock()

	root := ui.Column(c).Fill().Background(pal.ChromeBar)
	hovered := root.Hovered()
	if hovered != wasHovered {
		go a.setBarHovered(hovered)
	}

	if docked {
		root.Radius(5).Padding(2, 0).Center().Border(1, pal.CardBorder).Children(func() {
			ui.Box(c).Width(72).Height(3).Radius(2).Background(pal.Accent)
		})
		return
	}

	root.Radius(10).Border(1, pal.CardBorder).Padding(6, 7).Gap(6).Children(func() {
		// Top draggable floating bar
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).DragWindow().Children(func() {
			ui.Badge(c, a.channel).Background(pal.Selection).TextColor(pal.Text)
			barTitle := a.machineName
			if strings.TrimSpace(a.studentNo) != "" {
				barTitle = fmt.Sprintf("%s (%s)", a.machineName, a.studentNo)
			}
			ui.Text(c, barTitle).FontSize(12).Bold().TextColor(pal.Text).SingleLine()
			if a.handRaised {
				ui.Badge(c, "已举手").Background(pal.Accent).TextColor(pal.AccentText)
			}
			if a.remoteControlled {
				ui.Badge(c, "远控中").Background(pal.Selection).TextColor(pal.Text)
			}
			if a.monitoringActive {
				ui.Badge(c, "监控中").Background(pal.Selection).TextColor(pal.Text)
			}
			ui.Spacer(c)

			handLabel := "举手"
			if a.handRaised {
				handLabel = "已举手"
			}
			a.barIconButton(c, pal, handLabel, theme.IconHandRaise, a.handRaised, func() {
				a.ToggleHandRaise()
			})
			a.barIconButton(c, pal, "作业", theme.IconCollectHomework, a.homeworkCollecting, func() {
				if a.homeworkCollecting {
					a.OpenHomeworkSubmitWindow()
					return
				}
				a.OpenMode(ModeHomeworkSubmit)
			})
			a.barIconButton(c, pal, "消息", theme.IconChat, false, func() {
				a.OpenMode(ModeChat)
			})
			if a.videoBitmap != nil && a.broadcastActive {
				a.barIconButton(c, pal, "广播", theme.IconBroadcast, false, func() {
					a.mu.Lock()
					a.mode = ModeBroadcast
					a.mu.Unlock()
					a.syncWindow()
				})
			}
			collapseLabel := "收起"
			collapseIcon := theme.IconChevronUp
			if a.barCollapsed {
				collapseLabel = "展开"
				collapseIcon = theme.IconChevronDown
			}
			a.barIconButton(c, pal, collapseLabel, collapseIcon, false, func() {
				a.mu.Lock()
				a.barCollapsed = !a.barCollapsed
				a.mu.Unlock()
				a.syncWindow()
			})
		})

		if !a.barCollapsed {
			ui.Row(c).FillWidth().Padding(4, 6).Radius(6).Background(pal.Canvas).Border(1, pal.CardBorder).AlignItems(ui.Center).Gap(10).Children(func() {
				ui.Text(c, a.statusNote).FontSize(11).TextColor(pal.TextMuted)
				ui.Spacer(c)
				ui.Text(c, fmt.Sprintf("音量 %.0f%%", a.volume)).FontSize(11).TextColor(pal.TextMuted)
				sl := ui.Slider(c, &a.volume, 0, 100).Width(110).Step(1)
				if sl.Changed() {
					if a.opts.Media != nil {
						a.opts.Media.SetVolume(float32(a.volume / 100.0))
					}
				}
			})
		}
	})
}

func (a *App) barIconButton(c *ui.Context, pal theme.Palette, label string, icon *ui.SVG, active bool, onClick func()) {
	bg := pal.Card
	fg := pal.Text
	border := pal.CardBorder
	if active {
		bg = pal.Accent
		fg = pal.AccentText
		border = pal.Accent
	}
	btn := ui.Row(c).AlignItems(ui.Center).Gap(5).Padding(6, 11).Radius(5).
		Background(bg).Border(1, border).Focusable().Label(label)
	if btn.Hovered() && !active {
		btn.Background(pal.ChromeHover)
	}
	btn.Children(func() {
		if icon != nil {
			ui.Image(c, icon).Size(20, 20)
		}
		ui.Text(c, label).FontSize(13).Bold().TextColor(fg).SingleLine()
	})
	if btn.Clicked() && onClick != nil {
		onClick()
	}
}
