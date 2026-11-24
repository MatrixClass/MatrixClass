package teacher

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"nexlab/internal/core"
	"nexlab/internal/protocol"
	"nexlab/internal/remoteexec"
	"nexlab/internal/store"
	"nexlab/internal/ui/theme"
)

// Services bundles the classroom backend interfaces injected into the Teacher Console.
type Services struct {
	Transport core.TransportManager
	Media     core.MediaEngine
	Control   core.ControlEngine
	Classroom core.ClassroomService
	Files     core.FileTransferService
	Exam      core.ExamService
}

// Options configures the Teacher Console window and identity.
type Options struct {
	TeacherID         string
	TeacherName       string
	Channel           string
	ArchiveDir        string
	Dark              bool
	Store             *store.Store
	PromptChannel     bool
	OnChannelSelected func(channel string)
	TheatreStreamURL  string
	// Fingerprint is the teacher identity fingerprint students pin to.
	Fingerprint string
	Services    Services
}

const (
	TabSeatMap = iota
	TabMonitorWall
	TabMyTheatre
	TabExamStats
	TabFiles
)

const (
	SideTabChatQueue = iota
	SideTabPolicy
)

const (
	remoteExecPanelLaunch = iota
	remoteExecPanelCommand
)

// App is the MyGo native Teacher Console for 矩联电子教室 MatrixClass.
type App struct {
	mu               sync.Mutex
	lifecycleCtx     context.Context
	lifecycleCancel  context.CancelFunc
	lifecycleClosed  bool
	lifecycleStarted bool
	lifecycleWG      sync.WaitGroup

	opts        Services
	fingerprint string
	win         *mygo.Window
	tray        *mygo.Tray
	fileDistWin *mygo.Window

	teacherID   string
	teacherName string
	channel     string
	dark        bool

	// Primary navigation & layout state
	mainTab       int
	sideTab       int
	leftPaneSize  float32
	mainSplitPct  float32
	selectedGroup string
	creatingGroup bool
	newGroupInput string
	selectedID    string
	selectedIDs   map[string]bool
	searchQuery   string

	// Marquee (click-and-drag) multi-selection state
	itemBounds      map[string]ui.Rect
	marqueeActive   bool
	marqueeMoved    bool
	marqueeAdditive bool
	marqueeStartX   float32
	marqueeStartY   float32
	marqueeCurX     float32
	marqueeCurY     float32
	marqueeBase     map[string]bool
	suppressClear   bool

	// Active classroom modes
	broadcasting      bool
	broadcastWindowed bool
	voiceActive       bool
	studentDemoID     string
	monitoring        bool
	remoteControlID   string
	remoteControlOpen bool
	remoteExecOpen    bool
	remoteExecPanel   int
	remoteShell       string
	remoteCommand     string
	remoteExecutable  string
	remoteArguments   string
	remoteWorkDir     string
	remoteTimeout     float64
	remoteRequestID   string
	remoteOutput      string
	remoteResult      *remoteexec.CommandResult
	remoteApps        []remoteexec.Application
	screenLocked      bool
	lockMu            sync.RWMutex
	lockAll           bool
	lockedIDs         map[string]bool
	lockMessageSnap   string
	lockMessage       string
	activeModeLabel   string
	statusNote        string

	// Bottom-right fixed volume sliders
	volume       float64 // 0..100
	speakerMuted bool
	micVolume    float64 // 0..100
	micMuted     bool
	monitorFPS   float64 // 5..30
	monitorQual  float64 // 30..95
	useMulticast bool

	// Monitor wall & MyTheatre video bitmaps
	monitorTiles map[string]*ui.Bitmap
	remoteBitmap *ui.Bitmap
	// remoteTargetSnap mirrors remoteControlID under mu for frame callbacks.
	remoteTargetSnap string
	demoBitmap       *ui.Bitmap
	theatreFrame     *ui.Bitmap
	theatrePlayer    *ui.VideoPlayer
	theatre          core.TheatreState
	theatreMedia     []string
	theatreStreamURL string

	pendingMonitor       map[string]*core.VideoFrame
	pendingRemote        *core.VideoFrame
	pendingDemo          *core.VideoFrame
	videoUpdateScheduled atomic.Bool

	// Exam builder / publisher state
	examTitleInput     string
	examDurInput       float64
	examKindInput      string
	examPromptInput    string
	examOptionsInput   string
	examAnswerInput    string
	examPointsInput    float64
	examDraftQuestions []core.ExamQuestion
	examPublished      bool

	// File distribution & collection state
	distributePath     string
	fileQueue          []string
	fileDistModal      bool
	fileSending        bool
	remoteDir          string
	collectSaveDir     string
	autoOpenFile       bool
	fileTargetAll      bool
	collectingHomework bool

	// Chat & Policy state
	chatInput         string
	policyMode        string
	policyActive      bool
	blockedAppsInput  string
	allowedAppsInput  string
	blockedSitesInput string
	allowedSitesInput string

	// Widget states
	settingsOpen          bool
	settingsTab           string
	lastChatCount         int
	settingsSnapshot      settingsDraft
	fileDistHidden        map[string]bool
	renamingGroupID       string
	renameGroupInput      string
	remoteCmdWorkDir      string
	remoteAppPicked       string
	networkConfigOpen     bool
	networkModeInput      string
	networkInterfaceInput string
	networkIgnoreVirtual  bool
	networkIgnoreVPN      bool
	policyModal           bool
	channelModal          bool
	channelInput          string
	pendingChannel        string
	channelNum            float64
	authUsername          string
	authPasswordHash      string
	loginUserInput        string
	loginPassInput        string
	loginError            string
	recentChannels        []string
	store                 *store.Store
	onChannelSelected     func(string)

	// Lock-free snapshots for background media/transport goroutines
	broadcastFlag         atomic.Bool
	broadcastWindowedFlag atomic.Bool
	voiceFlag             atomic.Bool
	theatreFlag           atomic.Bool
	mcastFlag             atomic.Bool
	qualValue             atomic.Int64
	monitoringFlag        atomic.Bool
	monitorFPSValue       atomic.Int64
	micGainBits           atomic.Uint32
	demoIDMu              sync.RWMutex
	demoIDAtomic          string

	ctrlNotify      chan struct{}
	ctrlMu          sync.Mutex
	ctrlQueue       []ctrlOutbound
	ctrlQueuedInput int
}

