package media

// Portable building blocks for the macOS AudioQueue bridge. This file has no
// build constraint on purpose: the ABI mirrors, sizing math, jitter queue,
// gain and state machine are pure Go so they can be unit-tested on any OS.
// Every identifier is prefixed with darwinAudio/aq to avoid clashing with the
// other platform files of package media.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"
)

const (
	darwinAudioSampleRate      = 48000
	darwinAudioChannels        = 2
	darwinAudioBytesPerSample  = 2
	darwinAudioFrameDuration   = 20 * time.Millisecond
	darwinAudioMinSampleRate   = 8000
	darwinAudioMaxSampleRate   = 192000
	darwinAudioMaxPlayChannels = 2

	// darwinAudioCaptureQueueCapacity bounds frames waiting for the consumer;
	// the AudioQueue callback drops frames instead of blocking when it is full.
	darwinAudioCaptureQueueCapacity = 8
	darwinAudioCaptureBufferCount   = 3
	darwinAudioPlaybackBufferCount  = 3

	// Playback jitter queue: at most 200 ms buffered (oldest audio dropped
	// beyond that) and 40 ms of priming after start/underrun.
	darwinAudioJitterMaxDuration   = 200 * time.Millisecond
	darwinAudioJitterPrimeDuration = 40 * time.Millisecond

	darwinAudioPermissionRecheck = 500 * time.Millisecond
	darwinAudioDeviceDebounce    = 300 * time.Millisecond
	darwinAudioPlaybackRetry     = time.Second
)

// CoreAudio constants (CoreAudioTypes.h / AudioHardware.h).
const (
	aqFormatLinearPCM           uint32 = 0x6C70636D // 'lpcm'
	aqFormatFlagIsFloat         uint32 = 1 << 0
	aqFormatFlagIsBigEndian     uint32 = 1 << 1
	aqFormatFlagIsSignedInteger uint32 = 1 << 2
	aqFormatFlagIsPacked        uint32 = 1 << 3

	aqSystemObject                  uint32 = 1
	aqPropertyScopeGlobal           uint32 = 0x676C6F62 // 'glob'
	aqPropertyElementMain           uint32 = 0
	aqHardwarePropertyDefaultInput  uint32 = 0x64496E20 // 'dIn '
	aqHardwarePropertyDefaultOutput uint32 = 0x644F7574 // 'dOut'
	aqHardwarePropertyDevices       uint32 = 0x64657623 // 'dev#'
	aqHardwarePropertyRunLoop       uint32 = 0x726E6C70 // 'rnlp'
	aqAudioObjectUnknown            uint32 = 0
)

// aqStreamDescription mirrors AudioStreamBasicDescription (40 bytes, natural
// alignment on both x86_64 and arm64).
type aqStreamDescription struct {
	SampleRate       float64
	FormatID         uint32
	FormatFlags      uint32
	BytesPerPacket   uint32
	FramesPerPacket  uint32
	BytesPerFrame    uint32
	ChannelsPerFrame uint32
	BitsPerChannel   uint32
	Reserved         uint32
}

// aqBuffer mirrors AudioQueueBuffer on 64-bit Darwin.
type aqBuffer struct {
	AudioDataBytesCapacity    uint32
	AudioData                 unsafe.Pointer
	AudioDataByteSize         uint32
	UserData                  unsafe.Pointer
	PacketDescriptionCapacity uint32
	PacketDescriptions        unsafe.Pointer
	PacketDescriptionCount    uint32
}

// aqPropertyAddress mirrors AudioObjectPropertyAddress.
type aqPropertyAddress struct {
	Selector uint32
	Scope    uint32
	Element  uint32
}

// aqFourCC converts a four character code to its numeric value.
func aqFourCC(code string) uint32 {
	if len(code) != 4 {
		return 0
	}
	return binary.BigEndian.Uint32([]byte(code))
}

// aqOSStatusString renders an OSStatus, as a four character code when every
// byte is printable (the AudioToolbox convention) and as an integer otherwise.
func aqOSStatusString(status int32) string {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(status))
	printable := true
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			printable = false
			break
		}
	}
	if printable {
		return fmt.Sprintf("'%s' (%d)", string(b[:]), status)
	}
	return fmt.Sprintf("%d", status)
}

