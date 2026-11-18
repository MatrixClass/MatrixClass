package teacher

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/chart"
	"github.com/egoist/mygo/ui/video"

	"nexlab/internal/core"
	"nexlab/internal/protocol"
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
	TeacherID   string
	TeacherName string
	Channel     string
	Dark        bool
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
	SideTabStudents = iota
	SideTabChatQueue
	SideTabPolicy
)

// App is the MyGo native Teacher Console for 矩联电子教室 MatrixClass.
type App struct {
	mu sync.Mutex

	opts Services
	win  *mygo.Window

	teacherID   string
	teacherName string
	channel     string
	dark        bool

	// Primary navigation & layout state
	mainTab       int
	sideTab       int
	leftPaneSize  float32
	mainSplitPct  float32
	treeAllOpen   bool
	treeGroupOpen bool
	treeMediaOpen bool
	selectedGroup string
	selectedID    string
	searchQuery   string

	// Active classroom modes
	broadcasting      bool
	broadcastWindowed bool
	voiceActive       bool
	studentDemoID     string
	monitoring        bool
	remoteControlID   string
	remoteControlOpen bool
	screenLocked      bool
	lockMessage       string
	activeModeLabel   string
	statusNote        string

	// Bottom-right fixed volume sliders
	volume       float64 // 0..100
	monitorFPS   float64 // 5..30
	monitorQual  float64 // 30..95
	useMulticast bool

	// Monitor wall & MyTheatre video bitmaps
	monitorTiles  map[string]*ui.Bitmap
	remoteBitmap  *ui.Bitmap
	demoBitmap    *ui.Bitmap
	theatreFrame  *ui.Bitmap
	theatrePlayer *ui.VideoPlayer
	theatre       core.TheatreState
	theatreMedia  []string

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
	distributePath string
	remoteDir      string
	collectSaveDir string
	autoOpenFile   bool
	fileTargetAll  bool

	// Chat & Policy state
	chatInput         string
	policyMode        string
	blockedAppsInput  string
	allowedAppsInput  string
	blockedSitesInput string
	allowedSitesInput string

	// Widget states
	studentTable ui.ListState
	selectedRow  int
	settingsOpen bool

	// Lock-free snapshots for background media/transport goroutines
	broadcastFlag         atomic.Bool
	broadcastWindowedFlag atomic.Bool
	voiceFlag             atomic.Bool
	theatreFlag           atomic.Bool
	mcastFlag             atomic.Bool
	qualValue             atomic.Int64
	demoIDMu              sync.RWMutex
	demoIDAtomic          string

	ctrlCh chan ctrlOutbound
}

type ctrlOutbound struct {
	env       protocol.Envelope
	targetIDs []string
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
		opts.Channel = "301机房"
	}
	app := &App{
		opts:              opts.Services,
		teacherID:         opts.TeacherID,
		teacherName:       opts.TeacherName,
		channel:           opts.Channel,
		dark:              opts.Dark,
		broadcastWindowed: true,
		mainTab:           TabSeatMap,
		sideTab:           SideTabStudents,
		leftPaneSize:      185,
		mainSplitPct:      72,
		treeAllOpen:       true,
		treeGroupOpen:     true,
		treeMediaOpen:     true,
		selectedGroup:     "all",
		lockMessage:       "请注意听讲，教师已锁定屏幕。",
		activeModeLabel:   "待机监听",
		statusNote:        "就绪 · 局域网低延迟组播通道正常",
		volume:            75,
		monitorFPS:        15,
		monitorQual:       75,
		useMulticast:      true,
		monitorTiles:      make(map[string]*ui.Bitmap),
		pendingMonitor:    make(map[string]*core.VideoFrame),
		theatreMedia:      []string{"01_计算机网络拓扑演示.mp4", "02_TCP三次握手动画.mkv", "03_局域网交换实验讲解.mp4"},
		theatre:           core.TheatreState{Media: "01_计算机网络拓扑演示.mp4", Duration: 360, Volume: 0.75},
		examTitleInput:    "随堂小测：计算机网络基础",
		examDurInput:      10,
		examKindInput:     "single",
		examPromptInput:   "UDP 协议提供面向连接、可靠的字节流服务吗？",
		examOptionsInput:  "是,否",
		examAnswerInput:   "否",
		examPointsInput:   100,
		distributePath:    "D:\\Teaching\\Lab03_PacketGuide.pdf",
		remoteDir:         "",
		collectSaveDir:    "D:\\Submissions\\Class301",
		autoOpenFile:      true,
		fileTargetAll:     true,
		policyMode:        "blacklist",
		blockedAppsInput:  "steam.exe, wechat.exe, chrome.exe",
		allowedAppsInput:  "code.exe, wireshark.exe, notepad.exe",
		blockedSitesInput: "game.example.com, video.example.com",
		allowedSitesInput: "lab.nexlab.local, docs.golang.org",
		selectedRow:       -1,
	}
	app.studentTable.Selected = &app.selectedRow
	app.studentTable.RoundRows = true
	app.syncRuntimeFlags()
	app.ctrlCh = make(chan ctrlOutbound, 64)
	go app.controlSendWorker()
	return app
}

func (a *App) syncRuntimeFlags() {
	a.broadcastFlag.Store(a.broadcasting)
	a.broadcastWindowedFlag.Store(a.broadcastWindowed)
	a.voiceFlag.Store(a.voiceActive)
	a.theatreFlag.Store(a.theatre.Playing)
	a.mcastFlag.Store(a.useMulticast)
	a.qualValue.Store(int64(a.monitorQual))
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
	}
}

// Invalidate requests a redraw on the attached window if any.
func (a *App) Invalidate() {
	a.mu.Lock()
	w := a.win
	a.mu.Unlock()
	if w != nil {
		w.Invalidate()
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
				{Label: "课堂参数设置", Accelerator: "CmdOrCtrl+,", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.settingsOpen = true; a.Invalidate() }},
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
				{Label: "网络影院 (MyTheatre)", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.mainTab = TabMyTheatre; a.Invalidate() }},
			},
		},
		{
			Label: "管控(&M)",
			Submenu: []*mygo.MenuItem{
				{Label: "屏幕监控墙", Accelerator: "F7", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.ToggleMonitor() }},
				{Label: "远程控制选中学生", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.StartRemoteControl(a.selectedID) }},
				{Label: "黑屏锁定/解锁", Accelerator: "F8", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.ToggleScreenLock() }},
				mygo.Separator(),
				{Label: "远程开机 (WOL)", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.ExecutePower(core.PowerWake) }},
				{Label: "远程重启选中机", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.ExecutePower(core.PowerReboot) }},
				{Label: "远程关机选中机", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.ExecutePower(core.PowerShutdown) }},
			},
		},
		{
			Label: "视图(&V)",
			Submenu: []*mygo.MenuItem{
				{Label: "座位视图", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.mainTab = TabSeatMap; a.Invalidate() }},
				{Label: "屏幕监控", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.mainTab = TabMonitorWall; a.Invalidate() }},
				{Label: "网络影院", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.mainTab = TabMyTheatre; a.Invalidate() }},
				{Label: "随堂测验", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.mainTab = TabExamStats; a.Invalidate() }},
				{Label: "文件与作业", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.mainTab = TabFiles; a.Invalidate() }},
				mygo.Separator(),
				{Label: "切换明暗主题", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.dark = !a.dark; a.Invalidate() }},
			},
		},
	})
}

// UpdateMonitorFrame pushes a decoded student monitor video frame into the Monitor Wall tile.
func (a *App) UpdateMonitorFrame(studentID string, frame *core.VideoFrame) {
	if studentID == "" || frame == nil || frame.Width <= 0 || frame.Height <= 0 {
		return
	}
	a.mu.Lock()
	w := a.win
	if w == nil {
		bmp := a.ensureTileBitmap(studentID, frame.Width, frame.Height)
		a.mu.Unlock()
		updateBitmapFromFrame(bmp, frame)
		return
	}
	a.pendingMonitor[studentID] = frame
	a.mu.Unlock()
	a.scheduleVideoFramesUpdate(w)
}

// UpdateRemoteFrame updates the live Remote Control interactive screen bitmap.
func (a *App) UpdateRemoteFrame(studentID string, frame *core.VideoFrame) {
	if studentID == "" || frame == nil || frame.Width <= 0 || frame.Height <= 0 {
		return
	}
	a.mu.Lock()
	w := a.win
	if w == nil {
		if a.remoteBitmap == nil {
			a.remoteBitmap = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, frame.Width, frame.Height)))
		}
		bmp := a.remoteBitmap
		a.mu.Unlock()
		updateBitmapFromFrame(bmp, frame)
		return
	}
	a.pendingRemote = frame
	a.mu.Unlock()
	a.scheduleVideoFramesUpdate(w)
}

// UpdateDemoFrame updates the Student Demo preview bitmap on the Teacher Console.
func (a *App) UpdateDemoFrame(studentID string, frame *core.VideoFrame) {
	if studentID == "" || frame == nil || frame.Width <= 0 || frame.Height <= 0 {
		return
	}
	a.mu.Lock()
	w := a.win
	if w == nil {
		if a.demoBitmap == nil {
			a.demoBitmap = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, frame.Width, frame.Height)))
		}
		bmp := a.demoBitmap
		a.mu.Unlock()
		updateBitmapFromFrame(bmp, frame)
		return
	}
	a.pendingDemo = frame
	a.mu.Unlock()
	a.scheduleVideoFramesUpdate(w)
}

