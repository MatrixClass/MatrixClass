package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"nexlab/internal/core"
)

func TestDarwinAudioASBDLayoutAndEncoding(t *testing.T) {
	if got := unsafe.Sizeof(aqStreamDescription{}); got != 40 {
		t.Fatalf("AudioStreamBasicDescription size = %d, want 40", got)
	}
	if got := unsafe.Offsetof(aqStreamDescription{}.FormatID); got != 8 {
		t.Fatalf("mFormatID offset = %d, want 8", got)
	}
	if got := unsafe.Offsetof(aqStreamDescription{}.Reserved); got != 36 {
		t.Fatalf("mReserved offset = %d, want 36", got)
	}
	if got := unsafe.Sizeof(aqPropertyAddress{}); got != 12 {
		t.Fatalf("AudioObjectPropertyAddress size = %d, want 12", got)
	}
	if aqFourCC("lpcm") != aqFormatLinearPCM || aqFourCC("dIn ") != aqHardwarePropertyDefaultInput ||
		aqFourCC("dOut") != aqHardwarePropertyDefaultOutput || aqFourCC("glob") != aqPropertyScopeGlobal ||
		aqFourCC("dev#") != aqHardwarePropertyDevices || aqFourCC("rnlp") != aqHardwarePropertyRunLoop {
		t.Fatal("four character code constants do not match their strings")
	}
	if aqFourCC("abc") != 0 {
		t.Fatal("invalid fourcc must be zero")
	}

	d, err := aqPCM16Description(48000, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := aqStreamDescription{SampleRate: 48000, FormatID: aqFormatLinearPCM, FormatFlags: 0xC,
		BytesPerPacket: 4, FramesPerPacket: 1, BytesPerFrame: 4, ChannelsPerFrame: 2, BitsPerChannel: 16}
	if d != want {
		t.Fatalf("ASBD = %+v, want %+v", d, want)
	}
	if d.FormatFlags&(aqFormatFlagIsBigEndian|aqFormatFlagIsFloat) != 0 {
		t.Fatal("PCM16 must be little-endian integer")
	}
	mono, _ := aqPCM16Description(16000, 1)
	if mono.BytesPerFrame != 2 || mono.ChannelsPerFrame != 1 || mono.SampleRate != 16000 {
		t.Fatalf("mono ASBD = %+v", mono)
	}
	for _, bad := range [][2]int{{0, 2}, {7999, 2}, {192001, 2}, {48000, 0}, {48000, 3}} {
		if _, err := aqPCM16Description(bad[0], bad[1]); err == nil {
			t.Fatalf("expected error for %v", bad)
		}
	}
}

func TestDarwinAudioQueueBufferLayout64(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("AudioQueueBuffer mirror is for 64-bit targets")
	}
	var b aqBuffer
	offsets := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"mAudioData", unsafe.Offsetof(b.AudioData), 8},
		{"mAudioDataByteSize", unsafe.Offsetof(b.AudioDataByteSize), 16},
		{"mUserData", unsafe.Offsetof(b.UserData), 24},
		{"mPacketDescriptionCapacity", unsafe.Offsetof(b.PacketDescriptionCapacity), 32},
		{"mPacketDescriptions", unsafe.Offsetof(b.PacketDescriptions), 40},
		{"mPacketDescriptionCount", unsafe.Offsetof(b.PacketDescriptionCount), 48},
	}
	for _, o := range offsets {
		if o.got != o.want {
			t.Fatalf("%s offset = %d, want %d", o.name, o.got, o.want)
		}
	}
}

func TestDarwinAudioBufferSizing(t *testing.T) {
	cases := []struct {
		rate, ch int
		d        time.Duration
		want     int
	}{
		{48000, 2, 20 * time.Millisecond, 3840},
		{48000, 1, 20 * time.Millisecond, 1920},
		{44100, 2, 20 * time.Millisecond, 882 * 4},
		{8000, 1, 10 * time.Millisecond, 160},
		{8000, 1, time.Microsecond, 2},
		{0, 2, time.Second, 0},
		{48000, 2, 0, 0},
	}
	for _, c := range cases {
		if got := aqBufferBytes(c.rate, c.ch, c.d); got != c.want {
			t.Fatalf("aqBufferBytes(%d,%d,%v) = %d, want %d", c.rate, c.ch, c.d, got, c.want)
		}
	}
}

