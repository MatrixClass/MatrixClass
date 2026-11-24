package main

import (
	"context"
	"flag"
	"log"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"nexlab/internal/apppaths"
	"nexlab/internal/logging"
	"nexlab/internal/platform"
	"nexlab/internal/store"
	"nexlab/internal/transport"
	"nexlab/internal/ui/demo"
	"nexlab/internal/ui/teacher"
)

// version is stamped at link time with -X main.version by the build scripts.
var version = ""

func main() {
	demoMode := flag.Bool("demo", false, "run teacher console with built-in demo classroom data")
	channel := flag.String("channel", "1", "classroom numeric channel ID")
	listenAddr := flag.String("listen", ":39100", "KCP listen address")
	teacherLabel := "教师端"
	archiveDir := flag.String("archive", "", "homework archive directory (defaults to Downloads)")
	flag.Parse()
	if version != "" {
		teacher.AppVersion = version
	}
	// Portable folder next to the exe when writable, otherwise per-user
	// standard dirs (macOS .app bundles, Linux /opt or /usr installs).
	paths := apppaths.Resolve("nexlab-teacher.db")
	appLogger, loggerErr := logging.New(logging.Config{Dir: paths.LogDir, Role: "teacher"})
	if loggerErr == nil {
		defer appLogger.Close()
		log.SetOutput(appLogger)
		log.SetFlags(0)
	}
	log.Printf("teacher starting channel=%s listen=%s", *channel, *listenAddr)
	capabilities := platform.Detect()
	log.Printf("platform capabilities: %s", capabilities.Summary())
	log.Printf("platform capabilities detail: %+v", capabilities)

	if *archiveDir == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			*archiveDir = filepath.Join(home, "Downloads")
		} else {
			*archiveDir = "submissions"
		}
	}

	if !mygo.App.RequestSingleInstanceLock() {
		return
	}
	mygo.App.SetName("矩联电子教室 MatrixClass - 教师端")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var app *teacher.App
	var rt *Runtime
	var winRef *mygo.Window
	windowTitle := "矩联电子教室 MatrixClass - 教师端"

	if *demoMode {
		windowTitle += " [" + *channel + "]"
		app = teacher.New(teacher.Options{
			TeacherID:   "teacher-01",
			TeacherName: teacherLabel,
			Channel:     *channel,
			Services: teacher.Services{
				Transport: demo.NewTransport(*channel, 12),
				Media:     demo.NewMedia(),
				Control:   demo.NewControl(),
				Classroom: demo.NewClassroom(),
				Files:     demo.NewFileTransfer(),
				Exam:      demo.NewExam(),
			},
		})
	} else {
		dbPath := filepath.Join(paths.DataDir, "nexlab-teacher.db")
		log.Printf("teacher data dir=%s log dir=%s portable=%v", paths.DataDir, paths.LogDir, paths.Portable)
		dbStore, dbErr := store.Open(dbPath)
		if dbErr != nil {
			log.Printf("open sqlite store %s failed: %v", dbPath, dbErr)
		}

		var err error
		identityPath := filepath.Join(paths.DataDir, "teacher-identity.key")
		identity, idErr := transport.LoadOrCreateTeacherIdentity(identityPath)
		if idErr != nil {
			log.Fatalf("load teacher identity %s: %v", identityPath, idErr)
		}
		log.Printf("teacher identity fingerprint=%s", identity.Fingerprint())
		rt, err = NewRuntimeWithStore(*channel, "teacher-01", teacherLabel, *archiveDir, dbStore, true, func(chosen string) {
			rt.SetChannel(chosen)
			if winRef != nil {
				winRef.SetTitle("矩联电子教室 MatrixClass - 教师端 [" + chosen + "]")
			}
			if startErr := rt.Start(ctx, *listenAddr, teacherLabel); startErr != nil {
				log.Printf("start teacher transport: %v", startErr)
				rt.App.SetStatusNote("课堂网络启动失败，正在重试：" + startErr.Error())
				go retryTeacherStart(ctx, rt, *listenAddr, teacherLabel)
			}
		}, identity)
		if err != nil {
			log.Fatalf("init teacher runtime: %v", err)
		}
		app = rt.App
	}

	// Shut down the UI first (stops UI-owned background tasks), then the
	// runtime (transport, media, store), so nothing uses a closed service.
	defer func() {
		if closeErr := app.Close(); closeErr != nil {
			log.Printf("close teacher app: %v", closeErr)
		}
		if rt != nil {
			if closeErr := rt.Close(); closeErr != nil {
				log.Printf("close teacher runtime: %v", closeErr)
			}
		}
	}()

	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:     windowTitle,
			Width:     1280,
			Height:    800,
			MinWidth:  960,
			MinHeight: 600,
			StateKey:  "nexlab-teacher-main-v3",
			Content:   ui.View(app.View),
		})
		winRef = win
		app.AttachWindow(win)
		app.InstallTray()
	})

	mygo.App.OnWindowAllClosed(func() {
		cancel()
		mygo.App.Quit()
	})

	if err := mygo.App.Run(); err != nil {
		log.Printf("teacher ui run: %v", err)
	}
}