func (a *App) scheduleVideoFramesUpdate(w *mygo.Window) {
	if w == nil || !a.videoUpdateScheduled.CompareAndSwap(false, true) {
		return
	}
	w.Update(func() {
		a.videoUpdateScheduled.Store(false)
		a.mu.Lock()
		var monBatch map[string]*core.VideoFrame
		if len(a.pendingMonitor) > 0 {
			monBatch = a.pendingMonitor
			a.pendingMonitor = make(map[string]*core.VideoFrame, len(monBatch))
		}
		remFrame := a.pendingRemote
		a.pendingRemote = nil
		demoFrame := a.pendingDemo
		a.pendingDemo = nil

		for id, f := range monBatch {
			bmp := a.ensureTileBitmap(id, f.Width, f.Height)
			updateBitmapFromFrame(bmp, f)
		}
		if remFrame != nil {
			if a.remoteBitmap == nil {
				a.remoteBitmap = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, remFrame.Width, remFrame.Height)))
			}
			updateBitmapFromFrame(a.remoteBitmap, remFrame)
		}
		if demoFrame != nil {
			if a.demoBitmap == nil {
				a.demoBitmap = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, demoFrame.Width, demoFrame.Height)))
			}
			updateBitmapFromFrame(a.demoBitmap, demoFrame)
		}
		a.mu.Unlock()
	})
}

func updateBitmapFromFrame(bmp *ui.Bitmap, frame *core.VideoFrame) {
	if bmp == nil || frame == nil {
		return
	}
	if len(frame.NV12Y) > 0 && len(frame.NV12UV) > 0 && frame.StrideY > 0 && frame.StrideUV > 0 {
		bmp.UpdateYUV(&ui.YUVImage{
			Format:  ui.NV12,
			Width:   frame.Width,
			Height:  frame.Height,
			Planes:  [3][]byte{frame.NV12Y, frame.NV12UV},
			Strides: [3]int{frame.StrideY, frame.StrideUV},
			Matrix:  ui.BT709,
		})
		return
	}
	if len(frame.Data) >= frame.Width*frame.Height*4 {
		img := &image.RGBA{
			Pix:    frame.Data,
			Stride: frame.Width * 4,
			Rect:   image.Rect(0, 0, frame.Width, frame.Height),
		}
		bmp.Update(img)
	}
}

// UpdateTheatreFrame updates the MyTheatre preview bitmap via YUV or RGBA data.
func (a *App) UpdateTheatreFrame(frame *core.VideoFrame) {
	if frame == nil || frame.Width <= 0 || frame.Height <= 0 {
		return
	}
	if a.theatreFrame == nil {
		a.theatreFrame = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, frame.Width, frame.Height)))
	}
	if len(frame.NV12Y) > 0 && len(frame.NV12UV) > 0 && frame.StrideY > 0 && frame.StrideUV > 0 {
		a.theatreFrame.UpdateYUV(&ui.YUVImage{
			Format:  ui.NV12,
			Width:   frame.Width,
			Height:  frame.Height,
			Planes:  [3][]byte{frame.NV12Y, frame.NV12UV},
			Strides: [3]int{frame.StrideY, frame.StrideUV},
			Matrix:  ui.BT709,
		})
		return
	}
	if len(frame.Data) >= frame.Width*frame.Height*4 {
		a.theatreFrame.Update(&image.RGBA{
			Pix:    frame.Data,
			Stride: frame.Width * 4,
			Rect:   image.Rect(0, 0, frame.Width, frame.Height),
		})
	}
}

func (a *App) ensureTileBitmap(studentID string, w, h int) *ui.Bitmap {
	if bmp, ok := a.monitorTiles[studentID]; ok && bmp != nil {
		return bmp
	}
	img := placeholderScreenRGBA(w, h, len(a.monitorTiles)+1)
	bmp := ui.NewBitmap(img)
	a.monitorTiles[studentID] = bmp
	return bmp
}

func placeholderScreenRGBA(w, h, seed int) *image.RGBA {
	if w <= 0 {
		w = 240
	}
	if h <= 0 {
		h = 135
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	shade := uint8(36 + (seed*11)%24)
	for y := range h {
		for x := range w {
			c := color.RGBA{R: shade, G: shade, B: shade, A: 255}
			if y < 18 {
				c = color.RGBA{R: 26, G: 26, B: 26, A: 255}
			} else if x > 14 && x < w-14 && y > 26 && y < h-18 {
				c = color.RGBA{R: 242, G: 242, B: 242, A: 255}
				if y < 40 {
					c = color.RGBA{R: 64, G: 64, B: 64, A: 255}
				}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
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

	ui.Modal(c, &a.settingsOpen, func() {
		a.buildSettingsModal(c, pal)
	})

	ui.Modal(c, &a.remoteControlOpen, func() {
		a.buildRemoteControlModal(c, pal)
	})

	a.syncRuntimeFlags()
}

func (a *App) buildToolbar(c *ui.Context, pal theme.Palette) {
	ui.Column(c).Background(pal.ChromeBar).Padding(6, 10).Gap(4).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).DragWindow().Children(func() {
			ui.Text(c, "矩联电子教室 MatrixClass 教师端").FontSize(14).Bold().TextColor(pal.Accent)
			ui.Badge(c, a.channel).Background(pal.Selection).TextColor(pal.Accent)
			ui.Spacer(c)
			ui.SearchField(c, &a.searchQuery).Placeholder("筛选机位 / 学号 / IP...").Width(220)
			if ui.Button(c, "").Label("切换主题").Tooltip("切换深色/浅色主题").Children(func() {
				ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
					ui.Image(c, theme.IconThemeToggle).Size(16, 16)
					if a.dark {
						ui.Text(c, "浅色").FontSize(12)
					} else {
						ui.Text(c, "深色").FontSize(12)
					}
				})
			}).Clicked() {
				a.dark = !a.dark
			}
			if ui.Button(c, "").Label("设置").Tooltip("课堂通道与组播设置").Children(func() {
				ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
					ui.Image(c, theme.IconSettings).Size(16, 16)
					ui.Text(c, "设置").FontSize(12)
				})
			}).Clicked() {
				a.settingsOpen = true
			}
		})

		ui.Toolbar(c, func() {
			a.toolbarToggle(c, pal, "屏幕广播", theme.IconBroadcast, &a.broadcasting, func() {
				a.applyBroadcastState()
			})
			a.toolbarToggle(c, pal, "语音广播", theme.IconVoice, &a.voiceActive, func() {
				a.applyVoiceState()
			})
			a.toolbarButton(c, pal, "学生演示", theme.IconStudentDemo, a.studentDemoID != "", func() {
				a.ToggleStudentDemo(a.selectedID)
			})
			a.toolbarButton(c, pal, "网络影院", theme.IconMyTheatre, a.mainTab == TabMyTheatre, func() {
				a.mainTab = TabMyTheatre
				a.activeModeLabel = "网络影院"
				a.statusNote = "已切换至网络影院 (MyTheatre)"
			})
			a.toolbarToggle(c, pal, "屏幕监控", theme.IconMonitor, &a.monitoring, func() {
				a.applyMonitorState()
			})
			a.toolbarButton(c, pal, "远程控制", theme.IconRemoteControl, a.remoteControlID != "", func() {
				a.StartRemoteControl(a.selectedID)
			})
			a.toolbarToggle(c, pal, "黑屏锁屏", theme.IconLockScreen, &a.screenLocked, func() {
				a.applyScreenLock()
			})
			a.toolbarButton(c, pal, "文件分发", theme.IconFileDistribute, a.mainTab == TabFiles, func() {
				a.mainTab = TabFiles
				a.statusNote = "文件分发面板已打开"
			})
			a.toolbarButton(c, pal, "收取作业", theme.IconCollectHomework, false, func() {
				a.CollectHomework()
			})
			a.toolbarButton(c, pal, "随堂测验", theme.IconExam, a.mainTab == TabExamStats, func() {
				a.mainTab = TabExamStats
				a.statusNote = "随堂测验与统计面板已打开"
			})
			a.toolbarButton(c, pal, "点名签到", theme.IconCheckIn, false, func() {
				a.StartCheckIn()
			})
			ui.Spacer(c)
			a.toolbarButton(c, pal, "远程开机", theme.IconPower, false, func() {
				a.ExecutePower(core.PowerWake)
			})
		}).Label("教师主工具栏")
	})
}

func (a *App) toolbarButton(c *ui.Context, pal theme.Palette, label string, icon *ui.SVG, active bool, onClick func()) {
	btn := ui.Button(c, "").Label(label).Tooltip(label).Padding(4, 8).Radius(3)
	if active {
		btn.Background(pal.Selection).Border(1, pal.Accent)
	}
	btn.Children(func() {
		ui.Column(c).AlignItems(ui.Center).Gap(2).Children(func() {
			ui.Image(c, icon).Size(22, 22)
			ui.Text(c, label).FontSize(11).SingleLine()
		})
	})
	if btn.Clicked() {
		onClick()
	}
}

func (a *App) toolbarToggle(c *ui.Context, pal theme.Palette, label string, icon *ui.SVG, state *bool, onChanged func()) {
	tg := ui.Toggle(c, state, "").Label(label).Tooltip(label).Padding(4, 8).Radius(3)
	if *state {
		tg.Background(pal.Selection).Border(1, pal.Accent)
	}
	tg.Children(func() {
		ui.Column(c).AlignItems(ui.Center).Gap(2).Children(func() {
			ui.Image(c, icon).Size(22, 22)
			ui.Text(c, label).FontSize(11).SingleLine()
		})
	})
	if tg.Changed() {
		onChanged()
	}
}

func (a *App) buildBody(c *ui.Context, pal theme.Palette, students []core.StudentInfo) {
	ui.Split(c, &a.leftPaneSize, func() {
		a.buildLeftSidebar(c, pal)
	}, func() {
		ui.SplitPercent(c, &a.mainSplitPct, func() {
			a.buildMainWorkspace(c, pal, students)
		}, func() {
			a.buildRightSidebar(c, pal, students)
		}).SplitReset(72).Grow(1)
	}).SplitReset(185).Grow(1)
}