type ctrlOutbound struct {
	env       protocol.Envelope
	targetIDs []string
	input     bool
}

// New creates a Teacher Console App wired to core interfaces.
func New(opts Options) *App {
	if opts.TeacherID == "" {
		opts.TeacherID = "teacher-01"
	}
	if opts.TeacherName == "" {
		opts.TeacherName = "教师端"
	}
	if opts.Channel == "" {
		opts.Channel = "1"
	}
	archiveDir := strings.TrimSpace(opts.ArchiveDir)
	if archiveDir == "" {
		archiveDir = defaultDownloadsDir()
	}
	app := &App{
		opts:              opts.Services,
		fingerprint:       opts.Fingerprint,
		store:             opts.Store,
		onChannelSelected: opts.OnChannelSelected,
		teacherID:         opts.TeacherID,
		teacherName:       opts.TeacherName,
		channel:           opts.Channel,
		dark:              false,
		channelModal:      opts.PromptChannel,
		channelInput:      opts.Channel,
		channelNum:        parseChannelNum(opts.Channel),
		authUsername:      "admin",
		loginUserInput:    "admin",
		broadcastWindowed: true,
		mainTab:           TabSeatMap,
		sideTab:           SideTabChatQueue,
		leftPaneSize:      185,
		mainSplitPct:      72,
		selectedGroup:     "all",
		selectedIDs:       make(map[string]bool),
		itemBounds:        make(map[string]ui.Rect),
		marqueeBase:       make(map[string]bool),
		lockMessage:       "请注意听讲，教师已锁定屏幕。",
		activeModeLabel:   "待机监听",
		statusNote:        "就绪 · 局域网低延迟组播通道正常",
		volume:            75,
		micVolume:         80,
		monitorFPS:        15,
		monitorQual:       75,
		useMulticast:      true,
		monitorTiles:      make(map[string]*ui.Bitmap),
		pendingMonitor:    make(map[string]*core.VideoFrame),
		theatreMedia:      nil,
		theatre:           core.TheatreState{Volume: 0.75},
		theatreStreamURL:  opts.TheatreStreamURL,
		examTitleInput:    "随堂小测：计算机网络基础",
		examDurInput:      10,
		examKindInput:     "single",
		examPromptInput:   "UDP 协议提供面向连接、可靠的字节流服务吗？",
		examOptionsInput:  "是,否",
		examAnswerInput:   "否",
		examPointsInput:   100,
		distributePath:    "",
		remoteDir:         "",
		remoteShell:       string(remoteexec.DefaultShell()),
		remoteTimeout:     60,
		remoteExecPanel:   remoteExecPanelLaunch,
		collectSaveDir:    archiveDir,
		autoOpenFile:      false,
		fileTargetAll:     true,
		policyMode:        "blacklist",
		blockedAppsInput:  "",
		allowedAppsInput:  "",
		blockedSitesInput: "",
		allowedSitesInput: "",
	}
	app.Start(context.Background())
	app.loadPersistedState()
	app.syncRuntimeFlags()
	return app
}

