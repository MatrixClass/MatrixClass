package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"os"
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
	"nexlab/internal/ui/student"
)

type Runtime struct {
	App       *student.App
	Transport *transport.Manager
	Media     *media.Manager
	Control   *control.Manager
	Classroom *classroom.Service
	Files     *filetransfer.Service
	Exam      *exam.Service

	studentID   string
	studentName string
	studentNo   string
	machineName string
	channel     string
}

func NewRuntime(channel, studentID, studentName, studentNo, teacherAddr, recvDir string) (*Runtime, error) {
	host, _ := os.Hostname()
	if host == "" {
		host = "LAB-PC"
	}
	var opts []transport.ManagerOption
	if teacherAddr != "" {
		opts = append(opts, transport.WithTeacherAddr(teacherAddr))
	}
	tr := transport.NewManager(opts...)
	md, err := media.NewManager(media.ManagerOptions{PreferH264MFT: false})
	if err != nil {
		return nil, err
	}
	ctrl := control.NewManager(control.ManagerOptions{
		EnforceOnApply: true,
	})
	cr := classroom.NewService()
	ex := exam.NewService()
	ft := filetransfer.NewService(filetransfer.Config{
		ReceiveDir: recvDir,
		ArchiveDir: recvDir,
		OnAutoOpen: func(path string) {
			_ = mygo.Shell.OpenPath(path)
		},
		SessionLookup: func(_ string) (core.Session, bool) {
			for _, s := range tr.Sessions() {
				return s, true
			}
			return nil, false
		},
	})

	app := student.New(student.Options{
		StudentID:   studentID,
		StudentName: studentName,
		StudentNo:   studentNo,
		MachineName: host,
		Channel:     channel,
		Services: student.Services{
			Transport: tr,
			Media:     md,
			Control:   ctrl,
			Classroom: cr,
			Files:     ft,
			Exam:      ex,
		},
	})

	rt := &Runtime{
		App:         app,
		Transport:   tr,
		Media:       md,
		Control:     ctrl,
		Classroom:   cr,
		Files:       ft,
		Exam:        ex,
		studentID:   studentID,
		studentName: studentName,
		studentNo:   studentNo,
		machineName: host,
		channel:     channel,
	}
	rt.wireHandlers()
	return rt, nil
}

func (rt *Runtime) wireHandlers() {
	rt.Transport.OnSession(func(sess core.Session) {
		rt.Files.SetTeacherSession(sess)
		go rt.acceptFileStreams(sess)
		env, err := protocol.NewEnvelope(protocol.MsgStudentRegister, rt.studentID, core.StudentInfo{
			ID:          rt.studentID,
			Name:        rt.studentName,
			StudentNo:   rt.studentNo,
			MachineName: rt.machineName,
			Channel:     rt.channel,
			Status:      core.StatusOnline,
		})
		if err == nil {
			env.Channel = rt.channel
			env.Timestamp = time.Now().UnixMilli()
			_ = sess.SendEnvelope(env)
		}
	})

	rt.Transport.OnMulticastData(func(data []byte) {
		frame, err := rt.Media.DecodeFrame(data)
		if err == nil && frame != nil {
			rt.App.UpdateVideoFrame(frame)
		}
	})

	rt.Transport.OnEnvelope(func(_ core.Session, env protocol.Envelope) {
		switch env.Type {
		case protocol.MsgBroadcastStart:
			rt.App.OpenMode(student.ModeBroadcast)
		case protocol.MsgBroadcastStop:
			rt.App.ShowFloatingBar()
		case protocol.MsgScreenLock:
			var req struct {
				Locked  bool   `json:"locked"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(env.Payload, &req); err == nil {
				rt.App.SetLocked(req.Locked, req.Message)
			}
		case protocol.MsgPolicySync:
			var rule core.PolicyRule
			if err := json.Unmarshal(env.Payload, &rule); err == nil {
				_ = rt.Control.ApplyPolicy(rule)
			}
		case protocol.MsgPowerCommand:
			var req struct {
				Action core.PowerAction `json:"action"`
				MAC    string           `json:"mac"`
			}
			if err := json.Unmarshal(env.Payload, &req); err == nil {
				_ = rt.Control.ExecutePower(req.Action, req.MAC)
			}
		case protocol.MsgRemoteInputEvent:
			var ev core.RemoteInputEvent
			if err := json.Unmarshal(env.Payload, &ev); err == nil {
				_ = rt.Control.InjectInput(ev)
			}
		case protocol.MsgCheckInStart:
			rt.App.OpenMode(student.ModeCheckIn)
		case protocol.MsgExamPublish:
			var paper core.ExamPaper
			if err := json.Unmarshal(env.Payload, &paper); err == nil {
				rt.Exam.PublishPaper(paper)
				rt.App.OpenMode(student.ModeExam)
			}
		case protocol.MsgChat:
			var msg core.ChatMessage
			if err := json.Unmarshal(env.Payload, &msg); err == nil {
				rt.Classroom.AddChat(msg)
				rt.App.Invalidate()
			}
		}
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

func (rt *Runtime) Start(ctx context.Context) error {
	if _, err := rt.Control.Start(ctx); err != nil {
		return err
	}
	return rt.Transport.StartStudent(ctx, rt.channel)
}

func (rt *Runtime) Close() error {
	_ = rt.Control.Close()
	return rt.Transport.Close()
}

func main() {
	demoMode := flag.Bool("demo", false, "run student client with built-in demo data")
	channel := flag.String("channel", "301机房", "classroom channel name")
	teacherAddr := flag.String("teacher", "", "optional explicit teacher KCP address (host:port)")
	studentID := flag.String("id", "stu-01", "student ID")
	studentName := flag.String("name", "王小明", "student display name")
	studentNo := flag.String("no", "202601", "student number")
	recvDir := flag.String("recv", "received_files", "received files directory")
	flag.Parse()

	if !mygo.App.RequestSingleInstanceLock() {
		return
	}
	mygo.App.SetName("矩联电子教室 MatrixClass 学生端")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var app *student.App
	var cleanup func()

	if *demoMode {
		app = student.New(student.Options{
			StudentID:   *studentID,
			StudentName: *studentName,
			StudentNo:   *studentNo,
			Channel:     *channel,
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
		rt, err := NewRuntime(*channel, *studentID, *studentName, *studentNo, *teacherAddr, *recvDir)
		if err != nil {
			log.Fatalf("init student runtime: %v", err)
		}
		if err := rt.Start(ctx); err != nil {
			log.Fatalf("start student transport: %v", err)
		}
		app = rt.App
		cleanup = func() { _ = rt.Close() }
	}
	if cleanup != nil {
		defer cleanup()
	}

	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:       "矩联电子教室 MatrixClass 学生端",
			Width:       780,
			Height:      96,
			Frameless:   true,
			AlwaysOnTop: true,
			StateKey:    "nexlab-student-bar",
			Content:     ui.View(app.View),
		})
		app.AttachWindow(win)
		app.InstallTray()
	})

	mygo.App.OnWindowAllClosed(func() {
		cancel()
		mygo.App.Quit()
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
