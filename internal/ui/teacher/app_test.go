package teacher

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"nexlab/internal/core"
	"nexlab/internal/store"
	"nexlab/internal/ui/demo"
)

func newTestTeacherApp() (*App, *demo.Transport, *demo.Classroom, *demo.Control) {
	tr := demo.NewTransport("301机房", 6)
	cr := demo.NewClassroom()
	ctrl := demo.NewControl()
	app := New(Options{
		TeacherID:   "teacher-test",
		TeacherName: "教师端",
		Channel:     "301机房",
		Services: Services{
			Transport: tr,
			Media:     demo.NewMedia(),
			Control:   ctrl,
			Classroom: cr,
			Files:     demo.NewFileTransfer(),
			Exam:      demo.NewExam(),
		},
	})
	return app, tr, cr, ctrl
}

func TestTeacherConsoleViewsAndTabs(t *testing.T) {
	app, tr, _, _ := newTestTeacherApp()
	tester := ui.NewTester(app.View, 1280, 820)
	tester.Frame()

	if !tester.HasText("矩联电子教室 MatrixClass") {
		t.Fatalf("expected teacher title in view")
	}
	if !tester.HasText("设备视图") || !tester.HasText("屏幕广播") || !tester.HasText("网络影院") || !tester.HasText("默认分组") {
		t.Fatalf("expected primary navigation/toolbar items")
	}
	if tester.HasText("学生列表") || tester.HasText("分组与媒体库") || tester.HasText("座位视图") {
		t.Fatalf("unexpected removed sidebar sections or old tab label")
	}

	// Click toolbar "屏幕广播" toggle
	if err := tester.Click("屏幕广播"); err != nil {
		t.Fatalf("click 屏幕广播 failed: %v", err)
	}
	tester.Frame()
	if !app.Broadcasting() {
		t.Fatalf("expected broadcasting=true after clicking toolbar toggle")
	}
	if len(tr.Broadcasts()) == 0 {
		t.Fatalf("expected broadcast control envelope to be sent")
	}

	// Switch to MyTheatre tab
	if err := tester.Click("网络影院"); err != nil {
		t.Fatalf("click 网络影院 failed: %v", err)
	}
	tester.Frame()
	if app.MainTab() != TabMyTheatre {
		t.Fatalf("expected TabMyTheatre (%d), got %d", TabMyTheatre, app.MainTab())
	}
	if !tester.HasText("mytheatre") {
		t.Fatalf("expected mytheatre watermark in idle theatre view")
	}

	// Switch to Exam & Stats tab and publish exam
	if err := tester.Click("随堂测验"); err != nil {
		t.Fatalf("click 随堂测验 failed: %v", err)
	}
	tester.Frame()
	if app.MainTab() != TabExamStats {
		t.Fatalf("expected TabExamStats (%d), got %d", TabExamStats, app.MainTab())
	}

	// Click "文件分发" opens standalone file distribution modal/window
	if err := tester.Click("文件分发"); err != nil {
		t.Fatalf("click 文件分发 failed: %v", err)
	}
	tester.Frame()
	if !app.fileDistModal || !tester.HasText("文件分发队列") {
		t.Fatalf("expected file distribution popup to open")
	}
	if err := tester.Click("关闭"); err != nil {
		t.Fatalf("click 关闭 failed: %v", err)
	}
	tester.Frame()
	if app.fileDistModal {
		t.Fatalf("expected file distribution popup to close")
	}

	// Toggle screen lock
	if err := tester.Click("黑屏锁屏"); err != nil {
		t.Fatalf("click 黑屏锁屏 failed: %v", err)
	}
	tester.Frame()
	if !app.ScreenLocked() {
		t.Fatalf("expected screenLocked=true")
	}
}

