package student

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"nexlab/internal/core"
	"nexlab/internal/transport"
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
	StudentID               string
	StudentName             string
	StudentNo               string
	MachineName             string
	Channel                 string
	Dark                    bool
	ExitPasswordPath        string
	ChannelPath             string
	InitialSetup            bool
	OnSetupComplete         func(string)
	NetworkConfigPath       string
	OnNetworkConfigComplete func(transport.NetworkConfig)
	// Trust stores the pinned teacher fingerprint; nil hides the setting.
	Trust    *transport.FileTrust
	Services Services
}

const (
	ModeBar = iota
	ModeBroadcast
	ModeLock
	ModeExam
	ModeCheckIn
	ModeChat
	ModeHomeworkSubmit
	ModeTheatre
)

// App implements the MyGo native Student Client (tray + floating bar + broadcast/lock/exam/check-in views).
type App struct {
	mu               sync.Mutex
	lifecycleCtx     context.Context
	lifecycleCancel  context.CancelFunc
	lifecycleClosed  bool
	lifecycleStarted bool

	opts                Services
	win                 *mygo.Window
	toastWin            *mygo.Window
	connectionNoticeWin *mygo.Window
	tray                *mygo.Tray

	studentID   string
	studentNo   string
	machineName string
	channel     string
	dark        bool

	mode                int
	barCollapsed        bool
	barDocked           bool
	barHovered          bool
	barFocused          bool
	barAutoDockSeq      uint64
	handRaised          bool
	connected           bool
	broadcasting        bool
	broadcastWindow     bool
	remoteControlled    bool
	monitoringActive    bool
	screenLocked        bool
	lockMessage         string
	unlockPassInput     string
	unlockStatus        string
	checkInNo           string
	checkInClass        string
	checkInName         string
	checkInSubmitted    bool
	homeworkPath        string
	homeworkSubmitting  bool
	homeworkQueue       []string
	homeworkSubmitWin   *mygo.Window
	homeworkCollecting  bool
	homeworkQueueBusy   bool
	homeworkSubmitNote  string
	fileReceiveWin      *mygo.Window
	exitPasswordWin     *mygo.Window
	settingsWin         *mygo.Window
	exitPasswordPath    string
	channelPath         string
	exitPasswordHash    string
	exitPasswordInput   string
	exitPasswordConfirm string
	exitPasswordError   string
	exitPasswordSetup   bool
	passwordMode        string
	channelInput        string
	settingsError       string
	trust               *transport.FileTrust
	trustInput          string
	networkConfigPath   string
	networkConfig       transport.NetworkConfig
	networkInterface    string
	networkMode         string
	ignoreVirtual       bool
	ignoreVPN           bool
	onNetworkConfig     func(transport.NetworkConfig)
	onSetupComplete     func(string)
	exitAuthorized      bool
	fileReceiveItems    []receiveFileItem
	fileReceiveNote     string
	chatInput           string
	statusNote          string
	volume              float64

	lastWinMode      int
	lastWinCollapsed bool
	lastWinBcastWin  bool
	lastWinDocked    bool
	winSyncedOnce    bool

	videoBitmap      *ui.Bitmap
	broadcastActive  bool
	theatrePlayer    *ui.VideoPlayer
	theatreMedia     string
	theatreStreamURL string
	theatrePlaying   bool
	lastFrameAt      time.Time
	pendingFrame     *core.VideoFrame
	frameScheduled   atomic.Bool

	activePaper   *core.ExamPaper
	examAnswers   map[string]string
	lastChatCount int
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
		opts.Channel = "1"
	}
	app := &App{
		opts:              opts.Services,
		studentID:         opts.StudentID,
		studentNo:         opts.StudentNo,
		machineName:       opts.MachineName,
		channel:           opts.Channel,
		dark:              opts.Dark,
		exitPasswordPath:  opts.ExitPasswordPath,
		channelPath:       opts.ChannelPath,
		onSetupComplete:   opts.OnSetupComplete,
		networkConfigPath: opts.NetworkConfigPath,
		networkConfig:     transport.DefaultNetworkConfig(),
		onNetworkConfig:   opts.OnNetworkConfigComplete,
		trust:             opts.Trust,
		mode:              ModeBar,
		broadcastWindow:   true,
		connected:         true,
		lockMessage:       "课堂黑屏安静模式：请注意前方黑板或教师讲解。",
		checkInNo:         opts.StudentNo,
		checkInClass:      "",
		checkInName:       "",
		homeworkPath:      "",
		statusNote:        "已连接频道: " + opts.Channel,
		volume:            80,
		examAnswers:       make(map[string]string),
		recentNotices:     []string{"欢迎加入 矩联电子教室 MatrixClass"},
		lastWinMode:       -1,
	}
	app.loadNetworkConfig()
	app.exitPasswordHash = app.loadExitPasswordHash()
	app.channelInput = app.channel
	if configured := app.loadChannel(); configured != "" {
		app.channel = configured
	}
	if opts.InitialSetup || app.exitPasswordHash == "" || app.loadChannel() == "" {
		app.passwordMode = "setup"
	} else {
		app.passwordMode = "exit"
	}
	_ = app.Start(context.Background())
	return app
}

