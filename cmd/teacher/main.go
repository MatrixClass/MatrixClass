package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"nexlab/internal/classroom"
	"nexlab/internal/control"
	"nexlab/internal/core"
	"nexlab/internal/exam"
	"nexlab/internal/filetransfer"
	"nexlab/internal/media"
	"nexlab/internal/protocol"
	"nexlab/internal/transport"
	"nexlab/internal/ui/demo"
	"nexlab/internal/ui/teacher"
	"nexlab/internal/ui/theme"
)

type Runtime struct {
	App       *teacher.App
	Transport *transport.Manager
	Media     *media.Manager
	Control   *control.Manager
	Classroom *classroom.Service
	Files     *filetransfer.Service
	Exam      *exam.Service

	channel   string
	teacherID string
}

func NewRuntime(channel, teacherID, teacherName, archiveDir string) (*Runtime, error) {
	tr := transport.NewManager()
	md, err := media.NewManager(media.ManagerOptions{PreferH264MFT: true})
	if err != nil {
		return nil, err
	}
	ctrl := control.NewManager(control.ManagerOptions{})
	cr := classroom.NewService()
	ex := exam.NewService()

	ft := filetransfer.NewService(filetransfer.Config{
		ArchiveDir: archiveDir,
		ReceiveDir: archiveDir,
		SessionLookup: func(peerID string) (core.Session, bool) {
			for _, s := range tr.Sessions() {
				if s.ID() == peerID {
					return s, true
				}
			}
			return nil, false
		},
		StudentResolver: func(studentID string) (filetransfer.StudentMeta, bool) {
			for _, st := range cr.ListStudents() {
				if st.ID == studentID {
					return filetransfer.StudentMeta{
						ID:        st.ID,
						StudentNo: st.StudentNo,
						Name:      st.Name,
					}, true
				}
			}
			return filetransfer.StudentMeta{}, false
		},
	})

	app := teacher.New(teacher.Options{
		TeacherID:   teacherID,
		TeacherName: teacherName,
		Channel:     channel,
		Services: teacher.Services{
			Transport: tr,
			Media:     md,
			Control:   ctrl,
			Classroom: cr,
			Files:     ft,
			Exam:      ex,
		},
	})

	rt := &Runtime{
		App:       app,
		Transport: tr,
		Media:     md,
		Control:   ctrl,
		Classroom: cr,
		Files:     ft,
		Exam:      ex,
		channel:   channel,
		teacherID: teacherID,
	}
	rt.wireHandlers()
	return rt, nil
}

func (rt *Runtime) wireHandlers() {
	rt.Classroom.OnChange(func() {
		rt.App.Invalidate()
	})
	rt.Exam.OnChange(func() {
		rt.App.Invalidate()
	})
	rt.Transport.OnSession(func(sess core.Session) {
		rt.Files.RegisterSession(sess.ID(), sess)
		go rt.acceptFileStreams(sess)
	})
	rt.Transport.OnEnvelope(func(sess core.Session, env protocol.Envelope) {
		switch env.Type {
		case protocol.MsgStudentRegister:
			var st core.StudentInfo
			if err := json.Unmarshal(env.Payload, &st); err == nil {
				if st.ID == "" {
					st.ID = sess.ID()
				}
				st.IP = sess.RemoteAddr()
				st.Status = core.StatusOnline
				st.LastSeen = time.Now()
				rt.Classroom.UpsertStudent(st)
			}
		case protocol.MsgCheckInSubmit:
			var req struct {
				StudentID string `json:"student_id"`
				Name      string `json:"name"`
				StudentNo string `json:"student_no"`
			}
			if err := json.Unmarshal(env.Payload, &req); err == nil {
				if req.StudentID == "" {
					req.StudentID = env.SenderID
				}
				rt.Classroom.SubmitCheckIn(req.StudentID, req.Name, req.StudentNo)
			}
		case protocol.MsgHandRaise:
			var req struct {
				StudentID string `json:"student_id"`
				Raised    bool   `json:"raised"`
			}
			if err := json.Unmarshal(env.Payload, &req); err == nil {
				if req.StudentID == "" {
					req.StudentID = env.SenderID
				}
				rt.Classroom.RaiseHand(req.StudentID, req.Raised)
			}
		case protocol.MsgChat:
			var msg core.ChatMessage
			if err := json.Unmarshal(env.Payload, &msg); err == nil {
				if msg.CreatedAt.IsZero() {
					msg.CreatedAt = time.Now()
				}
				rt.Classroom.AddChat(msg)
			}
		case protocol.MsgExamAnswer:
			var sub core.ExamSubmission
			if err := json.Unmarshal(env.Payload, &sub); err == nil {
				rt.Exam.SubmitAnswer(sub)
			}
		}
		rt.App.Invalidate()
	})
}