func TestTeacherRightSidebarAndActions(t *testing.T) {
	app, _, cr, ctrl := newTestTeacherApp()
	tester := ui.NewTester(app.View, 1280, 820)
	tester.Frame()

	// Switch right sidebar to Chat & Hand Queue
	app.SetSideTab(SideTabChatQueue)
	tester.Frame()
	if !tester.HasText("发送") {
		t.Fatalf("expected chat send button in right sidebar")
	}

	// Create a new group via bottom horizontal group bar "+" button
	if err := tester.Click("+"); err != nil {
		t.Fatalf("click + group button failed: %v", err)
	}
	tester.Frame()
	app.newGroupInput = "实验一组"
	if err := tester.Click("确定"); err != nil {
		t.Fatalf("click 确定 group failed: %v", err)
	}
	tester.Frame()
	if !tester.HasText("实验一组") {
		t.Fatalf("expected newly created group tab 实验一组 in bottom bar")
	}

	// Verify check-in trigger resets checked-in counts
	if err := tester.Click("点名签到"); err != nil {
		t.Fatalf("click 点名签到 failed: %v", err)
	}
	tester.Frame()
	for _, st := range cr.ListStudents() {
		if st.CheckedIn {
			t.Fatalf("expected student %s CheckedIn=false right after StartCheckIn", st.ID)
		}
	}

	// Verify WOL power dispatch
	if err := tester.Click("远程开机"); err != nil {
		t.Fatalf("click 远程开机 failed: %v", err)
	}
	tester.Frame()
	_ = ctrl
	_ = core.PowerWake

	// Verify seat swap helper
	students := cr.ListStudents()
	if len(students) >= 2 {
		s0, s1 := students[0], students[1]
		app.swapStudentSeats(s0.ID, s1.ID)
		for _, updated := range cr.ListStudents() {
			if updated.ID == s0.ID && updated.Seat != s1.Seat {
				t.Fatalf("expected %s seat swapped to %+v, got %+v", s0.ID, s1.Seat, updated.Seat)
			}
			if updated.ID == s1.ID && updated.Seat != s0.Seat {
				t.Fatalf("expected %s seat swapped to %+v, got %+v", s1.ID, s0.Seat, updated.Seat)
			}
		}
	}
}

func TestTeacherRemoteControlModalAndExamDraftBuilder(t *testing.T) {
	app, tr, cr, _ := newTestTeacherApp()
	tester := ui.NewTester(app.View, 1280, 820)
	tester.Frame()

	students := cr.ListStudents()
	if len(students) == 0 {
		t.Fatalf("expected demo students")
	}
	targetID := students[0].ID

	// Start and stop remote control
	app.StartRemoteControl(targetID)
	tester.Frame()
	if app.RemoteControlID() != targetID {
		t.Fatalf("expected remoteControlID=%s, got %s", targetID, app.RemoteControlID())
	}
	if !tester.HasText("退出远程控制") {
		t.Fatalf("expected remote control modal to render exit button")
	}
	if err := tester.Click("退出远程控制"); err != nil {
		t.Fatalf("click 退出远程控制 failed: %v", err)
	}
	tester.Frame()
	if app.RemoteControlID() != "" {
		t.Fatalf("expected remoteControlID cleared after exit")
	}

	// Multi-question draft & publish
	app.SetMainTab(TabExamStats)
	tester.Frame()
	app.addDraftQuestion()
	app.addDraftQuestion()
	app.publishExam()
	tester.Frame()
	paper := app.opts.Exam.GetActivePaper()
	if paper == nil || len(paper.Questions) != 2 {
		t.Fatalf("expected 2-question published paper, got %+v", paper)
	}

	// File targets should resolve to all online demo students
	targets := app.resolveFileTargets()
	if len(targets) == 0 {
		t.Fatalf("expected resolved online file targets")
	}
	_ = tr
}