func TestDarwinAudioOSStatusString(t *testing.T) {
	if got := aqOSStatusString(int32(aqFourCC("fmt?"))); got != "'fmt?' (1718449215)" {
		t.Fatalf("got %q", got)
	}
	if got := aqOSStatusString(-50); got != "-50" {
		t.Fatalf("got %q", got)
	}
}

func pcm16(samples ...int16) []byte {
	out := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(s))
	}
	return out
}

func TestDarwinAudioApplyGain(t *testing.T) {
	src := pcm16(1000, -1000, 32767, -32768)
	dst := make([]byte, len(src))
	darwinAudioApplyGain(dst, src, 1)
	if !bytes.Equal(dst, src) {
		t.Fatal("unity gain must copy")
	}
	darwinAudioApplyGain(dst, src, 0.5)
	if !bytes.Equal(dst, pcm16(500, -500, 16383, -16384)) {
		t.Fatalf("half gain = %v", dst)
	}
	darwinAudioApplyGain(dst, src, 2)
	if !bytes.Equal(dst, pcm16(2000, -2000, 32767, -32768)) {
		t.Fatalf("gain must saturate, got %v", dst)
	}
	darwinAudioApplyGain(dst, src, 0)
	if !bytes.Equal(dst, make([]byte, len(src))) {
		t.Fatal("zero gain must silence")
	}
	// In place, odd length ignored trailing byte.
	odd := append(pcm16(100), 7)
	if n := darwinAudioApplyGain(odd, odd, 0.5); n != 2 || !bytes.Equal(odd[:2], pcm16(50)) || odd[2] != 7 {
		t.Fatalf("in-place odd gain: n=%d data=%v", n, odd)
	}
}

func TestDarwinAudioPlayableFormat(t *testing.T) {
	data, ch, err := darwinAudioPlayableFormat(pcm16(1, 2, 3), 48000, 2)
	if err != nil || ch != 2 || !bytes.Equal(data, pcm16(1, 2)) {
		t.Fatalf("partial frame not trimmed: %v %d %v", data, ch, err)
	}
	data, ch, err = darwinAudioPlayableFormat(pcm16(1, 2, 3, 4, 5, 6, 7, 8), 48000, 4)
	if err != nil || ch != 2 || !bytes.Equal(data, pcm16(1, 2, 5, 6)) {
		t.Fatalf("4ch downmix: %v %d %v", data, ch, err)
	}
	if _, _, err := darwinAudioPlayableFormat(pcm16(1), 1000, 1); err == nil {
		t.Fatal("expected sample rate error")
	}
	if _, _, err := darwinAudioPlayableFormat(pcm16(1), 48000, 9); err == nil {
		t.Fatal("expected channel error")
	}
}