// Start begins UI-owned background work. Repeated calls are harmless.
func (a *App) Start(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lifecycleClosed {
		return errors.New("student ui app is closed")
	}
	if a.lifecycleStarted {
		return nil
	}
	a.lifecycleCtx, a.lifecycleCancel = context.WithCancel(parent)
	a.lifecycleStarted = true
	return nil
}

// Close cancels UI-owned work and closes standalone windows. It is idempotent.
func (a *App) Close() error {
	a.mu.Lock()
	if a.lifecycleClosed {
		a.mu.Unlock()
		return nil
	}
	a.lifecycleClosed = true
	cancel := a.lifecycleCancel
	toast := a.toastWin
	connectionNotice := a.connectionNoticeWin
	fileWin := a.fileReceiveWin
	homeworkWin := a.homeworkSubmitWin
	exitWin := a.exitPasswordWin
	settingsWin := a.settingsWin
	a.toastWin = nil
	a.connectionNoticeWin = nil
	a.fileReceiveWin = nil
	a.homeworkSubmitWin = nil
	a.exitPasswordWin = nil
	a.settingsWin = nil
	a.win = nil
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, w := range []*mygo.Window{toast, connectionNotice, fileWin, homeworkWin, exitWin} {
		if w != nil {
			w.Destroy()
		}
	}
	if settingsWin != nil {
		settingsWin.Destroy()
	}
	return nil
}

func (a *App) lifecycleContext() (context.Context, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lifecycleClosed {
		return nil, false
	}
	if a.lifecycleCtx == nil {
		a.lifecycleCtx, a.lifecycleCancel = context.WithCancel(context.Background())
	}
	return a.lifecycleCtx, true
}

// AttachWindow binds the floating/main window.
func (a *App) AttachWindow(w *mygo.Window) {
	a.mu.Lock()
	a.win = w
	a.barFocused = false
	a.mu.Unlock()
	if w != nil {
		_ = w.SetIcon(theme.AppIconPNG(false))
		a.positionBarTopCenter(w)
		w.SetAlwaysOnTop(true)
		w.OnFocus(func() { a.setBarFocused(true) })
		w.OnBlur(func() { a.setBarFocused(false) })
		w.OnClose(func(e *mygo.CloseEvent) {
			if a.ExitAuthorized() {
				return
			}
			e.PreventDefault()
			a.RequestExit()
		})
		w.OnClosed(func() {
			a.mu.Lock()
			tw := a.toastWin
			a.toastWin = nil
			a.mu.Unlock()
			if tw != nil {
				tw.Destroy()
			}
		})
		a.mu.Lock()
		showConnectionNotice := !a.connected
		a.mu.Unlock()
		if showConnectionNotice {
			a.showConnectionNotice()
		}
		a.scheduleBarAutoDock()
	}
}

