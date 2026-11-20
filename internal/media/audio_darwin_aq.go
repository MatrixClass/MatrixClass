//go:build darwin

package media

// Native AudioToolbox (AudioQueue), CoreAudio (default device listener) and
// AVFoundation (microphone TCC status) bindings through purego, CGO-free.

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

const darwinAVFoundationFramework = "/System/Library/Frameworks/AVFoundation.framework/AVFoundation"

type darwinAudioAQAPI struct {
	// AudioToolbox
	newInput       func(format *aqStreamDescription, callback, userData, runLoop, runLoopMode uintptr, flags uint32, outQueue *uintptr) int32
	newOutput      func(format *aqStreamDescription, callback, userData, runLoop, runLoopMode uintptr, flags uint32, outQueue *uintptr) int32
	allocateBuffer func(queue uintptr, size uint32, outBuffer **aqBuffer) int32
	enqueueBuffer  func(queue uintptr, buffer *aqBuffer, numPacketDescs uint32, packetDescs uintptr) int32
	start          func(queue uintptr, startTime uintptr) int32
	stop           func(queue uintptr, immediate bool) int32
	dispose        func(queue uintptr, immediate bool) int32

	// CoreAudio
	getPropertyData func(object uint32, address *aqPropertyAddress, qualifierSize uint32, qualifier uintptr, ioDataSize *uint32, outData unsafe.Pointer) int32
	setPropertyData func(object uint32, address *aqPropertyAddress, qualifierSize uint32, qualifier uintptr, dataSize uint32, data unsafe.Pointer) int32
	addListener     func(object uint32, address *aqPropertyAddress, listener, clientData uintptr) int32
	removeListener  func(object uint32, address *aqPropertyAddress, listener, clientData uintptr) int32

	inputCallback    uintptr
	outputCallback   uintptr
	listenerCallback uintptr

	// AVFoundation (optional: zero when unavailable)
	avCaptureDevice objc.ID
	selAuthStatus   objc.SEL
	selRequest      objc.SEL
	mediaTypeAudio  objc.ID
	// missingUsageDescription is true for a bundled app whose Info.plist
	// lacks NSMicrophoneUsageDescription.
	missingUsageDescription bool
}

var (
	darwinAudioAPIOnce sync.Once
	darwinAudioAPIInst *darwinAudioAQAPI
	darwinAudioAPIErr  error

	darwinAudioAccessRequested atomic.Bool

	// Listener addresses live in package memory so the pointers handed to
	// the HAL stay valid for the lifetime of the process.
	darwinAudioWatchAddresses = [...]aqPropertyAddress{
		{Selector: aqHardwarePropertyDefaultInput, Scope: aqPropertyScopeGlobal, Element: aqPropertyElementMain},
		{Selector: aqHardwarePropertyDefaultOutput, Scope: aqPropertyScopeGlobal, Element: aqPropertyElementMain},
		{Selector: aqHardwarePropertyDevices, Scope: aqPropertyScopeGlobal, Element: aqPropertyElementMain},
	}
)

func darwinAudioBind(handle uintptr, name string, fptr any) error {
	sym, err := purego.Dlsym(handle, name)
	if err != nil || sym == 0 {
		return fmt.Errorf("media: missing symbol %s: %v", name, err)
	}
	purego.RegisterFunc(fptr, sym)
	return nil
}

func loadDarwinAudioAPI() (*darwinAudioAQAPI, error) {
	darwinAudioAPIOnce.Do(func() {
		darwinAudioAPIInst, darwinAudioAPIErr = openDarwinAudioAPI()
	})
	return darwinAudioAPIInst, darwinAudioAPIErr
}

