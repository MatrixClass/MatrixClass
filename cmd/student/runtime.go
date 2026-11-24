package main

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"

	"nexlab/internal/apppaths"
	"nexlab/internal/classroom"
	"nexlab/internal/control"
	"nexlab/internal/core"
	"nexlab/internal/exam"
	"nexlab/internal/filetransfer"
	"nexlab/internal/media"
	"nexlab/internal/remoteexec"
	"nexlab/internal/transport"
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
	started         atomic.Bool
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
	return NewRuntimeWithTrust(channel, studentID, studentNo, teacherAddr, recvDir, nil)
}

// NewRuntimeWithTrust builds a student runtime that only accepts teachers
// verified by trust; nil keeps the legacy unauthenticated protocol.
func NewRuntimeWithTrust(channel, studentID, studentNo, teacherAddr, recvDir string, trust *transport.FileTrust) (*Runtime, error) {
	var runtimeRef *Runtime
	host, _ := os.Hostname()
	if host == "" {
		host = "LAB-PC"
	}
	mac := localPrimaryMAC()
	var opts []transport.ManagerOption
	if teacherAddr != "" {
		opts = append(opts, transport.WithTeacherAddr(teacherAddr))
	}
	if trust != nil {
		opts = append(opts, transport.WithTeacherTrust(trust))
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
		StudentID:         studentID,
		StudentNo:         studentNo,
		MachineName:       host,
		Channel:           channel,
		ExitPasswordPath:  filepath.Join(apppaths.Resolve("").DataDir, "student-exit-password.sha256"),
		ChannelPath:       filepath.Join(apppaths.Resolve("").DataDir, "student-channel-id"),
		NetworkConfigPath: filepath.Join(apppaths.Resolve("").DataDir, "student-network.json"),
		Trust:             trust,
		OnNetworkConfigComplete: func(cfg transport.NetworkConfig) {
			tr.SetNetworkConfig(cfg)
			if runtimeRef != nil {
				go func() {
					if err := runtimeRef.Transport.ReconnectStudent(context.Background(), runtimeRef.App.Channel()); err != nil {
						runtimeRef.App.SetStatusNote("网络参数已保存，重连失败: " + err.Error())
					}
				}()
			}
		},
		OnSetupComplete: func(newChannel string) {
			if runtimeRef != nil {
				runtimeRef.streamMu.Lock()
				runtimeRef.channel = newChannel
				runtimeRef.streamMu.Unlock()
				go func() {
					if err := runtimeRef.Start(context.Background()); err != nil {
						runtimeRef.App.SetStatusNote("连接频道失败: " + err.Error())
					}
				}()
			}
		},
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
	tr.SetNetworkConfig(app.NetworkConfig())
	channel = app.Channel()
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
	runtimeRef = rt
	rt.lifecycleCtx, rt.lifecycleStop = context.WithCancel(context.Background())
	rt.wireHandlers()
	rt.lifecycleWG.Add(1)
	go func() {
		defer rt.lifecycleWG.Done()
		rt.broadcastDecodeLoop()
	}()
	return rt, nil
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

// defaultStudentID returns an ID that is unique per machine and stable across
// restarts, so several students never collide on the teacher side.
func defaultStudentID() string {
	if mac := localPrimaryMAC(); mac != "" {
		return "stu-" + strings.ToLower(strings.ReplaceAll(mac, ":", ""))
	}
	host, _ := os.Hostname()
	host = strings.TrimSpace(host)
	if host == "" {
		host = "pc"
	}
	return "stu-" + strings.ToLower(host)
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

func (rt *Runtime) Start(ctx context.Context) error {
	rt.streamMu.Lock()
	rt.ctx = ctx
	startCtx := ctx
	channel := rt.channel
	rt.streamMu.Unlock()
	if rt.started.Load() {
		return rt.Transport.ReconnectStudent(startCtx, channel)
	}
	if _, err := rt.Control.Start(startCtx); err != nil {
		return err
	}
	if err := rt.Transport.StartStudent(startCtx, channel); err != nil {
		return err
	}
	rt.started.Store(true)
	return nil
}

func joinRuntimeCloseErrors(errs ...error) error {
	return errors.Join(errs...)
}

func (rt *Runtime) Close() error {
	rt.closeOnce.Do(func() {
		rt.started.Store(false)
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