// Start begins UI-owned background work. Repeated calls are harmless.
func (a *App) Start(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}
	a.mu.Lock()
	if a.lifecycleClosed {
		a.mu.Unlock()
		return errors.New("teacher ui app is closed")
	}
	if a.lifecycleStarted {
		a.mu.Unlock()
		return nil
	}
	a.lifecycleCtx, a.lifecycleCancel = context.WithCancel(parent)
	a.ctrlNotify = make(chan struct{}, 1)
	a.lifecycleStarted = true
	a.lifecycleWG.Add(1)
	a.mu.Unlock()
	go func() {
		defer a.lifecycleWG.Done()
		a.controlSendWorker()
	}()
	return nil
}

// Close stops UI-owned work and is safe to call more than once.
func (a *App) Close() error {
	a.mu.Lock()
	if a.lifecycleClosed {
		a.mu.Unlock()
		return nil
	}
	a.lifecycleClosed = true
	cancel := a.lifecycleCancel
	fw := a.fileDistWin
	a.fileDistWin = nil
	win := a.win
	a.win = nil
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if fw != nil {
		fw.Destroy()
	}
	if win != nil {
		win.Destroy()
	}
	a.lifecycleWG.Wait()
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

func (a *App) loadPersistedState() {
	if a.store == nil {
		return
	}
	if cfg, ok, err := a.store.LoadSettings(); err == nil && ok {
		if strings.TrimSpace(cfg.Channel) != "" {
			norm := normalizeChannelID(cfg.Channel)
			a.channel = norm
			a.channelInput = norm
			a.channelNum = parseChannelNum(norm)
		}
		if strings.TrimSpace(cfg.AuthUsername) != "" {
			a.authUsername = strings.TrimSpace(cfg.AuthUsername)
			a.loginUserInput = a.authUsername
		}
		a.authPasswordHash = strings.TrimSpace(cfg.AuthPasswordHash)
		a.useMulticast = cfg.UseMulticast
		a.dark = false
		if strings.TrimSpace(cfg.CollectSaveDir) != "" {
			a.collectSaveDir = cfg.CollectSaveDir
			a.applyArchiveDir(cfg.CollectSaveDir)
		}
		if cfg.MonitorFPS >= 5 && cfg.MonitorFPS <= 30 {
			a.monitorFPS = cfg.MonitorFPS
		}
		if cfg.MonitorQual >= 30 && cfg.MonitorQual <= 95 {
			a.monitorQual = cfg.MonitorQual
		}
		if cfg.SpeakerVolume >= 0 && cfg.SpeakerVolume <= 100 {
			a.volume = cfg.SpeakerVolume
		}
		if cfg.MicVolume >= 0 && cfg.MicVolume <= 100 {
			a.micVolume = cfg.MicVolume
		}
		a.speakerMuted = cfg.SpeakerMuted
		a.micMuted = cfg.MicMuted
		if cfg.PolicyMode == "blacklist" || cfg.PolicyMode == "whitelist" {
			a.policyMode = cfg.PolicyMode
		}
		if cfg.BlockedApps != "" {
			a.blockedAppsInput = cfg.BlockedApps
		}
		if cfg.AllowedApps != "" {
			a.allowedAppsInput = cfg.AllowedApps
		}
		if cfg.BlockedSites != "" {
			a.blockedSitesInput = cfg.BlockedSites
		}
		if cfg.LockMessage != "" {
			a.lockMessage = cfg.LockMessage
		}
	}
	if recent, err := a.store.ListRecentChannels(8); err == nil {
		a.recentChannels = recent
	}
	if a.opts.Classroom != nil {
		if groups, err := a.store.LoadGroups(); err == nil && len(groups) > 0 {
			a.opts.Classroom.SetGroups(groups)
		}
		if students, err := a.store.LoadStudents(); err == nil {
			for _, st := range students {
				a.opts.Classroom.UpsertStudent(st)
			}
		}
		if chats, err := a.store.LoadChats(200); err == nil {
			for _, m := range chats {
				a.opts.Classroom.AddChat(m)
			}
		}
	}
	if a.opts.Exam != nil {
		if paper, err := a.store.LoadLatestExamPaper(); err == nil && paper != nil {
			a.opts.Exam.PublishPaper(*paper)
			if subs, err := a.store.LoadExamSubmissions(paper.ID); err == nil {
				restorer, canRestore := a.opts.Exam.(interface {
					RestoreSubmission(core.ExamSubmission) core.ExamSubmission
				})
				for _, sub := range subs {
					if canRestore {
						// Keeps teacher-graded essay scores across restarts.
						restorer.RestoreSubmission(sub)
					} else {
						a.opts.Exam.SubmitAnswer(sub)
					}
				}
			}
		}
	}
}

func (a *App) savePersistedSettings() {
	if a.store == nil {
		return
	}
	_ = a.store.SaveSettings(store.TeacherSettings{
		Channel:          a.persistedChannel(),
		AuthUsername:     a.authUsername,
		AuthPasswordHash: a.authPasswordHash,
		UseMulticast:     a.useMulticast,
		Dark:             a.dark,
		CollectSaveDir:   a.collectSaveDir,
		MonitorFPS:       a.monitorFPS,
		MonitorQual:      a.monitorQual,
		SpeakerVolume:    a.volume,
		MicVolume:        a.micVolume,
		SpeakerMuted:     a.speakerMuted,
		MicMuted:         a.micMuted,
		PolicyMode:       a.policyMode,
		BlockedApps:      a.blockedAppsInput,
		AllowedApps:      a.allowedAppsInput,
		BlockedSites:     a.blockedSitesInput,
		LockMessage:      a.lockMessage,
	})
}

// persistedChannel is the channel to use on next start: a pending change
// from Settings, otherwise the live channel.
func (a *App) persistedChannel() string {
	if a.pendingChannel != "" {
		return a.pendingChannel
	}
	return a.channel
}

func (a *App) syncRuntimeFlags() {
	a.broadcastFlag.Store(a.broadcasting)
	a.broadcastWindowedFlag.Store(a.broadcastWindowed)
	a.voiceFlag.Store(a.voiceActive)
	a.theatreFlag.Store(a.theatre.Playing)
	a.mcastFlag.Store(a.useMulticast)
	a.qualValue.Store(int64(a.monitorQual))
	a.monitoringFlag.Store(a.monitoring)
	a.monitorFPSValue.Store(int64(a.monitorFPS))
	a.syncMicGain()
	a.demoIDMu.Lock()
	a.demoIDAtomic = a.studentDemoID
	a.demoIDMu.Unlock()
}

// AttachWindow binds the MyGo window and installs the native menu bar.
func (a *App) AttachWindow(w *mygo.Window) {
	a.mu.Lock()
	a.win = w
	a.mu.Unlock()
	if w != nil {
		w.SetMenu(a.BuildMenu())
		_ = w.SetIcon(theme.AppIconPNG(true))
		w.OnClosed(func() {
			a.mu.Lock()
			if a.win == w {
				a.win = nil
				a.videoUpdateScheduled.Store(false)
			}
			fw := a.fileDistWin
			a.fileDistWin = nil
			a.mu.Unlock()
			if fw != nil {
				fw.Destroy()
			}
		})
	}
}

// Invalidate requests a redraw on the attached window if any.
func (a *App) Invalidate() {
	a.mu.Lock()
	w := a.win
	fw := a.fileDistWin
	a.mu.Unlock()
	if w != nil {
		w.Invalidate()
	}
	if fw != nil {
		fw.Invalidate()
	}
}

// runOnUI runs fn on the UI thread when a window is attached, so state read
// by View is never mutated concurrently from transport or worker goroutines.
// Without a window there is no concurrent View, so fn runs inline.
func (a *App) runOnUI(fn func()) {
	a.mu.Lock()
	w := a.win
	a.mu.Unlock()
	if w != nil {
		w.Update(fn)
		return
	}
	fn()
}

// SetStatusNote updates the status bar note; safe to call from any goroutine.
func (a *App) SetStatusNote(note string) {
	a.runOnUI(func() { a.statusNote = note })
}

// ShowMainWindow restores and focuses the main teacher window.
func (a *App) ShowMainWindow() {
	a.mu.Lock()
	w := a.win
	a.mu.Unlock()
	if w != nil {
		w.Show()
		w.Restore()
		w.Focus()
	}
}

// BuildTrayMenu builds the context menu shown when right-clicking the teacher tray icon.
func (a *App) BuildTrayMenu() *mygo.Menu {
	return mygo.NewMenu([]*mygo.MenuItem{
		{Label: "显示教师端主窗口", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.ShowMainWindow() }},
		mygo.Separator(),
		{Label: "开始/停止屏幕广播", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.ToggleBroadcast() }},
		{Label: "开启/停止屏幕监控", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.ToggleMonitor() }},
		{Label: "黑屏锁定 / 解锁", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.ToggleScreenLock() }},
		{Label: "发起点名签到", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.StartCheckIn() }},
		mygo.Separator(),
		{Role: mygo.RoleQuit, Label: "退出教师端"},
	})
}