func (a *App) buildLeftSidebar(c *ui.Context, pal theme.Palette) {
	ui.Column(c).Fill().Background(pal.ChromeRail).BorderWidth(0, 1, 0, 0).BorderColor(pal.CardBorder).Children(func() {
		// Quick vertical navigation rail
		ui.Column(c).Padding(8).Gap(4).BorderWidth(0, 0, 1, 0).BorderColor(pal.CardBorder).Children(func() {
			ui.Text(c, "课堂导航").FontSize(11).Bold().TextColor(pal.TextMuted)
			navItems := []struct {
				tab   int
				label string
				icon  *ui.SVG
			}{
				{TabSeatMap, "座位视图", theme.IconSeat},
				{TabMonitorWall, "屏幕监控", theme.IconMonitor},
				{TabMyTheatre, "网络影院", theme.IconMyTheatre},
				{TabExamStats, "随堂测验", theme.IconExam},
				{TabFiles, "文件与作业", theme.IconFileDistribute},
			}
			for _, item := range navItems {
				btn := ui.Button(c, "").Label(item.label).FillWidth().Padding(5, 8).Radius(3).Justify(ui.Start)
				if a.mainTab == item.tab {
					btn.Background(pal.Selection).Border(1, pal.Accent)
				}
				btn.Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Image(c, item.icon).Size(16, 16)
						ui.Text(c, item.label).FontSize(12).SingleLine()
					})
				})
				if btn.Clicked() {
					a.mainTab = item.tab
				}
			}
		})

		// Classroom group & media library tree
		ui.Scroll(c).Grow(1).Padding(8).Gap(6).Children(func() {
			ui.Text(c, "分组与媒体库").FontSize(11).Bold().TextColor(pal.TextMuted)
			ui.Tree(c, func() {
				allItem := ui.TreeItem(c, fmt.Sprintf("全部教室 (%s)", a.channel), &a.treeAllOpen, func() {
					if ui.TreeItem(c, "全部在线学生", nil, nil).Clicked() {
						a.selectedGroup = "all"
					}
				})
				if allItem.Clicked() {
					a.selectedGroup = "all"
				}
				ui.TreeItem(c, "教学小组", &a.treeGroupOpen, func() {
					for _, g := range a.listGroups() {
						label := fmt.Sprintf("%s (%d人)", g.Name, len(g.Members))
						if ui.TreeItem(c, label, nil, nil).Clicked() {
							a.selectedGroup = g.ID
						}
					}
				})
				ui.TreeItem(c, "影院媒体库 (MyTheatre)", &a.treeMediaOpen, func() {
					for _, media := range a.theatreMedia {
						if ui.TreeItem(c, media, nil, nil).Clicked() {
							a.theatre.Media = media
							a.mainTab = TabMyTheatre
						}
					}
				})
			})
		})
	})
}

func (a *App) buildMainWorkspace(c *ui.Context, pal theme.Palette, students []core.StudentInfo) {
	ui.Column(c).Fill().Background(pal.Canvas).Children(func() {
		ui.Row(c).Background(pal.ChromeBar).Padding(4, 10).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(pal.CardBorder).Children(func() {
			ui.Tabs(c, &a.mainTab, "座位视图", "屏幕监控", "网络影院", "随堂测验", "文件与作业")
			ui.Spacer(c)
			ui.Checkbox(c, &a.broadcastWindowed, "窗口模式广播").OnChange(func() {
				a.syncRuntimeFlags()
				if a.broadcasting {
					a.applyBroadcastState()
				}
			})
		})

		switch a.mainTab {
		case TabSeatMap:
			a.buildSeatMapTab(c, pal, students)
		case TabMonitorWall:
			a.buildMonitorWallTab(c, pal, students)
		case TabMyTheatre:
			a.buildMyTheatreTab(c, pal)
		case TabExamStats:
			a.buildExamStatsTab(c, pal)
		case TabFiles:
			a.buildFilesTab(c, pal)
		}
	})
}

func (a *App) buildSeatMapTab(c *ui.Context, pal theme.Palette, students []core.StudentInfo) {
	ui.Scroll(c).Grow(1).Padding(12).Gap(10).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "机房座位拓扑图").FontSize(15).Bold()
			ui.Badge(c, fmt.Sprintf("显示 %d 台", len(students)))
			if ui.Button(c, "自动排座").Padding(3, 10).Clicked() {
				a.autoLayoutSeats()
			}
			ui.Spacer(c)
			ui.Text(c, "提示：单击选中、双击监控，拖动卡片可调换座位").FontSize(11).TextColor(pal.TextMuted)
		})

		if len(students) == 0 {
			ui.Box(c).Padding(24).Center().Children(func() {
				ui.Text(c, "当前筛选条件下暂无学生机在线").TextColor(pal.TextMuted)
			})
			return
		}

		ui.Row(c).Wrap().Gap(12).Children(func() {
			for _, st := range students {
				a.buildSeatCard(c, pal, st)
			}
		})
	})
}

func (a *App) buildSeatCard(c *ui.Context, pal theme.Palette, st core.StudentInfo) {
	selected := a.selectedID == st.ID
	bg := pal.Card
	border := pal.CardBorder
	if selected {
		bg = pal.Selection
		border = pal.Accent
	}

	statusText, statusColor := statusBadge(pal, st)

	card := ui.Column(c).Key("seat-" + st.ID).Width(178).Padding(9).Gap(5).Radius(3).
		Background(bg).Border(1.5, border).Focusable().
		Drag(st.ID).
		Label(fmt.Sprintf("%s %s", st.MachineName, st.StudentNo))

	if draggedID, over := ui.DragOver[string](card); over && draggedID != st.ID {
		card.Border(2, pal.Warning)
	}
	if draggedID, ok := ui.Drop[string](card); ok && draggedID != "" && draggedID != st.ID {
		a.swapStudentSeats(draggedID, st.ID)
	}
	if card.Dragging() {
		card.Opacity(0.55)
	}

	card.ContextMenu(func(m *ui.Menu) {
		if m.Item("监控该学生屏幕").Chosen() {
			a.selectedID = st.ID
			a.monitoring = true
			a.mainTab = TabMonitorWall
		}
		if m.Item("远程控制").Chosen() {
			a.StartRemoteControl(st.ID)
		}
		if m.Item("指定为学生演示").Chosen() {
			a.ToggleStudentDemo(st.ID)
		}
		m.Separator()
		if m.Item("黑屏锁定/解锁").Chosen() {
			a.selectedID = st.ID
			a.ToggleScreenLock()
		}
		if m.Item("远程唤醒 (WOL)").Chosen() {
			a.selectedID = st.ID
			a.ExecutePower(core.PowerWake)
		}
	})

	card.Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Image(c, theme.IconSeat).Size(24, 24)
			ui.Column(c).Grow(1).Gap(1).Children(func() {
				ui.Text(c, st.MachineName).FontSize(13).Bold().SingleLine()
				if st.StudentNo != "" {
					ui.Text(c, "编号 "+st.StudentNo).FontSize(11).TextColor(pal.TextMuted).SingleLine()
				}
			})
			if st.HandRaised {
				ui.Image(c, theme.IconHandRaise).Size(18, 18)
			}
		})

		ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
			ui.Text(c, statusText).FontSize(11).Bold().TextColor(statusColor)
			ui.Spacer(c)
			if st.CheckedIn {
				ui.Text(c, "已签到").FontSize(10).TextColor(pal.Success)
			} else {
				ui.Text(c, "未签到").FontSize(10).TextColor(pal.TextMuted)
			}
		})

		ui.Text(c, fmt.Sprintf("%s · %dms", st.IP, st.RTTMs)).FontSize(10).TextColor(pal.TextMuted).SingleLine()
	})

	if card.Clicked() {
		a.selectedID = st.ID
	}
	if card.DoubleClicked() {
		a.selectedID = st.ID
		a.monitoring = true
		a.mainTab = TabMonitorWall
		a.statusNote = fmt.Sprintf("正在重点监控 %s", st.MachineName)
	}
}

func (a *App) buildMonitorWallTab(c *ui.Context, pal theme.Palette, students []core.StudentInfo) {
	ui.Column(c).Grow(1).Padding(12).Gap(10).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "多画面屏幕监控墙").FontSize(15).Bold()
			ui.Badge(c, fmt.Sprintf("%d FPS · 画质 %d%%", int(a.monitorFPS), int(a.monitorQual)))
			ui.Spacer(c)
			if a.monitoring {
				if ui.Button(c, "停止监控").Clicked() {
					a.monitoring = false
					a.applyMonitorState()
				}
			} else {
				if ui.PrimaryButton(c, "开启监控轮询").Clicked() {
					a.monitoring = true
					a.applyMonitorState()
				}
			}
		})

		ui.Scroll(c).Grow(1).Children(func() {
			ui.Row(c).Wrap().Gap(12).Children(func() {
				for _, st := range students {
					bmp := a.ensureTileBitmap(st.ID, 240, 135)
					border := pal.CardBorder
					if a.selectedID == st.ID {
						border = pal.Accent
					}
					tile := ui.Column(c).Key("mon-" + st.ID).Width(256).Padding(8).Gap(6).Radius(3).
						Background(pal.Card).Border(1.5, border)
					tile.Children(func() {
						ui.Image(c, bmp).Size(240, 135).Fit(ui.Contain).Radius(2)
						ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
							ui.Text(c, st.MachineName).FontSize(12).Bold().Grow(1).SingleLine()
							if ui.Button(c, "远控").Padding(2, 6).Clicked() {
								a.StartRemoteControl(st.ID)
							}
						})
					})
					if tile.Clicked() {
						a.selectedID = st.ID
					}
				}
			})
		})
	})
}