func TestDarwinAudioJitterQueuePrimingUnderrunAndOverflow(t *testing.T) {
	q := newDarwinAudioJitterQueue(16, 8, 4)
	dst := make([]byte, 8)
	q.Push(pcm16(1, 1))
	for i := range dst {
		dst[i] = 0xff
	}
	if n := q.Fill(dst); n != 0 || !bytes.Equal(dst, make([]byte, 8)) {
		t.Fatalf("must output silence until primed: n=%d %v", n, dst)
	}
	q.Push(pcm16(2, 2))
	if n := q.Fill(dst); n != 8 || !bytes.Equal(dst, pcm16(1, 1, 2, 2)) {
		t.Fatalf("primed fill: n=%d %v", n, dst)
	}
	// Underrun: partial data then silence, and the queue re-primes.
	q.Push(pcm16(3, 3))
	if n := q.Fill(dst); n != 4 || !bytes.Equal(dst, pcm16(3, 3, 0, 0)) {
		t.Fatalf("underrun fill: n=%d %v", n, dst)
	}
	if u, _ := q.Stats(); u != 1 {
		t.Fatalf("underruns = %d", u)
	}
	q.Push(pcm16(4, 4))
	if n := q.Fill(dst); n != 0 {
		t.Fatalf("must re-prime after underrun, n=%d", n)
	}
	// Overflow drops oldest whole frames; wraparound keeps order.
	q.Push(pcm16(5, 5, 6, 6, 7, 7))
	if dropped := q.Push(pcm16(8, 8)); dropped != 4 {
		t.Fatalf("dropped = %d, want 4", dropped)
	}
	if q.Buffered() != 16 {
		t.Fatalf("buffered = %d", q.Buffered())
	}
	big := make([]byte, 16)
	if n := q.Fill(big); n != 16 || !bytes.Equal(big, pcm16(5, 5, 6, 6, 7, 7, 8, 8)) {
		t.Fatalf("wrapped fill: n=%d %v", n, big)
	}
	// Oversized push keeps the newest audio only; partial frames discarded.
	q.Push(append(pcm16(9, 9, 10, 10, 11, 11, 12, 12, 13, 13), 1))
	if n := q.Fill(big); n != 16 || !bytes.Equal(big, pcm16(10, 10, 11, 11, 12, 12, 13, 13)) {
		t.Fatalf("oversized push: %v", big)
	}
}

func TestDarwinAudioJitterQueueConcurrentAccess(t *testing.T) {
	q := newDarwinAudioJitterQueue(3840*10, 3840*2, 4)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		frame := make([]byte, 3840)
		for i := 0; i < 500; i++ {
			q.Push(frame)
		}
	}()
	go func() {
		defer wg.Done()
		dst := make([]byte, 3840)
		for i := 0; i < 500; i++ {
			q.Fill(dst)
		}
	}()
	wg.Wait()
	if q.Buffered() > 3840*10 {
		t.Fatalf("queue exceeded capacity: %d", q.Buffered())
	}
}

func TestDarwinAudioAuthMappingAndStateMachine(t *testing.T) {
	perm := errors.New("perm")
	for _, s := range []darwinAudioAuthStatus{darwinAudioAuthDenied, darwinAudioAuthRestricted, darwinAudioAuthMissingUsageDescription} {
		if err := darwinAudioAuthError(s, perm); !errors.Is(err, perm) {
			t.Fatalf("%s must map to permission error, got %v", s, err)
		}
	}
	for _, s := range []darwinAudioAuthStatus{darwinAudioAuthAuthorized, darwinAudioAuthNotDetermined, darwinAudioAuthUnknown} {
		if err := darwinAudioAuthError(s, perm); err != nil {
			t.Fatalf("%s must not fail start, got %v", s, err)
		}
	}
	st := darwinAudioStateIdle
	steps := []struct {
		ev   darwinAudioStreamEvent
		want darwinAudioStreamState
	}{
		{darwinAudioEventStarted, darwinAudioStateRunning},
		{darwinAudioEventStopped, darwinAudioStateIdle},
		{darwinAudioEventFailed, darwinAudioStateFailed},
		{darwinAudioEventStarted, darwinAudioStateRunning},
		{darwinAudioEventClosed, darwinAudioStateClosed},
	}
	for _, step := range steps {
		next, err := darwinAudioNextState(st, step.ev)
		if err != nil || next != step.want {
			t.Fatalf("%s + %d = %s, %v; want %s", st, step.ev, next, err, step.want)
		}
		st = next
	}
	if _, err := darwinAudioNextState(st, darwinAudioEventStarted); err == nil {
		t.Fatal("closed must be terminal")
	}
	if next, err := darwinAudioNextState(st, darwinAudioEventClosed); err != nil || next != darwinAudioStateClosed {
		t.Fatal("close must be idempotent")
	}
}

// ---- bridge tests with a fake native layer ----

