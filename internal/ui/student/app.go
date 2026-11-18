package student

import (
	"context"
	"fmt"
	"image"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"nexlab/internal/core"
	"nexlab/internal/protocol"
	"nexlab/internal/ui/theme"
)

type Services struct {
	Transport core.TransportManager
	Media     core.MediaEngine
	Control   core.ControlEngine
	Classroom core.ClassroomService
	Files     core.FileTransferService
	Exam      core.ExamService
}

type Options struct {
	StudentID   string
	StudentName string
	StudentNo   string
	MachineName string
	Channel     string
	Dark        bool
	Services    Services
}

const (
	ModeBar = iota
	ModeBroadcast
	ModeLock
	ModeExam
	ModeCheckIn
	ModeChat
)

// App implements the MyGo native Student Client (tray + floating bar + broadcast/lock/exam/check-in views).
type App struct {
	mu sync.Mutex

	opts Services
	win  *mygo.Window
	toastWin *mygo.Window
	tray *mygo.Tray

	studentID   string
	studentNo   string
	machineName string
	channel     string
	dark        bool

	mode             int
	barCollapsed     bool
	handRaised       bool
	connected        bool
	broadcasting     bool
	broadcastWindow  bool
	remoteControlled bool
	monitoringActive bool
	screenLocked     bool
	lockMessage      string
	unlockPassInput  string
	unlockStatus     string
	checkInNo        string
	checkInSubmitted bool
	homeworkPath     string
	chatInput        string
	statusNote       string
	volume           float64

	lastWinMode      int
	lastWinCollapsed bool
	lastWinBcastWin  bool
	winSyncedOnce    bool

	videoBitmap *ui.Bitmap
	lastFrameAt time.Time
	pendingFrame   *core.VideoFrame
	frameScheduled atomic.Bool

	activePaper   *core.ExamPaper
	examAnswers   map[string]string
	examResult    *core.ExamSubmission
	recentNotices []string

	toastSender  string
	toastText    string
	toastVisible bool
	toastSeq     uint64
}

func New(opts Options) *App {
	if opts.StudentID == "" {
		opts.StudentID = "stu-01"
	}
	if opts.StudentNo == "" {
		opts.StudentNo = "202601"
	}
	if opts.MachineName == "" {
		opts.MachineName = "LAB-PC01"
	}
	if opts.Channel == "" {
		opts.Channel = "301机房"
	}
	return &App{
		opts:          opts.Services,
		studentID:     opts.StudentID,
		studentNo:     opts.StudentNo,
		machineName:   opts.MachineName,
		channel:       opts.Channel,
		dark:          opts.Dark,
		mode:          ModeBar,
		broadcastWindow: true,
		connected:     true,
		lockMessage:   "课堂黑屏安静模式：请注意前方黑板或教师讲解。",
		checkInNo:     opts.StudentNo,
		homeworkPath:  "D:\\Homework\\Lab03_Report.docx",
		statusNote:    "已连接频道: " + opts.Channel,
		volume:        80,
		examAnswers:   make(map[string]string),
		recentNotices: []string{"欢迎加入 NexLab 电子教室"},
		lastWinMode:   -1,
	}
}

// AttachWindow binds the floating/main window.
func (a *App) AttachWindow(w *mygo.Window) {
	a.mu.Lock()
	a.win = w
	a.mu.Unlock()
	if w != nil {
		_ = w.SetIcon(theme.AppIconPNG(false))
		w.OnClosed(func() {
			a.mu.Lock()
			tw := a.toastWin
			a.toastWin = nil
			a.mu.Unlock()
			if tw != nil {
				tw.Destroy()
			}
		})
	}
}

