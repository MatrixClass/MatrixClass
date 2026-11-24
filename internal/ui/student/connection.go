package student

import (
	"errors"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"nexlab/internal/protocol"
	"nexlab/internal/ui/theme"
)

// SetConnectionState updates the student connection indicator and owns the
// persistent, topmost disconnect notice shown during reconnect attempts.
func (a *App) SetConnectionState(connected bool, reason string) {
	a.mu.Lock()
	a.connected = connected
	if connected {
		a.statusNote = "已连接频道: " + a.channel
	} else {
		a.statusNote = "连接已断开，正在重新连接…"
	}
	w := a.win
	a.mu.Unlock()
	if connected {
		a.hideConnectionNotice()
	} else if w != nil {
		a.showConnectionNotice()
	}
	if w != nil {
		w.Invalidate()
	}
}

func (a *App) showConnectionNotice() {
	a.mu.Lock()
	mainWin := a.win
	notice := a.connectionNoticeWin
	a.mu.Unlock()
	if mainWin == nil {
		return
	}
	mainWin.Update(func() {
		const noticeW, noticeH = 380, 150
		wa := mygo.Screen.PrimaryDisplay().WorkArea
		x := wa.X + (wa.Width-noticeW)/2
		y := wa.Y + (wa.Height-noticeH)/2
		if notice == nil {
			notice = mygo.NewWindow(mygo.WindowOptions{
				Title:         "矩联电子教室 - 连接状态",
				Width:         noticeW,
				Height:        noticeH,
				X:             x,
				Y:             y,
				AlwaysOnTop:   true,
				SkipTaskbar:   false,
				DisableResize: true,
				Content:       ui.View(a.ConnectionNoticeView),
			})
			a.mu.Lock()
			a.connectionNoticeWin = notice
			a.mu.Unlock()
		} else {
			notice.SetPosition(x, y)
			notice.SetAlwaysOnTop(true)
			notice.Invalidate()
		}
		notice.ShowInactive()
	})
}

func (a *App) hideConnectionNotice() {
	a.mu.Lock()
	notice := a.connectionNoticeWin
	a.mu.Unlock()
	if notice != nil {
		notice.Hide()
	}
}

func (a *App) ConnectionNoticeView(c *ui.Context) {
	a.mu.Lock()
	channel := a.channel
	a.mu.Unlock()
	pal := theme.GetPalette(false)
	c.SetTheme(theme.Theme(false))
	ui.Column(c).Fill().Background(pal.Canvas).Padding(18).Gap(10).Children(func() {
		ui.Text(c, "连接已断开").FontSize(18).Bold().TextColor(pal.Text)
		ui.Text(c, "正在重新连接教师端，请稍候…").FontSize(13).TextColor(pal.TextMuted)
		ui.Text(c, "频道 ID: "+channel).FontSize(12).TextColor(pal.TextMuted)
		ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
			ui.Text(c, "学生端会持续尝试连接").FontSize(12).TextColor(pal.Accent)
		})
	})
}

// errNotConnected is returned when no teacher session exists; the transport's
// BroadcastControl silently succeeds with zero sessions, so check first.
var errNotConnected = errors.New("未连接到教师端")

func (a *App) teacherConnected() bool {
	return a.opts.Transport != nil && a.opts.Transport.Stats().ActiveSessions > 0
}

func (a *App) sendControl(msgType protocol.MsgType, payload any) error {
	if !a.teacherConnected() {
		return errNotConnected
	}
	env, err := protocol.NewEnvelope(msgType, a.studentID, payload)
	if err != nil {
		return err
	}
	env.Channel = a.channel
	env.Timestamp = time.Now().UnixMilli()
	return a.opts.Transport.BroadcastControl(env, nil)
}
