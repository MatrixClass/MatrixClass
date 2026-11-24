package student

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"nexlab/internal/control"
	"nexlab/internal/core"
	"nexlab/internal/protocol"
	"nexlab/internal/ui/demo"
)

type blockingStudentFiles struct {
	started   chan struct{}
	cancelled chan struct{}
}

func (f *blockingStudentFiles) DistributeFile(context.Context, string, string, []string, bool) (*core.TransferTask, error) {
	return nil, nil
}
func (f *blockingStudentFiles) SubmitHomework(ctx context.Context, _ string, _ string) (*core.TransferTask, error) {
	select {
	case <-f.started:
	default:
		close(f.started)
	}
	<-ctx.Done()
	select {
	case <-f.cancelled:
	default:
		close(f.cancelled)
	}
	return nil, ctx.Err()
}
func (f *blockingStudentFiles) CollectFiles(context.Context, string, string, []string) ([]*core.TransferTask, error) {
	return nil, nil
}
func (f *blockingStudentFiles) ListTasks() []*core.TransferTask { return nil }
func (f *blockingStudentFiles) CancelTask(string) error         { return nil }

func newTestStudentApp() *App {
	return New(Options{
		StudentID:   "stu-01",
		StudentNo:   "202601",
		MachineName: "LAB-PC01",
		Channel:     "301机房",
		Services: Services{
			Transport: demo.NewTransport("301机房", 1),
			Media:     demo.NewMedia(),
			Control:   demo.NewControl(),
			Classroom: demo.NewClassroom(),
			Files:     demo.NewFileTransfer(),
			Exam:      demo.NewExam(),
		},
	})
}

func TestStudentHomeworkPathStartsEmpty(t *testing.T) {
	app := newTestStudentApp()
	if app.homeworkPath != "" {
		t.Fatalf("expected homework path to start empty, got %q", app.homeworkPath)
	}
}

func TestStudentExitPasswordPersistsOnlyHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "student-exit-password.sha256")
	app := New(Options{ExitPasswordPath: path})
	if err := app.saveExitPassword("secret-123"); err != nil {
		t.Fatalf("save exit password: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read exit password file: %v", err)
	}
	if strings.Contains(string(data), "secret-123") {
		t.Fatal("exit password must not be stored in plaintext")
	}
	loaded := New(Options{ExitPasswordPath: path})
	if loaded.exitPasswordHash != hashExitPassword("secret-123") {
		t.Fatalf("loaded password hash mismatch: %q", loaded.exitPasswordHash)
	}
}

func TestStudentParameterSettingsRequiresExitPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "student-exit-password.sha256")
	app := New(Options{ExitPasswordPath: path})
	if err := app.saveExitPassword("secret-123"); err != nil {
		t.Fatalf("save exit password: %v", err)
	}

	app.OpenParameterSettings()
	if app.passwordMode != "settings-auth" {
		t.Fatalf("parameter settings should start in password auth mode, got %q", app.passwordMode)
	}
	if app.settingsWin != nil {
		t.Fatal("parameter settings window must stay closed before password verification")
	}
	if hashExitPassword("wrong-password") == app.exitPasswordHash {
		t.Fatal("wrong password unexpectedly matched the stored password hash")
	}
	if hashExitPassword("secret-123") != app.exitPasswordHash {
		t.Fatal("configured password did not match the stored password hash")
	}
}

func TestStudentTrayMenuIncludesParameterSettings(t *testing.T) {
	app := newTestStudentApp()
	menu := app.BuildTrayMenu()
	if menu == nil {
		t.Fatal("expected non-nil student tray menu")
	}
	found := false
	for _, item := range menu.Items() {
		if item.Label == "参数设置" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("student tray menu must include parameter settings")
	}
}

func TestStudentChannelPersistsAndRejectsNonNumericValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "student-channel-id")
	app := New(Options{ChannelPath: path})
	if err := app.saveChannel("42"); err != nil {
		t.Fatalf("save channel: %v", err)
	}
	loaded := New(Options{ChannelPath: path})
	if loaded.Channel() != "42" {
		t.Fatalf("loaded channel = %q, want 42", loaded.Channel())
	}
	if err := app.saveChannel("room-42"); err == nil {
		t.Fatal("expected non-numeric channel to be rejected")
	}
	if err := os.WriteFile(path, []byte("room-42\n"), 0o600); err != nil {
		t.Fatalf("write invalid channel: %v", err)
	}
	invalid := New(Options{ChannelPath: path})
	if invalid.Channel() != "1" || !invalid.InitialSetupRequired() {
		t.Fatalf("invalid persisted channel should require setup, got channel=%q setup=%v", invalid.Channel(), invalid.InitialSetupRequired())
	}
}

func TestStudentExitPasswordResetClearsStoredPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "student-exit-password.sha256")
	app := New(Options{ExitPasswordPath: path})
	if err := app.saveExitPassword("secret-123"); err != nil {
		t.Fatalf("save exit password: %v", err)
	}
	app.ResetExitPassword()
	if app.exitPasswordHash != "" {
		t.Fatalf("expected reset to clear hash, got %q", app.exitPasswordHash)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected password file to be removed, stat error: %v", err)
	}
}

func TestStudentAppStartCloseIsIdempotentAndCancelsHomework(t *testing.T) {
	files := &blockingStudentFiles{started: make(chan struct{}), cancelled: make(chan struct{})}
	app := New(Options{StudentID: "stu", Services: Services{Files: files}})
	ctx := context.Background()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := app.Start(ctx); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	app.homeworkPath = "homework.txt"
	app.SubmitHomework()
	select {
	case <-files.started:
	case <-time.After(time.Second):
		t.Fatal("homework task did not start")
	}
	if err := app.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := app.Start(ctx); err == nil {
		t.Fatal("expected Start after Close to fail")
	}
	select {
	case <-files.cancelled:
	case <-time.After(time.Second):
		t.Fatal("homework task was not cancelled")
	}
}

func TestStudentBarCheckInExamAndLockViews(t *testing.T) {
	app := newTestStudentApp()
	tester := ui.NewTester(app.View, 920, 560)
	tester.Frame()

	if !tester.HasText("举手") || tester.HasText("点名签到") {
		t.Fatalf("expected floating bar without manual 点名签到 button")
	}

	if err := tester.Click("举手"); err != nil {
		t.Fatalf("click 举手 failed: %v", err)
	}
	tester.Frame()
	if !app.HandRaised() {
		t.Fatalf("expected handRaised=true")
	}

	// Check-in flow triggered by teacher MsgCheckInStart -> OpenMode(ModeCheckIn)
	app.OpenMode(ModeCheckIn)
	app.checkInClass = "一班"
	app.checkInName = "一班"
	tester.Frame()
	if err := tester.Click("确认签到"); err != nil {
		t.Fatalf("click 确认签到 failed: %v", err)
	}
	tester.Frame()
	if !app.CheckInSubmitted() {
		t.Fatalf("expected checkInSubmitted=true")
	}
	_ = tester.Click("返回")
	tester.Frame()

	// Exam entry must be hidden on floating bar and only pop up when teacher publishes exam
	if tester.HasText("随堂测验") {
		t.Fatalf("expected 随堂测验 entry hidden from student floating bar")
	}
	app.OpenMode(ModeExam)
	tester.Frame()
	if err := tester.Click("提交答卷"); err != nil {
		t.Fatalf("click 提交答卷 failed: %v", err)
	}
	tester.Frame()
	if app.ExamResult() == nil {
		t.Fatalf("expected exam result after submission")
	}

	// Blackout lock & emergency unlock
	app.SetLocked(true, "课堂测验锁定中")
	tester.Frame()
	if !app.ScreenLocked() || !tester.HasText("黑屏锁屏中") {
		t.Fatalf("expected blackout lock overlay")
	}
	app.unlockPassInput = "nexlab-unlock"
	if err := tester.Click("验证解锁"); err != nil {
		t.Fatalf("click 验证解锁 failed: %v", err)
	}
	tester.Frame()
	if app.ScreenLocked() {
		t.Fatalf("expected emergency unlock to clear screen lock")
	}
}

func TestStudentCheckInRequiresManualClassName(t *testing.T) {
	app := newTestStudentApp()
	app.OpenMode(ModeCheckIn)
	app.SubmitCheckIn()
	if app.CheckInSubmitted() {
		t.Fatal("check-in succeeded without manually entered class name")
	}
	if !strings.Contains(app.statusNote, "班级和姓名") {
		t.Fatalf("status = %q, want class-name validation", app.statusNote)
	}
}