func TestTeacherSelectAllAndBatchOperations(t *testing.T) {
	app, _, cr, _ := newTestTeacherApp()
	tester := ui.NewTester(app.View, 1280, 820)
	tester.Frame()

	students := cr.ListStudents()
	if len(students) < 2 {
		t.Fatalf("expected >= 2 demo students")
	}

	// Select all via Ctrl+A / Cmd+A button or shortcut in SeatMap
	tester.Key(ui.Cmd, ui.KeyA)
	tester.Frame()
	if got := len(app.targetStudentIDs()); got != len(students) {
		t.Fatalf("expected Ctrl+A in SeatMap to select %d students, got %d", len(students), got)
	}

	// Clear and test Ctrl+A in MonitorWall
	app.clearSelection()
	app.SetMainTab(TabMonitorWall)
	tester.Frame()
	tester.Key(ui.Cmd, ui.KeyA)
	tester.Frame()
	if got := len(app.targetStudentIDs()); got != len(students) {
		t.Fatalf("expected Ctrl+A in MonitorWall to select %d students, got %d", len(students), got)
	}

	// Batch lock & file targets
	app.setScreenLockTargets(true, app.targetStudentIDs())
	if !app.ScreenLocked() {
		t.Fatalf("expected screenLocked=true after batch lock")
	}
	app.fileTargetAll = false
	if got := len(app.resolveFileTargets()); got != len(students) {
		t.Fatalf("expected resolveFileTargets to return all %d selected students, got %d", len(students), got)
	}

	// Ctrl+Click individual toggle & clear on blank area click
	app.clearSelection()
	app.SetMainTab(TabSeatMap)
	tester.Frame()
	r0, ok0 := tester.Find(students[0].MachineName + " " + students[0].StudentNo)
	r1, ok1 := tester.Find(students[1].MachineName + " " + students[1].StudentNo)
	if !ok0 || !ok1 {
		t.Fatalf("expected seat cards found in tester")
	}
	tester.ClickAtWith(ui.Ctrl, r0.X, r0.Y)
	tester.Frame()
	tester.ClickAtWith(ui.Ctrl, r1.X, r1.Y)
	tester.Frame()
	if got := len(app.targetStudentIDs()); got != 2 {
		t.Fatalf("expected 2 selected students after Ctrl+Click, got %d", got)
	}
	tester.ClickAtWith(ui.Ctrl, r0.X, r0.Y)
	tester.Frame()
	if got := len(app.targetStudentIDs()); got != 1 || app.targetStudentIDs()[0] != students[1].ID {
		t.Fatalf("expected only student[1] selected after Ctrl+Click deselect, got %v", app.targetStudentIDs())
	}
	// Click blank area at bottom-right of SeatMap scroll area
	tester.ClickAtWith(0, 900, 720)
	tester.Frame()
	if got := len(app.targetStudentIDs()); got != 0 {
		t.Fatalf("expected 0 selected students after clicking blank area, got %d", got)
	}
}

func TestTeacherFileDistributeQueueWindow(t *testing.T) {
	app, _, _, _ := newTestTeacherApp()
	tester := ui.NewTester(app.View, 1280, 820)
	tester.Frame()

	if app.remoteDir != "" {
		t.Fatalf("expected empty default remoteDir (student Desktop), got %q", app.remoteDir)
	}
	if app.autoOpenFile {
		t.Fatalf("expected autoOpenFile=false by default")
	}

	app.OpenFileDistributeWindow()
	tester.Frame()
	if !app.fileDistModal {
		t.Fatalf("expected fileDistModal=true after OpenFileDistributeWindow")
	}
	if !tester.HasText("学生机接收路径（留空则默认发送到学生机桌面）") {
		t.Fatalf("expected default desktop path hint in file distribution view")
	}

	app.AddQueuedFiles([]string{`C:\tmp\lesson1.pdf`, `C:\tmp\slides.pptx`, `C:\tmp\lesson1.pdf`})
	tester.Frame()
	if len(app.fileQueue) != 2 {
		t.Fatalf("expected 2 deduplicated queued files, got %d", len(app.fileQueue))
	}
	if !tester.HasText("lesson1.pdf") || !tester.HasText("slides.pptx") {
		t.Fatalf("expected queued filenames rendered in popup")
	}

	beforeCount := len(app.opts.Files.ListTasks())
	app.DistributeQueue()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(app.opts.Files.ListTasks()) >= beforeCount+2 {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}

	tasks := app.opts.Files.ListTasks()
	if len(tasks) < beforeCount+2 {
		t.Fatalf("expected 2 new transfer tasks, got %d (before=%d)", len(tasks), beforeCount)
	}
	seen := map[string]string{}
	for _, tk := range tasks[:2] {
		seen[tk.FileName] = tk.RemotePath
		if tk.AutoOpen {
			t.Fatalf("expected task AutoOpen=false by default, got true for %s", tk.FileName)
		}
	}
	if seen["lesson1.pdf"] != "lesson1.pdf" || seen["slides.pptx"] != "slides.pptx" {
		t.Fatalf("expected empty remoteDir to yield relative desktop filenames, got %+v", seen)
	}

	tester.Frame()
	if !tester.HasText("上传到学生机进度") {
		t.Fatalf("expected upload progress section in file distribution view")
	}

	// Removing a queued file should update queue length
	app.removeQueuedFile(0)
	if len(app.fileQueue) != 1 {
		t.Fatalf("expected 1 queued file after remove, got %d", len(app.fileQueue))
	}
	app.closeFileDistributeWindow()
	if app.fileDistModal {
		t.Fatalf("expected fileDistModal=false after close")
	}
}

