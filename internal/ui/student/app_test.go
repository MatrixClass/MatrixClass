package student

import (
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"nexlab/internal/ui/demo"
	"nexlab/internal/control"
)

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

func TestStudentBarCheckInExamAndLockViews(t *testing.T) {
	app := newTestStudentApp()
	tester := ui.NewTester(app.View, 920, 560)
	tester.Frame()

	if !tester.HasText("举手提问") || !tester.HasText("点名签到") {
		t.Fatalf("expected floating bar controls")
	}

	if err := tester.Click("举手提问"); err != nil {
		t.Fatalf("click 举手提问 failed: %v", err)
	}
	tester.Frame()
	if !app.HandRaised() {
		t.Fatalf("expected handRaised=true")
	}

	// Check-in flow
	if err := tester.Click("点名签到"); err != nil {
		t.Fatalf("click 点名签到 failed: %v", err)
	}
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

	// Exam flow
	if err := tester.Click("随堂测验"); err != nil {
		t.Fatalf("click 随堂测验 failed: %v", err)
	}
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

	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-done:
			return
		case <-deadline:
			t.Fatalf("timed out during concurrent student state transitions and View()")
		default:
			tester.Frame()
			time.Sleep(5 * time.Millisecond)
		}
	}
}
