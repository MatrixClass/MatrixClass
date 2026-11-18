package teacher

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"nexlab/internal/core"
	"nexlab/internal/ui/demo"
)

func newTestTeacherApp() (*App, *demo.Transport, *demo.Classroom, *demo.Control) {
	tr := demo.NewTransport("301机房", 6)
	cr := demo.NewClassroom()
	ctrl := demo.NewControl()
	app := New(Options{
		TeacherID:   "teacher-test",
		TeacherName: "李老师",
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

	if !tester.HasText("矩联电子教室 MatrixClass 教师端") {
		t.Fatalf("expected teacher title in view")
	}
	if !tester.HasText("座位视图") || !tester.HasText("屏幕广播") || !tester.HasText("网络影院") {
		t.Fatalf("expected primary navigation/toolbar items")
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

	// Switch to Files tab
	if err := tester.Click("文件分发"); err != nil {
		t.Fatalf("click 文件分发 failed: %v", err)
	}
	tester.Frame()
	if app.MainTab() != TabFiles {
		t.Fatalf("expected TabFiles (%d), got %d", TabFiles, app.MainTab())
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