// InstallTray creates the teacher system tray icon with a right-click context menu.
func (a *App) InstallTray() {
	tray, err := mygo.NewTray(mygo.TrayOptions{
		ID:      "nexlab-teacher-tray",
		Icon:    theme.AppIconPNG(true),
		ToolTip: "矩联电子教室 MatrixClass 教师端",
		Menu:    a.BuildTrayMenu(),
	})
	if err == nil {
		a.mu.Lock()
		a.tray = tray
		a.mu.Unlock()
	}
}

// BuildMenu creates the native desktop menu bar for the Teacher Console.
func (a *App) BuildMenu() *mygo.Menu {
	return mygo.NewMenu([]*mygo.MenuItem{
		{
			Label: "课堂(&C)",
			Submenu: []*mygo.MenuItem{
				{Label: "发起点名签到", Accelerator: "CmdOrCtrl+R", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.StartCheckIn() }},
				{Label: "清空举手队列", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.clearAllHands() }},
				mygo.Separator(),
				{Label: "课堂参数设置", Accelerator: "CmdOrCtrl+,", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.openSettings(); a.Invalidate() }},
				mygo.Separator(),
				{Role: mygo.RoleQuit, Label: "退出教师端"},
			},
		},
		{
			Label: "教学(&T)",
			Submenu: []*mygo.MenuItem{
				{Label: "屏幕广播 (全屏/窗口)", Accelerator: "F5", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.ToggleBroadcast() }},
				{Label: "语音广播", Accelerator: "F6", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.ToggleVoice() }},
				{Label: "学生演示", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.ToggleStudentDemo(a.selectedID) }},
				{Label: "网络影院", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.mainTab = TabMyTheatre; a.Invalidate() }},
			},
		},
		{
			Label: "管控(&M)",
			Submenu: []*mygo.MenuItem{
				{Label: "屏幕监控墙", Accelerator: "F7", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.ToggleMonitor() }},
				{Label: "远程控制选中学生", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.StartRemoteControl(a.selectedID) }},
				{Label: "黑屏锁定/解锁", Accelerator: "F8", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.ToggleScreenLock() }},
				{Label: "重置学生端退出密码", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.resetStudentExitPassword() }},
				mygo.Separator(),
				{Label: "远程开机 (WOL)", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.ExecutePower(core.PowerWake) }},
				{Label: "远程重启选中机", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.ExecutePower(core.PowerReboot) }},
				{Label: "远程关机选中机", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.ExecutePower(core.PowerShutdown) }},
			},
		},
		{
			Label: "视图(&V)",
			Submenu: []*mygo.MenuItem{
				{Label: "设备视图", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.mainTab = TabSeatMap; a.Invalidate() }},
				{Label: "屏幕监控", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.mainTab = TabMonitorWall; a.Invalidate() }},
				{Label: "网络影院", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.mainTab = TabMyTheatre; a.Invalidate() }},
				{Label: "随堂测验", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.mainTab = TabExamStats; a.Invalidate() }},
				{Label: "文件与作业", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.mainTab = TabFiles; a.Invalidate() }},
				mygo.Separator(),
			},
		},
	})
}

