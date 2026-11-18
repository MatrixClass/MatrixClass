package student

import (
	"context"
	"fmt"
	"image"
	"sort"
	"strings"
	"sync"
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
	tray *mygo.Tray

	studentID   string
	studentName string
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
	screenLocked     bool
	lockMessage      string
	unlockPassInput  string
	unlockStatus     string
	checkInName      string
	checkInNo        string
	checkInSubmitted bool
	homeworkPath     string
	chatInput        string
	statusNote       string
	volume           float64

	videoBitmap *ui.Bitmap
	lastFrameAt time.Time

	activePaper   *core.ExamPaper
	examAnswers   map[string]string
	examResult    *core.ExamSubmission
	recentNotices []string
}

func New(opts Options) *App {
	if opts.StudentID == "" {
		opts.StudentID = "stu-01"
	}
	if opts.StudentName == "" {
		opts.StudentName = "学生01"
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
		studentName:   opts.StudentName,
		studentNo:     opts.StudentNo,
		machineName:   opts.MachineName,
		channel:       opts.Channel,
		dark:          opts.Dark,
		mode:          ModeBar,
		connected:     true,
		lockMessage:   "课堂黑屏安静模式：请注意前方黑板或教师讲解。",
		checkInName:   opts.StudentName,
		checkInNo:     opts.StudentNo,
		homeworkPath:  "D:\\Homework\\Lab03_Report.docx",
		statusNote:    "已连接频道: " + opts.Channel,
		volume:        80,
		examAnswers:   make(map[string]string),
		recentNotices: []string{"欢迎加入 NexLab 电子教室"},
	}
}

// AttachWindow binds the floating/main window.
func (a *App) AttachWindow(w *mygo.Window) {
	a.mu.Lock()
	a.win = w
	a.mu.Unlock()
	if w != nil {
		_ = w.SetIcon(theme.AppIconPNG(false))
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
	defer a.mu.Unlock()
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
	a.lastFrameAt = time.Now()
	if a.win != nil {
		a.win.Invalidate()
	}
}

func (a *App) ShowFloatingBar() {
	a.mu.Lock()
	a.mode = ModeBar
	a.barCollapsed = false
	w := a.win
	a.mu.Unlock()
	if w != nil {
		w.Show()
		w.Focus()
		w.Invalidate()
	}
}

func (a *App) OpenMode(m int) {
	a.mu.Lock()
	a.mode = m
	w := a.win
	a.mu.Unlock()
	if w != nil {
		w.Show()
		w.Focus()
		w.Invalidate()
	}
}

// View builds the active student interface (floating bar, broadcast viewer, blackout lock, check-in, exam, or chat/homework).
func (a *App) View(c *ui.Context) {
	pal := theme.GetPalette(a.dark)
	c.SetTheme(theme.Theme(a.dark))
	c.Root().Background(pal.Canvas)
	switch a.mode {
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
			ui.Badge(c, a.channel).Background(pal.Selection).TextColor(pal.Accent)
			ui.Text(c, fmt.Sprintf("%s (%s)", a.studentName, a.studentNo)).FontSize(12).Bold().TextColor(pal.Text)
			if a.handRaised {
				ui.Badge(c, "已举手").Background(pal.Warning.Alpha(0.18)).TextColor(pal.Warning)
			}
			ui.Spacer(c)

			handLabel := "举手提问"
			if a.handRaised {
				handLabel = "取消举手"
			}
			if ui.Button(c, handLabel).Padding(4, 10).Radius(6).Clicked() {
				a.ToggleHandRaise()
			}
			if ui.Button(c, "点名签到").Padding(4, 10).Radius(6).Clicked() {
				a.mode = ModeCheckIn
			}
			if ui.Button(c, "随堂测验").Padding(4, 10).Radius(6).Clicked() {
				a.loadActivePaper()
				a.mode = ModeExam
			}
			if ui.Button(c, "作业与消息").Padding(4, 10).Radius(6).Clicked() {
				a.mode = ModeChat
			}
			if ui.Button(c, "广播窗口").Padding(4, 10).Radius(6).Clicked() {
				a.mode = ModeBroadcast
			}
			collapseLabel := "收起"
			if a.barCollapsed {
				collapseLabel = "展开"
			}
			if ui.Button(c, collapseLabel).Padding(4, 8).Radius(6).Clicked() {
				a.barCollapsed = !a.barCollapsed
			}
		})

		if !a.barCollapsed {
			ui.Row(c).FillWidth().Padding(4, 8).Radius(6).Background(pal.Canvas).Border(1, pal.CardBorder).AlignItems(ui.Center).Gap(10).Children(func() {
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
			ui.Text(c, "屏幕广播 / 网络影院接收").FontSize(12).Bold().TextColor(pal.Accent)
			if a.broadcastWindow {
				ui.Badge(c, "窗口模式").Background(pal.Info.Alpha(0.18)).TextColor(pal.Info)
			} else {
				ui.Badge(c, "全屏演示").Background(pal.Selection).TextColor(pal.Accent)
			}
			ui.Spacer(c)
			if ui.Button(c, "切换窗口/全屏").Padding(3, 8).Clicked() {
				a.broadcastWindow = !a.broadcastWindow
			}
			if ui.Button(c, "举手").Padding(3, 8).Clicked() {
				a.ToggleHandRaise()
			}
			if ui.Button(c, "返回悬浮条").Padding(3, 8).Clicked() {
				a.mode = ModeBar
			}
		})

		ui.Column(c).Grow(1).FillWidth().Center().Padding(12).Children(func() {
			if a.videoBitmap != nil {
				ui.Image(c, a.videoBitmap).Fit(ui.Contain).Radius(4)
			} else {
				ui.Text(c, "等待教师端推送屏幕或网络影院视频流...").FontSize(15).TextColor(ui.Hex("#CBD5E1"))
				ui.Text(c, "支持 NV12 GPU 直渲染与 KCP NACK 丢包重传").FontSize(12).TextColor(ui.Hex("#94A3B8"))
			}
		})
	})
}