func openDarwinAudioAPI() (api *darwinAudioAQAPI, err error) {
	var toolbox, coreAudio uintptr
	keepLibraries := false
	defer func() {
		if !keepLibraries {
			if coreAudio != 0 {
				_ = purego.Dlclose(coreAudio)
			}
			if toolbox != 0 {
				_ = purego.Dlclose(toolbox)
			}
		}
	}()
	defer func() {
		if r := recover(); r != nil {
			api, err = nil, fmt.Errorf("%w: %v", ErrDarwinCoreAudioUnavailable, r)
		}
	}()
	toolbox, err = purego.Dlopen(darwinAudioToolboxFramework, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDarwinCoreAudioUnavailable, err)
	}
	coreAudio, err = purego.Dlopen(darwinCoreAudioFramework, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDarwinCoreAudioUnavailable, err)
	}
	api = &darwinAudioAQAPI{}
	binds := []struct {
		handle uintptr
		name   string
		fptr   any
	}{
		{toolbox, "AudioQueueNewInput", &api.newInput},
		{toolbox, "AudioQueueNewOutput", &api.newOutput},
		{toolbox, "AudioQueueAllocateBuffer", &api.allocateBuffer},
		{toolbox, "AudioQueueEnqueueBuffer", &api.enqueueBuffer},
		{toolbox, "AudioQueueStart", &api.start},
		{toolbox, "AudioQueueStop", &api.stop},
		{toolbox, "AudioQueueDispose", &api.dispose},
		{coreAudio, "AudioObjectGetPropertyData", &api.getPropertyData},
		{coreAudio, "AudioObjectSetPropertyData", &api.setPropertyData},
		{coreAudio, "AudioObjectAddPropertyListener", &api.addListener},
		{coreAudio, "AudioObjectRemovePropertyListener", &api.removeListener},
	}
	for _, bind := range binds {
		if err := darwinAudioBind(bind.handle, bind.name, bind.fptr); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrDarwinCoreAudioUnavailable, err)
		}
	}
	// Callbacks are process-global (purego callbacks are never freed); they
	// dispatch to streams through an integer handle in inUserData.
	api.inputCallback = purego.NewCallback(darwinAudioInputTrampoline)
	api.outputCallback = purego.NewCallback(darwinAudioOutputTrampoline)
	api.listenerCallback = purego.NewCallback(darwinAudioListenerTrampoline)

	// Let the HAL deliver property notifications on its own thread instead
	// of the main run loop, so device changes are seen without a UI loop.
	var nilRunLoop uintptr
	runLoopAddr := aqPropertyAddress{Selector: aqHardwarePropertyRunLoop, Scope: aqPropertyScopeGlobal, Element: aqPropertyElementMain}
	_ = api.setPropertyData(aqSystemObject, &runLoopAddr, 0, 0, uint32(unsafe.Sizeof(nilRunLoop)), unsafe.Pointer(&nilRunLoop))

	api.loadAVFoundation()
	keepLibraries = true
	return api, nil
}

func (api *darwinAudioAQAPI) loadAVFoundation() {
	handle, err := purego.Dlopen(darwinAVFoundationFramework, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return
	}
	cls := objc.GetClass("AVCaptureDevice")
	if cls == 0 {
		return
	}
	var mediaType objc.ID
	if sym, err := purego.Dlsym(handle, "AVMediaTypeAudio"); err == nil && sym != 0 {
		mediaType = darwinAudioLoadID(sym)
	}
	if mediaType == 0 {
		// AVMediaTypeAudio is documented as @"soun".
		mediaType = darwinAudioNSString("soun")
	}
	if mediaType == 0 {
		return
	}
	api.avCaptureDevice = objc.ID(cls)
	api.selAuthStatus = objc.RegisterName("authorizationStatusForMediaType:")
	api.selRequest = objc.RegisterName("requestAccessForMediaType:completionHandler:")
	api.mediaTypeAudio = mediaType
	api.missingUsageDescription = darwinAudioBundleLacksUsageDescription()
}

// darwinAudioBundleLacksUsageDescription reports whether the process runs
// from an app bundle (has a bundle identifier) without the microphone usage
// string. Unbundled command-line binaries are attributed to their parent
// (e.g. Terminal) by TCC and are not affected.
func darwinAudioBundleLacksUsageDescription() bool {
	cls := objc.GetClass("NSBundle")
	if cls == 0 {
		return false
	}
	bundle := objc.ID(cls).Send(objc.RegisterName("mainBundle"))
	if bundle == 0 || bundle.Send(objc.RegisterName("bundleIdentifier")) == 0 {
		return false
	}
	key := darwinAudioNSString("NSMicrophoneUsageDescription")
	if key == 0 {
		return false
	}
	return bundle.Send(objc.RegisterName("objectForInfoDictionaryKey:"), key) == 0
}