func (a *App) buildMyTheatreTab(c *ui.Context, pal theme.Palette) {
	ui.Column(c).Grow(1).Padding(12).Gap(10).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "网络影院 (MyTheatre)").FontSize(15).Bold()
			ui.Select(c, &a.theatre.Media, a.theatreMedia).Width(260)
			ui.Spacer(c)
			if a.theatre.Playing {
				ui.Badge(c, "正在同步播放").Background(pal.Selection).TextColor(pal.Text)
			} else {
				ui.Badge(c, "就绪")
			}
		})

		// Video stage with subtle "mytheatre" watermark when idle
		ui.Box(c).Grow(1).MinHeight(280).Background(pal.TheatreBg).Radius(3).Center().Clip().Children(func() {
			if a.theatrePlayer != nil {
				if a.theatrePlayer.Ready() {
					a.theatre.Duration = a.theatrePlayer.Duration().Seconds()
					a.theatre.Position = a.theatrePlayer.Position().Seconds()
				}
				ui.Video(c, a.theatrePlayer).Fill().Fit(ui.Contain)
			} else if a.theatreFrame != nil {
				ui.Image(c, a.theatreFrame).Fill().Fit(ui.Contain)
			}
			if a.theatrePlayer == nil && (!a.theatre.Playing || a.theatreFrame == nil) {
				ui.Column(c).Center().Gap(6).Children(func() {
					ui.Text(c, "mytheatre").FontSize(34).Bold().TextColor(ui.RGBA(255, 255, 255, 0.14))
					ui.Text(c, fmt.Sprintf("当前影片：%s", a.theatre.Media)).FontSize(12).TextColor(ui.RGBA(212, 212, 212, 0.7))
				})
			}
		})

		// Playback transport bar
		ui.Column(c).Background(pal.Card).Border(1, pal.CardBorder).Radius(3).Padding(10).Gap(8).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
				playLabel := "播放并分发"
				if a.theatre.Playing {
					playLabel = "暂停同步"
				}
				if ui.PrimaryButton(c, playLabel).Clicked() {
					a.toggleTheatrePlayback()
				}
				if ui.Button(c, "停止").Clicked() {
					a.stopTheatrePlayback()
				}
				if ui.Button(c, "选择本地视频").Clicked() {
					a.openVideoPicker()
				}
				ui.Spacer(c)
				ui.Text(c, fmt.Sprintf("%02d:%02d / %02d:%02d",
					int(a.theatre.Position)/60, int(a.theatre.Position)%60,
					int(a.theatre.Duration)/60, int(a.theatre.Duration)%60,
				)).FontSize(12).TextColor(pal.TextMuted)
			})

			pos := a.theatre.Position
			if ui.Slider(c, &pos, 0, max(1, a.theatre.Duration)).Label("播放进度").Changed() {
				a.theatre.Position = pos
				if a.theatrePlayer != nil {
					a.theatrePlayer.Seek(time.Duration(pos * float64(time.Second)))
				}
				a.sendControlEnvelope(protocol.MsgTheatreControl, a.theatre, nil)
			}
		})
	})
}

func (a *App) buildExamStatsTab(c *ui.Context, pal theme.Palette) {
	var paper *core.ExamPaper
	var stats core.ExamStats
	var subs []core.ExamSubmission
	if a.opts.Exam != nil {
		paper = a.opts.Exam.GetActivePaper()
		if paper != nil {
			stats = a.opts.Exam.ComputeStats(paper.ID)
			type subLister interface {
				ListSubmissions(paperID string) []core.ExamSubmission
			}
			if lister, ok := a.opts.Exam.(subLister); ok {
				subs = lister.ListSubmissions(paper.ID)
			}
		}
	}

	ui.Scroll(c).Grow(1).Padding(12).Gap(12).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Stretch).Children(func() {
			// Left: Quick paper builder & publisher
			ui.Column(c).Grow(1).Background(pal.Card).Border(1, pal.CardBorder).Radius(3).Padding(12).Gap(8).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "发布随堂测验").FontSize(14).Bold()
					ui.Spacer(c)
					ui.Badge(c, fmt.Sprintf("待发布题目: %d", len(a.examDraftQuestions)))
				})
				ui.Text(c, "测验标题").FontSize(12).TextColor(pal.TextMuted)
				ui.TextInput(c, &a.examTitleInput).Placeholder("输入试卷标题")
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Column(c).Width(130).Gap(4).Children(func() {
						ui.Text(c, "题型").FontSize(12).TextColor(pal.TextMuted)
						ui.Select(c, &a.examKindInput, []string{"single", "multi", "bool", "essay"})
					})
					ui.Column(c).Width(110).Gap(4).Children(func() {
						ui.Text(c, "分值").FontSize(12).TextColor(pal.TextMuted)
						ui.NumberInput(c, &a.examPointsInput, 1, 100, 5)
					})
					ui.Column(c).Width(120).Gap(4).Children(func() {
						ui.Text(c, "时长(分钟)").FontSize(12).TextColor(pal.TextMuted)
						ui.NumberInput(c, &a.examDurInput, 1, 120, 1)
					})
				})
				ui.Text(c, "题目内容").FontSize(12).TextColor(pal.TextMuted)
				ui.TextInput(c, &a.examPromptInput).Placeholder("输入题干内容")
				ui.Text(c, "选项 (逗号分隔)").FontSize(12).TextColor(pal.TextMuted)
				ui.TextInput(c, &a.examOptionsInput).Placeholder("例如：A,B,C,D")
				ui.Text(c, "参考答案 (多选用逗号分隔)").FontSize(12).TextColor(pal.TextMuted)
				ui.TextInput(c, &a.examAnswerInput)
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, "添加到试卷").Clicked() {
						a.addDraftQuestion()
					}
					if len(a.examDraftQuestions) > 0 {
						if ui.Button(c, "清空题目").Clicked() {
							a.examDraftQuestions = nil
						}
					}
					if ui.PrimaryButton(c, "下发随堂测验").Clicked() {
						a.publishExam()
					}
					if a.examPublished {
						ui.Text(c, "已下发至全班").FontSize(12).TextColor(pal.Success)
					}
				})
			})

			// Right: Live score & accuracy chart using mygo/ui/chart
			ui.Column(c).Grow(1).Background(pal.Card).Border(1, pal.CardBorder).Radius(3).Padding(12).Gap(8).Children(func() {
				title := "暂无已发布试卷"
				if paper != nil {
					title = paper.Title
				}
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "作答统计与正确率").FontSize(14).Bold()
					ui.Spacer(c)
					ui.Badge(c, fmt.Sprintf("已交 %d 份 · 均分 %.1f", stats.TotalSubmitted, stats.AverageScore))
					if paper != nil {
						if ui.Button(c, "导出CSV").Padding(2, 8).Clicked() {
							a.exportExamCSV(paper.ID)
						}
					}
				})
				ui.Text(c, title).FontSize(12).TextColor(pal.TextMuted)

				if paper != nil && len(paper.Questions) > 0 {
					labels := make([]string, len(paper.Questions))
					values := make([]float64, len(paper.Questions))
					for i, q := range paper.Questions {
						labels[i] = fmt.Sprintf("Q%d", i+1)
						values[i] = stats.QuestionRates[q.ID] * 100
					}
					xScale := chart.NewBand(labels...).Padding(0.3, 0.15)
					yScale := chart.NewLinear(0, 100).Nice(5)
					chart.Plot(c, chart.Axes{
						Left:   yScale.Ticks(5),
						Bottom: xScale.Ticks(),
					}, func(area ui.Element) {
						area.Background(pal.Canvas).Border(1, pal.CardBorder).Radius(2)
						for i, v := range values {
							barColor := pal.Accent
							if v < 60 {
								barColor = pal.TextMuted
							}
							chart.Rect(c, xScale.Band(i), yScale.Span(0, v)).Background(barColor).Radius(2)
						}
					}).Height(190).Gap(8).Label("随堂测验正确率图表")
				}

				if len(subs) > 0 {
					ui.Text(c, "学生答卷明细").FontSize(12).Bold()
					for _, sub := range subs {
						ui.Row(c).Key("sub-" + sub.StudentID).Padding(4, 8).Background(pal.Canvas).Border(1, pal.CardBorder).Radius(2).AlignItems(ui.Center).Children(func() {
							ui.Text(c, sub.StudentID).FontSize(12).Grow(1)
							ui.Text(c, fmt.Sprintf("%d / %d 分", sub.Score, sub.MaxScore)).FontSize(12).Bold()
						})
					}
				}
			})
		})
	})
}

