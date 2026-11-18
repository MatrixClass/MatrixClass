package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"sync"
	"sync/atomic"
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

type videoStreamPacket struct {
	Kind string `json:"kind"`
	Data []byte `json:"data"`
}

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

	decMu           sync.Mutex
	studentDecoders map[string]*media.HybridVideoCodec

	audioMu   sync.Mutex
	audioMode string // "", "voice", "loopback"

	forceKeyFrame atomic.Bool
}

func NewRuntime(channel, teacherID, teacherName, archiveDir string) (*Runtime, error) {
	tr := transport.NewManager()
	md, err := media.NewManager(media.ManagerOptions{PreferH264MFT: false})
	if err != nil {
		return nil, err
	}
	ctrl := control.NewManager(control.ManagerOptions{})
	cr := classroom.NewService()
	ex := exam.NewService()

	var appRef *teacher.App
	ft := filetransfer.NewService(filetransfer.Config{
		ArchiveDir: archiveDir,
		ReceiveDir: archiveDir,
		OnProgress: func(_ core.TransferTask) {
			if appRef != nil {
				appRef.Invalidate()
			}
		},
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
	appRef = app

	rt := &Runtime{
		App:             app,
		Transport:       tr,
		Media:           md,
		Control:         ctrl,
		Classroom:       cr,
		Files:           ft,
		Exam:            ex,
		channel:         channel,
		teacherID:       teacherID,
		studentDecoders: make(map[string]*media.HybridVideoCodec),
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
		senderID := env.SenderID
		if senderID == "" {
			senderID = sess.ID()
		}
		switch env.Type {
		case protocol.MsgPong:
			var sentTs int64
			if err := json.Unmarshal(env.Payload, &sentTs); err == nil && sentTs > 0 {
				rtt := int(time.Now().UnixMilli() - sentTs)
				if rtt < 0 {
					rtt = 0
				}
				_ = rt.Classroom.UpdateTelemetry(senderID, rtt, rt.Transport.Stats().PacketLossRate)
			}
		case protocol.MsgStudentRegister:
			var st core.StudentInfo
			if err := json.Unmarshal(env.Payload, &st); err == nil {
				if st.ID == "" {
					st.ID = sess.ID()
				}
				rt.Files.RegisterSession(st.ID, sess)
				rt.Files.RegisterStudentMeta(filetransfer.StudentMeta{
					ID:        st.ID,
					StudentNo: st.StudentNo,
				})
				if existing, ok := rt.Classroom.GetStudent(st.ID); ok {
					st.Seat = existing.Seat
					st.GroupID = existing.GroupID
					st.CheckedIn = existing.CheckedIn
				} else if st.Seat.Row == 0 && st.Seat.Col == 0 {
					st.Seat = rt.nextFreeSeat()
				}
				st.IP = sess.RemoteAddr()
				st.Status = core.StatusOnline
				st.LastSeen = time.Now()
				rt.Classroom.UpsertStudent(st)
				if rt.App.Broadcasting() || rt.App.TheatrePlaying() {
					rt.forceKeyFrame.Store(true)
					if bEnv, err := protocol.NewEnvelope(protocol.MsgBroadcastStart, rt.teacherID, rt.App.CurrentBroadcastConfig()); err == nil {
						bEnv.Channel = rt.channel
						bEnv.Timestamp = time.Now().UnixMilli()
						_ = sess.SendEnvelope(bEnv)
					}
				}
				if rt.App.ScreenLocked() {
					if lEnv, err := protocol.NewEnvelope(protocol.MsgScreenLock, rt.teacherID, map[string]any{
						"locked":  true,
						"message": "请注意听讲，教师已锁定屏幕。",
					}); err == nil {
						lEnv.Channel = rt.channel
						lEnv.Timestamp = time.Now().UnixMilli()
						_ = sess.SendEnvelope(lEnv)
					}
				}
			}
		case protocol.MsgCheckInSubmit:
			var req struct {
				StudentID string `json:"student_id"`
				StudentNo string `json:"student_no"`
			}
			if err := json.Unmarshal(env.Payload, &req); err == nil {
				if req.StudentID == "" {
					req.StudentID = senderID
				}
				rt.Classroom.SubmitCheckIn(req.StudentID, "", req.StudentNo)
			}
		case protocol.MsgHandRaise:
			var req struct {
				StudentID string `json:"student_id"`
				Raised    bool   `json:"raised"`
			}
			if err := json.Unmarshal(env.Payload, &req); err == nil {
				if req.StudentID == "" {
					req.StudentID = senderID
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
		case protocol.MsgVideoFrame:
			var pkt videoStreamPacket
			if err := json.Unmarshal(env.Payload, &pkt); err == nil && len(pkt.Data) > 0 {
				frame, err := rt.decodeStudentFrame(senderID, pkt.Data)
				if err == nil && frame != nil {
					switch pkt.Kind {
					case "remote":
						rt.App.UpdateRemoteFrame(senderID, frame)
					case "demo":
						rt.App.UpdateDemoFrame(senderID, frame)
						if rt.App.StudentDemoID() == senderID {
							_ = rt.Transport.BroadcastMulticast(pkt.Data)
						}
					default:
						rt.App.UpdateMonitorFrame(senderID, frame)
					}
				}
			}
		}
		rt.App.Invalidate()
	})
}

func (rt *Runtime) nextFreeSeat() core.SeatPos {
	used := make(map[core.SeatPos]bool)
	for _, st := range rt.Classroom.ListStudents() {
		used[st.Seat] = true
	}
	for idx := 0; idx < 256; idx++ {
		pos := core.SeatPos{Row: idx / 4, Col: idx % 4}
		if !used[pos] {
			return pos
		}
	}
	return core.SeatPos{}
}

func (rt *Runtime) decodeStudentFrame(studentID string, data []byte) (*core.VideoFrame, error) {
	rt.decMu.Lock()
	dec, ok := rt.studentDecoders[studentID]
	if !ok {
		var err error
		dec, err = media.NewHybridVideoCodec(false)
		if err != nil {
			rt.decMu.Unlock()
			return nil, err
		}
		rt.studentDecoders[studentID] = dec
	}
	rt.decMu.Unlock()
	return dec.Decode(data)
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
		McastPort:   39102,
		Version:     "0.1.0",
	}
	if err := rt.Transport.StartTeacher(ctx, listenAddr, beacon); err != nil {
		return err
	}
	go rt.broadcastLoop(ctx)
	go rt.heartbeatLoop(ctx)
	return nil
}

func (rt *Runtime) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			env, err := protocol.NewEnvelope(protocol.MsgPing, rt.teacherID, time.Now().UnixMilli())
			if err == nil {
				env.Channel = rt.channel
				env.Timestamp = time.Now().UnixMilli()
				_ = rt.Transport.BroadcastControl(env, nil)
			}
			rt.Classroom.MarkOfflineStale(15 * time.Second)
		}
	}
}

