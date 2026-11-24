package main

import (
	"context"
	"flag"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"nexlab/internal/apppaths"
	"nexlab/internal/logging"
	"nexlab/internal/platform"
	"nexlab/internal/transport"
	"nexlab/internal/ui/demo"
	"nexlab/internal/ui/student"
)

func main() {
	demoMode := flag.Bool("demo", false, "run student client with built-in demo data")
	verifyUninstall := flag.Bool("verify-uninstall", false, "verify the student exit password for uninstall")
	channel := flag.String("channel", "1", "classroom numeric channel ID")
	teacherAddr := flag.String("teacher", "", "optional explicit teacher KCP address (host:port)")
	studentID := flag.String("id", "", "student ID (defaults to a stable ID derived from this machine)")
	studentNo := flag.String("no", "", "student number (optional)")
	recvDir := flag.String("recv", "", "received files directory (defaults to Desktop)")
	teacherFP := flag.String("teacher-fingerprint", "", "pin the trusted teacher identity fingerprint (for unattended deployment)")
	flag.Parse()
	if *studentID == "" {
		if *demoMode {
			*studentID = "stu-01"
		} else {
			*studentID = defaultStudentID()
		}
	}
	paths := apppaths.Resolve("")
	if *verifyUninstall {
		os.Exit(runUninstallPasswordVerification(filepath.Join(paths.DataDir, "student-exit-password.sha256")))
	}
	appLogger, loggerErr := logging.New(logging.Config{Dir: paths.LogDir, Role: "student"})
	if loggerErr == nil {
		defer appLogger.Close()
		log.SetOutput(appLogger)
		log.SetFlags(0)
	}
	log.Printf("student starting channel=%s id=%s", *channel, *studentID)
	capabilities := platform.Detect()
	log.Printf("platform capabilities: %s", capabilities.Summary())
	log.Printf("platform capabilities detail: %+v", capabilities)

	if *recvDir == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			desktop := filepath.Join(home, "Desktop")
			if info, statErr := os.Stat(desktop); statErr == nil && info.IsDir() {
				*recvDir = desktop
			}
		}
		if *recvDir == "" {
			*recvDir = "received_files"
		}
	}

	if !mygo.App.RequestSingleInstanceLock() {
		return
	}
	mygo.App.SetName("矩联电子教室 MatrixClass - 学生端")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var app *student.App
	var cleanup func()

	if *demoMode {
		app = student.New(student.Options{
			StudentID:        *studentID,
			StudentNo:        *studentNo,
			Channel:          *channel,
			ExitPasswordPath: filepath.Join(paths.DataDir, "student-exit-password.sha256"),
			Services: student.Services{
				Transport: demo.NewTransport(*channel, 1),
				Media:     demo.NewMedia(),
				Control:   demo.NewControl(),
				Classroom: demo.NewClassroom(),
				Files:     demo.NewFileTransfer(),
				Exam:      demo.NewExam(),
			},
		})
	} else {
		trust := transport.NewFileTrust(filepath.Join(paths.DataDir, "trusted-teacher.txt"))
		if *teacherFP != "" {
			if err := trust.SetPinned(*teacherFP); err != nil {
				log.Fatalf("invalid -teacher-fingerprint: %v", err)
			}
		}
		rt, err := NewRuntimeWithTrust(*channel, *studentID, *studentNo, *teacherAddr, *recvDir, trust)
		if err != nil {
			log.Fatalf("init student runtime: %v", err)
		}
		startStudent := func() {
			rt.App.SetStatusNote("正在搜索频道 ID: " + rt.App.Channel() + " ...")
			go func() {
				for ctx.Err() == nil {
					if err := rt.Start(ctx); err == nil {
						return
					}
					timer := time.NewTimer(1 * time.Second)
					select {
					case <-ctx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
				}
			}()
		}
		if !rt.App.InitialSetupRequired() {
			startStudent()
		}
		app = rt.App
		cleanup = func() { _ = rt.Close() }
	}
	if cleanup != nil {
		defer cleanup()
	}

	mygo.App.WhenReady(func() {
		wa := mygo.Screen.PrimaryDisplay().WorkArea
		barW := 740
		barX := wa.X + (wa.Width-barW)/2
		if barX < wa.X {
			barX = wa.X
		}
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:         "矩联电子教室 MatrixClass - 学生端",
			Width:         barW,
			Height:        92,
			X:             barX,
			Y:             wa.Y,
			MinWidth:      560,
			MinHeight:     8,
			Frameless:     true,
			Transparent:   true,
			DisableShadow: true,
			AlwaysOnTop:   true,
			Content:       ui.View(app.View),
		})
		app.AttachWindow(win)
		app.InstallTray()
		app.EnsureExitPasswordConfigured()
	})

	mygo.App.OnWindowAllClosed(func() {
		if app != nil && !app.ExitAuthorized() {
			app.RequestExit()
			return
		}
		cancel()
		mygo.App.Quit()
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

func runUninstallPasswordVerification(passwordPath string) int {
	app := student.New(student.Options{ExitPasswordPath: passwordPath})
	if app.ExitPasswordConfigured() == false {
		return 2
	}
	mygo.App.SetName("矩联电子教室 MatrixClass - 卸载验证")

	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:       "矩联电子教室 MatrixClass - 卸载验证",
			Width:       420,
			Height:      250,
			SkipTaskbar: true,
			Content:     ui.View(app.View),
		})
		app.AttachWindow(win)
		win.Hide()
		app.RequestExit()
	})
	mygo.App.OnWindowAllClosed(func() {})
	if err := mygo.App.Run(); err != nil {
		return 2
	}
	if app.ExitAuthorized() {
		return 0
	}
	return 1
}