func (a *App) buildFilesTab(c *ui.Context, pal theme.Palette) {
	var tasks []*core.TransferTask
	if a.opts.Files != nil {
		tasks = a.opts.Files.ListTasks()
	}

	ui.Column(c).Grow(1).Padding(12).Gap(10).Children(func() {
		ui.Column(c).Background(pal.Card).Border(1, pal.CardBorder).Radius(3).Padding(12).Gap(8).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "文件分发与作业收集").FontSize(14).Bold()
				ui.Spacer(c)
				ui.Checkbox(c, &a.fileTargetAll, "发送/收取全部在线学生 (取消则仅选中学生)")
			})
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.TextInput(c, &a.distributePath).Placeholder("本地分发文件路径").Grow(1)
				if ui.Button(c, "选择文件").Clicked() {
					a.openDistributeFilePicker()
				}
				ui.TextInput(c, &a.remoteDir).Placeholder("学生机接收目录").Width(180)
				ui.Checkbox(c, &a.autoOpenFile, "到达后自动打开")
				if ui.PrimaryButton(c, "发送文件").Clicked() {
					a.DistributeFile()
				}
			})
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.TextInput(c, &a.collectSaveDir).Placeholder("作业回收保存目录").Grow(1)
				if ui.Button(c, "选择目录").Clicked() {
					a.openCollectDirPicker()
				}
				if ui.Button(c, "一键收取全班作业").Clicked() {
					a.CollectHomework()
				}
			})
		})

		ui.Text(c, fmt.Sprintf("传输任务队列 (%d)", len(tasks))).FontSize(13).Bold()
		ui.Scroll(c).Grow(1).Gap(6).Children(func() {
			for _, tk := range tasks {
				ratio := 0.0
				if tk.TotalBytes > 0 {
					ratio = float64(tk.DoneBytes) / float64(tk.TotalBytes)
				}
				ui.Column(c).Key("task-" + tk.ID).Background(pal.Card).Border(1, pal.CardBorder).Radius(3).Padding(10).Gap(6).Children(func() {
					ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
						ui.Badge(c, string(tk.Direction))
						ui.Text(c, tk.FileName).FontSize(13).Bold().Grow(1).SingleLine()
						ui.Text(c, fmt.Sprintf("%s · %.0f%%", tk.Status, ratio*100)).FontSize(11).TextColor(pal.TextMuted)
					})
					ui.Progress(c, ratio).Height(6)
					ui.Text(c, fmt.Sprintf("%s -> %s", tk.LocalPath, tk.RemotePath)).FontSize(11).TextColor(pal.TextMuted).SingleLine()
				})
			}
		})
	})
}

func (a *App) buildRightSidebar(c *ui.Context, pal theme.Palette, students []core.StudentInfo) {
	ui.Column(c).Fill().Background(pal.ChromeRail).BorderWidth(0, 0, 0, 1).BorderColor(pal.CardBorder).Children(func() {
		ui.Row(c).Padding(6, 8).Background(pal.ChromeBar).BorderWidth(0, 0, 1, 0).BorderColor(pal.CardBorder).Children(func() {
			ui.Segmented(c, &a.sideTab, "学生列表", "消息/举手", "管控策略").FillEqually()
		})

		ui.Column(c).Grow(1).Padding(8).Gap(8).Children(func() {
			switch a.sideTab {
			case SideTabStudents:
				a.buildSideStudents(c, pal, students)
			case SideTabChatQueue:
				a.buildSideChatQueue(c, pal, students)
			case SideTabPolicy:
				a.buildSidePolicy(c, pal)
			}
		})

		ui.Divider(c)
		// Fixed bottom volume sliders for the right panel
		ui.Column(c).Background(pal.ChromeBar).Padding(10).Gap(8).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "广播/影院音量").FontSize(12).Bold()
				ui.Spacer(c)
				ui.Text(c, fmt.Sprintf("%d%%", int(a.volume))).FontSize(12).TextColor(pal.Accent)
			})
			if ui.Slider(c, &a.volume, 0, 100).Label("音量滑块").Changed() {
				if a.opts.Media != nil {
					a.opts.Media.SetVolume(float32(a.volume / 100.0))
				}
				if a.theatrePlayer != nil {
					a.theatrePlayer.SetVolume(a.volume / 100.0)
				}
				a.theatre.Volume = float32(a.volume / 100.0)
			}

			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "监控画质 / 帧率").FontSize(12).Bold()
				ui.Spacer(c)
				ui.Text(c, fmt.Sprintf("%d%% · %d FPS", int(a.monitorQual), int(a.monitorFPS))).FontSize(11).TextColor(pal.Accent)
			})
			if ui.Slider(c, &a.monitorQual, 30, 95).Label("监控画质滑块").Changed() && a.monitoring {
				a.sendMonitorConfig()
			}
			if ui.Slider(c, &a.monitorFPS, 5, 30).Label("监控帧率滑块").Changed() && a.monitoring {
				a.sendMonitorConfig()
			}
		})
	})
}

func (a *App) buildSideStudents(c *ui.Context, pal theme.Palette, students []core.StudentInfo) {
	cols := []ui.TableColumn{
		{Title: "机位", ID: "machine", Width: 64},
		{Title: "学号", ID: "student_no", Width: 76},
		{Title: "状态", ID: "status", Width: 70},
		{Title: "延迟", ID: "rtt", Align: ui.End},
	}
	tbl := ui.Table(c, &a.studentTable, cols, len(students), func(row, col int) {
		st := students[row]
		switch col {
		case 0:
			ui.Text(c, st.MachineName).FontSize(12).SingleLine()
		case 1:
			ui.Text(c, st.StudentNo).FontSize(12).SingleLine()
		case 2:
			txt, colr := statusBadge(pal, st)
			ui.Text(c, txt).FontSize(11).TextColor(colr).SingleLine()
		case 3:
			ui.Text(c, fmt.Sprintf("%dms", st.RTTMs)).FontSize(11).TextColor(pal.TextMuted)
		}
	}).Grow(1)

	if tbl.Changed() && a.selectedRow >= 0 && a.selectedRow < len(students) {
		a.selectedID = students[a.selectedRow].ID
	}
	if tbl.Submitted() && a.selectedRow >= 0 && a.selectedRow < len(students) {
		a.StartRemoteControl(students[a.selectedRow].ID)
	}
}

func (a *App) buildSideChatQueue(c *ui.Context, pal theme.Palette, students []core.StudentInfo) {
	var raised []core.StudentInfo
	for _, st := range students {
		if st.HandRaised {
			raised = append(raised, st)
		}
	}

	ui.Column(c).Background(pal.Card).Border(1, pal.CardBorder).Radius(3).Padding(8).Gap(6).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "举手队列").FontSize(12).Bold()
			ui.Spacer(c)
			ui.Badge(c, fmt.Sprintf("%d 人举手", len(raised)))
		})
		if len(raised) == 0 {
			ui.Text(c, "暂无学生举手").FontSize(11).TextColor(pal.TextMuted)
		} else {
			for _, st := range raised {
				ui.Row(c).Key("hand-" + st.ID).AlignItems(ui.Center).Gap(6).Children(func() {
					ui.Image(c, theme.IconHandRaise).Size(16, 16)
					ui.Text(c, fmt.Sprintf("%s (%s)", st.MachineName, st.StudentNo)).FontSize(12).Grow(1).SingleLine()
					if ui.PrimaryButton(c, "批准演示").Padding(2, 8).Clicked() {
						a.ApproveHandRaise(st.ID)
					}
				})
			}
		}
	})

	var chats []core.ChatMessage
	if a.opts.Classroom != nil {
		chats = a.opts.Classroom.ListChats()
	}

	ui.Scroll(c).Grow(1).Gap(6).Children(func() {
		for _, msg := range chats {
			ui.Column(c).Key("chat-" + msg.ID).Background(pal.Card).Border(1, pal.CardBorder).Radius(3).Padding(8).Gap(2).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					ui.Text(c, msg.Sender).FontSize(11).Bold().TextColor(pal.Accent)
					ui.Spacer(c)
					ui.Text(c, msg.CreatedAt.Format("15:04")).FontSize(10).TextColor(pal.TextMuted)
				})
				ui.Text(c, msg.Text).FontSize(12)
			})
		}
	})

	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		input := ui.TextInput(c, &a.chatInput).Placeholder("发送课堂消息...").Grow(1)
		if input.Submitted() || ui.PrimaryButton(c, "发送").Clicked() {
			a.SendChatMessage()
		}
	})
}

func (a *App) buildSidePolicy(c *ui.Context, pal theme.Palette) {
	ui.Scroll(c).Grow(1).Gap(8).Children(func() {
		ui.Text(c, "课堂管控策略").FontSize(13).Bold()
		ui.Select(c, &a.policyMode, []string{"blacklist", "whitelist"})
		ui.Text(c, "禁止运行进程 (逗号分隔)").FontSize(11).TextColor(pal.TextMuted)
		ui.TextInput(c, &a.blockedAppsInput)
		ui.Text(c, "允许运行进程 (白名单模式)").FontSize(11).TextColor(pal.TextMuted)
		ui.TextInput(c, &a.allowedAppsInput)
		ui.Text(c, "禁用网址域名").FontSize(11).TextColor(pal.TextMuted)
		ui.TextInput(c, &a.blockedSitesInput)
		ui.Text(c, "黑屏锁定提示语").FontSize(11).TextColor(pal.TextMuted)
		ui.TextInput(c, &a.lockMessage)
		if ui.PrimaryButton(c, "下发管控策略").FillWidth().Clicked() {
			a.ApplyClassroomPolicy()
		}
	})
}

func (a *App) buildStatusBar(c *ui.Context, pal theme.Palette) {
	all := a.listStudents()
	online, checkedIn := 0, 0
	for _, st := range all {
		if st.Status != core.StatusOffline {
			online++
		}
		if st.CheckedIn {
			checkedIn++
		}
	}
	var stats core.TransportStats
	if a.opts.Transport != nil {
		stats = a.opts.Transport.Stats()
	}

	ui.Row(c).Background(pal.StatusBarBg).Padding(4, 12).Gap(14).AlignItems(ui.Center).Children(func() {
		ui.Text(c, fmt.Sprintf("在线: %d/%d", online, len(all))).FontSize(11).Bold().TextColor(pal.StatusBarText)
		ui.Text(c, fmt.Sprintf("已签到: %d", checkedIn)).FontSize(11).TextColor(pal.StatusBarText)
		ui.Text(c, fmt.Sprintf("频道: %s", a.channel)).FontSize(11).TextColor(pal.StatusBarText)
		ui.Text(c, fmt.Sprintf("当前模式: %s", a.activeModeLabel)).FontSize(11).Bold().TextColor(pal.Accent)
		ui.Spacer(c)
		ui.Text(c, a.statusNote).FontSize(11).TextColor(pal.TextMuted).SingleLine()
		ui.Text(c, fmt.Sprintf("KCP 会话:%d | Tx:%dKB/s | 丢包:%.2f%%",
			stats.ActiveSessions, stats.TxBytesPerSec/1024, stats.PacketLossRate*100)).FontSize(11).TextColor(pal.StatusBarText)
	})
}