func (rt *Runtime) acceptFileStreams(sess core.Session) {
	type streamAccepter interface {
		AcceptStream() (protocol.StreamKind, io.ReadWriteCloser, error)
	}
	ac, ok := sess.(streamAccepter)
	if !ok {
		return
	}
	for {
		kind, stream, err := ac.AcceptStream()
		if err != nil {
			return
		}
		if kind == protocol.StreamFile {
			go func(st io.ReadWriteCloser) {
				_ = rt.Files.HandleStream(context.Background(), st)
				rt.App.Invalidate()
			}(stream)
		} else {
			_ = stream.Close()
		}
	}
}

func (rt *Runtime) Start(ctx context.Context, listenAddr string, teacherName string) error {
	beacon := protocol.DiscoveryBeacon{
		Role:        string(core.RoleTeacher),
		TeacherID:   rt.teacherID,
		TeacherName: teacherName,
		Channel:     rt.channel,
		MulticastIP: "239.255.42.10",
		McastPort:   39101,
		Version:     "0.1.0",
	}
	if err := rt.Transport.StartTeacher(ctx, listenAddr, beacon); err != nil {
		return err
	}
	go rt.broadcastLoop(ctx)
	return nil
}

func (rt *Runtime) broadcastLoop(ctx context.Context) {
	ticker := time.NewTicker(66 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !rt.App.Broadcasting() {
				continue
			}
			frame, err := rt.Media.CaptureScreen()
			if err != nil {
				continue
			}
			encoded, err := rt.Media.EncodeFrame(frame, int(rt.App.MonitorQual()))
			if err != nil {
				continue
			}
			_ = rt.Transport.BroadcastMulticast(encoded)
		}
	}
}

func (rt *Runtime) Close() error {
	_ = rt.Control.Close()
	return rt.Transport.Close()
}

func main() {
	demoMode := flag.Bool("demo", false, "run teacher console with built-in demo classroom data")
	channel := flag.String("channel", "301机房", "classroom channel name")
	listenAddr := flag.String("listen", ":39100", "KCP listen address")
	teacherLabel := "教师端"
	archiveDir := flag.String("archive", "submissions", "homework archive directory")
	flag.Parse()

	if !mygo.App.RequestSingleInstanceLock() {
		return
	}
	mygo.App.SetName("矩联电子教室 MatrixClass 教师端")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var app *teacher.App
	var cleanup func()

	if *demoMode {
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
		rt, err := NewRuntime(*channel, "teacher-01", teacherLabel, *archiveDir)
		if err != nil {
			log.Fatalf("init teacher runtime: %v", err)
		}
		if err := rt.Start(ctx, *listenAddr, teacherLabel); err != nil {
			log.Fatalf("start teacher transport: %v", err)
		}
		app = rt.App
		cleanup = func() { _ = rt.Close() }
	}
	if cleanup != nil {
		defer cleanup()
	}

	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:     "矩联电子教室 MatrixClass - 教师端 [" + *channel + "]",
			Width:     1320,
			Height:    840,
			MinWidth:  1024,
			MinHeight: 680,
			StateKey:  "nexlab-teacher-main",
			Content:   ui.View(app.View),
		})
		app.AttachWindow(win)
		_, _ = mygo.NewTray(mygo.TrayOptions{
			ID:      "nexlab-teacher-tray",
			Icon:    theme.AppIconPNG(true),
			ToolTip: "矩联电子教室 MatrixClass 教师端",
			Menu:    app.BuildMenu(),
		})
	})

	mygo.App.OnWindowAllClosed(func() {
		cancel()
		mygo.App.Quit()
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
