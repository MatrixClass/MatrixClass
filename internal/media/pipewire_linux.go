//go:build linux

package media

// PipeWire frame consumer without cgo: libpipewire-0.3.so.0 is dlopen'ed via
// purego and the stream callbacks are purego.NewCallback trampolines.
//
// Threading model: a pw_main_loop is run with pw_main_loop_run on a goroutine
// locked to its OS thread. Every PipeWire call and every callback therefore
// happens on a Go-created thread (no foreign-thread callbacks, no
// pw_thread_loop). The stream is created without PW_STREAM_FLAG_RT_PROCESS, so
// "process" is dispatched on this main loop too. Only pw_main_loop_quit is
// called from another goroutine, which PipeWire implements as a thread-safe
// loop invoke.

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	pwDirectionInput        = 0
	pwStreamFlagAutoconnect = 1 << 0
	pwStreamFlagMapBuffers  = 1 << 2

	pwStreamStateError       = -1
	pwStreamStateUnconnected = 0
	pwStreamStateConnecting  = 1
	pwStreamStatePaused      = 2
	pwStreamStateStreaming   = 3

	// struct pw_stream_events slots (uint32 version + 11 function pointers).
	pwEventsSlots        = 12
	pwEventsStateChanged = 2
	pwEventsParamChanged = 5
	pwEventsProcess      = 8
	// struct spa_hook is 6 pointers; over-allocate for safety.
	pwHookSlots = 8

	pwStreamProps = "media.type=Video media.category=Capture media.role=Screen"

	// pwConnectTimeout bounds how long a stream may stay in CONNECTING (e.g.
	// the portal node vanished) before it is treated as disconnected.
	pwConnectTimeout = 20 * time.Second
)

var pwLibraryNames = []string{"libpipewire-0.3.so.0", "libpipewire-0.3.so"}

type pwLibrary struct {
	version             string
	initFn              func(argc, argv uintptr)
	mainLoopNew         func(props uintptr) uintptr
	mainLoopGetLoop     func(loop uintptr) uintptr
	mainLoopRun         func(loop uintptr) int32
	mainLoopQuit        func(loop uintptr) int32
	mainLoopDestroy     func(loop uintptr)
	contextNew          func(loop, props, userDataSize uintptr) uintptr
	contextDestroy      func(context uintptr)
	contextConnectFd    func(context uintptr, fd int32, props, userDataSize uintptr) uintptr
	coreDisconnect      func(core uintptr) int32
	propertiesNewString func(s string) uintptr
	streamNew           func(core uintptr, name string, props uintptr) uintptr
	streamAddListener   func(stream, hook, events, data uintptr)
	streamConnect       func(stream uintptr, direction, target, flags uint32, params uintptr, nParams uint32) int32
	streamUpdateParams  func(stream, params uintptr, nParams uint32) int32
	streamDequeueBuffer func(stream uintptr) uintptr
	streamQueueBuffer   func(stream, buffer uintptr) int32
	streamDestroy       func(stream uintptr)
}

var (
	pwLoadOnce sync.Once
	pwLib      *pwLibrary
	pwLoadErr  error

	pwCallbackState   uintptr
	pwCallbackParam   uintptr
	pwCallbackProcess uintptr

	pwSessions      sync.Map // uintptr id -> *pwSession
	pwNextSessionID atomic.Uintptr
)

// pwArchSupported reports whether purego callbacks and the LP64 struct
// mirrors used here are valid on this architecture. purego v0.11 implements
// NewCallback for linux amd64/arm64 (tier 1) and loong64 (tier 2); we pass
// no structs by value, which loong64 callbacks do not support.
func pwArchSupported() bool {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		return false
	}
	switch runtime.GOARCH {
	case "amd64", "arm64", "loong64":
		return true
	}
	return false
}

// pwAvailable reports whether PipeWire frames can be consumed in-process.
func pwAvailable() error {
	if !pwArchSupported() {
		return fmt.Errorf("%w: purego callbacks are not supported on linux/%s", ErrWaylandPipeWireUnavailable, runtime.GOARCH)
	}
	_, err := pwLoad()
	return err
}

func pwLoad() (*pwLibrary, error) {
	pwLoadOnce.Do(func() {
		pwLib, pwLoadErr = pwOpenLibrary()
		if pwLoadErr != nil {
			return
		}
		pwLib.initFn(0, 0)
		pwCallbackState = purego.NewCallback(func(data, oldState, state, errMsg uintptr) {
			if s := pwLookupSession(data); s != nil {
				s.onStateChanged(int32(state), pwCString(errMsg))
			}
		})
		pwCallbackParam = purego.NewCallback(func(data, id, param uintptr) {
			if s := pwLookupSession(data); s != nil {
				s.onParamChanged(uint32(id), param)
			}
		})
		pwCallbackProcess = purego.NewCallback(func(data uintptr) {
			if s := pwLookupSession(data); s != nil {
				s.onProcess()
			}
		})
	})
	return pwLib, pwLoadErr
}

