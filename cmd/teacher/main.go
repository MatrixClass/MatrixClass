package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"os"
	"path/filepath"
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
	"nexlab/internal/logging"
	"nexlab/internal/media"
	"nexlab/internal/platform"
	"nexlab/internal/protocol"
	"nexlab/internal/store"
	"nexlab/internal/transport"
	"nexlab/internal/ui/demo"
	"nexlab/internal/ui/teacher"
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
	Store     *store.Store

	channel   string
	teacherID string

	decMu           sync.Mutex
	studentDecoders map[string]*media.HybridVideoCodec

	audioMu   sync.Mutex
	audioMode string // "", "voice", "loopback"

	forceKeyFrame   atomic.Bool
	startOnce       sync.Once
	lifecycleMu     sync.Mutex
	lifecycleCtx    context.Context
	lifecycleStop   context.CancelFunc
	lifecycleWG     sync.WaitGroup
	lifecycleClosed bool
	closeOnce       sync.Once
	closeErr        error
}

func NewRuntime(channel, teacherID, teacherName, archiveDir string) (*Runtime, error) {
	return NewRuntimeWithStore(channel, teacherID, teacherName, archiveDir, nil, false, nil)
}

func NewRuntimeWithStore(channel, teacherID, teacherName, archiveDir string, dbStore *store.Store, promptChannel bool, onChannel func(string)) (*Runtime, error) {
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
		TeacherID:         teacherID,
		TeacherName:       teacherName,
		Channel:           channel,
		ArchiveDir:        archiveDir,
		Store:             dbStore,
		PromptChannel:     promptChannel,
		OnChannelSelected: onChannel,
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
		Store:           dbStore,
		channel:         channel,
		teacherID:       teacherID,
		studentDecoders: make(map[string]*media.HybridVideoCodec),
	}
	rt.lifecycleCtx, rt.lifecycleStop = context.WithCancel(context.Background())
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
		rt.goLifecycle(func(ctx context.Context) { rt.acceptFileStreams(ctx, sess) })
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
				if rt.Store != nil {
					_ = rt.Store.UpsertStudent(st)
				}
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
				if rt.App.Monitoring() {
					rt.resetStudentDecoder(st.ID + ":monitor")
					if mEnv, err := protocol.NewEnvelope(protocol.MsgMonitorControl, rt.teacherID, struct {
						Active bool `json:"active"`
						core.MonitorConfig
					}{
						Active: true,
						MonitorConfig: core.MonitorConfig{
							Width:   320,
							Height:  180,
							FPS:     int(rt.App.MonitorFPS()),
							Quality: int(rt.App.MonitorQual()),
							Reset:   true,
						},
					}); err == nil {
						mEnv.Channel = rt.channel
						mEnv.Timestamp = time.Now().UnixMilli()
						_ = sess.SendEnvelope(mEnv)
					}
				} else {
					rt.resetStudentDecoder(st.ID + ":monitor")
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
				if rt.Store != nil {
					if st, ok := rt.Classroom.GetStudent(req.StudentID); ok {
						_ = rt.Store.UpsertStudent(st)
					}
				}
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
				if rt.Store != nil {
					_ = rt.Store.SaveChat(msg)
				}
			}
		case protocol.MsgExamAnswer:
			var sub core.ExamSubmission
			if err := json.Unmarshal(env.Payload, &sub); err == nil {
				rt.Exam.SubmitAnswer(sub)
				if rt.Store != nil {
					_ = rt.Store.SaveExamSubmission(sub)
				}
			}
		case protocol.MsgFileRetryReq:
			var req struct {
				StudentID  string `json:"student_id"`
				FileName   string `json:"file_name"`
				RemotePath string `json:"remote_path"`
				SHA256     string `json:"sha256"`
				AutoOpen   bool   `json:"auto_open"`
			}
			if err := json.Unmarshal(env.Payload, &req); err == nil {
				targetID := req.StudentID
				if targetID == "" {
					targetID = senderID
				}
				if targetID != "" && rt.Files != nil {
					rt.goLifecycle(func(ctx context.Context) {
						_, _ = rt.Files.RetryDistribute(ctx, targetID, req.FileName, req.RemotePath, req.SHA256, req.AutoOpen)
					})
				}
			}
		case protocol.MsgRemoteAppListResp, protocol.MsgRemoteAppResult, protocol.MsgRemoteCommandOutput, protocol.MsgRemoteCommandResult:
			rt.App.HandleRemoteEnvelope(env)
		case protocol.MsgVideoFrame:
			var pkt videoStreamPacket
			if err := json.Unmarshal(env.Payload, &pkt); err == nil && len(pkt.Data) > 0 {
				rt.Classroom.TouchStudent(senderID)
				kind := pkt.Kind
				if kind == "" {
					kind = "monitor"
				}
				decKey := senderID + ":" + kind
				frame, err := rt.decodeStudentFrame(decKey, pkt.Data)
				if err != nil {
					rt.resetStudentDecoder(decKey)
					if kind == "monitor" && rt.App.Monitoring() && sess != nil {
						if mEnv, mErr := protocol.NewEnvelope(protocol.MsgMonitorControl, rt.teacherID, struct {
							Active bool `json:"active"`
							core.MonitorConfig
						}{
							Active: true,
							MonitorConfig: core.MonitorConfig{
								Width:   320,
								Height:  180,
								FPS:     int(rt.App.MonitorFPS()),
								Quality: int(rt.App.MonitorQual()),
								Reset:   true,
							},
						}); mErr == nil {
							mEnv.Channel = rt.channel
							mEnv.Timestamp = time.Now().UnixMilli()
							_ = sess.SendEnvelope(mEnv)
						}
					}
				}
				if err == nil && frame != nil {
					switch kind {
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

func (rt *Runtime) resetStudentDecoder(key string) {
	rt.decMu.Lock()
	if dec, ok := rt.studentDecoders[key]; ok {
		if dec != nil {
			_ = dec.Close()
		}
		delete(rt.studentDecoders, key)
	}
	rt.decMu.Unlock()
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

func (rt *Runtime) goLifecycle(task func(context.Context)) bool {
	rt.lifecycleMu.Lock()
	if rt.lifecycleClosed || rt.lifecycleCtx == nil {
		rt.lifecycleMu.Unlock()
		return false
	}
	ctx := rt.lifecycleCtx
	rt.lifecycleWG.Add(1)
	rt.lifecycleMu.Unlock()
	go func() {
		defer rt.lifecycleWG.Done()
		task(ctx)
	}()
	return true
}

func (rt *Runtime) acceptFileStreams(ctx context.Context, sess core.Session) {
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
		select {
		case <-ctx.Done():
			_ = stream.Close()
			return
		default:
		}
		if kind == protocol.StreamFile {
			rt.goLifecycle(func(taskCtx context.Context) {
				_ = rt.Files.HandleStream(taskCtx, stream)
				rt.App.Invalidate()
			})
		} else {
			_ = stream.Close()
		}
	}
}

func (rt *Runtime) Start(ctx context.Context, listenAddr string, teacherName string) error {
	var startErr error
	rt.startOnce.Do(func() {
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
			startErr = err
			return
		}
		rt.lifecycleMu.Lock()
		if rt.lifecycleStop != nil {
			rt.lifecycleStop()
		}
		rt.lifecycleCtx, rt.lifecycleStop = context.WithCancel(ctx)
		lifecycleCtx := rt.lifecycleCtx
		rt.lifecycleMu.Unlock()
		rt.lifecycleWG.Add(2)
		go func() {
			defer rt.lifecycleWG.Done()
			rt.broadcastLoop(lifecycleCtx)
		}()
		go func() {
			defer rt.lifecycleWG.Done()
			rt.heartbeatLoop(lifecycleCtx)
		}()
	})
	return startErr
}

func (rt *Runtime) SetChannel(ch string) {
	rt.channel = ch
}

func joinRuntimeCloseErrors(errs ...error) error {
	return errors.Join(errs...)
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
			rt.markOfflineStudents(rt.Classroom.MarkOfflineStale(15 * time.Second))
		}
	}
}