type fakeDarwinAudioStream struct {
	native *fakeDarwinAudioNative
	input  bool
	closed atomic.Bool
	mu     sync.Mutex
	onData func([]byte)
	fill   func([]byte)
	format aqStreamDescription
	size   int
}

func (s *fakeDarwinAudioStream) Close() error {
	s.mu.Lock()
	s.onData, s.fill = nil, nil // detach: no callbacks after Close
	s.mu.Unlock()
	s.closed.Store(true)
	s.native.closes.Add(1)
	return nil
}

func (s *fakeDarwinAudioStream) deliver(p []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.onData == nil {
		return false
	}
	s.onData(p)
	return true
}

func (s *fakeDarwinAudioStream) pull() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fill == nil {
		return nil
	}
	dst := make([]byte, s.size)
	s.fill(dst)
	return dst
}

type fakeDarwinAudioNative struct {
	mu        sync.Mutex
	avail     error
	auth      darwinAudioAuthStatus
	requested int
	inputs    []*fakeDarwinAudioStream
	outputs   []*fakeDarwinAudioStream
	openErr   error
	devIn     uint32
	devOut    uint32
	onChange  func()
	cancelled atomic.Bool
	closes    atomic.Int32
}

func (n *fakeDarwinAudioNative) Available() error { return n.avail }
func (n *fakeDarwinAudioNative) MicrophoneAuthorization() darwinAudioAuthStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.auth
}
func (n *fakeDarwinAudioNative) RequestMicrophoneAccess() {
	n.mu.Lock()
	n.requested++
	n.mu.Unlock()
}
func (n *fakeDarwinAudioNative) OpenInput(f aqStreamDescription, size, count int, onData func([]byte)) (darwinAudioNativeStream, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.openErr != nil {
		return nil, n.openErr
	}
	s := &fakeDarwinAudioStream{native: n, input: true, onData: onData, format: f, size: size}
	n.inputs = append(n.inputs, s)
	return s, nil
}
func (n *fakeDarwinAudioNative) OpenOutput(f aqStreamDescription, size, count int, fill func([]byte)) (darwinAudioNativeStream, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.openErr != nil {
		return nil, n.openErr
	}
	s := &fakeDarwinAudioStream{native: n, fill: fill, format: f, size: size}
	n.outputs = append(n.outputs, s)
	return s, nil
}
func (n *fakeDarwinAudioNative) DefaultDevices() (uint32, uint32, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.devIn, n.devOut, nil
}
func (n *fakeDarwinAudioNative) WatchDevices(onChange func()) (func(), error) {
	n.mu.Lock()
	n.onChange = onChange
	n.mu.Unlock()
	return func() { n.cancelled.Store(true) }, nil
}
func (n *fakeDarwinAudioNative) lastInput() *fakeDarwinAudioStream {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.inputs) == 0 {
		return nil
	}
	return n.inputs[len(n.inputs)-1]
}
func (n *fakeDarwinAudioNative) lastOutput() *fakeDarwinAudioStream {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.outputs) == 0 {
		return nil
	}
	return n.outputs[len(n.outputs)-1]
}

var testDarwinAudioErrs = darwinAudioBridgeErrors{
	Unavailable: errors.New("unavailable"),
	Permission:  errors.New("permission"),
	Device:      errors.New("device"),
	Loopback:    errors.New("loopback"),
	Closed:      errors.New("closed"),
}

func newTestDarwinBridge() (*darwinAudioQueueBridge, *fakeDarwinAudioNative) {
	n := &fakeDarwinAudioNative{auth: darwinAudioAuthAuthorized, devIn: 10, devOut: 20}
	return newDarwinAudioQueueBridge(n, testDarwinAudioErrs), n
}