func (a *App) buildSettingsModal(c *ui.Context, pal theme.Palette) {
	ui.Column(c).Width(420).Background(pal.Canvas).Border(1, pal.CardBorder).Radius(4).Padding(16).Gap(10).Children(func() {
		ui.Text(c, "教师端课堂设置").FontSize(16).Bold()
		ui.Text(c, "教室频道名").FontSize(12).TextColor(pal.TextMuted)
		ui.TextInput(c, &a.channel)
		ui.Checkbox(c, &a.useMulticast, "优先使用 UDP 组播 + FEC 广播画面")
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "关闭").Clicked() {
				a.settingsOpen = false
			}
			if ui.PrimaryButton(c, "保存并应用").Clicked() {
				a.settingsOpen = false
				a.statusNote = "课堂通道设置已更新: " + a.channel
			}
		})
	})
}

func (a *App) buildRemoteControlModal(c *ui.Context, pal theme.Palette) {
	targetLabel := a.remoteControlID
	for _, st := range a.listStudents() {
		if st.ID == a.remoteControlID {
			targetLabel = fmt.Sprintf("%s (%s · %s)", st.MachineName, st.StudentNo, st.IP)
			break
		}
	}

	ui.Column(c).Width(880).Height(580).Background(pal.Canvas).Border(1, pal.CardBorder).Radius(4).Padding(12).Gap(8).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "远程控制: "+targetLabel).FontSize(14).Bold()
			ui.Badge(c, "实时交互").Background(pal.Selection).TextColor(pal.Text)
			ui.Spacer(c)
			if ui.Button(c, "发送 Esc").Padding(3, 10).Clicked() {
				a.sendRemoteInput(core.RemoteInputEvent{Kind: core.InputKeyDown, VKCode: 0x1B})
				a.sendRemoteInput(core.RemoteInputEvent{Kind: core.InputKeyUp, VKCode: 0x1B})
			}
			if ui.PrimaryButton(c, "退出远程控制").Padding(4, 12).Clicked() {
				a.StopRemoteControl()
			}
		})

		a.mu.Lock()
		bmp := a.remoteBitmap
		if bmp == nil {
			bmp = a.monitorTiles[a.remoteControlID]
		}
		a.mu.Unlock()

		stage := ui.Box(c).Grow(1).FillWidth().Background(pal.TheatreBg).Border(1, pal.CardBorder).Radius(2).Center().Focusable().Clip()
		const stageW, stageH = 856.0, 490.0
		stage.HandleInput(func(ev ui.InputEvent) bool {
			nx := float64(ev.X) / stageW
			ny := float64(ev.Y) / stageH
			if b := stage.Bounds(); b.W > 0 && b.H > 0 {
				nx = float64(ev.X) / float64(b.W)
				ny = float64(ev.Y) / float64(b.H)
			}
			if nx < 0 {
				nx = 0
			} else if nx > 1 {
				nx = 1
			}
			if ny < 0 {
				ny = 0
			} else if ny > 1 {
				ny = 1
			}

			switch ev.Kind {
			case ui.InputPointerMove:
				a.sendRemoteInput(core.RemoteInputEvent{Kind: core.InputMouseMove, X: nx, Y: ny})
				return true
			case ui.InputPointerDown:
				a.sendRemoteInput(core.RemoteInputEvent{Kind: core.InputMouseDown, X: nx, Y: ny, Button: mapUIButton(ev.Button)})
				return true
			case ui.InputPointerUp:
				a.sendRemoteInput(core.RemoteInputEvent{Kind: core.InputMouseUp, X: nx, Y: ny, Button: mapUIButton(ev.Button)})
				return true
			case ui.InputScroll:
				a.sendRemoteInput(core.RemoteInputEvent{Kind: core.InputMouseWheel, Delta: int(-ev.DY * 120)})
				return true
			case ui.InputKeyDown:
				if vk := uiKeyToVK(ev.Key); vk != 0 {
					a.sendRemoteInput(core.RemoteInputEvent{Kind: core.InputKeyDown, VKCode: vk})
					return true
				}
			case ui.InputKeyUp:
				if vk := uiKeyToVK(ev.Key); vk != 0 {
					a.sendRemoteInput(core.RemoteInputEvent{Kind: core.InputKeyUp, VKCode: vk})
					return true
				}
			}
			return false
		})
		stage.Children(func() {
			if bmp != nil {
				ui.Image(c, bmp).Fill().Fit(ui.Contain)
			} else {
				ui.Text(c, "正在请求学生机实时画面...").FontSize(13).TextColor(ui.Hex("#D4D4D4"))
			}
		})
	})
}

// Action methods exported for menu bar, toolbar, and tests.

func (a *App) ToggleBroadcast() {
	a.broadcasting = !a.broadcasting
	a.applyBroadcastState()
}

func (a *App) applyBroadcastState() {
	a.syncRuntimeFlags()
	if a.broadcasting {
		cfg := a.CurrentBroadcastConfig()
		a.activeModeLabel = "屏幕广播"
		a.statusNote = fmt.Sprintf("正在向全班广播教师屏幕 (%s)", cfg.Mode)
		a.sendControlEnvelope(protocol.MsgBroadcastStart, cfg, nil)
	} else {
		a.activeModeLabel = "待机监听"
		a.statusNote = "已停止屏幕广播"
		a.sendControlEnvelope(protocol.MsgBroadcastStop, nil, nil)
	}
}

func (a *App) CurrentBroadcastConfig() core.BroadcastConfig {
	mode := core.BroadcastFullScreen
	if a.broadcastWindowedFlag.Load() {
		mode = core.BroadcastWindowed
	}
	qual := int(a.qualValue.Load())
	if qual <= 0 {
		qual = 75
	}
	return core.BroadcastConfig{
		Mode:         mode,
		WithAudio:    true,
		FPS:          24,
		Quality:      qual,
		UseMulticast: a.mcastFlag.Load(),
		SourceID:     a.teacherID,
	}
}

func (a *App) ToggleVoice() {
	a.voiceActive = !a.voiceActive
	a.applyVoiceState()
}

func (a *App) applyVoiceState() {
	a.syncRuntimeFlags()
	if a.voiceActive {
		a.activeModeLabel = "语音广播"
		a.statusNote = "全班语音广播开启中"
		a.sendControlEnvelope(protocol.MsgVoiceControl, map[string]any{"enabled": true, "volume": a.volume / 100.0}, nil)
	} else {
		a.activeModeLabel = "待机监听"
		a.statusNote = "语音广播已关闭"
		a.sendControlEnvelope(protocol.MsgVoiceControl, map[string]any{"enabled": false}, nil)
	}
}

func (a *App) ToggleStudentDemo(studentID string) {
	if studentID == "" {
		students := a.listStudents()
		if len(students) > 0 {
			studentID = students[0].ID
		}
	}
	if a.studentDemoID != "" {
		prev := a.studentDemoID
		a.studentDemoID = ""
		a.activeModeLabel = "待机监听"
		a.statusNote = "已结束学生演示"
		a.sendControlEnvelope(protocol.MsgStudentDemoReq, map[string]any{"active": false, "student_id": prev}, []string{prev})
		a.sendControlEnvelope(protocol.MsgBroadcastStop, nil, nil)
		return
	}
	if studentID == "" {
		a.statusNote = "暂无在线学生可用于学生演示"
		return
	}
	a.studentDemoID = studentID
	a.activeModeLabel = "学生演示"
	a.statusNote = "正在全班转播学生演示: " + studentID
	a.sendControlEnvelope(protocol.MsgStudentDemoReq, map[string]any{"active": true, "student_id": studentID}, []string{studentID})
	var viewers []string
	for _, st := range a.listStudents() {
		if st.ID != studentID && st.Status != core.StatusOffline {
			viewers = append(viewers, st.ID)
		}
	}
	if len(viewers) > 0 {
		a.sendControlEnvelope(protocol.MsgBroadcastStart, core.BroadcastConfig{
			Mode:         core.BroadcastWindowed,
			WithAudio:    false,
			FPS:          15,
			Quality:      int(a.monitorQual),
			UseMulticast: a.useMulticast,
			SourceID:     studentID,
		}, viewers)
	}
}

func (a *App) ToggleMonitor() {
	a.monitoring = !a.monitoring
	a.applyMonitorState()
}

func (a *App) applyMonitorState() {
	if a.monitoring {
		a.mainTab = TabMonitorWall
		a.activeModeLabel = "屏幕监控"
		a.statusNote = fmt.Sprintf("屏幕监控墙运行中 (%d FPS, 画质 %d%%)", int(a.monitorFPS), int(a.monitorQual))
	} else {
		a.activeModeLabel = "待机监听"
		a.statusNote = "已停止屏幕监控"
	}
	a.sendMonitorConfig()
}

func (a *App) sendMonitorConfig() {
	fps := int(a.monitorFPS)
	if !a.monitoring {
		fps = 0
	}
	a.sendControlEnvelope(protocol.MsgMonitorControl, struct {
		Active bool `json:"active"`
		core.MonitorConfig
	}{
		Active: a.monitoring,
		MonitorConfig: core.MonitorConfig{
			FPS:     fps,
			Quality: int(a.monitorQual),
			Width:   320,
			Height:  180,
		},
	}, nil)
}

