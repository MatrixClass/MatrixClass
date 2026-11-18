package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"sync"
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
	studentNo   string
	machineName string
	macAddr     string
	channel     string

	streamMu     sync.Mutex
	monitorStop  context.CancelFunc
	remoteStop   context.CancelFunc
	demoStop     context.CancelFunc
	demoActive   bool
	ctx          context.Context
}

type videoStreamPacket struct {
	Kind string `json:"kind"`
	Data []byte `json:"data"`
}

func NewRuntime(channel, studentID, studentNo, teacherAddr, recvDir string) (*Runtime, error) {
	host, _ := os.Hostname()
	if host == "" {
		host = "LAB-PC"
	}
	mac := localPrimaryMAC()
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
		ProxyAddr:      "127.0.0.1:0",
	})
	cr := classroom.NewService()
	ex := exam.NewService()
	var appRef *student.App
	ft := filetransfer.NewService(filetransfer.Config{
		ReceiveDir: recvDir,
		ArchiveDir: recvDir,
		OnProgress: func(task core.TransferTask) {
			if appRef != nil {
				if task.Status == "completed" {
					appRef.SetStatusNote("文件传输完成: " + task.FileName)
				} else {
					appRef.Invalidate()
				}
			}
		},
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
	appRef = app

	rt := &Runtime{
		App:         app,
		Transport:   tr,
		Media:       md,
		Control:     ctrl,
		Classroom:   cr,
		Files:       ft,
		Exam:        ex,
		studentID:   studentID,
		studentNo:   studentNo,
		machineName: host,
		macAddr:     mac,
		channel:     channel,
		ctx:         context.Background(),
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
			StudentNo:   rt.studentNo,
			MachineName: rt.machineName,
			MAC:         rt.macAddr,
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
		rt.streamMu.Lock()
		isDemoSrc := rt.demoActive
		rt.streamMu.Unlock()
		if isDemoSrc {
			return
		}
		frame, err := rt.Media.DecodeFrame(data)
		if err == nil && frame != nil {
			rt.App.UpdateVideoFrame(frame)
		}
	})

	rt.Transport.OnEnvelope(func(sess core.Session, env protocol.Envelope) {
		switch env.Type {
		case protocol.MsgPing:
			rt.Control.NotifyTeacherHeartbeat()
			pong, err := protocol.NewEnvelope(protocol.MsgPong, rt.studentID, json.RawMessage(env.Payload))
			if err == nil {
				pong.Channel = rt.channel
				pong.Timestamp = time.Now().UnixMilli()
				_ = sess.SendEnvelope(pong)
			}
		case protocol.MsgBroadcastStart:
			var cfg core.BroadcastConfig
			_ = json.Unmarshal(env.Payload, &cfg)
			rt.App.StartBroadcast(cfg.Mode == core.BroadcastWindowed)
		case protocol.MsgBroadcastStop:
			rt.App.ShowFloatingBar()
		case protocol.MsgVoiceControl:
			var req struct {
				Enabled bool `json:"enabled"`
			}
			if json.Unmarshal(env.Payload, &req) == nil {
				if req.Enabled {
					rt.App.SetStatusNote("教师端语音广播进行中")
				} else {
					rt.App.SetStatusNote("已连接频道: " + rt.channel)
				}
			}
		case protocol.MsgTheatreControl:
			var ts core.TheatreState
			if json.Unmarshal(env.Payload, &ts) == nil {
				if ts.Playing {
					rt.App.SetStatusNote("网络影院同步播放: " + ts.Media)
				} else {
					rt.App.SetStatusNote("网络影院已暂停")
				}
			}
		case protocol.MsgAudioFrame:
			var encoded []byte
			if json.Unmarshal(env.Payload, &encoded) == nil && len(encoded) > 0 {
				if af, err := rt.Media.DecodeAudioFrame(encoded); err == nil {
					_ = rt.Media.PlayAudioFrame(af)
				}
			}
		case protocol.MsgMonitorControl:
			var req struct {
				Active bool `json:"active"`
				core.MonitorConfig
			}
			if json.Unmarshal(env.Payload, &req) == nil {
				if len(req.TargetIDs) > 0 && !containsID(req.TargetIDs, rt.studentID) {
					return
				}
				if req.FPS > 0 {
					w, h := req.Width, req.Height
					if w <= 0 {
						w = 320
					}
					if h <= 0 {
						h = 180
					}
					rt.startMonitorStream(w, h, req.FPS, req.Quality)
					rt.App.SetMonitoring(true)
				} else {
					rt.stopMonitorStream()
					rt.App.SetMonitoring(false)
				}
			}
		case protocol.MsgRemoteControlReq:
			var req struct {
				StudentID string `json:"student_id"`
				Enabled   bool   `json:"enabled"`
			}
			if json.Unmarshal(env.Payload, &req) == nil {
				if req.StudentID != "" && req.StudentID != rt.studentID {
					return
				}
				rt.App.SetRemoteControlled(req.Enabled)
				if req.Enabled {
					rt.startRemoteStream()
				} else {
					rt.stopRemoteStream()
				}
			}
		case protocol.MsgStudentDemoReq:
			var req struct {
				Active    bool   `json:"active"`
				StudentID string `json:"student_id"`
			}
			if json.Unmarshal(env.Payload, &req) == nil {
				if req.StudentID != "" && req.StudentID != rt.studentID {
					return
				}
				if req.Active {
					rt.startDemoStream()
					rt.App.SetStatusNote("正在向全班进行学生演示")
				} else {
					rt.stopDemoStream()
					rt.App.SetStatusNote("学生演示已结束")
				}
			}
		case protocol.MsgHandClear:
			rt.App.SetHandRaised(false)
			rt.App.SetStatusNote("教师已批准您的举手演示")
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
				rt.App.SetStatusNote("新消息: " + msg.Text)
				rt.App.Invalidate()
			}
		}
	})
}