// darwinAudioLoadID reads the id stored at a C global variable's address.
func darwinAudioLoadID(addr uintptr) objc.ID {
	return **(**objc.ID)(unsafe.Pointer(&addr))
}

func darwinAudioNSString(s string) objc.ID {
	cls := objc.GetClass("NSString")
	if cls == 0 {
		return 0
	}
	cstr := append([]byte(s), 0)
	return objc.ID(cls).Send(objc.RegisterName("stringWithUTF8String:"), &cstr[0])
}

// darwinAudioAQStream is one AudioQueue plus the Go state its callback uses.
type darwinAudioAQStream struct {
	api    *darwinAudioAQAPI
	id     uintptr
	queue  uintptr
	input  bool
	onData func([]byte)
	fill   func([]byte)
	size   uint32

	// cbMu is held by the callback while it touches AudioQueue buffers;
	// Close flips detached under it, so after detach no callback can touch a
	// buffer that AudioQueueDispose is about to free.
	cbMu      sync.Mutex
	detached  bool
	closeOnce sync.Once
}

var darwinAudioRegistry = struct {
	mu        sync.RWMutex
	next      uintptr
	streams   map[uintptr]*darwinAudioAQStream
	listeners map[uintptr]func()
}{streams: map[uintptr]*darwinAudioAQStream{}, listeners: map[uintptr]func(){}}

func darwinAudioRegisterStream(s *darwinAudioAQStream) uintptr {
	darwinAudioRegistry.mu.Lock()
	defer darwinAudioRegistry.mu.Unlock()
	darwinAudioRegistry.next++
	id := darwinAudioRegistry.next
	darwinAudioRegistry.streams[id] = s
	return id
}

func darwinAudioUnregisterStream(id uintptr) {
	darwinAudioRegistry.mu.Lock()
	delete(darwinAudioRegistry.streams, id)
	darwinAudioRegistry.mu.Unlock()
}

func darwinAudioLookupStream(id uintptr) *darwinAudioAQStream {
	darwinAudioRegistry.mu.RLock()
	defer darwinAudioRegistry.mu.RUnlock()
	return darwinAudioRegistry.streams[id]
}

// darwinAudioInputTrampoline implements AudioQueueInputCallback:
// (void *inUserData, AudioQueueRef inAQ, AudioQueueBufferRef inBuffer,
// const AudioTimeStamp *inStartTime, UInt32 inNumberPacketDescriptions,
// const AudioStreamPacketDescription *inPacketDescs).
func darwinAudioInputTrampoline(userData, queue uintptr, buffer *aqBuffer, _ uintptr, _ uintptr, _ uintptr) {
	s := darwinAudioLookupStream(userData)
	if s == nil || buffer == nil {
		return
	}
	s.cbMu.Lock()
	defer s.cbMu.Unlock()
	if s.detached {
		return
	}
	n := buffer.AudioDataByteSize
	if n > buffer.AudioDataBytesCapacity {
		n = buffer.AudioDataBytesCapacity
	}
	if n > 0 && buffer.AudioData != nil {
		s.onData(unsafe.Slice((*byte)(buffer.AudioData), n))
	}
	buffer.AudioDataByteSize = 0
	_ = s.api.enqueueBuffer(queue, buffer, 0, 0)
}

// darwinAudioOutputTrampoline implements AudioQueueOutputCallback:
// (void *inUserData, AudioQueueRef inAQ, AudioQueueBufferRef inBuffer).
func darwinAudioOutputTrampoline(userData, queue uintptr, buffer *aqBuffer) {
	s := darwinAudioLookupStream(userData)
	if s == nil || buffer == nil {
		return
	}
	s.cbMu.Lock()
	defer s.cbMu.Unlock()
	if s.detached {
		return
	}
	s.fillBuffer(buffer)
	_ = s.api.enqueueBuffer(queue, buffer, 0, 0)
}

func (s *darwinAudioAQStream) fillBuffer(buffer *aqBuffer) {
	n := s.size
	if n > buffer.AudioDataBytesCapacity {
		n = buffer.AudioDataBytesCapacity
	}
	if buffer.AudioData == nil || n == 0 {
		buffer.AudioDataByteSize = 0
		return
	}
	s.fill(unsafe.Slice((*byte)(buffer.AudioData), n))
	buffer.AudioDataByteSize = n
}