func (a *App) StartRemoteControl(studentID string) {
	if studentID == "" {
		a.statusNote = "请先在座位图或列表中选择一台学生机"
		return
	}
	if a.remoteControlID != "" && a.remoteControlID != studentID {
		a.sendControlEnvelope(protocol.MsgRemoteControlReq, map[string]any{"student_id": a.remoteControlID, "enabled": false}, []string{a.remoteControlID})
	}
	a.remoteControlID = studentID
	a.remoteControlOpen = true
	a.activeModeLabel = "远程控制"
	a.statusNote = "已发起远程控制请求 -> " + studentID
	a.sendControlEnvelope(protocol.MsgRemoteControlReq, map[string]any{"student_id": studentID, "enabled": true}, []string{studentID})
}

func (a *App) StopRemoteControl() {
	if a.remoteControlID != "" {
		a.sendControlEnvelope(protocol.MsgRemoteControlReq, map[string]any{"student_id": a.remoteControlID, "enabled": false}, []string{a.remoteControlID})
	}
	a.remoteControlID = ""
	a.remoteControlOpen = false
	a.activeModeLabel = "待机监听"
	a.statusNote = "已结束远程控制"
}

func (a *App) sendRemoteInput(ev core.RemoteInputEvent) {
	if a.remoteControlID == "" {
		return
	}
	a.sendControlEnvelope(protocol.MsgRemoteInputEvent, ev, []string{a.remoteControlID})
}

func (a *App) ToggleScreenLock() {
	a.screenLocked = !a.screenLocked
	a.applyScreenLock()
}

func (a *App) applyScreenLock() {
	if a.screenLocked {
		a.activeModeLabel = "黑屏锁屏"
		a.statusNote = "已对目标学生执行黑屏肃静锁定"
	} else {
		a.activeModeLabel = "待机监听"
		a.statusNote = "已解除学生端屏幕锁定"
	}
	var targets []string
	if a.selectedID != "" {
		targets = []string{a.selectedID}
	}
	a.sendControlEnvelope(protocol.MsgScreenLock, map[string]any{
		"locked":  a.screenLocked,
		"message": a.lockMessage,
	}, targets)
}

func (a *App) StartCheckIn() {
	if a.opts.Classroom != nil {
		a.opts.Classroom.StartCheckIn()
	}
	a.statusNote = "已发起课堂点名签到，等待学生提交"
	a.sendControlEnvelope(protocol.MsgCheckInStart, map[string]any{"channel": a.channel}, nil)
}

func (a *App) ApproveHandRaise(studentID string) {
	if a.opts.Classroom != nil {
		a.opts.Classroom.RaiseHand(studentID, false)
	}
	a.sendControlEnvelope(protocol.MsgHandClear, map[string]any{"student_id": studentID, "approved": true}, []string{studentID})
	a.studentDemoID = ""
	a.ToggleStudentDemo(studentID)
	a.statusNote = "已批准举手学生演示: " + studentID
}

func (a *App) clearAllHands() {
	for _, st := range a.listStudents() {
		if st.HandRaised && a.opts.Classroom != nil {
			a.opts.Classroom.RaiseHand(st.ID, false)
		}
	}
	a.statusNote = "已清空全部举手状态"
}

func (a *App) SendChatMessage() {
	text := strings.TrimSpace(a.chatInput)
	if text == "" {
		return
	}
	if a.opts.Classroom != nil {
		a.opts.Classroom.AddChat(core.ChatMessage{
			SenderID:  a.teacherID,
			Sender:    a.teacherName,
			Text:      text,
			CreatedAt: time.Now(),
		})
	}
	a.sendControlEnvelope(protocol.MsgChat, core.ChatMessage{
		SenderID: a.teacherID,
		Sender:   a.teacherName,
		Text:     text,
	}, nil)
	a.chatInput = ""
	a.statusNote = "已发送课堂公告消息"
}

func (a *App) ApplyClassroomPolicy() {
	rule := core.PolicyRule{
		Mode:           a.policyMode,
		BlockedApps:    splitCSV(a.blockedAppsInput),
		AllowedApps:    splitCSV(a.allowedAppsInput),
		BlockedDomains: splitCSV(a.blockedSitesInput),
		AllowedDomains: splitCSV(a.allowedSitesInput),
		LockScreen:     a.screenLocked,
		LockMessage:    a.lockMessage,
	}
	if a.opts.Control != nil {
		_ = a.opts.Control.ApplyPolicy(rule)
	}
	a.sendControlEnvelope(protocol.MsgPolicySync, rule, nil)
	a.statusNote = "管控策略已同步至学生端 (" + a.policyMode + ")"
}

func (a *App) ExecutePower(action core.PowerAction) {
	var targets []string
	mac := ""
	if a.selectedID != "" {
		targets = []string{a.selectedID}
		for _, st := range a.listStudents() {
			if st.ID == a.selectedID {
				mac = st.MAC
				break
			}
		}
	}
	if a.opts.Control != nil {
		_ = a.opts.Control.ExecutePower(action, mac)
	}
	a.sendControlEnvelope(protocol.MsgPowerCommand, map[string]any{"action": action, "mac": mac}, targets)
	a.statusNote = fmt.Sprintf("已发送电源指令: %s", action)
}

func (a *App) DistributeFile() {
	if a.opts.Files == nil {
		return
	}
	targets := a.resolveFileTargets()
	if len(targets) == 0 {
		a.statusNote = "没有可分发的在线学生目标"
		return
	}
	localPath := a.distributePath
	remoteDir := a.remoteDir
	autoOpen := a.autoOpenFile
	a.statusNote = "正在分发文件..."
	go func() {
		task, err := a.opts.Files.DistributeFile(context.Background(), localPath, remoteDir, targets, autoOpen)
		a.mu.Lock()
		if err != nil {
			a.statusNote = "文件分发失败: " + err.Error()
		} else {
			a.statusNote = "文件已分发: " + task.FileName
		}
		a.mu.Unlock()
		a.Invalidate()
	}()
}

func (a *App) CollectHomework() {
	if a.opts.Files == nil {
		return
	}
	targets := a.resolveFileTargets()
	if len(targets) == 0 {
		a.statusNote = "没有可收取作业的在线学生目标"
		return
	}
	remoteDir := a.remoteDir
	saveDir := a.collectSaveDir
	a.mainTab = TabFiles
	a.statusNote = "正在收取学生作业..."
	go func() {
		tasks, err := a.opts.Files.CollectFiles(context.Background(), remoteDir, saveDir, targets)
		a.mu.Lock()
		if err != nil {
			a.statusNote = "收取作业失败: " + err.Error()
		} else {
			a.statusNote = fmt.Sprintf("作业回收完成 (%d 个文件)", len(tasks))
		}
		a.mu.Unlock()
		a.Invalidate()
	}()
}

func (a *App) resolveFileTargets() []string {
	if !a.fileTargetAll && a.selectedID != "" {
		return []string{a.selectedID}
	}
	var targets []string
	for _, st := range a.listStudents() {
		if st.Status != core.StatusOffline {
			targets = append(targets, st.ID)
		}
	}
	if len(targets) == 0 && a.selectedID != "" {
		targets = []string{a.selectedID}
	}
	return targets
}

func (a *App) autoLayoutSeats() {
	type autoLayouter interface {
		AutoLayoutSeats(rows, cols int)
	}
	if al, ok := a.opts.Classroom.(autoLayouter); ok {
		al.AutoLayoutSeats(0, 4)
		a.statusNote = "已按学号自动排列机房座位"
	}
}

func (a *App) swapStudentSeats(idA, idB string) {
	if a.opts.Classroom == nil {
		return
	}
	var stuA, stuB *core.StudentInfo
	for _, s := range a.opts.Classroom.ListStudents() {
		sCopy := s
		if s.ID == idA {
			stuA = &sCopy
		} else if s.ID == idB {
			stuB = &sCopy
		}
	}
	if stuA == nil || stuB == nil {
		return
	}
	a.opts.Classroom.UpdateSeat(stuA.ID, stuB.Seat)
	a.opts.Classroom.UpdateSeat(stuB.ID, stuA.Seat)
	a.statusNote = fmt.Sprintf("已调换座位: %s ↔ %s", stuA.MachineName, stuB.MachineName)
}

func (a *App) publishExam() {
	if a.opts.Exam == nil {
		return
	}
	questions := append([]core.ExamQuestion(nil), a.examDraftQuestions...)
	if len(questions) == 0 {
		questions = []core.ExamQuestion{a.buildCurrentDraftQuestion("q1")}
	}
	paper := core.ExamPaper{
		ID:          fmt.Sprintf("exam-%d", time.Now().Unix()),
		Title:       a.examTitleInput,
		DurationSec: int(a.examDurInput) * 60,
		Questions:   questions,
	}
	a.opts.Exam.PublishPaper(paper)
	a.examPublished = true
	a.sendControlEnvelope(protocol.MsgExamPublish, paper, nil)
	a.statusNote = fmt.Sprintf("已发布随堂测验: %s (%d 题)", paper.Title, len(questions))
}

func (a *App) buildCurrentDraftQuestion(id string) core.ExamQuestion {
	kind := core.QuestionKind(a.examKindInput)
	if kind == "" {
		kind = core.QuestionSingle
	}
	pts := int(a.examPointsInput)
	if pts <= 0 {
		pts = 20
	}
	var opts []string
	if kind != core.QuestionEssay {
		opts = splitCSV(a.examOptionsInput)
	}
	ans := splitCSV(a.examAnswerInput)
	return core.ExamQuestion{
		ID:      id,
		Kind:    kind,
		Prompt:  a.examPromptInput,
		Options: opts,
		Answer:  ans,
		Points:  pts,
	}
}

func (a *App) addDraftQuestion() {
	id := fmt.Sprintf("q%d", len(a.examDraftQuestions)+1)
	q := a.buildCurrentDraftQuestion(id)
	if strings.TrimSpace(q.Prompt) == "" {
		a.statusNote = "请先填写题干内容"
		return
	}
	a.examDraftQuestions = append(a.examDraftQuestions, q)
	a.statusNote = fmt.Sprintf("已添加第 %d 题到待发布试卷", len(a.examDraftQuestions))
}