// aqPCM16Description builds an interleaved, packed, signed 16-bit
// little-endian linear PCM description.
func aqPCM16Description(sampleRate, channels int) (aqStreamDescription, error) {
	if sampleRate < darwinAudioMinSampleRate || sampleRate > darwinAudioMaxSampleRate {
		return aqStreamDescription{}, fmt.Errorf("media: unsupported audio sample rate %d", sampleRate)
	}
	if channels < 1 || channels > darwinAudioMaxPlayChannels {
		return aqStreamDescription{}, fmt.Errorf("media: unsupported audio channel count %d", channels)
	}
	bytesPerFrame := uint32(channels * darwinAudioBytesPerSample)
	return aqStreamDescription{
		SampleRate:       float64(sampleRate),
		FormatID:         aqFormatLinearPCM,
		FormatFlags:      aqFormatFlagIsSignedInteger | aqFormatFlagIsPacked,
		BytesPerPacket:   bytesPerFrame,
		FramesPerPacket:  1,
		BytesPerFrame:    bytesPerFrame,
		ChannelsPerFrame: uint32(channels),
		BitsPerChannel:   16,
	}, nil
}

// aqBufferBytes returns the byte size of duration worth of PCM16 audio,
// rounded down to whole frames and at least one frame.
func aqBufferBytes(sampleRate, channels int, duration time.Duration) int {
	if sampleRate <= 0 || channels <= 0 || duration <= 0 {
		return 0
	}
	frames := int(int64(sampleRate) * int64(duration) / int64(time.Second))
	if frames < 1 {
		frames = 1
	}
	return frames * channels * darwinAudioBytesPerSample
}

// darwinAudioApplyGain writes src scaled by gain into dst (which may alias
// src) with saturation, returning the number of bytes written.
func darwinAudioApplyGain(dst, src []byte, gain float32) int {
	n := len(src) &^ 1
	if len(dst) < n {
		n = len(dst) &^ 1
	}
	switch {
	case gain >= 0.999 && gain <= 1.001:
		copy(dst[:n], src[:n])
	case gain <= 0:
		clear(dst[:n])
	default:
		for i := 0; i < n; i += 2 {
			v := float32(int16(binary.LittleEndian.Uint16(src[i:]))) * gain
			if v > 32767 {
				v = 32767
			} else if v < -32768 {
				v = -32768
			}
			binary.LittleEndian.PutUint16(dst[i:], uint16(int16(v)))
		}
	}
	return n
}

// darwinAudioPlayableFormat normalizes an incoming frame to a format the
// output queue accepts: whole frames only and at most two channels (extra
// channels are dropped, keeping front left/right).
func darwinAudioPlayableFormat(data []byte, sampleRate, channels int) ([]byte, int, error) {
	if sampleRate < darwinAudioMinSampleRate || sampleRate > darwinAudioMaxSampleRate {
		return nil, 0, fmt.Errorf("media: unsupported audio sample rate %d", sampleRate)
	}
	if channels < 1 || channels > 8 {
		return nil, 0, fmt.Errorf("media: unsupported audio channel count %d", channels)
	}
	stride := channels * darwinAudioBytesPerSample
	frames := len(data) / stride
	if frames == 0 {
		return nil, channels, nil
	}
	if channels <= darwinAudioMaxPlayChannels {
		return data[:frames*stride], channels, nil
	}
	out := make([]byte, frames*2*darwinAudioBytesPerSample)
	for f := 0; f < frames; f++ {
		copy(out[f*4:f*4+4], data[f*stride:f*stride+4])
	}
	return out, 2, nil
}

// darwinAudioJitterQueue is a bounded PCM FIFO between PlayFrame (producer)
// and the AudioQueue output callback (consumer). Push never blocks: when the
// queue is full the oldest audio is discarded. Fill never blocks either: on
// underrun it emits silence and re-primes before resuming, which avoids
// rapid stutter on a jittery network.
type darwinAudioJitterQueue struct {
	mu        sync.Mutex
	buf       []byte
	head      int
	size      int
	stride    int
	prime     int
	primed    bool
	underruns uint64
	dropped   uint64
}

func newDarwinAudioJitterQueue(capacity, prime, stride int) *darwinAudioJitterQueue {
	if stride <= 0 {
		stride = 1
	}
	capacity -= capacity % stride
	if capacity < stride {
		capacity = stride
	}
	prime -= prime % stride
	if prime > capacity {
		prime = capacity
	}
	if prime < 0 {
		prime = 0
	}
	return &darwinAudioJitterQueue{buf: make([]byte, capacity), stride: stride, prime: prime}
}