// pwOpenLibrary dlopens libpipewire. The handle is intentionally never
// closed: PipeWire keeps global state and loaded SPA plugins.
func pwOpenLibrary() (*pwLibrary, error) {
	var handle uintptr
	var lastErr error
	for _, name := range pwLibraryNames {
		h, err := purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err == nil {
			handle = h
			break
		}
		lastErr = err
	}
	if handle == 0 {
		return nil, fmt.Errorf("%w: cannot load libpipewire-0.3: %v", ErrWaylandPipeWireUnavailable, lastErr)
	}
	lib := &pwLibrary{}
	symbols := []struct {
		name string
		fn   any
	}{
		{"pw_init", &lib.initFn},
		{"pw_main_loop_new", &lib.mainLoopNew},
		{"pw_main_loop_get_loop", &lib.mainLoopGetLoop},
		{"pw_main_loop_run", &lib.mainLoopRun},
		{"pw_main_loop_quit", &lib.mainLoopQuit},
		{"pw_main_loop_destroy", &lib.mainLoopDestroy},
		{"pw_context_new", &lib.contextNew},
		{"pw_context_destroy", &lib.contextDestroy},
		{"pw_context_connect_fd", &lib.contextConnectFd},
		{"pw_core_disconnect", &lib.coreDisconnect},
		{"pw_properties_new_string", &lib.propertiesNewString},
		{"pw_stream_new", &lib.streamNew},
		{"pw_stream_add_listener", &lib.streamAddListener},
		{"pw_stream_connect", &lib.streamConnect},
		{"pw_stream_update_params", &lib.streamUpdateParams},
		{"pw_stream_dequeue_buffer", &lib.streamDequeueBuffer},
		{"pw_stream_queue_buffer", &lib.streamQueueBuffer},
		{"pw_stream_destroy", &lib.streamDestroy},
	}
	for _, sym := range symbols {
		addr, err := purego.Dlsym(handle, sym.name)
		if err != nil || addr == 0 {
			_ = purego.Dlclose(handle)
			return nil, fmt.Errorf("%w: libpipewire lacks %s", ErrWaylandPipeWireUnavailable, sym.name)
		}
		purego.RegisterFunc(sym.fn, addr)
	}
	if addr, err := purego.Dlsym(handle, "pw_get_library_version"); err == nil && addr != 0 {
		var getVersion func() string
		purego.RegisterFunc(&getVersion, addr)
		lib.version = getVersion()
	}
	return lib, nil
}

func pwLookupSession(id uintptr) *pwSession {
	if v, ok := pwSessions.Load(id); ok {
		return v.(*pwSession)
	}
	return nil
}

// pwPtr converts a native address into an unsafe.Pointer. The memory is owned
// by PipeWire (never the Go heap), so it cannot move.
func pwPtr(addr uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&addr))
}

func pwCString(addr uintptr) string {
	if addr == 0 {
		return ""
	}
	const limit = 4096
	buf := make([]byte, 0, 64)
	for i := uintptr(0); i < limit; i++ {
		c := *(*byte)(pwPtr(addr + i))
		if c == 0 {
			break
		}
		buf = append(buf, c)
	}
	return string(buf)
}

// pwSession is one connected stream; it lives entirely on the loop thread.
type pwSession struct {
	lib    *pwLibrary
	sink   *pwStreamSink
	loop   uintptr
	stream uintptr

	connected  atomic.Bool // reached PAUSED/STREAMING; read by the watchdog
	timedOut   atomic.Bool
	format     pwVideoFormatInfo
	haveFormat bool
	streamed   bool
	closing    bool
	err        error
	lastIssue  string

	pinner runtime.Pinner
	keep   []any
}

// pinWords copies b into 8-byte aligned pinned Go memory and returns its
// address; it stays valid until the session unpins.
func (s *pwSession) pinWords(words []uintptr) uintptr {
	s.pinner.Pin(&words[0])
	s.keep = append(s.keep, words)
	return uintptr(unsafe.Pointer(&words[0]))
}