func (a *App) exportExamCSV(paperID string) {
	type csvExporter interface {
		ExportResultsCSV(paperID string) ([]byte, error)
	}
	exp, ok := a.opts.Exam.(csvExporter)
	if !ok {
		return
	}
	data, err := exp.ExportResultsCSV(paperID)
	if err != nil {
		a.statusNote = "导出成绩失败: " + err.Error()
		return
	}
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
			Title:       "导出随堂测验成绩 (CSV)",
			DefaultPath: paperID + "_results.csv",
			Filters:     []mygo.FileFilter{{Name: "CSV 表格", Extensions: []string{"csv"}}},
		})
		if err != nil || path == "" {
			return
		}
		note := "成绩已导出: " + path
		if werr := os.WriteFile(path, data, 0o644); werr != nil {
			note = "导出写入失败: " + werr.Error()
		}
		a.mu.Lock()
		a.statusNote = note
		a.mu.Unlock()
		a.Invalidate()
	}()
}

func (a *App) toggleTheatrePlayback() {
	a.theatre.Playing = !a.theatre.Playing
	if a.theatrePlayer != nil {
		if a.theatre.Playing {
			a.theatrePlayer.Play()
		} else {
			a.theatrePlayer.Pause()
		}
	}
	if a.theatre.Playing {
		a.activeModeLabel = "网络影院"
		a.statusNote = "MyTheatre 正在同步播放: " + a.theatre.Media
		a.sendControlEnvelope(protocol.MsgBroadcastStart, core.BroadcastConfig{
			Mode:         core.BroadcastWindowed,
			WithAudio:    true,
			FPS:          20,
			Quality:      int(a.monitorQual),
			UseMulticast: a.useMulticast,
			SourceID:     a.teacherID,
		}, nil)
	} else {
		a.activeModeLabel = "待机监听"
		a.statusNote = "MyTheatre 已暂停"
		a.sendControlEnvelope(protocol.MsgBroadcastStop, nil, nil)
	}
	a.sendControlEnvelope(protocol.MsgTheatreControl, a.theatre, nil)
}

func (a *App) stopTheatrePlayback() {
	a.theatre.Playing = false
	a.theatre.Position = 0
	if a.theatrePlayer != nil {
		a.theatrePlayer.Pause()
		a.theatrePlayer.Seek(0)
	}
	a.activeModeLabel = "待机监听"
	a.statusNote = "MyTheatre 已停止"
	a.sendControlEnvelope(protocol.MsgBroadcastStop, nil, nil)
	a.sendControlEnvelope(protocol.MsgTheatreControl, a.theatre, nil)
}

func (a *App) openDistributeFilePicker() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Title: "选择要分发给学生的教学文件",
		})
		if err != nil || len(paths) == 0 {
			return
		}
		a.mu.Lock()
		a.distributePath = paths[0]
		a.mu.Unlock()
		a.Invalidate()
	}()
}

func (a *App) openCollectDirPicker() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Title:     "选择作业回收保存目录",
			Directory: true,
		})
		if err != nil || len(paths) == 0 {
			return
		}
		a.mu.Lock()
		a.collectSaveDir = paths[0]
		a.mu.Unlock()
		a.Invalidate()
	}()
}

func (a *App) openVideoPicker() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Title: "选择网络影院教学视频",
			Filters: []mygo.FileFilter{
				{Name: "视频文件", Extensions: []string{"mp4", "mkv", "avi", "mov"}},
			},
		})
		if err != nil || len(paths) == 0 {
			return
		}
		path := paths[0]
		if a.win != nil {
			a.win.Update(func() {
				a.loadLocalVideo(path)
			})
		} else {
			a.loadLocalVideo(path)
		}
	}()
}

func (a *App) loadLocalVideo(path string) {
	if a.theatrePlayer != nil {
		a.theatrePlayer.Close()
		a.theatrePlayer = nil
	}
	p, err := video.Open(path)
	if err != nil {
		a.theatre.Media = path
		a.statusNote = "视频打开失败 (改用屏幕演示): " + err.Error()
		return
	}
	p.SetVolume(a.volume / 100.0)
	a.theatrePlayer = p
	a.theatre.Media = path
	a.theatre.Playing = true
	a.activeModeLabel = "网络影院"
	a.statusNote = "MyTheatre 正在播放: " + path
	a.sendControlEnvelope(protocol.MsgBroadcastStart, core.BroadcastConfig{
		Mode:         core.BroadcastWindowed,
		WithAudio:    true,
		FPS:          20,
		Quality:      int(a.monitorQual),
		UseMulticast: a.useMulticast,
		SourceID:     a.teacherID,
	}, nil)
	a.sendControlEnvelope(protocol.MsgTheatreControl, a.theatre, nil)
}

func (a *App) sendControlEnvelope(msgType protocol.MsgType, payload any, targetIDs []string) {
	if a.opts.Transport == nil {
		return
	}
	env, err := protocol.NewEnvelope(msgType, a.teacherID, payload)
	if err != nil {
		return
	}
	env.Channel = a.channel
	env.Timestamp = time.Now().UnixMilli()
	var targets []string
	if len(targetIDs) > 0 {
		targets = append([]string(nil), targetIDs...)
	}
	if msgType == protocol.MsgRemoteInputEvent {
		select {
		case a.ctrlCh <- ctrlOutbound{env: env, targetIDs: targets}:
		default:
		}
		return
	}
	if a.ctrlCh == nil {
		_ = a.opts.Transport.BroadcastControl(env, targets)
		return
	}
	select {
	case a.ctrlCh <- ctrlOutbound{env: env, targetIDs: targets}:
	default:
		go func() {
			_ = a.opts.Transport.BroadcastControl(env, targets)
		}()
	}
}

func (a *App) controlSendWorker() {
	for item := range a.ctrlCh {
		if a.opts.Transport != nil {
			_ = a.opts.Transport.BroadcastControl(item.env, item.targetIDs)
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

func (a *App) listGroups() []core.GroupInfo {
	if a.opts.Classroom == nil {
		return nil
	}
	return a.opts.Classroom.ListGroups()
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

func statusBadge(pal theme.Palette, st core.StudentInfo) (string, ui.Color) {
	if st.HandRaised || st.Status == core.StatusHandRaised {
		return "举手", pal.Warning
	}
	switch st.Status {
	case core.StatusOffline:
		return "离线", pal.TextMuted
	case core.StatusLocked:
		return "锁屏", pal.Danger
	case core.StatusControlled:
		return "远控中", pal.Info
	case core.StatusBroadcast:
		return "接收广播", pal.Accent
	case core.StatusExamining:
		return "测验中", pal.Info
	default:
		return "在线", pal.Success
	}
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
func (a *App) MonitorFPS() float64 { return a.monitorFPS }
func (a *App) MonitorQual() float64 {
	return float64(a.qualValue.Load())
}
func (a *App) Broadcasting() bool { return a.broadcastFlag.Load() }
func (a *App) ScreenLocked() bool { return a.screenLocked }
func (a *App) SelectedID() string   { return a.selectedID }
func (a *App) VoiceActive() bool  { return a.voiceFlag.Load() }
func (a *App) Monitoring() bool   { return a.monitoring }
func (a *App) RemoteControlID() string {
	return a.remoteControlID
}
func (a *App) StudentDemoID() string {
	a.demoIDMu.RLock()
	defer a.demoIDMu.RUnlock()
	return a.demoIDAtomic
}
func (a *App) TheatrePlaying() bool {
	return a.theatreFlag.Load()
}
func (a *App) UseMulticast() bool {
	return a.mcastFlag.Load()
}

func mapUIButton(btn int) int {
	switch btn {
	case 1:
		return 2 // UI secondary -> Win32 right
	case 2:
		return 1 // UI middle -> Win32 middle
	default:
		return 0
	}
}

func uiKeyToVK(k ui.Key) uint16 {
	switch {
	case k >= ui.KeyA && k <= ui.KeyZ:
		return uint16('A' + (k - ui.KeyA))
	case k >= ui.Key0 && k <= ui.Key9:
		return uint16('0' + (k - ui.Key0))
	case k >= ui.KeyF1 && k <= ui.KeyF12:
		return uint16(0x70 + (k - ui.KeyF1))
	}
	switch k {
	case ui.KeyEnter:
		return 0x0D
	case ui.KeyEscape:
		return 0x1B
	case ui.KeyBackspace:
		return 0x08
	case ui.KeyTab:
		return 0x09
	case ui.KeySpace:
		return 0x20
	case ui.KeyDelete:
		return 0x2E
	case ui.KeyInsert:
		return 0x2D
	case ui.KeyHome:
		return 0x24
	case ui.KeyEnd:
		return 0x23
	case ui.KeyPageUp:
		return 0x21
	case ui.KeyPageDown:
		return 0x22
	case ui.KeyLeft:
		return 0x25
	case ui.KeyUp:
		return 0x26
	case ui.KeyRight:
		return 0x27
	case ui.KeyDown:
		return 0x28
	case ui.KeyMinus:
		return 0xBD
	case ui.KeyEqual:
		return 0xBB
	case ui.KeyComma:
		return 0xBC
	case ui.KeyPeriod:
		return 0xBE
	case ui.KeySlash:
		return 0xBF
	case ui.KeySemicolon:
		return 0xBA
	case ui.KeyQuote:
		return 0xDE
	case ui.KeyBracketLeft:
		return 0xDB
	case ui.KeyBracketRight:
		return 0xDD
	case ui.KeyBackslash:
		return 0xDC
	case ui.KeyBackquote:
		return 0xC0
	default:
		return 0
	}
}
