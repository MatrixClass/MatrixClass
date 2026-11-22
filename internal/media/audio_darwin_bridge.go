package media

// Portable lifecycle logic of the macOS AudioQueue bridge. The native
// AudioToolbox/CoreAudio/AVFoundation calls live behind darwinAudioNative
// (implemented in audio_darwin_aq.go), so this file compiles on every OS and
// its concurrency behaviour is unit-tested with a fake native layer.

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"nexlab/internal/core"
)

// darwinAudioNativeStream is an open AudioQueue. Close stops and disposes the
// queue; once it returns, the stream's data callback is never invoked again.
type darwinAudioNativeStream interface {
	Close() error
}

// darwinAudioNative is the OS boundary used by darwinAudioQueueBridge.
// onData and fill run on an AudioQueue thread and must not block.
type darwinAudioNative interface {
	Available() error
	MicrophoneAuthorization() darwinAudioAuthStatus
	RequestMicrophoneAccess()
	OpenInput(format aqStreamDescription, bufferBytes, bufferCount int, onData func([]byte)) (darwinAudioNativeStream, error)
	OpenOutput(format aqStreamDescription, bufferBytes, bufferCount int, fill func([]byte)) (darwinAudioNativeStream, error)
	DefaultDevices() (input, output uint32, err error)
	WatchDevices(onChange func()) (cancel func(), err error)
}

// darwinAudioBridgeErrors injects the exported sentinel errors (declared in
// the darwin-only file) into the portable bridge.
type darwinAudioBridgeErrors struct {
	Unavailable error
	Permission  error
	Device      error
	Loopback    error
	Closed      error
}

type darwinAudioCaptureSession struct {
	stream     darwinAudioNativeStream
	queue      chan *core.AudioFrame
	stopCh     chan struct{}
	workerDone chan struct{}
}

type darwinAudioPlaybackSession struct {
	stream     darwinAudioNativeStream
	jitter     *darwinAudioJitterQueue
	sampleRate int
	channels   int
}

type darwinAudioQueueBridge struct {
	native darwinAudioNative
	errs   darwinAudioBridgeErrors

	// capMu serializes capture lifecycle (start/stop/restart/close). It is
	// never taken by AudioQueue callbacks.
	capMu sync.Mutex
	// playMu serializes playback stream creation in PlayFrame.
	playMu sync.Mutex

	// mu guards the fields below and is only held for short, non-blocking
	// sections (never across native Close or a consumer callback).
	mu              sync.Mutex
	closed          bool
	capState        darwinAudioStreamState
	capture         *darwinAudioCaptureSession
	captureCallback func(*core.AudioFrame)
	playback        *darwinAudioPlaybackSession
	playFailAt      time.Time
	playFailErr     error
	devIn, devOut   uint32
	lastCaptureErr  error
	watchStarted    bool
	watchCancel     func()
	watchStop       chan struct{}
	watchDone       chan struct{}

	seq            atomic.Uint64
	captureDropped atomic.Uint64
	captureGated   atomic.Uint64

	now func() time.Time
}

func newDarwinAudioQueueBridge(native darwinAudioNative, errs darwinAudioBridgeErrors) *darwinAudioQueueBridge {
	return &darwinAudioQueueBridge{native: native, errs: errs, now: time.Now}
}

func (b *darwinAudioQueueBridge) StartCapture(loopback bool, onFrame func(*core.AudioFrame)) error {
	if onFrame == nil {
		return nil
	}
	if loopback {
		return b.errs.Loopback
	}
	b.capMu.Lock()
	defer b.capMu.Unlock()
	b.stopCaptureLocked()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return b.errs.Closed
	}
	b.captureCallback = onFrame
	b.mu.Unlock()
	return b.openCaptureLocked(onFrame)
}