func TestDarwinAudioBridgeCaptureDeliversFramesAndNeverBlocks(t *testing.T) {
	b, n := newTestDarwinBridge()
	defer b.Close()
	release := make(chan struct{})
	got := make(chan *core.AudioFrame, 100)
	if err := b.StartCapture(false, func(f *core.AudioFrame) {
		got <- f
		<-release // a stalled consumer must not stall the audio callback
	}); err != nil {
		t.Fatal(err)
	}
	in := n.lastInput()
	if in.format.SampleRate != 48000 || in.format.ChannelsPerFrame != 2 || in.size != 3840 {
		t.Fatalf("unexpected capture format %+v size %d", in.format, in.size)
	}
	buf := pcm16(1, 2, 3, 4)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			in.deliver(buf)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("capture callback blocked on a slow consumer")
	}
	first := <-got
	if first.SampleRate != 48000 || first.Channels != 2 || !bytes.Equal(first.Data, buf) || first.Seq != 1 {
		t.Fatalf("unexpected frame %+v", first)
	}
	if b.stats().CaptureDropped == 0 {
		t.Fatal("expected drops with a stalled consumer")
	}
	close(release)
	if err := b.StopCapture(); err != nil {
		t.Fatal(err)
	}
	if !in.closed.Load() || in.deliver(buf) {
		t.Fatal("native stream must be closed and detached after StopCapture")
	}
	if b.stats().CaptureState != darwinAudioStateIdle {
		t.Fatalf("state = %s", b.stats().CaptureState)
	}
}

func TestDarwinAudioBridgeCaptureCopiesNativeBuffer(t *testing.T) {
	b, n := newTestDarwinBridge()
	defer b.Close()
	got := make(chan *core.AudioFrame, 1)
	_ = b.StartCapture(false, func(f *core.AudioFrame) { got <- f })
	buf := pcm16(7, 7)
	n.lastInput().deliver(buf)
	buf[0] = 0xAA // native buffer is reused by AudioQueue
	f := <-got
	if !bytes.Equal(f.Data, pcm16(7, 7)) {
		t.Fatal("frame data must be a copy of the AudioQueue buffer")
	}
}

func TestDarwinAudioBridgeLoopbackAndPermission(t *testing.T) {
	b, n := newTestDarwinBridge()
	defer b.Close()
	if err := b.StartCapture(true, func(*core.AudioFrame) {}); !errors.Is(err, testDarwinAudioErrs.Loopback) {
		t.Fatalf("loopback err = %v", err)
	}
	n.auth = darwinAudioAuthDenied
	if err := b.StartCapture(false, func(*core.AudioFrame) {}); !errors.Is(err, testDarwinAudioErrs.Permission) {
		t.Fatalf("denied err = %v", err)
	}
	if n.lastInput() != nil {
		t.Fatal("must not open input when permission is denied")
	}
	if st := b.stats(); st.CaptureState != darwinAudioStateFailed || !errors.Is(st.CaptureErr, testDarwinAudioErrs.Permission) {
		t.Fatalf("stats = %+v", st)
	}
	if err := b.StartCapture(false, nil); err != nil {
		t.Fatal("nil callback is a no-op")
	}
}

