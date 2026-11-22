package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"nexlab/internal/apppaths"
	"nexlab/internal/classroom"
	"nexlab/internal/control"
	"nexlab/internal/core"
	"nexlab/internal/exam"
	"nexlab/internal/filetransfer"
	"nexlab/internal/logging"
	"nexlab/internal/media"
	"nexlab/internal/platform"
	"nexlab/internal/protocol"
	"nexlab/internal/remoteexec"
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
	Remote    *remoteexec.Service

	studentID   string
	studentNo   string
	machineName string
	macAddr     string
	channel     string

	streamMu        sync.Mutex
	monitorStop     context.CancelFunc
	remoteStop      context.CancelFunc
	demoStop        context.CancelFunc
	demoActive      bool
	monitorCfg      monitorStreamConfig
	ctx             context.Context
	lifecycleCtx    context.Context
	bcastDecodeCh   chan []byte
	remoteMu        sync.Mutex
	remoteTasks     map[string]remoteexec.CommandTask
	lifecycleMu     sync.Mutex
	lifecycleStop   context.CancelFunc
	lifecycleWG     sync.WaitGroup
	lifecycleClosed bool
	closeOnce       sync.Once
	closeErr        error
}

type monitorStreamConfig struct {
	maxW    int
	maxH    int
	fps     int
	quality int
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
		// Auto-unlock if the teacher disappears (crash / network loss) so a
		// student machine is never left locked with no way out.
		Lock: control.LockOptions{
			DisconnectTimeout: 60 * time.Second,
			EmergencyPassword: control.DefaultEmergencyPassword,
		},
	})
	cr := classroom.NewService()
	ex := exam.NewService()
	remote := remoteexec.NewService()
	var appRef *student.App
	ft := filetransfer.NewService(filetransfer.Config{
		Role:       filetransfer.RoleStudent,
		ReceiveDir: recvDir,
		ArchiveDir: recvDir,
		OnProgress: func(task core.TransferTask) {
			if appRef != nil {
				if task.Direction == core.TransferSend {
					appRef.RecordIncomingFile(task)
					if task.Status == "completed" {
						appRef.OpenTheatreFileIfMatching(task)
					}
					return
				}
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
	tr.OnConnectionState(func(connected bool, reason string) {
		if connected {
			app.SetConnectionState(true, "")
			return
		}
		app.SetConnectionState(false, reason)
	})
	ctrl.Lock().SetOnStateChange(func(st control.LockState) {
		// Keep the lock UI in sync when the controller unlocks on its own
		// (teacher heartbeat timeout).
		if !st.Locked && st.UnlockReason == control.UnlockReasonDisconnectTimeout {
			app.SetLocked(false, "")
			app.SetStatusNote("与教师端失去连接，屏幕已自动解锁")
		}
	})

	rt := &Runtime{
		App:           app,
		Transport:     tr,
		Media:         md,
		Control:       ctrl,
		Classroom:     cr,
		Files:         ft,
		Exam:          ex,
		Remote:        remote,
		studentID:     studentID,
		studentNo:     studentNo,
		machineName:   host,
		macAddr:       mac,
		channel:       channel,
		ctx:           context.Background(),
		bcastDecodeCh: make(chan []byte, 16),
		remoteTasks:   make(map[string]remoteexec.CommandTask),
	}
	rt.lifecycleCtx, rt.lifecycleStop = context.WithCancel(context.Background())
	rt.wireHandlers()
	rt.lifecycleWG.Add(1)
	go func() {
		defer rt.lifecycleWG.Done()
		rt.broadcastDecodeLoop()
	}()
	return rt, nil
}

func (rt *Runtime) broadcastDecodeLoop() {
	for {
		select {
		case <-rt.lifecycleCtx.Done():
			return
		case data := <-rt.bcastDecodeCh:
			frame, err := rt.Media.DecodeFrame(data)
			if err == nil && frame != nil {
				rt.App.UpdateVideoFrame(frame)
			}
		}
	}
}

func enqueueLatestFrame(queue chan []byte, data []byte) bool {
	select {
	case queue <- data:
		return true
	default:
	}
	select {
	case <-queue:
	default:
	}
	select {
	case queue <- data:
		return true
	default:
		return false
	}
}

func (rt *Runtime) wireHandlers() {
	rt.Transport.OnSession(func(sess core.Session) {
		rt.Files.SetTeacherSession(sess)
		rt.App.SetStatusNote("已连接频道: " + rt.channel)
		rt.goLifecycle(func(ctx context.Context) { rt.acceptFileStreams(ctx, sess) })
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
		cp := make([]byte, len(data))
		copy(cp, data)
		enqueueLatestFrame(rt.bcastDecodeCh, cp)
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
				rt.App.SetTheatreState(ts)
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
				if req.Active && req.FPS > 0 {
					w, h := req.Width, req.Height
					if w <= 0 {
						w = 320
					}
					if h <= 0 {
						h = 180
					}
					rt.startMonitorStreamWithReset(w, h, req.FPS, req.Quality, req.Reset)
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
		case protocol.MsgHomeworkCollectStart:
			rt.App.OpenHomeworkSubmitWindow()
		case protocol.MsgHomeworkCollectEnd:
			rt.App.CloseHomeworkSubmitWindow()
		case protocol.MsgChat:
			var msg core.ChatMessage
			if err := json.Unmarshal(env.Payload, &msg); err == nil {
				rt.Classroom.AddChat(msg)
				rt.App.ShowTeacherNotice(msg.Sender, msg.Text)
				rt.App.Invalidate()
			}
		case protocol.MsgRemoteAppListReq:
			rt.sendRemoteEnvelope(sess, protocol.MsgRemoteAppListResp, env.RequestID, rt.Remote.ListApplications())
		case protocol.MsgRemoteAppLaunch:
			var req remoteexec.AppLaunchRequest
			if json.Unmarshal(env.Payload, &req) == nil {
				rt.App.SetStatusNote("教师请求启动程序")
				result := rt.Remote.LaunchApp(context.Background(), req, rt.Remote.ListApplications())
				rt.sendRemoteEnvelope(sess, protocol.MsgRemoteAppResult, env.RequestID, result)
			}
		case protocol.MsgRemoteCommandStart:
			var req remoteexec.CommandRequest
			if json.Unmarshal(env.Payload, &req) == nil {
				rt.startRemoteCommand(sess, req)
			}
		case protocol.MsgRemoteCommandCancel:
			requestID := env.RequestID
			if requestID == "" {
				var req struct {
					RequestID string `json:"request_id"`
				}
				if json.Unmarshal(env.Payload, &req) == nil {
					requestID = req.RequestID
				}
			}
			rt.remoteMu.Lock()
			task, ok := rt.remoteTasks[requestID]
			rt.remoteMu.Unlock()
			if ok && task.Cancel != nil {
				task.Cancel()
			}
		}
	})
}

func (rt *Runtime) sendRemoteEnvelope(sess core.Session, msgType protocol.MsgType, requestID string, payload any) {
	if sess == nil {
		return
	}
	env, err := protocol.NewEnvelope(msgType, rt.studentID, payload)
	if err != nil {
		return
	}
	env.RequestID = requestID
	env.Channel = rt.channel
	env.Timestamp = time.Now().UnixMilli()
	_ = sess.SendEnvelope(env)
}

func (rt *Runtime) startRemoteCommand(sess core.Session, req remoteexec.CommandRequest) {
	rt.App.SetStatusNote("教师正在执行远程命令")
	rt.lifecycleMu.Lock()
	if rt.lifecycleCtx == nil || rt.lifecycleClosed {
		rt.lifecycleMu.Unlock()
		return
	}
	lifecycleCtx := rt.lifecycleCtx
	rt.lifecycleMu.Unlock()
	task, err := rt.Remote.StartCommand(lifecycleCtx, req, func(output remoteexec.CommandOutput) {
		rt.sendRemoteEnvelope(sess, protocol.MsgRemoteCommandOutput, req.RequestID, output)
	})
	if err != nil {
		rt.sendRemoteEnvelope(sess, protocol.MsgRemoteCommandResult, req.RequestID, remoteexec.CommandResult{
			RequestID:  req.RequestID,
			Status:     "rejected",
			Error:      err.Error(),
			ExitCode:   -1,
			StartedAt:  time.Now(),
			FinishedAt: time.Now(),
		})
		return
	}
	rt.remoteMu.Lock()
	rt.remoteTasks[req.RequestID] = task
	rt.remoteMu.Unlock()
	rt.goLifecycle(func(ctx context.Context) {
		result := <-task.Result
		rt.remoteMu.Lock()
		delete(rt.remoteTasks, req.RequestID)
		rt.remoteMu.Unlock()
		rt.sendRemoteEnvelope(sess, protocol.MsgRemoteCommandResult, req.RequestID, result)
	})
}

func (rt *Runtime) startMonitorStream(maxW, maxH, fps, quality int) {
	rt.startMonitorStreamWithReset(maxW, maxH, fps, quality, false)
}

func shouldRestartMonitorStream(current, requested monitorStreamConfig, reset bool) bool {
	return reset || current != requested
}

func (rt *Runtime) startMonitorStreamWithReset(maxW, maxH, fps, quality int, reset bool) {
	if maxW <= 0 {
		maxW = 320
	}
	if maxH <= 0 {
		maxH = 180
	}
	if fps <= 0 {
		fps = 10
	}
	if fps > 30 {
		fps = 30
	}
	if quality <= 0 {
		quality = 65
	}
	cfg := monitorStreamConfig{maxW: maxW, maxH: maxH, fps: fps, quality: quality}

	rt.streamMu.Lock()
	if rt.monitorStop != nil && !shouldRestartMonitorStream(rt.monitorCfg, cfg, reset) {
		rt.streamMu.Unlock()
		return
	}
	parentCtx := rt.ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	rt.streamMu.Unlock()

	rt.stopMonitorStream()

	ctx, cancel := context.WithCancel(parentCtx)
	rt.streamMu.Lock()
	rt.monitorStop = cancel
	rt.monitorCfg = cfg
	rt.streamMu.Unlock()
	rt.goLifecycle(func(context.Context) { rt.runScreenStream(ctx, "monitor", maxW, maxH, fps, quality) })
}

func (rt *Runtime) stopMonitorStream() {
	rt.streamMu.Lock()
	cancel := rt.monitorStop
	rt.monitorStop = nil
	rt.monitorCfg = monitorStreamConfig{}
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
	rt.goLifecycle(func(context.Context) { rt.runScreenStream(ctx, "remote", 1280, 720, 15, 75) })
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
	rt.goLifecycle(func(context.Context) { rt.runScreenStream(ctx, "demo", 1280, 720, 15, 75) })
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
	forceKeyFrame := true
	kfInterval := uint64(60)
	if kind == "monitor" {
		kfInterval = 5
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if len(rt.Transport.Sessions()) == 0 {
				seq = 0
				forceKeyFrame = true
				continue
			}
			frame, err := rt.Media.CaptureScreenMax(maxW, maxH)
			if err != nil || frame == nil {
				forceKeyFrame = true
				continue
			}
			seq++
			frame.Seq = seq
			frame.KeyFrame = forceKeyFrame || seq == 1 || seq%kfInterval == 0
			encoded, err := codec.Encode(frame, quality)
			if err != nil {
				forceKeyFrame = true
				continue
			}
			env, err := protocol.NewEnvelope(protocol.MsgVideoFrame, rt.studentID, videoStreamPacket{
				Kind: kind,
				Data: encoded,
			})
			if err != nil {
				forceKeyFrame = true
				continue
			}
			env.Channel = rt.channel
			env.Timestamp = time.Now().UnixMilli()
			if err := rt.Transport.BroadcastControl(env, nil); err != nil {
				forceKeyFrame = true
				continue
			}
			forceKeyFrame = false
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

func (rt *Runtime) Start(ctx context.Context) error {
	rt.streamMu.Lock()
	rt.ctx = ctx
	startCtx := ctx
	rt.streamMu.Unlock()
	if _, err := rt.Control.Start(startCtx); err != nil {
		return err
	}
	return rt.Transport.StartStudent(startCtx, rt.channel)
}

func joinRuntimeCloseErrors(errs ...error) error {
	return errors.Join(errs...)
}

func (rt *Runtime) Close() error {
	rt.closeOnce.Do(func() {
		rt.lifecycleMu.Lock()
		rt.lifecycleClosed = true
		rt.lifecycleMu.Unlock()
		if rt.lifecycleStop != nil {
			rt.lifecycleStop()
		}
		rt.stopMonitorStream()
		rt.stopRemoteStream()
		rt.stopDemoStream()
		rt.remoteMu.Lock()
		for id, task := range rt.remoteTasks {
			if task.Cancel != nil {
				task.Cancel()
			}
			delete(rt.remoteTasks, id)
		}
		rt.remoteMu.Unlock()
		var closeErrs []error
		if rt.Transport != nil {
			closeErrs = append(closeErrs, rt.Transport.Close())
		}
		rt.lifecycleWG.Wait()

		if rt.Media != nil {
			closeErrs = append(closeErrs, rt.Media.Close())
		}
		if rt.Control != nil {
			closeErrs = append(closeErrs, rt.Control.Close())
		}
		rt.closeErr = joinRuntimeCloseErrors(closeErrs...)
	})
	return rt.closeErr
}

func main() {
	demoMode := flag.Bool("demo", false, "run student client with built-in demo data")
	channel := flag.String("channel", "1", "classroom numeric channel ID")
	teacherAddr := flag.String("teacher", "", "optional explicit teacher KCP address (host:port)")
	studentID := flag.String("id", "stu-01", "student ID")
	studentNo := flag.String("no", "202601", "student number")
	recvDir := flag.String("recv", "", "received files directory (defaults to Desktop)")
	flag.Parse()
	paths := apppaths.Resolve("")
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
			StudentID: *studentID,
			StudentNo: *studentNo,
			Channel:   *channel,
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
		rt.App.SetStatusNote("正在搜索频道 ID: " + *channel + " ...")
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
	})

	mygo.App.OnWindowAllClosed(func() {
		cancel()
		mygo.App.Quit()
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