// openCaptureLocked requires capMu.
func (b *darwinAudioQueueBridge) openCaptureLocked(onFrame func(*core.AudioFrame)) error {
	fail := func(err error) error {
		b.mu.Lock()
		b.lastCaptureErr = err
		b.capState, _ = darwinAudioNextState(b.capState, darwinAudioEventFailed)
		b.mu.Unlock()
		return err
	}
	if err := b.native.Available(); err != nil {
		return fail(err)
	}
	status := b.native.MicrophoneAuthorization()
	if err := darwinAudioAuthError(status, b.errs.Permission); err != nil {
		return fail(err)
	}
	if status == darwinAudioAuthNotDetermined {
		// Shows the system prompt asynchronously; frames are gated below
		// until the user grants access, so nothing is faked meanwhile.
		b.native.RequestMicrophoneAccess()
	}
	format, err := aqPCM16Description(darwinAudioSampleRate, darwinAudioChannels)
	if err != nil {
		return fail(err)
	}
	session := &darwinAudioCaptureSession{
		queue:      make(chan *core.AudioFrame, darwinAudioCaptureQueueCapacity),
		stopCh:     make(chan struct{}),
		workerDone: make(chan struct{}),
	}
	queue := session.queue
	onData := func(p []byte) {
		if len(p) == 0 {
			return
		}
		frame := &core.AudioFrame{
			Seq:        b.seq.Add(1),
			SampleRate: darwinAudioSampleRate,
			Channels:   darwinAudioChannels,
			Timestamp:  time.Now().UnixMilli(),
			Data:       append([]byte(nil), p...),
		}
		select {
		case queue <- frame:
		default:
			b.captureDropped.Add(1)
		}
	}
	bufferBytes := aqBufferBytes(darwinAudioSampleRate, darwinAudioChannels, darwinAudioFrameDuration)
	stream, err := b.native.OpenInput(format, bufferBytes, darwinAudioCaptureBufferCount, onData)
	if err != nil {
		// Keep watching devices so plugging in a microphone later restarts
		// capture with the remembered consumer.
		b.ensureDeviceWatch()
		return fail(fmt.Errorf("%w: %v", b.errs.Device, err))
	}
	session.stream = stream
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		_ = stream.Close()
		return b.errs.Closed
	}
	b.capture = session
	b.lastCaptureErr = nil
	b.capState, _ = darwinAudioNextState(b.capState, darwinAudioEventStarted)
	b.mu.Unlock()
	go b.captureWorker(session, onFrame, status == darwinAudioAuthAuthorized)
	b.ensureDeviceWatch()
	return nil
}

func (b *darwinAudioQueueBridge) captureWorker(s *darwinAudioCaptureSession, onFrame func(*core.AudioFrame), authorized bool) {
	defer close(s.workerDone)
	var nextCheck time.Time
	for {
		select {
		case <-s.stopCh:
			return
		default:
		}
		select {
		case <-s.stopCh:
			return
		case frame := <-s.queue:
			if !authorized {
				if now := b.now(); !now.Before(nextCheck) {
					nextCheck = now.Add(darwinAudioPermissionRecheck)
					status := b.native.MicrophoneAuthorization()
					switch status {
					case darwinAudioAuthAuthorized:
						authorized = true
					case darwinAudioAuthDenied, darwinAudioAuthRestricted:
						b.mu.Lock()
						b.lastCaptureErr = darwinAudioAuthError(status, b.errs.Permission)
						b.mu.Unlock()
					}
				}
				if !authorized {
					b.captureGated.Add(1)
					continue
				}
			}
			onFrame(frame)
		}
	}
}

func (b *darwinAudioQueueBridge) StopCapture() error {
	b.capMu.Lock()
	defer b.capMu.Unlock()
	b.mu.Lock()
	b.captureCallback = nil
	b.mu.Unlock()
	b.stopCaptureLocked()
	return nil
}

// stopCaptureLocked requires capMu. The native stream is closed first so
// the callback is detached before the worker is told to stop; the queue
// channel is never closed, so a late send cannot panic.
func (b *darwinAudioQueueBridge) stopCaptureLocked() {
	b.mu.Lock()
	session := b.capture
	b.capture = nil
	if session != nil && b.capState == darwinAudioStateRunning {
		b.capState, _ = darwinAudioNextState(b.capState, darwinAudioEventStopped)
	}
	b.mu.Unlock()
	if session == nil {
		return
	}
	_ = session.stream.Close()
	close(session.stopCh)
	<-session.workerDone
}