func (a *App) buildLockView(c *ui.Context, pal theme.Palette) {
	ui.Column(c).Fill().Background(pal.LockBg).Center().Gap(14).Padding(24).Children(func() {
		ui.Badge(c, "黑屏锁屏中").Background(pal.Danger.Alpha(0.2)).TextColor(pal.Danger)
		ui.Text(c, a.lockMessage).FontSize(22).Bold().TextColor(ui.Hex("#F8FAFC"))
		ui.Text(c, "键盘与鼠标已被教师端锁定，断网超时或教师解锁后自动恢复").FontSize(12).TextColor(ui.Hex("#94A3B8"))

		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.TextInput(c, &a.unlockPassInput).Placeholder("紧急本地解锁口令...").Width(220)
			if ui.Button(c, "验证解锁").Padding(6, 14).Clicked() {
				a.tryEmergencyUnlock()
			}
		})
		if a.unlockStatus != "" {
			ui.Text(c, a.unlockStatus).FontSize(12).TextColor(pal.Warning)
		}
	})
}

func (a *App) buildCheckInView(c *ui.Context, pal theme.Palette) {
	ui.Column(c).Fill().Padding(18).Gap(12).Background(pal.Canvas).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Text(c, "课堂电子点名签到").FontSize(16).Bold().TextColor(pal.Accent)
			ui.Spacer(c)
			if ui.Button(c, "返回").Padding(4, 10).Clicked() {
				a.mode = ModeBar
			}
		})
		ui.Text(c, "请核对姓名与学号后点击签到提交至教师端：").FontSize(12).TextColor(pal.TextMuted)
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "姓名").Width(48).FontSize(12)
			ui.TextInput(c, &a.checkInName).Width(200)
		})
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "学号").Width(48).FontSize(12)
			ui.TextInput(c, &a.checkInNo).Width(200)
		})
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			if ui.PrimaryButton(c, "确认签到").Padding(6, 16).Clicked() {
				a.SubmitCheckIn()
			}
			if a.checkInSubmitted {
				ui.Badge(c, "已完成签到").Background(pal.Success.Alpha(0.18)).TextColor(pal.Success)
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
				a.mode = ModeBar
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
					ui.Column(c).Key(q.ID).FillWidth().Padding(10).Radius(6).Background(pal.Card).Border(1, pal.CardBorder).Gap(6).Children(func() {
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
									btn := ui.Button(c, lbl).Padding(4, 10).Radius(4)
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
				ui.Badge(c, fmt.Sprintf("得分 %d / %d", a.examResult.Score, a.examResult.MaxScore)).Background(pal.Success.Alpha(0.18)).TextColor(pal.Success)
			}
		})
	})
}

