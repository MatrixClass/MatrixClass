package student

import (
	"context"
	"fmt"
	"image"
	"path/filepath"
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
	ModeHomeworkSubmit
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
	barDocked        bool
	barHovered       bool
	barFocused       bool
	barAutoDockSeq   uint64
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
	homeworkQueue    []string
	homeworkSubmitWin *mygo.Window
	homeworkCollecting bool
	homeworkSubmitNote  string
	fileReceiveWin     *mygo.Window
	fileReceiveItems   []receiveFileItem
	fileReceiveNote    string
	chatInput        string
	statusNote       string
	volume           float64

	lastWinMode      int
	lastWinCollapsed bool
	lastWinBcastWin  bool
	lastWinDocked    bool
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

type receiveFileItem struct {
	Task      core.TransferTask
	UpdatedAt time.Time
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
	a.barFocused = w != nil && w.IsFocused()
	a.mu.Unlock()
	if w != nil {
		_ = w.SetIcon(theme.AppIconPNG(false))
		a.positionBarTopCenter(w)
		w.SetAlwaysOnTop(true)
		w.OnFocus(func() { a.setBarFocused(true) })
		w.OnBlur(func() { a.setBarFocused(false) })
		w.OnClosed(func() {
			a.mu.Lock()
			tw := a.toastWin
			a.toastWin = nil
			a.mu.Unlock()
			if tw != nil {
				tw.Destroy()
			}
		})
		a.scheduleBarAutoDock()
	}
}

func (a *App) positionBarTopCenter(w *mygo.Window) {
	if w == nil {
		return
	}
	wa := mygo.Screen.PrimaryDisplay().WorkArea
	const barW = 640
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

// InstallTray creates the native system tray icon and menu for the student client.
func (a *App) InstallTray() {
	menu := mygo.NewMenu([]*mygo.MenuItem{
		{Label: "显示/隐藏悬浮条", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.ShowFloatingBar() }},
		{Label: "举手提问", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.ToggleHandRaise() }},
		{Label: "提交作业 / 消息", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.OpenMode(ModeChat) }},
		{Label: "文件接收队列", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.OpenFileReceiveWindow() }},
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
	hw := a.homeworkSubmitWin
	a.mu.Unlock()
	if w != nil {
		w.Invalidate()
	}
	if hw != nil {
		hw.Invalidate()
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

func (a *App) OpenMode(m int) {
	a.mu.Lock()
	a.mode = m
	if m == ModeExam {
		a.loadActivePaper()
	} else if m == ModeCheckIn {
		a.checkInSubmitted = false
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

func (a *App) syncWindowOnMain(w *mygo.Window, show, focus bool) {
	if w == nil {
		return
	}
	a.mu.Lock()
	mode := a.mode
	collapsed := a.barCollapsed
	bcastWin := a.broadcastWindow
	docked := a.barDocked
	firstSync := !a.winSyncedOnce
	prevMode := a.lastWinMode
	changed := firstSync || a.lastWinMode != mode || a.lastWinCollapsed != collapsed || a.lastWinBcastWin != bcastWin || a.lastWinDocked != docked
	if changed {
		a.winSyncedOnce = true
		a.lastWinMode = mode
		a.lastWinCollapsed = collapsed
		a.lastWinBcastWin = bcastWin
		a.lastWinDocked = docked
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
			w.SetAlwaysOnTop(true)
			if bcastWin {
				w.SetFullScreen(false)
				w.SetResizable(true)
				w.SetMinimumSize(480, 320)
				w.SetSize(720, 460)
				w.Center()
			} else {
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
			w.SetAlwaysOnTop(true)
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
			w.SetMinimumSize(520, 8)
			wa := mygo.Screen.PrimaryDisplay().WorkArea
			if docked {
				w.SetSize(640, 8)
				curX, _ := w.Position()
				if firstSync || prevMode != ModeBar {
					curX = wa.X + (wa.Width-640)/2
				}
				if curX < wa.X {
					curX = wa.X
				}
				if wa.Width > 640 && curX > wa.X+wa.Width-640 {
					curX = wa.X + wa.Width - 640
				}
				w.SetPosition(curX, wa.Y)
			} else {
				h := 88
				if collapsed {
					h = 48
				}
				w.SetSize(640, h)
				if firstSync || prevMode != ModeBar {
					a.positionBarTopCenter(w)
				}
			}
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
		root.Padding(2, 0).Center().Border(1, pal.CardBorder).Children(func() {
			ui.Box(c).Width(72).Height(3).Radius(2).Background(pal.Accent)
		})
		return
	}

	root.Padding(6, 10).Gap(6).Children(func() {
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

// RecordIncomingFile updates the student download queue for teacher-distributed files
// and automatically opens the standalone file receive window.
func (a *App) RecordIncomingFile(task core.TransferTask) {
	if task.Direction != core.TransferSend {
		return
	}

	a.mu.Lock()
	now := time.Now()
	updated := false
	for i := range a.fileReceiveItems {
		if a.fileReceiveItems[i].Task.ID == task.ID {
			a.fileReceiveItems[i].Task = task
			a.fileReceiveItems[i].UpdatedAt = now
			updated = true
			break
		}
	}
	if !updated {
		for i := range a.fileReceiveItems {
			existing := a.fileReceiveItems[i].Task
			if existing.FileName == task.FileName && existing.RemotePath == task.RemotePath &&
				(existing.Status == "failed" || existing.Status == "retrying") {
				a.fileReceiveItems[i].Task = task
				a.fileReceiveItems[i].UpdatedAt = now
				updated = true
				break
			}
		}
	}
	if !updated {
		a.fileReceiveItems = append(a.fileReceiveItems, receiveFileItem{
			Task:      task,
			UpdatedAt: now,
		})
	}
	if task.Status == "completed" {
		a.statusNote = "文件传输完成: " + task.FileName
		a.fileReceiveNote = "已接收: " + task.FileName
	} else if task.Status == "failed" {
		a.fileReceiveNote = "接收失败: " + task.FileName
	} else {
		a.fileReceiveNote = "正在接收教师下发文件..."
	}
	w := a.win
	frw := a.fileReceiveWin
	a.mu.Unlock()

	if w != nil {
		if frw == nil {
			w.Update(func() {
				a.ensureFileReceiveWindowOnMain()
			})
		} else {
			frw.Invalidate()
		}
	}
	a.Invalidate()
}

// OpenFileReceiveWindow opens or focuses the standalone file download queue window.
func (a *App) OpenFileReceiveWindow() {
	a.mu.Lock()
	w := a.win
	frw := a.fileReceiveWin
	a.mu.Unlock()

	if frw != nil {
		frw.SetAlwaysOnTop(true)
		frw.Show()
		frw.Focus()
		frw.Invalidate()
		return
	}
	if w != nil {
		w.Update(func() {
			a.ensureFileReceiveWindowOnMain()
		})
	}
}

func (a *App) ensureFileReceiveWindowOnMain() {
	a.mu.Lock()
	frw := a.fileReceiveWin
	a.mu.Unlock()
	if frw != nil {
		frw.SetAlwaysOnTop(true)
		frw.Show()
		frw.Focus()
		frw.Invalidate()
		return
	}
	frw = mygo.NewWindow(mygo.WindowOptions{
		Title:       "文件接收队列",
		Width:       520,
		Height:      420,
		MinWidth:    420,
		MinHeight:   320,
		AlwaysOnTop: true,
		Content:     ui.View(a.FileReceiveView),
	})
	frw.OnClosed(func() {
		a.mu.Lock()
		if a.fileReceiveWin == frw {
			a.fileReceiveWin = nil
		}
		a.mu.Unlock()
	})
	a.mu.Lock()
	a.fileReceiveWin = frw
	a.mu.Unlock()
}

// CloseFileReceiveWindow closes the standalone file receive window if open.
func (a *App) CloseFileReceiveWindow() {
	a.mu.Lock()
	frw := a.fileReceiveWin
	a.mu.Unlock()
	if frw != nil {
		frw.Close()
	}
}

// ClearCompletedReceiveFiles removes completed entries from the download queue.
func (a *App) ClearCompletedReceiveFiles() {
	a.mu.Lock()
	filtered := a.fileReceiveItems[:0]
	for _, it := range a.fileReceiveItems {
		if it.Task.Status != "completed" {
			filtered = append(filtered, it)
		}
	}
	a.fileReceiveItems = filtered
	if len(a.fileReceiveItems) == 0 {
		a.fileReceiveNote = ""
	}
	frw := a.fileReceiveWin
	a.mu.Unlock()
	if frw != nil {
		frw.Invalidate()
	}
	a.Invalidate()
}

// RetryReceiveFile requests the teacher to re-distribute the specified file.
func (a *App) RetryReceiveFile(task core.TransferTask) {
	a.mu.Lock()
	for i := range a.fileReceiveItems {
		if a.fileReceiveItems[i].Task.ID == task.ID {
			a.fileReceiveItems[i].Task.Status = "retrying"
			a.fileReceiveItems[i].Task.Error = ""
			a.fileReceiveItems[i].UpdatedAt = time.Now()
			break
		}
	}
	a.fileReceiveNote = "正在向教师端请求重传: " + task.FileName
	frw := a.fileReceiveWin
	a.mu.Unlock()

	payload := map[string]any{
		"student_id":  a.studentID,
		"file_name":   task.FileName,
		"remote_path": task.RemotePath,
		"sha256":      task.SHA256,
		"auto_open":   task.AutoOpen,
	}
	sent := false
	if a.opts.Transport != nil {
		env, err := protocol.NewEnvelope(protocol.MsgFileRetryReq, a.studentID, payload)
		if err == nil {
			if sendErr := a.opts.Transport.BroadcastControl(env, nil); sendErr == nil {
				sent = true
			}
		}
	}
	if !sent {
		a.mu.Lock()
		for i := range a.fileReceiveItems {
			if a.fileReceiveItems[i].Task.ID == task.ID {
				a.fileReceiveItems[i].Task.Status = "failed"
				a.fileReceiveItems[i].Task.Error = "未连接到教师端，无法发起重传"
				break
			}
		}
		a.fileReceiveNote = "重传请求发送失败：未连接到教师端"
		a.mu.Unlock()
	}
	if frw != nil {
		frw.Invalidate()
	}
	a.Invalidate()
}

func (a *App) FileReceiveView(c *ui.Context) {
	a.mu.Lock()
	dark := a.dark
	items := make([]receiveFileItem, len(a.fileReceiveItems))
	copy(items, a.fileReceiveItems)
	note := a.fileReceiveNote
	a.mu.Unlock()

	pal := theme.GetPalette(dark)
	c.SetTheme(theme.Theme(dark))

	doneCount, failCount, activeCount := 0, 0, 0
	for _, it := range items {
		switch it.Task.Status {
		case "completed":
			doneCount++
		case "failed":
			failCount++
		default:
			activeCount++
		}
	}

	ui.Column(c).Fill().Background(pal.Canvas).Padding(16).Gap(10).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).DragWindow().Children(func() {
			ui.Text(c, "接收教师下发文件").FontSize(15).Bold().TextColor(pal.Text)
			ui.Badge(c, fmt.Sprintf("共 %d 项", len(items)))
			if activeCount > 0 {
				ui.Badge(c, fmt.Sprintf("接收中 %d", activeCount)).Background(pal.Selection).TextColor(pal.Accent)
			}
			if doneCount > 0 {
				ui.Badge(c, fmt.Sprintf("完成 %d", doneCount))
			}
			if failCount > 0 {
				ui.Badge(c, fmt.Sprintf("失败 %d", failCount)).TextColor(pal.Danger)
			}
			ui.Spacer(c)
			if doneCount > 0 {
				if ui.Button(c, "清空已完成").Padding(4, 10).Clicked() {
					a.ClearCompletedReceiveFiles()
				}
			}
			if ui.Button(c, "关闭").Padding(4, 10).Clicked() {
				a.CloseFileReceiveWindow()
			}
		})

		if note != "" {
			ui.Text(c, note).FontSize(12).TextColor(pal.TextMuted).SingleLine()
		}

		if len(items) == 0 {
			ui.Box(c).Grow(1).Center().Background(pal.Card).Border(1, pal.CardBorder).Radius(4).Children(func() {
				ui.Text(c, "暂无教师下发文件").FontSize(13).TextColor(pal.TextMuted)
			})
			return
		}

		ui.Scroll(c).Grow(1).Gap(8).Children(func() {
			for _, it := range items {
				tk := it.Task
				ratio := 0.0
				if tk.TotalBytes > 0 {
					ratio = float64(tk.DoneBytes) / float64(tk.TotalBytes)
				} else if tk.Status == "completed" {
					ratio = 1.0
				}
				if ratio < 0 {
					ratio = 0
				}
				if ratio > 1 {
					ratio = 1
				}

				statusLabel := "等待接收"
				statusColor := pal.TextMuted
				switch tk.Status {
				case "running":
					statusLabel = fmt.Sprintf("接收中 %.0f%%", ratio*100)
					statusColor = pal.Accent
				case "completed":
					statusLabel = "已完成"
					statusColor = pal.Success
				case "retrying":
					statusLabel = "请求重传中..."
					statusColor = pal.Accent
				case "failed":
					statusLabel = "接收失败"
					statusColor = pal.Danger
				}

				savePath := tk.LocalPath
				if savePath == "" {
					savePath = tk.RemotePath
				}

				ui.Column(c).Key("recv-"+tk.ID).FillWidth().Padding(10).Gap(6).
					Background(pal.Card).Border(1, pal.CardBorder).Radius(4).Children(func() {
					ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
						ui.Text(c, tk.FileName).FontSize(13).Bold().TextColor(pal.Text).Grow(1).SingleLine()
						ui.Text(c, statusLabel).FontSize(11).Bold().TextColor(statusColor)
						if tk.Status == "completed" && tk.LocalPath != "" {
							if ui.Button(c, "打开").Padding(3, 10).Clicked() {
								_ = mygo.Shell.OpenPath(tk.LocalPath)
							}
						}
						if tk.Status == "failed" {
							if ui.PrimaryButton(c, "重试").Padding(3, 10).Clicked() {
								a.RetryReceiveFile(tk)
							}
						} else {
							if ui.Button(c, "重试").Padding(3, 10).Clicked() {
								a.RetryReceiveFile(tk)
							}
						}
					})

					ui.Progress(c, ratio).Height(6)

					ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
						if savePath != "" {
							ui.Text(c, savePath).FontSize(11).TextColor(pal.TextMuted).Grow(1).SingleLine()
						} else {
							ui.Spacer(c)
						}
						ui.Text(c, fmt.Sprintf("%s / %s", formatStudentBytes(tk.DoneBytes), formatStudentBytes(tk.TotalBytes))).
							FontSize(11).TextColor(pal.TextMuted)
					})

					if tk.Error != "" {
						ui.Text(c, tk.Error).FontSize(11).TextColor(pal.Danger).SingleLine()
					}
				})
			}
		})
	})
}

func formatStudentBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	const unit = 1024
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
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

// OpenHomeworkSubmitWindow opens the standalone homework submission popup.
func (a *App) OpenHomeworkSubmitWindow() {
	a.mu.Lock()
	a.homeworkCollecting = true
	a.homeworkSubmitNote = "教师已发起作业收取，请提交作业"
	w := a.win
	hw := a.homeworkSubmitWin
	a.mu.Unlock()

	if w == nil {
		a.mu.Lock()
		a.mode = ModeHomeworkSubmit
		a.mu.Unlock()
		a.syncWindow()
		return
	}
	if hw != nil {
		hw.Update(func() {
			hw.SetAlwaysOnTop(true)
			hw.Show()
			hw.Focus()
		})
		return
	}
	w.Update(func() {
		a.mu.Lock()
		existing := a.homeworkSubmitWin
		a.mu.Unlock()
		if existing != nil {
			existing.SetAlwaysOnTop(true)
			existing.Show()
			existing.Focus()
			return
		}
		newHW := mygo.NewWindow(mygo.WindowOptions{
			Title:       "提交作业",
			Width:       480,
			Height:      420,
			MinWidth:    380,
			MinHeight:   340,
			AlwaysOnTop: true,
			Content:     ui.View(a.HomeworkSubmitView),
		})
		newHW.OnFileDrop(func(e *mygo.FileDropEvent) {
			if e == nil || len(e.Paths) == 0 {
				return
			}
			newHW.Update(func() {
				a.addHomeworkFiles(e.Paths)
				a.homeworkSubmitNote = fmt.Sprintf("已拖入 %d 个文件", len(e.Paths))
			})
		})
		newHW.OnClosed(func() {
			a.mu.Lock()
			if a.homeworkSubmitWin == newHW {
				a.homeworkSubmitWin = nil
			}
			a.mu.Unlock()
		})
		a.mu.Lock()
		a.homeworkSubmitWin = newHW
		a.mu.Unlock()
	})
}

// CloseHomeworkSubmitWindow closes the homework submission popup (called on teacher end).
func (a *App) CloseHomeworkSubmitWindow() {
	a.mu.Lock()
	a.homeworkCollecting = false
	a.homeworkSubmitNote = "教师已结束作业收取"
	hw := a.homeworkSubmitWin
	a.mu.Unlock()

	if hw != nil {
		hw.Update(func() {
			a.homeworkSubmitNote = "教师已结束作业收取，窗口将关闭"
		})
		go func() {
			time.Sleep(1500 * time.Millisecond)
			a.mu.Lock()
			curHW := a.homeworkSubmitWin
			a.mu.Unlock()
			if curHW == hw {
				hw.Close()
			}
		}()
	}

	a.mu.Lock()
	if a.mode == ModeHomeworkSubmit {
		a.mode = ModeBar
	}
	a.mu.Unlock()
	a.syncWindow()
}

func (a *App) HomeworkSubmitView(c *ui.Context) {
	a.mu.Lock()
	dark := a.dark
	a.mu.Unlock()
	pal := theme.GetPalette(dark)
	c.SetTheme(theme.Theme(dark))
	ui.Column(c).Fill().Background(pal.Canvas).Children(func() {
		a.buildHomeworkSubmitContent(c, pal)
	})
}

func (a *App) buildHomeworkSubmitContent(c *ui.Context, pal theme.Palette) {
	ui.Column(c).Fill().Padding(14).Gap(10).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "提交作业").FontSize(15).Bold()
			ui.Badge(c, "教师正在收取").Background(pal.Selection).TextColor(pal.Accent)
		})

		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, fmt.Sprintf("待提交文件 (%d)", len(a.homeworkQueue))).FontSize(12).Bold()
			ui.Spacer(c)
			if ui.Button(c, "添加文件...").Padding(4, 12).Clicked() {
				a.openHomeworkQueuePicker()
			}
			if len(a.homeworkQueue) > 0 {
				if ui.Button(c, "清空").Padding(4, 10).Clicked() {
					a.homeworkQueue = nil
				}
			}
		})

		queueScroll := ui.Scroll(c.Key("hw-queue")).Grow(1).FillWidth().Background(pal.Card).Radius(3).Padding(8).Gap(6)
		if queueScroll.FileDragOver() {
			queueScroll.Border(2, pal.Accent)
		} else {
			queueScroll.Border(1, pal.CardBorder)
		}
		if dropped := queueScroll.DroppedFiles(); len(dropped) > 0 {
			a.addHomeworkFiles(dropped)
			a.homeworkSubmitNote = fmt.Sprintf("已拖入 %d 个文件", len(dropped))
		}
		queueScroll.Children(func() {
			if len(a.homeworkQueue) == 0 {
				ui.Box(c).Fill().Center().Padding(20).Children(func() {
					ui.Text(c, "将作业文件拖入此处，或点击「添加文件...」").FontSize(12).TextColor(pal.TextMuted)
				})
				return
			}
			for idx, p := range a.homeworkQueue {
				idx, p := idx, p
				ui.Row(c).Key("hq-"+p).FillWidth().Padding(6, 8).Radius(2).Background(pal.Canvas).Border(1, pal.CardBorder).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, filepath.Base(p)).FontSize(12).Bold()
					ui.Text(c, p).FontSize(11).TextColor(pal.TextMuted).Grow(1).SingleLine()
					if ui.Button(c, "移除").Padding(2, 8).Clicked() {
						a.removeHomeworkFile(idx)
					}
				})
			}
		})

		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, a.homeworkSubmitNote).FontSize(11).TextColor(pal.TextMuted).Grow(1).SingleLine()
			submitLabel := fmt.Sprintf("提交 (%d)", len(a.homeworkQueue))
			if ui.PrimaryButton(c, submitLabel).Padding(5, 16).Clicked() {
				a.SubmitHomeworkQueue()
			}
		})
	})
}