// InstallTray creates the native system tray icon and menu for the student client.
func (a *App) InstallTray() {
	menu := mygo.NewMenu([]*mygo.MenuItem{
		{Label: "显示/隐藏悬浮条", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.ShowFloatingBar() }},
		{Label: "举手提问", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.ToggleHandRaise() }},
		{Label: "打开点名签到", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.OpenMode(ModeCheckIn) }},
		{Label: "提交作业 / 消息", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.OpenMode(ModeChat) }},
		mygo.Separator(),
		{Role: mygo.RoleQuit, Label: "退出学生端"},
	})
	tray, err := mygo.NewTray(mygo.TrayOptions{
		ID:      "nexlab-student",
		Icon:    theme.AppIconPNG(false),
		ToolTip: "矩联电子教室 MatrixClass 学生端 - " + a.channel,
		Menu:    menu,
	})
	if err == nil {
		tray.OnClick(func() { a.ShowFloatingBar() })
		a.mu.Lock()
		a.tray = tray
		a.mu.Unlock()
	}
}

func (a *App) Invalidate() {
	a.mu.Lock()
	w := a.win
	a.mu.Unlock()
	if w != nil {
		w.Invalidate()
	}
}

// UpdateVideoFrame uploads an incoming RGBA or NV12 frame onto the GPU bitmap.
func (a *App) UpdateVideoFrame(frame *core.VideoFrame) {
	if frame == nil {
		return
	}
	a.mu.Lock()
	w := a.win
	if w == nil {
		a.applyVideoFrameLocked(frame)
		a.lastFrameAt = time.Now()
		a.mu.Unlock()
		return
	}
	a.pendingFrame = frame
	a.mu.Unlock()

	if a.frameScheduled.CompareAndSwap(false, true) {
		w.Update(func() {
			a.frameScheduled.Store(false)
			a.mu.Lock()
			f := a.pendingFrame
			a.pendingFrame = nil
			if f != nil {
				a.applyVideoFrameLocked(f)
				a.lastFrameAt = time.Now()
			}
			a.mu.Unlock()
		})
	}
}

func (a *App) applyVideoFrameLocked(frame *core.VideoFrame) {
	if len(frame.NV12Y) > 0 && len(frame.NV12UV) > 0 {
		if a.videoBitmap == nil {
			a.videoBitmap = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, frame.Width, frame.Height)))
		}
		a.videoBitmap.UpdateYUV(&ui.YUVImage{
			Format:  ui.NV12,
			Width:   frame.Width,
			Height:  frame.Height,
			Planes:  [3][]byte{frame.NV12Y, frame.NV12UV},
			Strides: [3]int{frame.StrideY, frame.StrideUV},
			Matrix:  ui.BT709,
		})
	} else if len(frame.Data) >= frame.Width*frame.Height*4 && frame.Width > 0 && frame.Height > 0 {
		rgba := &image.RGBA{
			Pix:    frame.Data,
			Stride: frame.Width * 4,
			Rect:   image.Rect(0, 0, frame.Width, frame.Height),
		}
		if a.videoBitmap == nil {
			a.videoBitmap = ui.NewBitmap(rgba)
		} else {
			a.videoBitmap.Update(rgba)
		}
	}
}

func (a *App) ShowFloatingBar() {
	a.mu.Lock()
	a.mode = ModeBar
	a.barCollapsed = false
	w := a.win
	a.mu.Unlock()
	if w != nil {
		w.Update(func() {
			a.syncWindowOnMain(w, true, true)
		})
	}
}

func (a *App) OpenMode(m int) {
	a.mu.Lock()
	a.mode = m
	if m == ModeExam {
		a.loadActivePaper()
	}
	w := a.win
	a.mu.Unlock()
	if w != nil {
		w.Update(func() {
			a.syncWindowOnMain(w, true, true)
		})
	}
}

func (a *App) StartBroadcast(windowed bool) {
	a.mu.Lock()
	a.broadcastWindow = windowed
	a.mode = ModeBroadcast
	a.statusNote = "正在接收教师端屏幕广播"
	w := a.win
	a.mu.Unlock()
	if w != nil {
		w.Update(func() {
			a.syncWindowOnMain(w, true, true)
		})
	}
}

func (a *App) SetBroadcastWindowed(windowed bool) {
	a.mu.Lock()
	a.broadcastWindow = windowed
	w := a.win
	a.mu.Unlock()
	if w != nil {
		w.Update(func() {
			a.syncWindowOnMain(w, false, false)
		})
	}
}

func (a *App) syncWindow() {
	a.mu.Lock()
	w := a.win
	a.mu.Unlock()
	if w != nil {
		w.Update(func() {
			a.syncWindowOnMain(w, false, false)
		})
	}
}