// Push appends whole frames and returns the number of bytes dropped to stay
// within capacity.
func (q *darwinAudioJitterQueue) Push(p []byte) int {
	p = p[:len(p)-len(p)%q.stride]
	if len(p) == 0 {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	capacity := len(q.buf)
	dropped := 0
	if len(p) > capacity {
		dropped += len(p) - capacity
		p = p[len(p)-capacity:]
	}
	if over := q.size + len(p) - capacity; over > 0 {
		q.head = (q.head + over) % capacity
		q.size -= over
		dropped += over
	}
	tail := (q.head + q.size) % capacity
	n := copy(q.buf[tail:], p)
	copy(q.buf, p[n:])
	q.size += len(p)
	if dropped > 0 {
		q.dropped += uint64(dropped)
	}
	return dropped
}

// Fill copies queued audio into dst, zero-filling whatever is not available,
// and returns the number of audio bytes copied.
func (q *darwinAudioJitterQueue) Fill(dst []byte) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.primed {
		if q.size < q.prime || q.size == 0 {
			clear(dst)
			return 0
		}
		q.primed = true
	}
	want := len(dst) - len(dst)%q.stride
	n := want
	if n > q.size {
		n = q.size
	}
	capacity := len(q.buf)
	first := copy(dst[:n], q.buf[q.head:])
	copy(dst[first:n], q.buf[:n-first])
	q.head = (q.head + n) % capacity
	q.size -= n
	clear(dst[n:])
	if n < want {
		q.underruns++
		q.primed = false
	}
	return n
}

func (q *darwinAudioJitterQueue) Buffered() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.size
}

func (q *darwinAudioJitterQueue) Stats() (underruns, dropped uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.underruns, q.dropped
}

// darwinAudioAuthStatus mirrors AVAuthorizationStatus.
type darwinAudioAuthStatus int

const (
	darwinAudioAuthNotDetermined darwinAudioAuthStatus = 0
	darwinAudioAuthRestricted    darwinAudioAuthStatus = 1
	darwinAudioAuthDenied        darwinAudioAuthStatus = 2
	darwinAudioAuthAuthorized    darwinAudioAuthStatus = 3
	// darwinAudioAuthUnknown is used when AVFoundation cannot be queried.
	darwinAudioAuthUnknown darwinAudioAuthStatus = -1
	// darwinAudioAuthMissingUsageDescription: the app bundle has no
	// NSMicrophoneUsageDescription, so touching the microphone would make TCC
	// terminate the process.
	darwinAudioAuthMissingUsageDescription darwinAudioAuthStatus = -2
)

func (s darwinAudioAuthStatus) String() string {
	switch s {
	case darwinAudioAuthNotDetermined:
		return "not-determined"
	case darwinAudioAuthRestricted:
		return "restricted"
	case darwinAudioAuthDenied:
		return "denied"
	case darwinAudioAuthAuthorized:
		return "authorized"
	case darwinAudioAuthMissingUsageDescription:
		return "missing NSMicrophoneUsageDescription"
	default:
		return "unknown"
	}
}

// darwinAudioAuthError maps an authorization status to the error StartCapture
// must return, or nil when capture may proceed (authorized, prompt pending or
// status unknown; frames are gated until authorized in the latter cases).
func darwinAudioAuthError(status darwinAudioAuthStatus, permission error) error {
	switch status {
	case darwinAudioAuthDenied, darwinAudioAuthRestricted, darwinAudioAuthMissingUsageDescription:
		return fmt.Errorf("%w (%s)", permission, status)
	default:
		return nil
	}
}

// darwinAudioStreamState is the capture lifecycle state machine.
type darwinAudioStreamState int

const (
	darwinAudioStateIdle darwinAudioStreamState = iota
	darwinAudioStateRunning
	darwinAudioStateFailed
	darwinAudioStateClosed
)

func (s darwinAudioStreamState) String() string {
	switch s {
	case darwinAudioStateIdle:
		return "idle"
	case darwinAudioStateRunning:
		return "running"
	case darwinAudioStateFailed:
		return "failed"
	case darwinAudioStateClosed:
		return "closed"
	default:
		return "invalid"
	}
}

type darwinAudioStreamEvent int

const (
	darwinAudioEventStarted darwinAudioStreamEvent = iota
	darwinAudioEventFailed
	darwinAudioEventStopped
	darwinAudioEventClosed
)

var errDarwinAudioInvalidTransition = errors.New("media: invalid audio stream state transition")

// darwinAudioNextState returns the state after event. Closed is terminal.
func darwinAudioNextState(cur darwinAudioStreamState, ev darwinAudioStreamEvent) (darwinAudioStreamState, error) {
	if cur == darwinAudioStateClosed {
		if ev == darwinAudioEventClosed {
			return cur, nil
		}
		return cur, errDarwinAudioInvalidTransition
	}
	switch ev {
	case darwinAudioEventStarted:
		return darwinAudioStateRunning, nil
	case darwinAudioEventFailed:
		return darwinAudioStateFailed, nil
	case darwinAudioEventStopped:
		return darwinAudioStateIdle, nil
	case darwinAudioEventClosed:
		return darwinAudioStateClosed, nil
	}
	return cur, errDarwinAudioInvalidTransition
}