// darwinAudioListenerTrampoline implements AudioObjectPropertyListenerProc:
// OSStatus (AudioObjectID, UInt32, const AudioObjectPropertyAddress*, void*).
func darwinAudioListenerTrampoline(_ uintptr, _ uintptr, _ uintptr, clientData uintptr) uintptr {
	darwinAudioRegistry.mu.RLock()
	fn := darwinAudioRegistry.listeners[clientData]
	darwinAudioRegistry.mu.RUnlock()
	if fn != nil {
		fn()
	}
	return 0
}

func (s *darwinAudioAQStream) Close() error {
	var err error
	s.closeOnce.Do(func() {
		darwinAudioUnregisterStream(s.id)
		s.cbMu.Lock()
		s.detached = true
		s.cbMu.Unlock()
		if s.queue == 0 {
			return
		}
		if st := s.api.stop(s.queue, true); st != 0 {
			err = fmt.Errorf("media: AudioQueueStop failed: %s", aqOSStatusString(st))
		}
		// Dispose frees the queue and all of its buffers synchronously.
		if st := s.api.dispose(s.queue, true); st != 0 && err == nil {
			err = fmt.Errorf("media: AudioQueueDispose failed: %s", aqOSStatusString(st))
		}
		s.queue = 0
	})
	return err
}

type darwinAudioAQNative struct {
	api *darwinAudioAQAPI
	err error
}

func newDarwinAudioAQNative() *darwinAudioAQNative {
	api, err := loadDarwinAudioAPI()
	return &darwinAudioAQNative{api: api, err: err}
}

func (n *darwinAudioAQNative) Available() error {
	if n.err != nil {
		return n.err
	}
	if n.api == nil {
		return ErrDarwinCoreAudioUnavailable
	}
	return nil
}

func (n *darwinAudioAQNative) MicrophoneAuthorization() darwinAudioAuthStatus {
	if n.api == nil || n.api.avCaptureDevice == 0 {
		return darwinAudioAuthUnknown
	}
	status := darwinAudioAuthStatus(objc.Send[int](n.api.avCaptureDevice, n.api.selAuthStatus, n.api.mediaTypeAudio))
	switch status {
	case darwinAudioAuthAuthorized, darwinAudioAuthDenied, darwinAudioAuthRestricted:
		return status
	case darwinAudioAuthNotDetermined:
		if n.api.missingUsageDescription {
			return darwinAudioAuthMissingUsageDescription
		}
		return status
	}
	return darwinAudioAuthUnknown
}

func (n *darwinAudioAQNative) RequestMicrophoneAccess() {
	if n.api == nil || n.api.avCaptureDevice == 0 || n.api.missingUsageDescription {
		return
	}
	if !darwinAudioAccessRequested.CompareAndSwap(false, true) {
		return
	}
	// The completion handler runs on an arbitrary queue; the capture worker
	// polls authorizationStatus, so the block only needs to exist.
	block := objc.NewBlock(func(_ objc.Block, _ bool) {})
	n.api.avCaptureDevice.Send(n.api.selRequest, n.api.mediaTypeAudio, block)
	block.Release()
}