func (b *darwinAudioQueueBridge) PlayFrame(frame *core.AudioFrame, gain float32) error {
	if frame == nil || len(frame.Data) == 0 {
		return nil
	}
	sampleRate, channels := frame.SampleRate, frame.Channels
	if sampleRate <= 0 {
		sampleRate = darwinAudioSampleRate
	}
	if channels <= 0 {
		channels = darwinAudioChannels
	}
	data, channels, err := darwinAudioPlayableFormat(frame.Data, sampleRate, channels)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	session, err := b.playbackSession(sampleRate, channels)
	if err != nil {
		return err
	}
	if gain <= 0 {
		return nil
	}
	scaled := make([]byte, len(data))
	darwinAudioApplyGain(scaled, data, gain)
	session.jitter.Push(scaled)
	return nil
}

func (b *darwinAudioQueueBridge) playbackSession(sampleRate, channels int) (*darwinAudioPlaybackSession, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, b.errs.Closed
	}
	if s := b.playback; s != nil && s.sampleRate == sampleRate && s.channels == channels {
		b.mu.Unlock()
		return s, nil
	}
	b.mu.Unlock()

	b.playMu.Lock()
	defer b.playMu.Unlock()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, b.errs.Closed
	}
	current := b.playback
	if current != nil && current.sampleRate == sampleRate && current.channels == channels {
		b.mu.Unlock()
		return current, nil
	}
	if current == nil && b.playFailErr != nil && b.now().Sub(b.playFailAt) < darwinAudioPlaybackRetry {
		err := b.playFailErr
		b.mu.Unlock()
		return nil, err
	}
	b.playback = nil
	b.mu.Unlock()
	if current != nil {
		_ = current.stream.Close()
	}

	session, err := b.openPlayback(sampleRate, channels)
	b.mu.Lock()
	defer b.mu.Unlock()
	if err != nil {
		b.playFailAt, b.playFailErr = b.now(), err
		return nil, err
	}
	if b.closed {
		_ = session.stream.Close()
		return nil, b.errs.Closed
	}
	b.playFailErr = nil
	b.playback = session
	go b.ensureDeviceWatch()
	return session, nil
}

func (b *darwinAudioQueueBridge) openPlayback(sampleRate, channels int) (*darwinAudioPlaybackSession, error) {
	if err := b.native.Available(); err != nil {
		return nil, err
	}
	format, err := aqPCM16Description(sampleRate, channels)
	if err != nil {
		return nil, err
	}
	stride := channels * darwinAudioBytesPerSample
	jitter := newDarwinAudioJitterQueue(
		aqBufferBytes(sampleRate, channels, darwinAudioJitterMaxDuration),
		aqBufferBytes(sampleRate, channels, darwinAudioJitterPrimeDuration),
		stride,
	)
	bufferBytes := aqBufferBytes(sampleRate, channels, darwinAudioFrameDuration)
	stream, err := b.native.OpenOutput(format, bufferBytes, darwinAudioPlaybackBufferCount, func(dst []byte) {
		jitter.Fill(dst)
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", b.errs.Device, err)
	}
	return &darwinAudioPlaybackSession{stream: stream, jitter: jitter, sampleRate: sampleRate, channels: channels}, nil
}

// RefreshDevices re-reads the default input/output devices and restarts the
// open streams when either changed (hot plug, default switch, coreaudiod
// restart).
func (b *darwinAudioQueueBridge) RefreshDevices() error {
	if err := b.native.Available(); err != nil {
		return fmt.Errorf("%w: %v", b.errs.Device, err)
	}
	in, out, err := b.native.DefaultDevices()
	if err != nil {
		return fmt.Errorf("%w: %v", b.errs.Device, err)
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return b.errs.Closed
	}
	changed := (b.devIn != 0 || b.devOut != 0) && (in != b.devIn || out != b.devOut)
	b.devIn, b.devOut = in, out
	b.mu.Unlock()
	if changed {
		if err := b.Restart(); err != nil {
			return err
		}
	}
	if in == aqAudioObjectUnknown && out == aqAudioObjectUnknown {
		return b.errs.Device
	}
	return nil
}

// Restart recreates the open streams against the current default devices.
// Capture is reopened immediately with the same consumer; playback is
// recreated lazily by the next PlayFrame.
func (b *darwinAudioQueueBridge) Restart() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return b.errs.Closed
	}
	playback := b.playback
	b.playback = nil
	b.playFailErr = nil
	b.mu.Unlock()
	if playback != nil {
		_ = playback.stream.Close()
	}
	if err := b.native.Available(); err != nil {
		return err
	}
	if in, out, err := b.native.DefaultDevices(); err == nil {
		b.mu.Lock()
		b.devIn, b.devOut = in, out
		b.mu.Unlock()
	}

	b.capMu.Lock()
	defer b.capMu.Unlock()
	b.mu.Lock()
	callback := b.captureCallback
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return b.errs.Closed
	}
	if callback == nil {
		return nil
	}
	b.stopCaptureLocked()
	return b.openCaptureLocked(callback)
}