// View builds the complete Teacher Console UI for a frame.
func (a *App) View(c *ui.Context) {
	pal := theme.GetPalette(a.dark)
	th := theme.Theme(a.dark)
	c.SetTheme(th)

	students := a.filteredStudents()

	ui.Column(c).Fill().Background(pal.Canvas).Children(func() {
		a.buildToolbar(c, pal)
		ui.Divider(c)
		a.buildBody(c, pal, students)
		ui.Divider(c)
		a.buildStatusBar(c, pal)
	})

	settingsDialog := ui.Modal(c, &a.settingsOpen, func() {
		a.buildSettingsModal(c, pal)
	})
	if settingsDialog.Dismissed() {
		a.cancelSettings()
	}
	ui.Modal(c, &a.networkConfigOpen, func() {
		a.buildNetworkConfigModal(c, pal)
	})

	ui.Modal(c, &a.policyModal, func() {
		ui.Column(c).Width(520).Height(560).Background(pal.Canvas).Border(1, pal.CardBorder).Radius(6).Padding(16).Gap(10).Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
				ui.Text(c, "管控策略编排").FontSize(16).Bold()
				if a.policyActive {
					ui.Badge(c, "已生效").Background(pal.Selection).TextColor(pal.Accent)
				} else {
					ui.Badge(c, "未生效")
				}
				ui.Spacer(c)
				if ui.Button(c, "关闭").Padding(4, 10).Clicked() {
					a.policyModal = false
				}
			})
			a.buildSidePolicy(c, pal)
		})
	})

	ui.Modal(c, &a.remoteControlOpen, func() {
		a.buildRemoteControlModal(c, pal)
	})

	ui.Modal(c, &a.remoteExecOpen, func() {
		a.buildRemoteExecModal(c, pal)
	})

	ui.Modal(c, &a.fileDistModal, func() {
		ui.Column(c).Width(560).Height(500).Background(pal.Canvas).Border(1, pal.CardBorder).Radius(4).Children(func() {
			a.buildFileDistributeContent(c, pal)
		})
	})

	channelDialog := ui.Modal(c, &a.channelModal, func() {
		a.buildChannelSelectModal(c, pal)
	})
	if channelDialog.Dismissed() {
		a.channelModal = true
	}

	a.syncRuntimeFlags()
}