func (a *App) SetRemoteControlled(active bool) {
	a.mu.Lock()
	a.remoteControlled = active
	if active {
		a.statusNote = "教师正在远程控制本机"
	} else {
		a.statusNote = "教师已结束远程控制"
	}
	w := a.win
	a.mu.Unlock()
	if w != nil {
		w.Invalidate()
	}
}

func (a *App) SetMonitoring(active bool) {
	a.mu.Lock()
	a.monitoringActive = active
	w := a.win
	a.mu.Unlock()
	if w != nil {
		w.Invalidate()
	}
}

func (a *App) SetHandRaised(raised bool) {
	a.mu.Lock()
	a.handRaised = raised
	w := a.win
	a.mu.Unlock()
	if w != nil {
		w.Invalidate()
	}
}

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
				Frameless:     true,
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

func (a *App) syncWindowOnMain(w *mygo.Window, show, focus bool) {
	if w == nil {
		return
	}
	a.mu.Lock()
	mode := a.mode
	collapsed := a.barCollapsed
	bcastWin := a.broadcastWindow
	changed := !a.winSyncedOnce || a.lastWinMode != mode || a.lastWinCollapsed != collapsed || a.lastWinBcastWin != bcastWin
	if changed {
		a.winSyncedOnce = true
		a.lastWinMode = mode
		a.lastWinCollapsed = collapsed
		a.lastWinBcastWin = bcastWin
	}
	a.mu.Unlock()

	if changed {
		switch mode {
		case ModeLock:
			w.SetAlwaysOnTop(true)
			w.SetClosable(false)
			w.SetFullScreen(true)
			w.Focus()
		case ModeBroadcast:
			w.SetClosable(true)
			if bcastWin {
				w.SetFullScreen(false)
				w.SetAlwaysOnTop(false)
				w.SetResizable(true)
				w.SetMinimumSize(480, 320)
				w.SetSize(720, 460)
				w.Center()
			} else {
				w.SetAlwaysOnTop(true)
				w.SetFullScreen(true)
			}
		case ModeCheckIn:
			w.SetFullScreen(false)
			w.SetAlwaysOnTop(true)
			w.SetClosable(true)
			w.SetResizable(true)
			w.SetMinimumSize(380, 220)
			w.SetSize(420, 250)
			w.Center()
		case ModeExam:
			w.SetFullScreen(false)
			w.SetAlwaysOnTop(false)
			w.SetClosable(true)
			w.SetResizable(true)
			w.SetMinimumSize(520, 380)
			w.SetSize(640, 460)
			w.Center()
		case ModeChat:
			w.SetFullScreen(false)
			w.SetAlwaysOnTop(false)
			w.SetClosable(true)
			w.SetResizable(true)
			w.SetMinimumSize(460, 340)
			w.SetSize(560, 420)
			w.Center()
		default: // ModeBar
			w.SetFullScreen(false)
			w.SetAlwaysOnTop(true)
			w.SetClosable(true)
			w.SetResizable(true)
			w.SetMinimumSize(520, 48)
			h := 88
			if collapsed {
				h = 48
			}
			w.SetSize(640, h)
		}
	}
	if show {
		w.Show()
	}
	if focus {
		w.Focus()
	}
}

// View builds the active student interface (floating bar, broadcast viewer, blackout lock, check-in, exam, or chat/homework).
func (a *App) View(c *ui.Context) {
	a.mu.Lock()
	dark := a.dark
	mode := a.mode
	a.mu.Unlock()

	pal := theme.GetPalette(dark)
	c.SetTheme(theme.Theme(dark))
	c.Root().Background(pal.Canvas)
	switch mode {
	case ModeLock:
		a.buildLockView(c, pal)
	case ModeBroadcast:
		a.buildBroadcastView(c, pal)
	case ModeExam:
		a.buildExamView(c, pal)
	case ModeCheckIn:
		a.buildCheckInView(c, pal)
	case ModeChat:
		a.buildChatHomeworkView(c, pal)
	default:
		a.buildFloatingBarView(c, pal)
	}
}