func (a *App) buildChatHomeworkView(c *ui.Context, pal theme.Palette) {
	ui.Column(c).Fill().Padding(16).Gap(10).Background(pal.Canvas).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Text(c, "作业提交与课堂消息").FontSize(16).Bold().TextColor(pal.Accent)
			ui.Spacer(c)
			if ui.Button(c, "返回").Padding(4, 10).Clicked() {
				a.mode = ModeBar
			}
		})

		ui.Column(c).FillWidth().Padding(10).Radius(6).Background(pal.Card).Border(1, pal.CardBorder).Gap(6).Children(func() {
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

		ui.Scroll(c.Key("student-chat-scroll")).Grow(1).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Gap(4).Children(func() {
				if a.opts.Classroom != nil {
					for _, msg := range a.opts.Classroom.ListChats() {
						ui.Row(c).FillWidth().Padding(6, 8).Radius(4).Background(pal.Card).Gap(6).Children(func() {
							ui.Text(c, msg.Sender+":").FontSize(11).Bold().TextColor(pal.Accent)
							ui.Text(c, msg.Text).FontSize(11).TextColor(pal.Text)
						})
					}
				}
			})
		})

		ui.Row(c).FillWidth().Gap(8).Children(func() {
			ui.TextInput(c, &a.chatInput).Grow(1).Placeholder("向教师端发送消息...")
			if ui.PrimaryButton(c, "发送消息").Padding(4, 14).Clicked() {
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
		a.opts.Classroom.SubmitCheckIn(a.studentID, a.checkInName, a.checkInNo)
	}
	a.sendControl(protocol.MsgCheckInSubmit, map[string]string{
		"student_id": a.studentID,
		"name":       a.checkInName,
		"student_no": a.checkInNo,
	})
	a.checkInSubmitted = true
	a.statusNote = fmt.Sprintf("签到成功: %s (%s)", a.checkInName, a.checkInNo)
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
	task, err := a.opts.Files.SubmitHomework(context.Background(), a.studentID, a.homeworkPath)
	if err != nil {
		a.statusNote = "作业上传失败: " + err.Error()
		return
	}
	a.statusNote = "作业已提交: " + task.FileName
}

func (a *App) SendChat() {
	txt := strings.TrimSpace(a.chatInput)
	if txt == "" {
		return
	}
	msg := core.ChatMessage{
		SenderID:  a.studentID,
		Sender:    a.studentName,
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
	a.mu.Unlock()
	if a.opts.Control != nil {
		_ = a.opts.Control.SetScreenLock(locked, message)
	}
	a.Invalidate()
}

func (a *App) tryEmergencyUnlock() {
	if strings.TrimSpace(a.unlockPassInput) == "" {
		a.unlockStatus = "请输入解锁口令"
		return
	}
	type emergencyUnlocker interface {
		EmergencyUnlock(pass string) error
	}
	if u, ok := a.opts.Control.(emergencyUnlocker); ok {
		if err := u.EmergencyUnlock(a.unlockPassInput); err != nil {
			a.unlockStatus = "口令错误: " + err.Error()
			return
		}
	} else if a.unlockPassInput != "nexlab-unlock" {
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
func (a *App) Mode() int         { return a.mode }
func (a *App) SetMode(m int)     { a.mode = m }
func (a *App) HandRaised() bool  { return a.handRaised }
func (a *App) ScreenLocked() bool { return a.screenLocked }
func (a *App) CheckInSubmitted() bool {
	return a.checkInSubmitted
}
func (a *App) ExamResult() *core.ExamSubmission { return a.examResult }