// Action methods exported for menu bar, toolbar, and tests.

func (a *App) sendControlEnvelope(msgType protocol.MsgType, payload any, targetIDs []string) {
	a.sendControlEnvelopeWithRequest(msgType, payload, targetIDs, "")
}

func (a *App) sendControlEnvelopeWithRequest(msgType protocol.MsgType, payload any, targetIDs []string, requestID string) {
	a.mu.Lock()
	closed := a.lifecycleClosed
	started := a.ctrlNotify != nil
	a.mu.Unlock()
	if closed {
		return
	}
	if a.opts.Transport == nil {
		return
	}
	env, err := protocol.NewEnvelope(msgType, a.teacherID, payload)
	if err != nil {
		return
	}
	env.Channel = a.channel
	env.RequestID = requestID
	env.Timestamp = time.Now().UnixMilli()
	var targets []string
	if len(targetIDs) > 0 {
		targets = append([]string(nil), targetIDs...)
	}
	if !started {
		_ = a.opts.Transport.BroadcastControl(env, targets)
		return
	}
	// A single FIFO keeps control messages ordered (e.g. lock then unlock);
	// the previous overflow goroutine could let a later message overtake.
	isInput := msgType == protocol.MsgRemoteInputEvent
	a.ctrlMu.Lock()
	if isInput && a.ctrlQueuedInput >= maxQueuedRemoteInput {
		a.ctrlMu.Unlock()
		return
	}
	a.ctrlQueue = append(a.ctrlQueue, ctrlOutbound{env: env, targetIDs: targets, input: isInput})
	if isInput {
		a.ctrlQueuedInput++
	}
	a.ctrlMu.Unlock()
	select {
	case a.ctrlNotify <- struct{}{}:
	default:
	}
}