func TestDarwinAudioBridgeGatesFramesUntilAuthorized(t *testing.T) {
	b, n := newTestDarwinBridge()
	defer b.Close()
	n.auth = darwinAudioAuthNotDetermined
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	b.now = func() time.Time { return time.Unix(0, clock.Load()) }
	got := make(chan *core.AudioFrame, 10)
	if err := b.StartCapture(false, func(f *core.AudioFrame) { got <- f }); err != nil {
		t.Fatal(err)
	}
	if n.requested != 1 {
		t.Fatalf("access requests = %d", n.requested)
	}
	in := n.lastInput()
	in.deliver(pcm16(1, 1))
	waitFor(t, func() bool { return b.stats().CaptureGated == 1 })
	select {
	case <-got:
		t.Fatal("frame delivered before authorization")
	default:
	}
	n.mu.Lock()
	n.auth = darwinAudioAuthAuthorized
	n.mu.Unlock()
	clock.Add(int64(time.Second))
	in.deliver(pcm16(2, 2))
	select {
	case f := <-got:
		if !bytes.Equal(f.Data, pcm16(2, 2)) {
			t.Fatal("wrong frame")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("frame not delivered after authorization")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDarwinAudioBridgePlaybackGainJitterAndFormatSwitch(t *testing.T) {
	b, n := newTestDarwinBridge()
	defer b.Close()
	frame := &core.AudioFrame{SampleRate: 48000, Channels: 2, Data: bytes.Repeat(pcm16(1000, -1000), 960)}
	for i := 0; i < 3; i++ {
		if err := b.PlayFrame(frame, 0.5); err != nil {
			t.Fatal(err)
		}
	}
	out := n.lastOutput()
	if out.format.SampleRate != 48000 || out.format.ChannelsPerFrame != 2 || out.size != 3840 {
		t.Fatalf("output format %+v size %d", out.format, out.size)
	}
	pulled := out.pull()
	if !bytes.Equal(pulled[:4], pcm16(500, -500)) {
		t.Fatalf("gain not applied: %v", pulled[:4])
	}
	if frame.Data[0] != byte(1000&0xff) {
		t.Fatal("PlayFrame must not modify the caller's buffer")
	}
	// Flooding never blocks and stays bounded (200 ms).
	for i := 0; i < 100; i++ {
		_ = b.PlayFrame(frame, 1)
	}
	if st := b.stats(); st.PlaybackBuffered > 48000/5*4 || st.PlaybackDropped == 0 {
		t.Fatalf("jitter queue not bounded: %+v", st)
	}
	// Underrun produces silence.
	for i := 0; i < 20; i++ {
		out.pull()
	}
	if s := out.pull(); !bytes.Equal(s, make([]byte, len(s))) {
		t.Fatal("underrun must produce silence")
	}
	// Format change recreates the output queue.
	if err := b.PlayFrame(&core.AudioFrame{SampleRate: 16000, Channels: 1, Data: pcm16(1, 2)}, 1); err != nil {
		t.Fatal(err)
	}
	if !out.closed.Load() {
		t.Fatal("old output must be closed on format change")
	}
	if o := n.lastOutput(); o.format.SampleRate != 16000 || o.format.ChannelsPerFrame != 1 {
		t.Fatalf("new output %+v", o.format)
	}
	// Muted playback keeps the stream but queues nothing.
	if err := b.PlayFrame(&core.AudioFrame{SampleRate: 16000, Channels: 1, Data: pcm16(5)}, 0); err != nil || b.stats().PlaybackBuffered != 4 {
		t.Fatalf("muted: err=%v buffered=%d", err, b.stats().PlaybackBuffered)
	}
}

func TestDarwinAudioBridgePlaybackOpenFailureIsRateLimited(t *testing.T) {
	b, n := newTestDarwinBridge()
	defer b.Close()
	n.openErr = errors.New("no device")
	frame := &core.AudioFrame{SampleRate: 48000, Channels: 2, Data: pcm16(1, 1)}
	if err := b.PlayFrame(frame, 1); !errors.Is(err, testDarwinAudioErrs.Device) {
		t.Fatalf("err = %v", err)
	}
	n.openErr = nil
	if err := b.PlayFrame(frame, 1); err == nil {
		t.Fatal("expected cached failure within retry window")
	}
	base := time.Now()
	b.now = func() time.Time { return base.Add(2 * darwinAudioPlaybackRetry) }
	if err := b.PlayFrame(frame, 1); err != nil {
		t.Fatalf("retry after window failed: %v", err)
	}
}

func TestDarwinAudioBridgeRestartAndDeviceChange(t *testing.T) {
	b, n := newTestDarwinBridge()
	defer b.Close()
	got := make(chan *core.AudioFrame, 10)
	_ = b.StartCapture(false, func(f *core.AudioFrame) { got <- f })
	_ = b.PlayFrame(&core.AudioFrame{SampleRate: 48000, Channels: 2, Data: pcm16(1, 1)}, 1)
	in1, out1 := n.lastInput(), n.lastOutput()

	if err := b.RefreshDevices(); err != nil {
		t.Fatal(err)
	}
	if in1.closed.Load() {
		t.Fatal("unchanged devices must not restart streams")
	}
	n.mu.Lock()
	n.devIn = 11
	onChange := n.onChange
	n.mu.Unlock()
	if onChange == nil {
		t.Fatal("device watcher not installed")
	}
	onChange()
	onChange() // debounced
	waitFor(t, func() bool { return in1.closed.Load() && n.lastInput() != in1 })
	if !out1.closed.Load() {
		t.Fatal("playback must be torn down on device change")
	}
	n.lastInput().deliver(pcm16(3, 3))
	select {
	case f := <-got:
		if !bytes.Equal(f.Data, pcm16(3, 3)) {
			t.Fatal("wrong frame after restart")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("capture consumer not reattached after restart")
	}
	if err := b.Restart(); err != nil {
		t.Fatal(err)
	}
	_ = b.StopCapture()
	before := len(n.inputs)
	if err := b.Restart(); err != nil || len(n.inputs) != before {
		t.Fatal("restart after StopCapture must not reopen capture")
	}
	n.mu.Lock()
	n.devIn, n.devOut = 0, 0
	n.mu.Unlock()
	if err := b.RefreshDevices(); !errors.Is(err, testDarwinAudioErrs.Device) {
		t.Fatalf("no devices err = %v", err)
	}
}

func TestDarwinAudioBridgeCloseIsCleanAndIdempotent(t *testing.T) {
	b, n := newTestDarwinBridge()
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	_ = b.StartCapture(false, func(*core.AudioFrame) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-block
	})
	_ = b.PlayFrame(&core.AudioFrame{SampleRate: 48000, Channels: 2, Data: pcm16(1, 1)}, 1)
	in, out := n.lastInput(), n.lastOutput()
	in.deliver(pcm16(1, 1))
	<-entered
	closed := make(chan struct{})
	go func() {
		_ = b.Close()
		close(closed)
	}()
	// Close detaches native streams before waiting on the consumer.
	waitFor(t, func() bool { return in.closed.Load() && out.closed.Load() })
	close(block)
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not finish")
	}
	if !n.cancelled.Load() {
		t.Fatal("device watcher not cancelled")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.StartCapture(false, func(*core.AudioFrame) {}); !errors.Is(err, testDarwinAudioErrs.Closed) {
		t.Fatalf("start after close = %v", err)
	}
	if err := b.PlayFrame(&core.AudioFrame{SampleRate: 48000, Channels: 2, Data: pcm16(1, 1)}, 1); !errors.Is(err, testDarwinAudioErrs.Closed) {
		t.Fatalf("play after close = %v", err)
	}
	if err := b.Restart(); !errors.Is(err, testDarwinAudioErrs.Closed) {
		t.Fatalf("restart after close = %v", err)
	}
	if b.stats().CaptureState != darwinAudioStateClosed {
		t.Fatalf("state = %s", b.stats().CaptureState)
	}
}

func TestDarwinAudioBridgeConcurrentLifecycle(t *testing.T) {
	b, n := newTestDarwinBridge()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			frame := &core.AudioFrame{SampleRate: 48000, Channels: 2, Data: pcm16(1, 1)}
			for {
				select {
				case <-stop:
					return
				default:
				}
				switch i {
				case 0:
					_ = b.StartCapture(false, func(*core.AudioFrame) {})
				case 1:
					_ = b.PlayFrame(frame, 0.7)
				case 2:
					_ = b.Restart()
				case 3:
					if s := n.lastInput(); s != nil {
						s.deliver(pcm16(1, 1))
					}
					if s := n.lastOutput(); s != nil {
						s.pull()
					}
				}
			}
		}(i)
	}
	time.Sleep(50 * time.Millisecond)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	close(stop)
	wg.Wait()
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, s := range append(append([]*fakeDarwinAudioStream{}, n.inputs...), n.outputs...) {
		if !s.closed.Load() {
			t.Fatal("stream leaked after Close")
		}
	}
}