func (n *darwinAudioAQNative) open(input bool, format aqStreamDescription, bufferBytes, bufferCount int, onData, fill func([]byte)) (darwinAudioNativeStream, error) {
	if err := n.Available(); err != nil {
		return nil, err
	}
	if bufferBytes <= 0 || bufferCount <= 0 {
		return nil, errors.New("media: invalid AudioQueue buffer configuration")
	}
	s := &darwinAudioAQStream{api: n.api, input: input, onData: onData, fill: fill, size: uint32(bufferBytes)}
	s.id = darwinAudioRegisterStream(s)
	var queue uintptr
	var st int32
	if input {
		st = n.api.newInput(&format, n.api.inputCallback, s.id, 0, 0, 0, &queue)
	} else {
		st = n.api.newOutput(&format, n.api.outputCallback, s.id, 0, 0, 0, &queue)
	}
	if st != 0 || queue == 0 {
		darwinAudioUnregisterStream(s.id)
		kind := "AudioQueueNewOutput"
		if input {
			kind = "AudioQueueNewInput"
		}
		return nil, fmt.Errorf("media: %s failed: %s", kind, aqOSStatusString(st))
	}
	s.queue = queue
	for i := 0; i < bufferCount; i++ {
		var buffer *aqBuffer
		if st := n.api.allocateBuffer(queue, uint32(bufferBytes), &buffer); st != 0 || buffer == nil {
			_ = s.Close()
			return nil, fmt.Errorf("media: AudioQueueAllocateBuffer failed: %s", aqOSStatusString(st))
		}
		if input {
			buffer.AudioDataByteSize = 0
		} else {
			// Prime output buffers (silence until the jitter queue fills).
			s.fillBuffer(buffer)
		}
		if st := n.api.enqueueBuffer(queue, buffer, 0, 0); st != 0 {
			_ = s.Close()
			return nil, fmt.Errorf("media: AudioQueueEnqueueBuffer failed: %s", aqOSStatusString(st))
		}
	}
	if st := n.api.start(queue, 0); st != 0 {
		_ = s.Close()
		return nil, fmt.Errorf("media: AudioQueueStart failed: %s", aqOSStatusString(st))
	}
	return s, nil
}

func (n *darwinAudioAQNative) OpenInput(format aqStreamDescription, bufferBytes, bufferCount int, onData func([]byte)) (darwinAudioNativeStream, error) {
	if onData == nil {
		return nil, errors.New("media: nil capture callback")
	}
	return n.open(true, format, bufferBytes, bufferCount, onData, nil)
}

func (n *darwinAudioAQNative) OpenOutput(format aqStreamDescription, bufferBytes, bufferCount int, fill func([]byte)) (darwinAudioNativeStream, error) {
	if fill == nil {
		return nil, errors.New("media: nil playback callback")
	}
	return n.open(false, format, bufferBytes, bufferCount, nil, fill)
}

func (n *darwinAudioAQNative) readDevice(selector uint32) (uint32, error) {
	addr := aqPropertyAddress{Selector: selector, Scope: aqPropertyScopeGlobal, Element: aqPropertyElementMain}
	var id uint32
	size := uint32(unsafe.Sizeof(id))
	if st := n.api.getPropertyData(aqSystemObject, &addr, 0, 0, &size, unsafe.Pointer(&id)); st != 0 {
		return 0, fmt.Errorf("media: AudioObjectGetPropertyData(%s) failed: %s", aqOSStatusString(int32(selector)), aqOSStatusString(st))
	}
	return id, nil
}

func (n *darwinAudioAQNative) DefaultDevices() (uint32, uint32, error) {
	if err := n.Available(); err != nil {
		return 0, 0, err
	}
	in, inErr := n.readDevice(aqHardwarePropertyDefaultInput)
	out, outErr := n.readDevice(aqHardwarePropertyDefaultOutput)
	if inErr != nil && outErr != nil {
		return 0, 0, inErr
	}
	return in, out, nil
}

func (n *darwinAudioAQNative) WatchDevices(onChange func()) (func(), error) {
	if err := n.Available(); err != nil {
		return nil, err
	}
	darwinAudioRegistry.mu.Lock()
	darwinAudioRegistry.next++
	id := darwinAudioRegistry.next
	darwinAudioRegistry.listeners[id] = onChange
	darwinAudioRegistry.mu.Unlock()

	var added []int
	for i := range darwinAudioWatchAddresses {
		if st := n.api.addListener(aqSystemObject, &darwinAudioWatchAddresses[i], n.api.listenerCallback, id); st == 0 {
			added = append(added, i)
		}
	}
	cancel := func() {
		for _, i := range added {
			_ = n.api.removeListener(aqSystemObject, &darwinAudioWatchAddresses[i], n.api.listenerCallback, id)
		}
		darwinAudioRegistry.mu.Lock()
		delete(darwinAudioRegistry.listeners, id)
		darwinAudioRegistry.mu.Unlock()
	}
	if len(added) == 0 {
		cancel()
		return nil, errors.New("media: AudioObjectAddPropertyListener failed")
	}
	return cancel, nil
}