func (a *App) buildFloatingBarView(c *ui.Context, pal theme.Palette) {
	ui.Column(c).Fill().Background(pal.ChromeBar).Padding(6, 10).Gap(6).Children(func() {
		// Top draggable floating bar
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).DragWindow().Children(func() {
			ui.Badge(c, a.channel).Background(pal.Selection).TextColor(pal.Text)
			ui.Text(c, fmt.Sprintf("%s (%s)", a.machineName, a.studentNo)).FontSize(12).Bold().TextColor(pal.Text)
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

			handLabel := "举手提问"
			if a.handRaised {
				handLabel = "取消举手"
			}
			if ui.Button(c, handLabel).Padding(4, 10).Radius(3).Clicked() {
				a.ToggleHandRaise()
			}
			if ui.Button(c, "点名签到").Padding(4, 10).Radius(3).Clicked() {
				a.mu.Lock()
				a.mode = ModeCheckIn
				a.mu.Unlock()
				a.syncWindow()
			}
			if ui.Button(c, "随堂测验").Padding(4, 10).Radius(3).Clicked() {
				a.loadActivePaper()
				a.mu.Lock()
				a.mode = ModeExam
				a.mu.Unlock()
				a.syncWindow()
			}
			if ui.Button(c, "作业与消息").Padding(4, 10).Radius(3).Clicked() {
				a.mu.Lock()
				a.mode = ModeChat
				a.mu.Unlock()
				a.syncWindow()
			}
			if ui.Button(c, "广播窗口").Padding(4, 10).Radius(3).Clicked() {
				a.mu.Lock()
				a.mode = ModeBroadcast
				a.mu.Unlock()
				a.syncWindow()
			}
			collapseLabel := "收起"
			if a.barCollapsed {
				collapseLabel = "展开"
			}
			if ui.Button(c, collapseLabel).Padding(4, 8).Radius(3).Clicked() {
				a.mu.Lock()
				a.barCollapsed = !a.barCollapsed
				a.mu.Unlock()
				a.syncWindow()
			}
		})

		if !a.barCollapsed {
			ui.Row(c).FillWidth().Padding(4, 8).Radius(3).Background(pal.Canvas).Border(1, pal.CardBorder).AlignItems(ui.Center).Gap(10).Children(func() {
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

func (a *App) buildBroadcastView(c *ui.Context, pal theme.Palette) {
	ui.Column(c).Fill().Background(pal.TheatreBg).Children(func() {
		ui.Row(c).FillWidth().Padding(6, 12).Background(pal.ChromeBar).AlignItems(ui.Center).Gap(8).DragWindow().Children(func() {
			ui.Text(c, "屏幕广播 / 网络影院接收").FontSize(12).Bold().TextColor(pal.Text)
			if a.broadcastWindow {
				ui.Badge(c, "窗口模式").Background(pal.Selection).TextColor(pal.Text)
			} else {
				ui.Badge(c, "全屏演示").Background(pal.Selection).TextColor(pal.Text)
			}
			ui.Spacer(c)
			if ui.Button(c, "切换窗口/全屏").Padding(3, 8).Clicked() {
				a.mu.Lock()
				a.broadcastWindow = !a.broadcastWindow
				a.mu.Unlock()
				a.syncWindow()
			}
			if ui.Button(c, "举手").Padding(3, 8).Clicked() {
				a.ToggleHandRaise()
			}
			if ui.Button(c, "返回悬浮条").Padding(3, 8).Clicked() {
				a.mu.Lock()
				a.mode = ModeBar
				a.mu.Unlock()
				a.syncWindow()
			}
		})

		ui.Column(c).Grow(1).FillWidth().Center().Padding(12).Children(func() {
			a.mu.Lock()
			bmp := a.videoBitmap
			a.mu.Unlock()
			if bmp != nil {
				ui.Image(c, bmp).Fill().Fit(ui.Contain).Radius(2)
			} else {
				ui.Text(c, "等待教师端屏幕或网络影院画面...").FontSize(14).TextColor(ui.Hex("#D4D4D4"))
				ui.Text(c, "当前无视频信号").FontSize(12).TextColor(ui.Hex("#8E8E8E"))
			}
		})
	})
}

func (a *App) buildLockView(c *ui.Context, pal theme.Palette) {
	ui.Column(c).Fill().Background(pal.LockBg).Center().Gap(14).Padding(24).Children(func() {
		ui.Badge(c, "黑屏锁屏中").Background(ui.Hex("#262626")).TextColor(ui.Hex("#FFFFFF"))
		ui.Text(c, a.lockMessage).FontSize(20).Bold().TextColor(ui.Hex("#F5F5F5"))
		ui.Text(c, "键盘与鼠标已被教师端锁定，断网超时或教师解锁后自动恢复").FontSize(12).TextColor(ui.Hex("#8E8E8E"))

		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.TextInput(c, &a.unlockPassInput).Placeholder("紧急本地解锁口令...").Width(220)
			if ui.Button(c, "验证解锁").Padding(6, 14).Clicked() {
				a.tryEmergencyUnlock()
			}
		})
		if a.unlockStatus != "" {
			ui.Text(c, a.unlockStatus).FontSize(12).TextColor(ui.Hex("#D4D4D4"))
		}
	})
}

func (a *App) buildCheckInView(c *ui.Context, pal theme.Palette) {
	ui.Column(c).Fill().Padding(18).Gap(12).Background(pal.Canvas).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Text(c, "课堂电子点名签到").FontSize(16).Bold().TextColor(pal.Accent)
			ui.Spacer(c)
			if ui.Button(c, "返回").Padding(4, 10).Clicked() {
				a.mu.Lock()
				a.mode = ModeBar
				a.mu.Unlock()
				a.syncWindow()
			}
		})
		ui.Text(c, fmt.Sprintf("当前机位：%s，请核对学号后点击签到提交至教师端：", a.machineName)).FontSize(12).TextColor(pal.TextMuted)
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "学号").Width(48).FontSize(12)
			ui.TextInput(c, &a.checkInNo).Width(200)
		})
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			if ui.PrimaryButton(c, "确认签到").Padding(6, 16).Clicked() {
				a.SubmitCheckIn()
			}
			if a.checkInSubmitted {
				ui.Badge(c, "已完成签到").Background(pal.Selection).TextColor(pal.Text)
			}
		})
	})
}