func TestStudentTheatreStateUsesDedicatedMode(t *testing.T) {
	app := newTestStudentApp()
	app.SetTheatreState(core.TheatreState{Playing: true, Media: `C:\lesson\network-theatre.mp4`})
	if app.Mode() != ModeTheatre {
		t.Fatalf("expected dedicated theatre mode, got %d", app.Mode())
	}
	if strings.Contains(app.statusNote, "屏幕广播") {
		t.Fatalf("theatre status must not be presented as screen broadcast: %q", app.statusNote)
	}

	app.SetTheatreState(core.TheatreState{Playing: false, Media: `C:\lesson\network-theatre.mp4`})
	if app.Mode() != ModeTheatre {
		t.Fatalf("expected theatre window to remain open while paused, got %d", app.Mode())
	}

	app.SetTheatreState(core.TheatreState{Stopped: true, Media: `C:\lesson\network-theatre.mp4`})
	if app.Mode() != ModeBar {
		t.Fatalf("expected floating bar after theatre stops, got %d", app.Mode())
	}
}

type testLockBackend struct{}

func (testLockBackend) EnableLock(string) error    { return nil }
func (testLockBackend) DisableLock() error         { return nil }
func (testLockBackend) UpdateMessage(string) error { return nil }

func TestStudentEmergencyUnlockWithRealControlManager(t *testing.T) {
	ctrl := control.NewManager(control.ManagerOptions{
		Lock: control.LockOptions{
			Backend:           testLockBackend{},
			EmergencyPassword: "secret-pass",
		},
	})
	defer ctrl.Close()
	app := New(Options{
		StudentID: "stu-real",
		Services: Services{
			Control: ctrl,
		},
	})
	app.SetLocked(true, "locked")
	app.unlockPassInput = "secret-pass"
	app.tryEmergencyUnlock()
	if app.ScreenLocked() {
		t.Fatalf("expected real control.Manager EmergencyUnlock(bool, error) to unlock")
	}
}

func TestStudentToastNoticeView(t *testing.T) {
	app := newTestStudentApp()
	app.ShowTeacherNotice("教师端", "请打开实验三文档")
	tester := ui.NewTester(app.ToastView, 320, 116)
	tester.Frame()
	if !tester.HasText("课堂通知") || !tester.HasText("请打开实验三文档") {
		t.Fatalf("expected toast notification to render teacher notice")
	}
}

func TestStudentConcurrentStateTransitionsAndView(t *testing.T) {
	app := newTestStudentApp()
	tester := ui.NewTester(app.View, 920, 560)
	tester.Frame()

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			app.StartBroadcast(i%2 == 0)
			app.SetBroadcastWindowed(i%3 == 0)
			app.OpenMode(ModeCheckIn)
			app.ShowFloatingBar()
			app.SetLocked(i%5 == 0, "lock")
		}
	}()

	go func() {
		wg.Wait()
		close(done)
	}()

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case <-done:
			return
		case <-deadline.C:
			t.Fatalf("timed out during concurrent student state transitions and View()")
		default:
			tester.Frame()
			runtime.Gosched()
		}
	}
}