// BuildTrayMenu creates the native context menu for the student tray icon.
func (a *App) BuildTrayMenu() *mygo.Menu {
	return mygo.NewMenu([]*mygo.MenuItem{
		{Label: "显示/隐藏悬浮条", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.ShowFloatingBar() }},
		{Label: "举手提问", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.ToggleHandRaise() }},
		{Label: "作业提交", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.OpenMode(ModeHomeworkSubmit) }},
		{Label: "课堂消息", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.OpenMode(ModeChat) }},
		{Label: "文件接收队列", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.OpenFileReceiveWindow() }},
		{Label: "参数设置", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.OpenParameterSettings() }},
		mygo.Separator(),
		{Label: "退出学生端", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.RequestExit() }},
	})
}

// InstallTray creates the native system tray icon and menu for the student client.
func (a *App) InstallTray() {
	menu := a.BuildTrayMenu()
	tray, err := mygo.NewTray(mygo.TrayOptions{
		ID:      "nexlab-student",
		Icon:    theme.AppIconPNG(false),
		ToolTip: "矩联电子教室 MatrixClass 学生端 - " + a.channel,
	})
	if err == nil {
		if runtime.GOOS == "linux" {
			// AppIndicator primarily exposes a menu on Linux panels.
			tray.SetMenu(menu)
		} else {
			tray.OnClick(func() { a.ShowFloatingBar() })
			tray.OnRightClick(func() {
				tray.SetMenu(menu)
				defer tray.SetMenu(nil)
				tray.PopUpMenu()
			})
		}
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
		case ModeTheatre:
			w.SetFullScreen(false)
			w.SetClosable(true)
			w.SetAlwaysOnTop(true)
			w.SetResizable(true)
			w.SetMinimumSize(480, 320)
			w.SetSize(720, 460)
			w.Center()
		case ModeCheckIn:
			w.SetFullScreen(false)
			w.SetAlwaysOnTop(true)
			w.SetClosable(true)
			w.SetResizable(true)
			w.SetMinimumSize(380, 270)
			w.SetSize(420, 310)
			w.Center()
		case ModeExam:
			w.SetFullScreen(false)
			w.SetAlwaysOnTop(true)
			w.SetClosable(true)
			w.SetResizable(true)
			w.SetMinimumSize(520, 380)
			w.SetSize(640, 460)
			w.Center()
		case ModeChat, ModeHomeworkSubmit:
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
			w.SetMinimumSize(560, 8)
			wa := mygo.Screen.PrimaryDisplay().WorkArea
			if docked {
				w.SetSize(740, 10)
				curX, _ := w.Position()
				if firstSync || prevMode != ModeBar {
					curX = wa.X + (wa.Width-740)/2
				}
				if curX < wa.X {
					curX = wa.X
				}
				if wa.Width > 740 && curX > wa.X+wa.Width-740 {
					curX = wa.X + wa.Width - 740
				}
				w.SetPosition(curX, wa.Y)
			} else {
				h := 92
				if collapsed {
					h = 52
				}
				w.SetSize(740, h)
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
	if mode == ModeBar {
		c.Root().Background(ui.Transparent)
	} else {
		c.Root().Background(pal.Canvas)
	}
	switch mode {
	case ModeLock:
		a.buildLockView(c, pal)
	case ModeBroadcast, ModeTheatre:
		a.buildBroadcastView(c, pal)
	case ModeExam:
		a.buildExamView(c, pal)
	case ModeCheckIn:
		a.buildCheckInView(c, pal)
	case ModeChat:
		a.buildChatView(c, pal)
	case ModeHomeworkSubmit:
		a.buildHomeworkView(c, pal)
	default:
		a.buildFloatingBarView(c, pal)
	}
}

// Accessors for testing.
func (a *App) Mode() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}
func (a *App) SetMode(m int)      { a.mode = m }
func (a *App) HandRaised() bool   { return a.handRaised }
func (a *App) ScreenLocked() bool { return a.screenLocked }