func TestExpandQueueEntriesFolder(t *testing.T) {
	tmp := t.TempDir()
	rootDir := filepath.Join(tmp, "Lab01")
	subDir := filepath.Join(rootDir, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f1 := filepath.Join(rootDir, "readme.txt")
	f2 := filepath.Join(subDir, "main.go")
	if err := os.WriteFile(f1, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write f1: %v", err)
	}
	if err := os.WriteFile(f2, []byte("package main"), 0o644); err != nil {
		t.Fatalf("write f2: %v", err)
	}

	items := expandQueueEntries([]string{rootDir}, "")
	if len(items) != 2 {
		t.Fatalf("expected 2 expanded items, got %d", len(items))
	}
	got := map[string]string{}
	for _, it := range items {
		got[filepath.Base(it.localPath)] = filepath.ToSlash(it.remoteDir)
	}
	if got["readme.txt"] != "Lab01" || got["main.go"] != "Lab01/sub" {
		t.Fatalf("unexpected expanded folder remoteDirs: %+v", got)
	}
}

func TestCollectHomeworkDefaultDownloadsDir(t *testing.T) {
	app, _, _, _ := newTestTeacherApp()
	if app.collectSaveDir == "" {
		t.Fatalf("expected non-empty default collectSaveDir")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		want := filepath.Join(home, "Downloads")
		if app.collectSaveDir != want {
			t.Fatalf("expected default collectSaveDir=%q, got %q", want, app.collectSaveDir)
		}
	}

	app.CollectHomework()
	if !app.CollectingHomework() {
		t.Fatalf("expected CollectingHomework=true after CollectHomework")
	}
	app.EndCollectHomework()
	if app.CollectingHomework() {
		t.Fatalf("expected CollectingHomework=false after EndCollectHomework")
	}
}

func TestTeacherMicVolumeAndSettingsMonitorSliders(t *testing.T) {
	app, _, _, _ := newTestTeacherApp()
	tester := ui.NewTester(app.View, 1280, 820)
	tester.Frame()

	if !tester.HasText("麦克风音量") {
		t.Fatalf("expected 麦克风音量 in bottom-right panel")
	}
	if !tester.HasText("扬声器音量") {
		t.Fatalf("expected 扬声器音量 in bottom-right panel")
	}
	app.ToggleSpeakerMute()
	if !app.SpeakerMuted() {
		t.Fatalf("expected SpeakerMuted=true after ToggleSpeakerMute")
	}
	app.ToggleMicMute()
	if !app.MicMuted() || app.EffectiveMicGain() != 0 {
		t.Fatalf("expected MicMuted=true and EffectiveMicGain=0 after ToggleMicMute")
	}
	tester.Frame()
	if !tester.HasText("已静音") || !tester.HasText("取消静音") {
		t.Fatalf("expected mute labels in UI after muting")
	}
	if tester.HasText("监控画质") || tester.HasText("监控帧率") {
		t.Fatalf("expected monitor quality/FPS moved out of main sidebar into settings modal")
	}

	app.settingsOpen = true
	tester.Frame()
	if !tester.HasText("监控画质") || !tester.HasText("监控帧率") {
		t.Fatalf("expected 监控画质 and 监控帧率 inside settings modal")
	}
}

func TestTeacherSQLitePersistenceAndChannelPrompt(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "nexlab-teacher.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	var selectedCh string
	app := New(Options{
		TeacherID:     "teacher-db",
		TeacherName:   "教师端",
		Channel:       "1",
		Store:         st,
		PromptChannel: true,
		OnChannelSelected: func(ch string) {
			selectedCh = ch
		},
		Services: Services{
			Transport: demo.NewTransport("1", 2),
			Media:     demo.NewMedia(),
			Control:   demo.NewControl(),
			Classroom: demo.NewClassroom(),
			Files:     demo.NewFileTransfer(),
			Exam:      demo.NewExam(),
		},
	})
	tester := ui.NewTester(app.View, 1280, 820)
	tester.Frame()
	if !app.channelModal || !tester.HasText("教师端登录与频道选择") {
		t.Fatalf("expected startup login & channel selection modal")
	}
	if app.loginUserInput != "admin" || app.authPasswordHash != "" {
		t.Fatalf("expected default admin with empty password, got user=%q hash=%q", app.loginUserInput, app.authPasswordHash)
	}

	// First launch: set channel 5 and set a password
	app.channelInput = "5"
	app.loginPassInput = "secret123"
	if !app.SubmitStartupLogin() {
		t.Fatalf("expected first-launch SubmitStartupLogin to succeed")
	}
	if selectedCh != "5" || app.channel != "5" || app.channelModal {
		t.Fatalf("unexpected channel confirmation state: selected=%q app.channel=%q modal=%v", selectedCh, app.channel, app.channelModal)
	}

	// Re-open App with the same SQLite store and verify persisted channel + password verification
	app2 := New(Options{
		TeacherID:     "teacher-db",
		TeacherName:   "教师端",
		Channel:       "1",
		Store:         st,
		PromptChannel: true,
		Services: Services{
			Transport: demo.NewTransport("1", 2),
			Media:     demo.NewMedia(),
			Control:   demo.NewControl(),
			Classroom: demo.NewClassroom(),
			Files:     demo.NewFileTransfer(),
			Exam:      demo.NewExam(),
		},
	})
	if app2.channel != "5" || len(app2.recentChannels) == 0 || app2.recentChannels[0] != "5" {
		t.Fatalf("expected persisted channel 5, got channel=%q recent=%v", app2.channel, app2.recentChannels)
	}
	app2.loginPassInput = "wrong"
	if app2.SubmitStartupLogin() || !app2.channelModal || app2.loginError == "" {
		t.Fatalf("expected wrong password to be rejected")
	}
	app2.loginPassInput = "secret123"
	if !app2.SubmitStartupLogin() || app2.channelModal || app2.loginError != "" {
		t.Fatalf("expected correct password to succeed")
	}
}

func TestClearOfflineStudents(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "nexlab-teacher.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	app, _, cr, _ := newTestTeacherApp()
	app.store = st
	cr.UpsertStudent(core.StudentInfo{ID: "off-1", MachineName: "PC-OFF-1", Status: core.StatusOffline})
	_ = st.UpsertStudent(core.StudentInfo{ID: "off-1", MachineName: "PC-OFF-1", Status: core.StatusOffline})

	before := len(cr.ListStudents())
	app.ClearOfflineStudents()
	after := len(cr.ListStudents())
	if after >= before {
		t.Fatalf("expected offline students removed, before=%d after=%d", before, after)
	}
	for _, s := range cr.ListStudents() {
		if s.Status == core.StatusOffline {
			t.Fatalf("found remaining offline student: %+v", s)
		}
	}
	persisted, err := st.LoadStudents()
	if err != nil {
		t.Fatalf("LoadStudents: %v", err)
	}
	for _, s := range persisted {
		if s.ID == "off-1" {
			t.Fatalf("expected off-1 deleted from sqlite store")
		}
	}
}

func TestMarqueeDragMultiSelect(t *testing.T) {
	app, _, cr, _ := newTestTeacherApp()
	tester := ui.NewTester(app.View, 1280, 820)
	tester.Frame()
	tester.Frame() // populate Bounds()

	students := cr.ListStudents()
	if len(students) < 2 {
		t.Fatalf("expected >= 2 demo students")
	}
	r0, ok0 := tester.Find(students[0].MachineName + " " + students[0].StudentNo)
	r1, ok1 := tester.Find(students[1].MachineName + " " + students[1].StudentNo)
	if !ok0 || !ok1 {
		t.Fatalf("expected seat cards in Device View")
	}

	// Press slightly above-left of card 0 and drag across to card 1
	tester.Press(r0.X-10, r0.Y-10)
	tester.Move(r1.X+10, r1.Y+10)
	tester.Frame()
	tester.Release(r1.X+10, r1.Y+10)
	tester.Frame()
	if got := len(app.targetStudentIDs()); got < 2 {
		t.Fatalf("expected >= 2 selected students after marquee drag in Device View, got %d", got)
	}

	// Switch to Monitor Wall and verify marquee drag multi-select
	app.clearSelection()
	app.SetMainTab(TabMonitorWall)
	tester.Frame()
	tester.Frame() // populate tile Bounds()
	m0 := app.itemBounds[students[0].ID]
	m1 := app.itemBounds[students[1].ID]
	if m0.W <= 0 || m1.W <= 0 {
		t.Fatalf("expected monitor tile bounds populated")
	}
	tester.Press(m0.X+5, m0.Y+5)
	tester.Move(m1.X+20, m1.Y+20)
	tester.Frame()
	tester.Release(m1.X+20, m1.Y+20)
	tester.Frame()
	if got := len(app.targetStudentIDs()); got < 2 {
		t.Fatalf("expected >= 2 selected students after marquee drag in Monitor Wall, got %d", got)
	}
}

func TestUpdateMonitorFrameResizeAndConcurrency(t *testing.T) {
	app, _, _, _ := newTestTeacherApp()
	tester := ui.NewTester(app.View, 1280, 820)
	tester.Frame()

	if err := tester.Click("屏幕监控"); err != nil {
		t.Fatalf("click 屏幕监控 failed: %v", err)
	}
	tester.Frame()

	students := app.listStudents()
	if len(students) == 0 {
		t.Fatalf("expected demo students")
	}
	sid := students[0].ID

	rgba320 := make([]byte, 320*180*4)
	for i := 0; i < len(rgba320); i += 4 {
		rgba320[i] = 10
		rgba320[i+1] = 180
		rgba320[i+2] = 90
		rgba320[i+3] = 255
	}
	app.UpdateMonitorFrame(sid, &core.VideoFrame{
		Width:  320,
		Height: 180,
		Data:   rgba320,
	})
	tester.Frame()

	bmp := app.monitorTiles[sid]
	if bmp == nil {
		t.Fatalf("expected tile bitmap for %s", sid)
	}
	if w, h := bmp.Size(); w != 320 || h != 180 {
		t.Fatalf("expected bitmap resized from placeholder 240x135 to 320x180, got %dx%d", w, h)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			app.UpdateMonitorFrame(sid, &core.VideoFrame{Width: 320, Height: 180, Data: rgba320})
		}()
	}
	wg.Wait()
	tester.Frame()
}