func TestStudentFileReceiveWindowAndRetry(t *testing.T) {
	tr := demo.NewTransport("1", 1)
	app := New(Options{
		StudentID:   "stu-01",
		StudentNo:   "202601",
		MachineName: "LAB-PC01",
		Channel:     "1",
		Services: Services{
			Transport: tr,
			Media:     demo.NewMedia(),
			Control:   demo.NewControl(),
			Classroom: demo.NewClassroom(),
			Files:     demo.NewFileTransfer(),
			Exam:      demo.NewExam(),
		},
	})

	app.RecordIncomingFile(core.TransferTask{
		ID:         "rx-1",
		Direction:  core.TransferSend,
		FileName:   "lesson1.pdf",
		LocalPath:  `C:\Users\Student\Desktop\lesson1.pdf`,
		RemotePath: "Desktop",
		TotalBytes: 1000,
		DoneBytes:  400,
		SHA256:     "abc123",
		Status:     "running",
	})
	app.RecordIncomingFile(core.TransferTask{
		ID:         "rx-2",
		Direction:  core.TransferSend,
		FileName:   "slides.pptx",
		LocalPath:  `C:\Users\Student\Desktop\slides.pptx`,
		RemotePath: "Desktop",
		TotalBytes: 2000,
		DoneBytes:  500,
		SHA256:     "def456",
		Status:     "failed",
		Error:      "connection reset",
	})

	tester := ui.NewTester(app.FileReceiveView, 520, 420)
	tester.Frame()

	if _, ok := tester.Find("lesson1.pdf"); !ok {
		t.Fatalf("expected lesson1.pdf in FileReceiveView")
	}
	if _, ok := tester.Find("slides.pptx"); !ok {
		t.Fatalf("expected slides.pptx in FileReceiveView")
	}
	if err := tester.Click("重试"); err != nil {
		t.Fatalf("expected clickable 重试 button, got err: %v", err)
	}
	tester.Frame()

	foundRetry := false
	for _, env := range tr.Broadcasts() {
		if env.Type == protocol.MsgFileRetryReq {
			foundRetry = true
			break
		}
	}
	if !foundRetry {
		t.Fatalf("expected MsgFileRetryReq sent when clicking 重试")
	}

	// Subsequent retry task with same FileName+RemotePath should replace failed entry
	app.RecordIncomingFile(core.TransferTask{
		ID:         "rx-2-retry",
		Direction:  core.TransferSend,
		FileName:   "slides.pptx",
		LocalPath:  `C:\Users\Student\Desktop\slides.pptx`,
		RemotePath: "Desktop",
		TotalBytes: 2000,
		DoneBytes:  2000,
		SHA256:     "def456",
		Status:     "completed",
	})
	tester.Frame()

	app.mu.Lock()
	count := len(app.fileReceiveItems)
	app.mu.Unlock()
	if count != 2 {
		t.Fatalf("expected failed item replaced by retry task (total 2 items), got %d", count)
	}
}

func TestStudentFloatingBarHoverFocusAndDock(t *testing.T) {
	app := newTestStudentApp()
	tester := ui.NewTester(app.View, 640, 88)
	tester.Frame()
	if !tester.HasText("举手") {
		t.Fatalf("expected expanded floating bar initially")
	}

	app.mu.Lock()
	app.barDocked = true
	app.mu.Unlock()
	tester.Frame()
	if tester.HasText("举手") {
		t.Fatalf("expected docked floating bar to collapse into top handle")
	}

	// Cursor hover wakes up docked bar
	app.setBarHovered(true)
	tester.Frame()
	if app.barDocked {
		t.Fatalf("expected barDocked=false when hovered")
	}
	if !tester.HasText("举手") {
		t.Fatalf("expected expanded floating bar after hover")
	}

	// Focus also wakes up docked bar
	app.setBarHovered(false)
	app.mu.Lock()
	app.barDocked = true
	app.mu.Unlock()
	app.setBarFocused(true)
	tester.Frame()
	if app.barDocked {
		t.Fatalf("expected barDocked=false when focused")
	}
	if !tester.HasText("举手") {
		t.Fatalf("expected expanded floating bar after focus")
	}
}

func TestStudentSeparateHomeworkAndChatPages(t *testing.T) {
	app := newTestStudentApp()
	tester := ui.NewTester(app.View, 740, 480)
	tester.Frame()

	if !tester.HasText("作业") || !tester.HasText("消息") {
		t.Fatalf("expected separate 作业 and 消息 buttons on floating bar")
	}

	if err := tester.Click("作业"); err != nil {
		t.Fatalf("failed to click 作业: %v", err)
	}
	tester.Frame()
	if !tester.HasText("作业提交") || !tester.HasText("上传作业") || tester.HasText("发送消息") {
		t.Fatalf("expected standalone homework page without chat controls")
	}

	if err := tester.Click("返回"); err != nil {
		t.Fatalf("failed to click 返回 from homework: %v", err)
	}
	tester.Frame()

	if err := tester.Click("消息"); err != nil {
		t.Fatalf("failed to click 消息: %v", err)
	}
	tester.Frame()
	if !tester.HasText("课堂消息") || !tester.HasText("发送消息") || tester.HasText("上传作业") {
		t.Fatalf("expected standalone chat page without homework controls")
	}
}