// pinParams pins one SPA pod and a one-element "const struct spa_pod **".
func (s *pwSession) pinParams(pod []byte) uintptr {
	words := make([]uint64, (len(pod)+7)/8)
	for i := range words {
		var w uint64
		for j := 0; j < 8 && i*8+j < len(pod); j++ {
			w |= uint64(pod[i*8+j]) << (8 * j)
		}
		words[i] = w
	}
	s.pinner.Pin(&words[0])
	s.keep = append(s.keep, words)
	params := make([]uintptr, 1)
	params[0] = uintptr(unsafe.Pointer(&words[0]))
	return s.pinWords(params)
}

func (s *pwSession) fail(err error) {
	if s.err == nil {
		s.err = err
	}
	if s.loop != 0 {
		s.lib.mainLoopQuit(s.loop)
	}
}

func (s *pwSession) issue(err error) {
	if msg := err.Error(); msg != s.lastIssue {
		s.lastIssue = msg
		if s.sink != nil && s.sink.onProblem != nil {
			s.sink.onProblem(err)
		}
	}
}

func (s *pwSession) onStateChanged(state int32, msg string) {
	if s.closing {
		return
	}
	switch state {
	case pwStreamStateStreaming:
		s.connected.Store(true)
		s.streamed = true
		if s.sink != nil && s.sink.onState != nil {
			s.sink.onState(true, false)
		}
	case pwStreamStatePaused:
		s.connected.Store(true)
		// PAUSED precedes STREAMING during negotiation; after streaming it
		// means the compositor paused (locked screen, session switch).
		if s.streamed && s.sink != nil && s.sink.onState != nil {
			s.sink.onState(false, true)
		}
	case pwStreamStateError:
		if msg == "" {
			msg = "stream error"
		}
		s.fail(fmt.Errorf("%w: %s", ErrWaylandPipeWireDisconnected, msg))
	case pwStreamStateUnconnected:
		s.fail(fmt.Errorf("%w: stream unconnected", ErrWaylandPipeWireDisconnected))
	}
}

func (s *pwSession) onParamChanged(id uint32, param uintptr) {
	if s.closing || id != pwSpaParamFormat || param == 0 {
		return
	}
	size := *(*uint32)(pwPtr(param))
	if size > pwMaxPodSize {
		s.issue(fmt.Errorf("%w: format pod of %d bytes", errPwPodTruncated, size))
		return
	}
	pod := make([]byte, 8+int(size))
	copy(pod, unsafe.Slice((*byte)(pwPtr(param)), len(pod)))
	info, err := pwParseVideoFormat(pod)
	if err != nil {
		s.haveFormat = false
		s.fail(fmt.Errorf("%w: negotiated format unusable: %v", ErrWaylandPipeWireDisconnected, err))
		return
	}
	s.format, s.haveFormat = info, true
	if s.sink != nil && s.sink.onFormat != nil {
		s.sink.onFormat(info)
	}
	if rc := s.lib.streamUpdateParams(s.stream, s.pinParams(pwBuildBuffersParam()), 1); rc < 0 {
		s.issue(fmt.Errorf("media: pw_stream_update_params: %v", syscall.Errno(-rc)))
	}
}

func (s *pwSession) onProcess() {
	if s.closing || s.stream == 0 {
		return
	}
	// Drain the queue and keep only the newest buffer.
	var newest uintptr
	for {
		b := s.lib.streamDequeueBuffer(s.stream)
		if b == 0 {
			break
		}
		if newest != 0 {
			s.lib.streamQueueBuffer(s.stream, newest)
		}
		newest = b
	}
	if newest == 0 {
		return
	}
	defer s.lib.streamQueueBuffer(s.stream, newest)
	if err := s.copyBuffer(newest); err != nil {
		s.issue(err)
	}
}

func (s *pwSession) copyBuffer(pwBuf uintptr) error {
	if !s.haveFormat || s.sink == nil || s.sink.frames == nil {
		return nil
	}
	hdr := (*pwBufferHeader)(pwPtr(pwBuf))
	if hdr.Buffer == 0 {
		return nil
	}
	buf := (*pwSpaBuffer)(pwPtr(hdr.Buffer))
	if buf.NDatas == 0 || buf.Datas == 0 {
		return nil
	}
	d := (*pwSpaData)(pwPtr(buf.Datas))
	if d.Chunk == 0 {
		return nil
	}
	chunk := (*pwSpaChunk)(pwPtr(d.Chunk))
	if chunk.Flags&pwSpaChunkFlagCorrupted != 0 || chunk.Size == 0 {
		// Corrupted or empty (e.g. cursor-only/no-damage) buffer: keep the
		// previous real frame.
		return nil
	}
	if d.Data == 0 {
		return fmt.Errorf("media: PipeWire delivered an unmappable buffer (type %d); DMA-BUF is not supported", d.Type)
	}
	if chunk.Offset > d.MaxSize {
		return fmt.Errorf("%w: chunk offset %d beyond %d", errPwBadGeometry, chunk.Offset, d.MaxSize)
	}
	size := chunk.Size
	if size > d.MaxSize-chunk.Offset {
		size = d.MaxSize - chunk.Offset
	}
	if chunk.Stride < 0 {
		return fmt.Errorf("%w: negative stride %d", errPwBadGeometry, chunk.Stride)
	}
	src := unsafe.Slice((*byte)(pwPtr(d.Data+uintptr(chunk.Offset))), int(size))
	if err := s.sink.frames.store(src, s.format.Width, s.format.Height, int(chunk.Stride), s.format.Format); err != nil {
		return err
	}
	s.lastIssue = ""
	if s.sink.onFrame != nil {
		s.sink.onFrame()
	}
	return nil
}