func (rt *Runtime) broadcastLoop(ctx context.Context) {
	ticker := time.NewTicker(66 * time.Millisecond)
	defer ticker.Stop()
	wasActive := false
	var bcastSeq uint64
	var lastSentAt time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rt.syncAudioCapture()
			rt.Transport.SetMulticastEnabled(rt.App.UseMulticast())
			active := rt.App.Broadcasting() || rt.App.TheatrePlaying()
			if !active {
				wasActive = false
				continue
			}
			frame, err := rt.Media.CaptureScreenMax(1280, 720)
			if err != nil {
				continue
			}
			forceKF := !wasActive || rt.forceKeyFrame.Swap(false) || (bcastSeq > 0 && bcastSeq%150 == 0)
			if !forceKF && !rt.Media.LastFrameChanged() && time.Since(lastSentAt) < 1000*time.Millisecond {
				continue
			}
			bcastSeq++
			if forceKF {
				frame.KeyFrame = true
			}
			wasActive = true
			lastSentAt = time.Now()
			encoded, err := rt.Media.EncodeFrame(frame, int(rt.App.MonitorQual()))
			if err != nil {
				continue
			}
			_ = rt.Transport.BroadcastMulticast(encoded)
		}
	}
}

func (rt *Runtime) syncAudioCapture() {
	wantMode := ""
	if rt.App.VoiceActive() {
		wantMode = "voice"
	} else if rt.App.Broadcasting() || rt.App.TheatrePlaying() {
		wantMode = "loopback"
	}

	rt.audioMu.Lock()
	curMode := rt.audioMode
	if curMode == wantMode {
		rt.audioMu.Unlock()
		return
	}
	rt.audioMode = wantMode
	rt.audioMu.Unlock()

	if wantMode == "" {
		_ = rt.Media.StopAudioCapture()
		return
	}
	loopback := wantMode == "loopback"
	_ = rt.Media.StartAudioCapture(loopback, func(af *core.AudioFrame) {
		encoded, err := rt.Media.EncodeAudioFrame(af)
		if err != nil || len(encoded) == 0 {
			return
		}
		env, err := protocol.NewEnvelope(protocol.MsgAudioFrame, rt.teacherID, encoded)
		if err != nil {
			return
		}
		env.Channel = rt.channel
		env.Timestamp = time.Now().UnixMilli()
		_ = rt.Transport.BroadcastControl(env, nil)
	})
}

func (rt *Runtime) Close() error {
	_ = rt.Media.StopAudioCapture()
	_ = rt.Media.Close()
	rt.decMu.Lock()
	for _, dec := range rt.studentDecoders {
		_ = dec.Close()
	}
	rt.studentDecoders = nil
	rt.decMu.Unlock()
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