func (a *App) buildExamView(c *ui.Context, pal theme.Palette) {
	a.loadActivePaper()
	ui.Column(c).Fill().Padding(16).Gap(10).Background(pal.Canvas).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			title := "随堂测验答题卡"
			if a.activePaper != nil {
				title = a.activePaper.Title
			}
			ui.Text(c, title).FontSize(16).Bold().TextColor(pal.Accent)
			ui.Spacer(c)
			if ui.Button(c, "返回").Padding(4, 10).Clicked() {
				a.mu.Lock()
				a.mode = ModeBar
				a.mu.Unlock()
				a.syncWindow()
			}
		})

		if a.activePaper == nil {
			ui.Text(c, "教师端暂未下发随堂测验试卷。").FontSize(13).TextColor(pal.TextMuted)
			return
		}

		ui.Scroll(c.Key("student-exam-scroll")).Grow(1).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Gap(10).Children(func() {
				for idx, q := range a.activePaper.Questions {
					q := q
					ui.Column(c).Key(q.ID).FillWidth().Padding(10).Radius(3).Background(pal.Card).Border(1, pal.CardBorder).Gap(6).Children(func() {
						ui.Text(c, fmt.Sprintf("%d. %s (%d分)", idx+1, q.Prompt, q.Points)).FontSize(13).Bold()
						cur := a.examAnswers[q.ID]
						if len(q.Options) > 0 {
							ui.Row(c).Gap(8).Children(func() {
								for _, opt := range q.Options {
									opt := opt
									selected := cur == opt
									lbl := opt
									if selected {
										lbl = "✓ " + opt
									}
									btn := ui.Button(c, lbl).Padding(4, 10).Radius(2)
									if selected {
										btn.Background(pal.Selection)
									}
									if btn.Clicked() {
										a.examAnswers[q.ID] = opt
									}
								}
							})
						} else {
							val := cur
							inp := ui.TextInput(c, &val).FillWidth().Placeholder("请输入作答...")
							if inp.Changed() {
								a.examAnswers[q.ID] = val
							}
						}
					})
				}
			})
		})

		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			if ui.PrimaryButton(c, "提交答卷").Padding(6, 18).Clicked() {
				a.SubmitExam()
			}
			if a.examResult != nil {
				ui.Badge(c, fmt.Sprintf("得分 %d / %d", a.examResult.Score, a.examResult.MaxScore)).Background(pal.Selection).TextColor(pal.Text)
			}
		})
	})
}