func (rt *Runtime) startMonitorStream(maxW, maxH, fps, quality int) {
	rt.stopMonitorStream()
	if fps <= 0 {
		fps = 10
	}
	if fps > 30 {
		fps = 30
	}
	if quality <= 0 {
		quality = 65
	}
	ctx, cancel := context.WithCancel(rt.ctx)
	rt.streamMu.Lock()
	rt.monitorStop = cancel
	rt.streamMu.Unlock()
	go rt.runScreenStream(ctx, "monitor", maxW, maxH, fps, quality)
}

func (rt *Runtime) stopMonitorStream() {
	rt.streamMu.Lock()
	cancel := rt.monitorStop
	rt.monitorStop = nil
	rt.streamMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (rt *Runtime) startRemoteStream() {
	rt.stopRemoteStream()
	ctx, cancel := context.WithCancel(rt.ctx)
	rt.streamMu.Lock()
	rt.remoteStop = cancel
	rt.streamMu.Unlock()
	go rt.runScreenStream(ctx, "remote", 1280, 720, 15, 75)
}

func (rt *Runtime) stopRemoteStream() {
	rt.streamMu.Lock()
	cancel := rt.remoteStop
	rt.remoteStop = nil
	rt.streamMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (rt *Runtime) startDemoStream() {
	rt.stopDemoStream()
	ctx, cancel := context.WithCancel(rt.ctx)
	rt.streamMu.Lock()
	rt.demoActive = true
	rt.demoStop = cancel
	rt.streamMu.Unlock()
	go rt.runScreenStream(ctx, "demo", 1280, 720, 15, 75)
}

func (rt *Runtime) stopDemoStream() {
	rt.streamMu.Lock()
	rt.demoActive = false
	cancel := rt.demoStop
	rt.demoStop = nil
	rt.streamMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (rt *Runtime) runScreenStream(ctx context.Context, kind string, maxW, maxH, fps, quality int) {
	codec, err := media.NewHybridVideoCodec(false)
	if err != nil {
		return
	}
	defer codec.Close()

	interval := time.Second / time.Duration(fps)
	if interval < 33*time.Millisecond {
		interval = 33 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var seq uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			frame, err := rt.Media.CaptureScreenMax(maxW, maxH)
			if err != nil || frame == nil {
				continue
			}
			seq++
			frame.Seq = seq
			frame.KeyFrame = seq == 1 || seq%60 == 0
			encoded, err := codec.Encode(frame, quality)
			if err != nil {
				continue
			}
			env, err := protocol.NewEnvelope(protocol.MsgVideoFrame, rt.studentID, videoStreamPacket{
				Kind: kind,
				Data: encoded,
			})
			if err != nil {
				continue
			}
			env.Channel = rt.channel
			env.Timestamp = time.Now().UnixMilli()
			_ = rt.Transport.BroadcastControl(env, nil)
		}
	}
}

func containsID(ids []string, target string) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

func localPrimaryMAC() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if len(iface.HardwareAddr) > 0 {
			return iface.HardwareAddr.String()
		}
	}
	return ""
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
	rt.streamMu.Lock()
	rt.ctx = ctx
	rt.streamMu.Unlock()
	if _, err := rt.Control.Start(ctx); err != nil {
		return err
	}
	return rt.Transport.StartStudent(ctx, rt.channel)
}

func (rt *Runtime) Close() error {
	rt.stopMonitorStream()
	rt.stopRemoteStream()
	rt.stopDemoStream()
	_ = rt.Media.Close()
	_ = rt.Control.Close()
	return rt.Transport.Close()
}

func main() {
	demoMode := flag.Bool("demo", false, "run student client with built-in demo data")
	channel := flag.String("channel", "301机房", "classroom channel name")
	teacherAddr := flag.String("teacher", "", "optional explicit teacher KCP address (host:port)")
	studentID := flag.String("id", "stu-01", "student ID")
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
		rt, err := NewRuntime(*channel, *studentID, *studentNo, *teacherAddr, *recvDir)
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