// pwRunStream connects to the portal-provided PipeWire remote fd, consumes
// nodeID and blocks until ctx ends or the stream fails. Ownership of fd passes
// to this function. Everything native is torn down before it returns.
func pwRunStream(ctx context.Context, fd int, nodeID uint32, sink *pwStreamSink) (err error) {
	fdOwned := true
	defer func() {
		if fdOwned && fd >= 0 {
			_ = syscall.Close(fd)
		}
	}()
	if err := pwAvailable(); err != nil {
		return err
	}
	lib := pwLib

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	s := &pwSession{lib: lib, sink: sink}
	id := pwNextSessionID.Add(1)
	pwSessions.Store(id, s)
	defer pwSessions.Delete(id)
	defer s.pinner.Unpin() // after every native object below is destroyed

	s.loop = lib.mainLoopNew(0)
	if s.loop == 0 {
		return fmt.Errorf("%w: pw_main_loop_new failed", ErrWaylandPipeWireDisconnected)
	}
	defer lib.mainLoopDestroy(s.loop)
	pwContext := lib.contextNew(lib.mainLoopGetLoop(s.loop), 0, 0)
	if pwContext == 0 {
		return fmt.Errorf("%w: pw_context_new failed", ErrWaylandPipeWireDisconnected)
	}
	defer lib.contextDestroy(pwContext)
	fdOwned = false // pw_context_connect_fd owns (and closes) the fd from here
	core := lib.contextConnectFd(pwContext, int32(fd), 0, 0)
	if core == 0 {
		return fmt.Errorf("%w: pw_context_connect_fd failed", ErrWaylandPipeWireDisconnected)
	}
	defer lib.coreDisconnect(core)

	s.stream = lib.streamNew(core, "matrixclass-screen-capture", lib.propertiesNewString(pwStreamProps))
	if s.stream == 0 {
		return fmt.Errorf("%w: pw_stream_new failed", ErrWaylandPipeWireDisconnected)
	}
	defer func() {
		s.closing = true
		lib.streamDestroy(s.stream)
		s.stream = 0
	}()

	events := make([]uintptr, pwEventsSlots) // events[0] = version 0 (base events)
	events[pwEventsStateChanged] = pwCallbackState
	events[pwEventsParamChanged] = pwCallbackParam
	events[pwEventsProcess] = pwCallbackProcess
	hook := make([]uintptr, pwHookSlots)
	lib.streamAddListener(s.stream, s.pinWords(hook), s.pinWords(events), id)

	flags := uint32(pwStreamFlagAutoconnect | pwStreamFlagMapBuffers)
	if rc := lib.streamConnect(s.stream, pwDirectionInput, nodeID, flags, s.pinParams(pwBuildEnumFormat()), 1); rc < 0 {
		return fmt.Errorf("%w: pw_stream_connect: %v", ErrWaylandPipeWireDisconnected, syscall.Errno(-rc))
	}
	if ctx.Err() != nil {
		return nil
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		timer := time.NewTimer(pwConnectTimeout)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				lib.mainLoopQuit(s.loop)
				return
			case <-stop:
				return
			case <-timer.C:
				if !s.connected.Load() {
					s.timedOut.Store(true)
					lib.mainLoopQuit(s.loop)
					return
				}
			}
		}
	}()
	lib.mainLoopRun(s.loop)
	close(stop)
	wg.Wait()

	if s.err != nil {
		return s.err
	}
	if ctx.Err() != nil {
		return nil
	}
	if s.timedOut.Load() {
		return fmt.Errorf("%w: stream did not connect within %s", ErrWaylandPipeWireDisconnected, pwConnectTimeout)
	}
	return errors.New("media: PipeWire main loop exited unexpectedly")
}