func (a *App) buildChatHomeworkView(c *ui.Context, pal theme.Palette) {
	var fileTasks []*core.TransferTask
	if a.opts.Files != nil {
		fileTasks = a.opts.Files.ListTasks()
	}
	ui.Column(c).Fill().Padding(16).Gap(10).Background(pal.Canvas).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Text(c, "作业提交与课堂消息").FontSize(16).Bold().TextColor(pal.Accent)
			ui.Spacer(c)
			if ui.Button(c, "返回").Padding(4, 10).Clicked() {
				a.mu.Lock()
				a.mode = ModeBar
				a.mu.Unlock()
				a.syncWindow()
			}
		})

		ui.Column(c).FillWidth().Padding(10).Radius(3).Background(pal.Card).Border(1, pal.CardBorder).Gap(6).Children(func() {
			ui.Text(c, "提交作业文件至教师端").FontSize(12).Bold()
			ui.Row(c).FillWidth().Gap(8).Children(func() {
				ui.TextInput(c, &a.homeworkPath).Grow(1).Placeholder("作业文件路径...")
				if ui.Button(c, "浏览").Padding(4, 10).Clicked() {
					a.openHomeworkPicker()
				}
				if ui.PrimaryButton(c, "上传作业").Padding(4, 14).Clicked() {
					a.SubmitHomework()
				}
			})
		})

		if len(fileTasks) > 0 {
			ui.Column(c).FillWidth().Padding(8).Radius(3).Background(pal.Card).Border(1, pal.CardBorder).Gap(4).Children(func() {
				ui.Text(c, fmt.Sprintf("文件记录 (%d)", len(fileTasks))).FontSize(11).Bold().TextColor(pal.TextMuted)
				limit := len(fileTasks)
				if limit > 4 {
					limit = 4
				}
				for i := len(fileTasks) - 1; i >= len(fileTasks)-limit; i-- {
					tk := fileTasks[i]
					ui.Row(c).Key("st-file-" + tk.ID).FillWidth().AlignItems(ui.Center).Gap(6).Children(func() {
						ui.Badge(c, string(tk.Direction))
						ui.Text(c, tk.FileName).FontSize(11).Grow(1).SingleLine()
						ui.Text(c, tk.Status).FontSize(10).TextColor(pal.TextMuted)
						if tk.Status == "completed" && tk.LocalPath != "" {
							if ui.Button(c, "打开").Padding(2, 8).Clicked() {
								_ = mygo.Shell.OpenPath(tk.LocalPath)
							}
						}
					})
				}
			})
		}

		ui.Scroll(c.Key("student-chat-scroll")).Grow(1).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Gap(4).Children(func() {
				if a.opts.Classroom != nil {
					for _, msg := range a.opts.Classroom.ListChats() {
						ui.Row(c).FillWidth().Padding(6, 8).Radius(2).Background(pal.Card).Gap(6).Children(func() {
							ui.Text(c, msg.Sender+":").FontSize(11).Bold().TextColor(pal.Accent)
							ui.Text(c, msg.Text).FontSize(11).TextColor(pal.Text)
						})
					}
				}
			})
		})

		ui.Row(c).FillWidth().Gap(8).Children(func() {
			inp := ui.TextInput(c, &a.chatInput).Grow(1).Placeholder("向教师端发送消息...")
			if inp.Submitted() || ui.PrimaryButton(c, "发送消息").Padding(4, 14).Clicked() {
				a.SendChat()
			}
		})
	})
}