func (a *App) controlSendWorker() {
	a.mu.Lock()
	ctx := a.lifecycleCtx
	notify := a.ctrlNotify
	a.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return
		case <-notify:
		}
		for {
			a.ctrlMu.Lock()
			if len(a.ctrlQueue) == 0 {
				a.ctrlMu.Unlock()
				break
			}
			item := a.ctrlQueue[0]
			a.ctrlQueue[0] = ctrlOutbound{}
			a.ctrlQueue = a.ctrlQueue[1:]
			if item.input {
				a.ctrlQueuedInput--
			}
			a.ctrlMu.Unlock()
			if ctx.Err() != nil {
				return
			}
			if a.opts.Transport != nil {
				_ = a.opts.Transport.BroadcastControl(item.env, item.targetIDs)
			}
		}
	}
}

func (a *App) listStudents() []core.StudentInfo {
	if a.opts.Classroom == nil {
		return nil
	}
	all := a.opts.Classroom.ListStudents()
	sort.Slice(all, func(i, j int) bool {
		if all[i].Seat.Row != all[j].Seat.Row {
			return all[i].Seat.Row < all[j].Seat.Row
		}
		if all[i].Seat.Col != all[j].Seat.Col {
			return all[i].Seat.Col < all[j].Seat.Col
		}
		return all[i].MachineName < all[j].MachineName
	})
	return all
}

func (a *App) filteredStudents() []core.StudentInfo {
	all := a.listStudents()
	q := strings.ToLower(strings.TrimSpace(a.searchQuery))
	var out []core.StudentInfo
	for _, st := range all {
		if a.selectedGroup != "" && a.selectedGroup != "all" && st.GroupID != a.selectedGroup {
			continue
		}
		if q != "" {
			hay := strings.ToLower(st.StudentNo + " " + st.MachineName + " " + st.IP)
			if !strings.Contains(hay, q) {
				continue
			}
		}
		out = append(out, st)
	}
	return out
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Expose read-only accessors for testing and external coordination.
func (a *App) MainTab() int        { return a.mainTab }
func (a *App) SetMainTab(tab int)  { a.mainTab = tab }
func (a *App) SideTab() int        { return a.sideTab }
func (a *App) SetSideTab(tab int)  { a.sideTab = tab }
func (a *App) Volume() float64     { return a.volume }
func (a *App) MonitorFPS() float64 { return float64(a.monitorFPSValue.Load()) }
func (a *App) MonitorQual() float64 {
	return float64(a.qualValue.Load())
}
func (a *App) Broadcasting() bool { return a.broadcastFlag.Load() }
func (a *App) ScreenLocked() bool {
	a.lockMu.RLock()
	defer a.lockMu.RUnlock()
	return a.lockAll || len(a.lockedIDs) > 0
}

// ScreenLockAll reports whether the whole class is locked.
func (a *App) ScreenLockAll() bool {
	a.lockMu.RLock()
	defer a.lockMu.RUnlock()
	return a.lockAll
}

// ScreenLockedFor reports whether studentID should be locked, e.g. when it
// (re)registers. Safe to call from runtime goroutines.
func (a *App) ScreenLockedFor(studentID string) (bool, string) {
	a.lockMu.RLock()
	defer a.lockMu.RUnlock()
	msg := a.lockMessageSnap
	if msg == "" {
		msg = "请注意听讲，教师已锁定屏幕。"
	}
	return a.lockAll || a.lockedIDs[studentID], msg
}
func (a *App) SelectedID() string { return a.selectedID }
func (a *App) VoiceActive() bool  { return a.voiceFlag.Load() }
func (a *App) Monitoring() bool   { return a.monitoringFlag.Load() }
func (a *App) RemoteControlID() string {
	return a.remoteControlID
}
func (a *App) StudentDemoID() string {
	a.demoIDMu.RLock()
	defer a.demoIDMu.RUnlock()
	return a.demoIDAtomic
}

func (a *App) UseMulticast() bool {
	return a.mcastFlag.Load()
}
