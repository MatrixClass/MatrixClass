package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"nexlab/internal/classroom"
	"nexlab/internal/control"
	"nexlab/internal/core"
	"nexlab/internal/exam"
	"nexlab/internal/filetransfer"
	"nexlab/internal/media"
	"nexlab/internal/protocol"
	"nexlab/internal/store"
	"nexlab/internal/transport"
	"nexlab/internal/ui/teacher"
)

type videoStreamPacket struct {
	Kind string `json:"kind"`
	Data []byte `json:"data"`
}

type Runtime struct {
	// sessionStudents binds a transport session to the student ID it
	// registered as, so later messages can't claim another identity.
	sessMu          sync.RWMutex
	sessionStudents map[string]string
	App             *teacher.App
	Transport       *transport.Manager
	Media           *media.Manager
	Control         *control.Manager
	Classroom       *classroom.Service
	Files           *filetransfer.Service
	Exam            *exam.Service
	Store           *store.Store

	channel   string
	teacherID string

	decMu           sync.Mutex
	studentDecoders map[string]*media.HybridVideoCodec

	audioMu   sync.Mutex
	audioMode string // "", "voice", "loopback"

	forceKeyFrame   atomic.Bool
	startMu         sync.Mutex
	started         bool
	lifecycleMu     sync.Mutex
	lifecycleCtx    context.Context
	lifecycleStop   context.CancelFunc
	lifecycleWG     sync.WaitGroup
	lifecycleClosed bool
	closeOnce       sync.Once
	closeErr        error
	theatreHTTP     *http.Server
}

func NewRuntime(channel, teacherID, teacherName, archiveDir string) (*Runtime, error) {
	return NewRuntimeWithStore(channel, teacherID, teacherName, archiveDir, nil, false, nil, nil)
}

func NewRuntimeWithStore(channel, teacherID, teacherName, archiveDir string, dbStore *store.Store, promptChannel bool, onChannel func(string), identity *transport.TeacherIdentity) (*Runtime, error) {
	var trOpts []transport.ManagerOption
	fingerprint := ""
	if identity != nil {
		trOpts = append(trOpts, transport.WithTeacherIdentity(identity))
		fingerprint = identity.Fingerprint()
	}
	tr := transport.NewManager(trOpts...)
	md, err := media.NewManager(media.ManagerOptions{PreferH264MFT: false})
	if err != nil {
		return nil, err
	}
	ctrl := control.NewManager(control.ManagerOptions{})
	cr := classroom.NewService()
	ex := exam.NewService()

	var appRef *teacher.App
	var rtRef *Runtime
	ft := filetransfer.NewService(filetransfer.Config{
		Role:       filetransfer.RoleTeacher,
		ArchiveDir: archiveDir,
		ReceiveDir: archiveDir,
		SessionIdentity: func(sessID string) (filetransfer.StudentMeta, bool) {
			if rtRef == nil {
				return filetransfer.StudentMeta{}, false
			}
			studentID, ok := rtRef.studentForSession(sessID)
			if !ok {
				return filetransfer.StudentMeta{}, false
			}
			st, ok := cr.GetStudent(studentID)
			if !ok {
				return filetransfer.StudentMeta{ID: studentID}, true
			}
			return filetransfer.StudentMeta{ID: st.ID, StudentNo: st.StudentNo, Name: st.MachineName}, true
		},
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
		Fingerprint:       fingerprint,
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
	rtRef = rt
	rt.lifecycleCtx, rt.lifecycleStop = context.WithCancel(context.Background())
	rt.wireHandlers()
	return rt, nil
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

func (rt *Runtime) Start(ctx context.Context, listenAddr string, teacherName string) error {
	// Unlike sync.Once, a failed start (port in use, NIC not ready) can be
	// retried instead of leaving the teacher silently offline.
	rt.startMu.Lock()
	defer rt.startMu.Unlock()
	if rt.started {
		return nil
	}
	rt.lifecycleMu.Lock()
	closed := rt.lifecycleClosed
	rt.lifecycleMu.Unlock()
	if closed {
		return errors.New("teacher runtime is closed")
	}
	var startErr error
	func() {
		beacon := protocol.DiscoveryBeacon{
			Role:        string(core.RoleTeacher),
			TeacherID:   rt.teacherID,
			TeacherName: teacherName,
			Channel:     rt.channel,
			MulticastIP: "239.255.42.10",
			McastPort:   39102,
			Version:     teacher.AppVersion,
		}
		if err := rt.Transport.StartTeacher(ctx, listenAddr, beacon); err != nil {
			startErr = err
			return
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/matrixclass/theatre", rt.serveTheatreStream)
		rt.theatreHTTP = &http.Server{Addr: ":39103", Handler: mux}
		streamHost := localLANIPv4()
		if streamHost == "" {
			streamHost = "127.0.0.1"
		}
		rt.App.SetTheatreStreamURL("http://" + net.JoinHostPort(streamHost, "39103") + "/matrixclass/theatre")
		go func() {
			if err := rt.theatreHTTP.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("theatre stream server: %v", err)
			}
		}()
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
		rt.started = true
	}()
	return startErr
}

// retryTeacherStart keeps retrying a failed transport start (e.g. port busy,
// network not ready) until it succeeds or the app shuts down.
func retryTeacherStart(ctx context.Context, rt *Runtime, listenAddr, teacherName string) {
	delay := 2 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		err := rt.Start(ctx, listenAddr, teacherName)
		if err == nil {
			rt.App.SetStatusNote("课堂网络已启动 · 等待学生端加入")
			return
		}
		rt.lifecycleMu.Lock()
		closed := rt.lifecycleClosed
		rt.lifecycleMu.Unlock()
		if closed {
			return
		}
		log.Printf("retry teacher transport: %v", err)
		rt.App.SetStatusNote("课堂网络启动失败，正在重试：" + err.Error())
		if delay < 30*time.Second {
			delay *= 2
		}
	}
}

func (rt *Runtime) SetChannel(ch string) {
	rt.channel = ch
}

func joinRuntimeCloseErrors(errs ...error) error {
	return errors.Join(errs...)
}

func (rt *Runtime) Close() error {
	rt.closeOnce.Do(func() {
		// Take startMu first (same order as Start) so an in-flight start
		// retry cannot bring the transport up after Close.
		rt.startMu.Lock()
		rt.lifecycleMu.Lock()
		rt.lifecycleClosed = true
		if rt.lifecycleStop != nil {
			rt.lifecycleStop()
		}
		rt.lifecycleMu.Unlock()
		rt.startMu.Unlock()
		var closeErrs []error
		if rt.theatreHTTP != nil {
			closeErrs = append(closeErrs, rt.theatreHTTP.Shutdown(context.Background()))
		}
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