func (a *App) ToggleHandRaise() {
	a.handRaised = !a.handRaised
	if a.opts.Classroom != nil {
		a.opts.Classroom.RaiseHand(a.studentID, a.handRaised)
	}
	a.sendControl(protocol.MsgHandRaise, map[string]any{
		"student_id": a.studentID,
		"raised":     a.handRaised,
	})
	if a.handRaised {
		a.statusNote = "已举手，等待教师批准演示"
	} else {
		a.statusNote = "已取消举手"
	}
}

func (a *App) SubmitCheckIn() {
	if a.opts.Classroom != nil {
		a.opts.Classroom.SubmitCheckIn(a.studentID, "", a.checkInNo)
	}
	a.sendControl(protocol.MsgCheckInSubmit, map[string]string{
		"student_id": a.studentID,
		"student_no": a.checkInNo,
	})
	a.checkInSubmitted = true
	a.statusNote = fmt.Sprintf("签到成功: %s (%s)", a.machineName, a.checkInNo)
}

func (a *App) SubmitExam() {
	a.loadActivePaper()
	if a.activePaper == nil || a.opts.Exam == nil {
		return
	}
	ans := make(map[string][]string, len(a.examAnswers))
	keys := make([]string, 0, len(a.examAnswers))
	for k := range a.examAnswers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.TrimSpace(a.examAnswers[k]) != "" {
			ans[k] = []string{a.examAnswers[k]}
		}
	}
	sub := a.opts.Exam.SubmitAnswer(core.ExamSubmission{
		PaperID:   a.activePaper.ID,
		StudentID: a.studentID,
		Answers:   ans,
		Submitted: time.Now(),
	})
	a.examResult = &sub
	a.sendControl(protocol.MsgExamAnswer, sub)
	a.statusNote = fmt.Sprintf("测验已提交，得分 %d/%d", sub.Score, sub.MaxScore)
}

func (a *App) SubmitHomework() {
	if a.opts.Files == nil {
		return
	}
	path := strings.TrimSpace(a.homeworkPath)
	if path == "" {
		a.statusNote = "请先选择作业文件"
		return
	}
	a.statusNote = "正在上传作业..."
	go func() {
		task, err := a.opts.Files.SubmitHomework(context.Background(), a.studentID, path)
		note := ""
		if err != nil {
			note = "作业上传失败: " + err.Error()
		} else {
			note = "作业已提交: " + task.FileName
		}
		a.SetStatusNote(note)
	}()
}

func (a *App) SendChat() {
	txt := strings.TrimSpace(a.chatInput)
	if txt == "" {
		return
	}
	msg := core.ChatMessage{
		SenderID:  a.studentID,
		Sender:    a.machineName,
		Text:      txt,
		CreatedAt: time.Now(),
	}
	if a.opts.Classroom != nil {
		a.opts.Classroom.AddChat(msg)
	}
	a.sendControl(protocol.MsgChat, msg)
	a.chatInput = ""
	a.statusNote = "消息已发送"
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

func (a *App) loadActivePaper() {
	if a.opts.Exam == nil {
		return
	}
	p := a.opts.Exam.GetActivePaper()
	if p != nil {
		a.activePaper = p
	}
}

func (a *App) openHomeworkPicker() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Title: "选择要提交的作业文件",
		})
		if err != nil || len(paths) == 0 {
			return
		}
		a.mu.Lock()
		a.homeworkPath = paths[0]
		a.mu.Unlock()
		a.Invalidate()
	}()
}

func (a *App) sendControl(msgType protocol.MsgType, payload any) {
	if a.opts.Transport == nil {
		return
	}
	env, err := protocol.NewEnvelope(msgType, a.studentID, payload)
	if err != nil {
		return
	}
	env.Channel = a.channel
	env.Timestamp = time.Now().UnixMilli()
	_ = a.opts.Transport.BroadcastControl(env, nil)
}

// Accessors for testing.
func (a *App) Mode() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}
func (a *App) HasVideoFrame() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.videoBitmap != nil
}
func (a *App) SetMode(m int)     { a.mode = m }
func (a *App) HandRaised() bool  { return a.handRaised }
func (a *App) ScreenLocked() bool { return a.screenLocked }
func (a *App) CheckInSubmitted() bool {
	return a.checkInSubmitted
}
func (a *App) ExamResult() *core.ExamSubmission { return a.examResult }