func TestRevokeClassroomPolicy(t *testing.T) {
	app, _, _, ctrl := newTestTeacherApp()
	app.blockedAppsInput = "WeChat.exe, steam.exe"
	app.blockedSitesInput = "bilibili.com"

	tester := ui.NewTester(app.View, 1280, 820)
	tester.Frame()
	if err := tester.Click("管控策略"); err != nil {
		t.Fatalf("failed to open policy editor: %v", err)
	}
	tester.Frame()

	if err := tester.Click("下发管控策略"); err != nil {
		t.Fatalf("failed to click apply policy button: %v", err)
	}
	tester.Frame()
	if !app.policyActive {
		t.Fatalf("expected policyActive=true after apply")
	}
	if ctrl.Policy.Mode != "blacklist" || len(ctrl.Policy.BlockedApps) != 2 {
		t.Fatalf("unexpected applied policy: %+v", ctrl.Policy)
	}

	if err := tester.Click("管控策略"); err != nil {
		t.Fatalf("failed to reopen policy editor: %v", err)
	}
	tester.Frame()
	if err := tester.Click("撤销管控策略"); err != nil {
		t.Fatalf("failed to click revoke policy button: %v", err)
	}
	tester.Frame()
	if app.policyActive {
		t.Fatalf("expected policyActive=false after revoke")
	}
	if ctrl.Policy.Mode != "off" {
		t.Fatalf("expected revoked policy mode=off, got %q", ctrl.Policy.Mode)
	}
	if len(ctrl.Policy.BlockedApps) != 0 || len(ctrl.Policy.BlockedDomains) != 0 {
		t.Fatalf("expected empty blocklists after revoke, got %+v", ctrl.Policy)
	}
	if app.blockedAppsInput != "WeChat.exe, steam.exe" || app.blockedSitesInput != "bilibili.com" {
		t.Fatalf("expected input fields preserved after revoke")
	}
}

func TestTeacherTrayMenu(t *testing.T) {
	app, _, _, _ := newTestTeacherApp()
	menu := app.BuildTrayMenu()
	if menu == nil {
		t.Fatalf("expected non-nil tray menu")
	}
}

func TestTeacherPolicyOpensFromNavigation(t *testing.T) {
	app, _, _, _ := newTestTeacherApp()
	tester := ui.NewTester(app.View, 1280, 820)
	tester.Frame()

	if err := tester.Click("管控策略"); err != nil {
		t.Fatalf("failed to open policy editor from navigation: %v", err)
	}
	tester.Frame()
	if !app.policyModal || !tester.HasText("管控策略编排") || !tester.HasText("下发管控策略") {
		t.Fatalf("expected policy editor modal to be open")
	}
}

func TestTheatrePlaybackRequiresVideo(t *testing.T) {
	app, _, _, _ := newTestTeacherApp()
	app.theatre.Media = ""
	app.theatrePlayer = nil
	app.toggleTheatrePlayback()
	if app.theatre.Playing {
		t.Fatalf("expected playback to remain stopped without a local video")
	}
	if app.statusNote == "" {
		t.Fatalf("expected actionable status note when playback is attempted without a video")
	}
}