func (rt *Runtime) markOfflineStudents(studentIDs []string) {
	for _, studentID := range studentIDs {
		rt.resetStudentDecoder(studentID + ":monitor")
		rt.resetStudentDecoder(studentID + ":remote")
		rt.resetStudentDecoder(studentID + ":demo")
	}
	if len(studentIDs) > 0 && rt.App != nil {
		rt.App.Invalidate()
	}
}

func (rt *Runtime) broadcastLoop(ctx context.Context) {
	ticker := time.NewTicker(66 * time.Millisecond)
	defer ticker.Stop()

	frameTrigger := make(chan struct{}, 1)
	var broadcastActive atomic.Bool
	go func() {
		var bcastSeq uint64
		var lastSentAt time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-frameTrigger:
				active := rt.App.Broadcasting() || rt.App.TheatrePlaying()
				if !active {
					broadcastActive.Store(false)
					continue
				}
				useMulticast := rt.App.UseMulticast()
				if !useMulticast && len(rt.Transport.Sessions()) == 0 {
					broadcastActive.Store(false)
					continue
				}
				frame, err := rt.Media.CaptureScreenMax(1280, 720)
				if err != nil {
					continue
				}
				changed := rt.Media.LastFrameChanged()
				explicitKF := !broadcastActive.Swap(true) || rt.forceKeyFrame.Swap(false)
				periodicKF := changed && bcastSeq > 0 && bcastSeq%150 == 0
				forceKF := explicitKF || periodicKF
				if !forceKF && !changed && time.Since(lastSentAt) < 2500*time.Millisecond {
					continue
				}
				bcastSeq++
				if forceKF {
					frame.KeyFrame = true
				}
				lastSentAt = time.Now()
				encoded, err := rt.Media.EncodeFrame(frame, int(rt.App.MonitorQual()))
				if err != nil {
					continue
				}
				_ = rt.Transport.BroadcastMulticast(encoded)
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rt.syncAudioCapture()
			rt.Transport.SetMulticastEnabled(rt.App.UseMulticast())
			if rt.App.Broadcasting() || rt.App.TheatrePlaying() {
				select {
				case frameTrigger <- struct{}{}:
				default:
				}
			} else {
				broadcastActive.Store(false)
			}
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
		if wantMode == "voice" {
			gain := rt.App.EffectiveMicGain()
			if gain <= 0 {
				return
			}
			if gain != 1.0 && len(af.Data) >= 2 {
				for i := 0; i+1 < len(af.Data); i += 2 {
					s := int16(binary.LittleEndian.Uint16(af.Data[i : i+2]))
					v := float32(s) * gain
					if v > 32767 {
						v = 32767
					} else if v < -32768 {
						v = -32768
					}
					binary.LittleEndian.PutUint16(af.Data[i:i+2], uint16(int16(v)))
				}
			}
		}
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
	rt.closeOnce.Do(func() {
		rt.lifecycleMu.Lock()
		rt.lifecycleClosed = true
		if rt.lifecycleStop != nil {
			rt.lifecycleStop()
		}
		rt.lifecycleMu.Unlock()
		var closeErrs []error
		if rt.Transport != nil {
			closeErrs = append(closeErrs, rt.Transport.Close())
		}
		rt.lifecycleWG.Wait()

		if rt.Media != nil {
			closeErrs = append(closeErrs, rt.Media.StopAudioCapture(), rt.Media.Close())
		}
		rt.decMu.Lock()
		for _, dec := range rt.studentDecoders {
			if dec != nil {
				closeErrs = append(closeErrs, dec.Close())
			}
		}
		rt.studentDecoders = nil
		rt.decMu.Unlock()
		if rt.Control != nil {
			closeErrs = append(closeErrs, rt.Control.Close())
		}
		if rt.Store != nil {
			closeErrs = append(closeErrs, rt.Store.Close())
		}
		rt.closeErr = joinRuntimeCloseErrors(closeErrs...)
	})
	return rt.closeErr
}

func main() {
	demoMode := flag.Bool("demo", false, "run teacher console with built-in demo classroom data")
	channel := flag.String("channel", "1", "classroom numeric channel ID")
	listenAddr := flag.String("listen", ":39100", "KCP listen address")
	teacherLabel := "教师端"
	archiveDir := flag.String("archive", "", "homework archive directory (defaults to Downloads)")
	flag.Parse()
	logDir := "logs"
	if exePath, err := os.Executable(); err == nil && exePath != "" {
		logDir = filepath.Join(filepath.Dir(exePath), "logs")
	}
	appLogger, loggerErr := logging.New(logging.Config{Dir: logDir, Role: "teacher"})
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
		exePath, exeErr := os.Executable()
		dbDir := "."
		if exeErr == nil && exePath != "" {
			dbDir = filepath.Dir(exePath)
		}
		dbPath := filepath.Join(dbDir, "nexlab-teacher.db")
		dbStore, dbErr := store.Open(dbPath)
		if dbErr != nil {
			log.Printf("open sqlite store %s failed: %v", dbPath, dbErr)
		}

		var rt *Runtime
		var winRef *mygo.Window
		rt, err := NewRuntimeWithStore(*channel, "teacher-01", teacherLabel, *archiveDir, dbStore, true, func(chosen string) {
			rt.SetChannel(chosen)
			if winRef != nil {
				winRef.SetTitle("矩联电子教室 MatrixClass - 教师端 [" + chosen + "]")
			}
			if startErr := rt.Start(ctx, *listenAddr, teacherLabel); startErr != nil {
				log.Printf("start teacher transport: %v", startErr)
			}
		})
		if err != nil {
			log.Fatalf("init teacher runtime: %v", err)
		}
		app = rt.App
		cleanup = func() { _ = rt.Close() }
		mygo.App.WhenReady(func() {
			win := mygo.NewWindow(mygo.WindowOptions{
				Title:     "矩联电子教室 MatrixClass - 教师端",
				Width:     980,
				Height:    640,
				MinWidth:  800,
				MinHeight: 540,
				StateKey:  "nexlab-teacher-main-v2",
				Content:   ui.View(app.View),
			})
			winRef = win
			app.AttachWindow(win)
			app.InstallTray()
		})
		mygo.App.Run()
		return
	}
	if cleanup != nil {
		defer cleanup()
	}

	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:     "矩联电子教室 MatrixClass - 教师端 [" + *channel + "]",
			Width:     980,
			Height:    640,
			MinWidth:  800,
			MinHeight: 540,
			StateKey:  "nexlab-teacher-main-v2",
			Content:   ui.View(app.View),
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