func (a *App) addHomeworkFiles(paths []string) {
	existing := make(map[string]bool, len(a.homeworkQueue))
	for _, p := range a.homeworkQueue {
		existing[p] = true
	}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p != "" && !existing[p] {
			existing[p] = true
			a.homeworkQueue = append(a.homeworkQueue, p)
		}
	}
}

func (a *App) removeHomeworkFile(idx int) {
	if idx < 0 || idx >= len(a.homeworkQueue) {
		return
	}
	a.homeworkQueue = append(a.homeworkQueue[:idx], a.homeworkQueue[idx+1:]...)
}

func (a *App) openHomeworkQueuePicker() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Title:    "选择要提交的作业文件",
			Multiple: true,
		})
		if err != nil || len(paths) == 0 {
			return
		}
		a.mu.Lock()
		hw := a.homeworkSubmitWin
		w := a.win
		a.mu.Unlock()
		apply := func() {
			a.addHomeworkFiles(paths)
			a.homeworkSubmitNote = fmt.Sprintf("已添加 %d 个文件", len(paths))
		}
		if hw != nil {
			hw.Update(apply)
		} else if w != nil {
			w.Update(apply)
		} else {
			apply()
		}
	}()
}

func (a *App) SubmitHomeworkQueue() {
	if a.opts.Files == nil || len(a.homeworkQueue) == 0 {
		a.homeworkSubmitNote = "请先添加作业文件"
		return
	}
	queue := append([]string(nil), a.homeworkQueue...)
	a.homeworkSubmitNote = fmt.Sprintf("正在上传 %d 个作业文件...", len(queue))
	go func() {
		sent := 0
		var lastErr error
		for _, p := range queue {
			if _, err := a.opts.Files.SubmitHomework(context.Background(), a.studentID, p); err != nil {
				lastErr = err
			} else {
				sent++
			}
		}
		a.mu.Lock()
		if lastErr != nil {
			a.homeworkSubmitNote = fmt.Sprintf("提交完成 %d/%d（部分失败: %v）", sent, len(queue), lastErr)
		} else {
			a.homeworkSubmitNote = fmt.Sprintf("已成功提交 %d 个作业文件", sent)
			a.homeworkQueue = nil
		}
		hw := a.homeworkSubmitWin
		a.mu.Unlock()
		if hw != nil {
			hw.Invalidate()
		}
		a.Invalidate()
	}()
}