// ensureDeviceWatch lazily installs the default-device listener once a
// stream is open. Notifications are debounced and handled off the HAL thread.
func (b *darwinAudioQueueBridge) ensureDeviceWatch() {
	b.mu.Lock()
	if b.closed || b.watchStarted {
		b.mu.Unlock()
		return
	}
	b.watchStarted = true
	events := make(chan struct{}, 1)
	stop := make(chan struct{})
	done := make(chan struct{})
	b.watchStop, b.watchDone = stop, done
	b.mu.Unlock()

	if in, out, err := b.native.DefaultDevices(); err == nil {
		b.mu.Lock()
		if b.devIn == 0 && b.devOut == 0 {
			b.devIn, b.devOut = in, out
		}
		b.mu.Unlock()
	}
	cancel, err := b.native.WatchDevices(func() {
		select {
		case events <- struct{}{}:
		default:
		}
	})
	b.mu.Lock()
	if err == nil {
		b.watchCancel = cancel
	}
	closed := b.closed
	b.mu.Unlock()
	if err != nil || closed {
		if err == nil && cancel != nil && closed {
			// Close ran concurrently and could not see the cancel func.
			cancel()
			b.mu.Lock()
			b.watchCancel = nil
			b.mu.Unlock()
		}
		close(done)
		return
	}
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-events:
			}
			timer := time.NewTimer(darwinAudioDeviceDebounce)
		debounce:
			for {
				select {
				case <-stop:
					timer.Stop()
					return
				case <-events:
				case <-timer.C:
					break debounce
				}
			}
			_ = b.RefreshDevices()
		}
	}()
}

func (b *darwinAudioQueueBridge) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	cancel, stop, done := b.watchCancel, b.watchStop, b.watchDone
	b.watchCancel = nil
	playback := b.playback
	b.playback = nil
	b.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if stop != nil {
		close(stop)
	}
	if done != nil {
		<-done
	}
	if playback != nil {
		_ = playback.stream.Close()
	}
	b.capMu.Lock()
	b.stopCaptureLocked()
	b.mu.Lock()
	b.captureCallback = nil
	b.capState, _ = darwinAudioNextState(b.capState, darwinAudioEventClosed)
	b.mu.Unlock()
	b.capMu.Unlock()
	return nil
}

// darwinAudioBridgeStats is a diagnostic snapshot.
type darwinAudioBridgeStats struct {
	CaptureState     darwinAudioStreamState
	CaptureErr       error
	CaptureDropped   uint64
	CaptureGated     uint64
	PlaybackOpen     bool
	PlaybackBuffered int
	PlaybackUnderrun uint64
	PlaybackDropped  uint64
}

func (b *darwinAudioQueueBridge) stats() darwinAudioBridgeStats {
	b.mu.Lock()
	st := darwinAudioBridgeStats{CaptureState: b.capState, CaptureErr: b.lastCaptureErr}
	playback := b.playback
	b.mu.Unlock()
	st.CaptureDropped = b.captureDropped.Load()
	st.CaptureGated = b.captureGated.Load()
	if playback != nil {
		st.PlaybackOpen = true
		st.PlaybackBuffered = playback.jitter.Buffered()
		st.PlaybackUnderrun, st.PlaybackDropped = playback.jitter.Stats()
	}
	return st
}
