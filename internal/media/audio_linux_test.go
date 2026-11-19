//go:build linux

package media

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nexlab/internal/core"
)

var errCommandMissing = errors.New("command missing")

func waitForLinuxAudioCondition(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("condition not met within %s", timeout)
		case <-ticker.C:
		}
	}
}

func TestLinuxAudioCommandSelectionUsesAvailableFallback(t *testing.T) {
	original := linuxLookPath
	defer func() { linuxLookPath = original }()
	linuxLookPath = func(name string) (string, error) {
		if name == "parec" {
			return "/usr/bin/parec", nil
		}
		return "", errCommandMissing
	}

	command, ok := linuxCaptureCommand(false)
	if !ok || command.name != "/usr/bin/parec" {
		t.Fatalf("expected parec fallback, got command=%+v ok=%v", command, ok)
	}
	if len(command.args) == 0 || command.args[0] != "--raw" {
		t.Fatalf("expected raw PCM arguments, got %v", command.args)
	}
}

func TestLinuxPlaybackCommandUsesInjectedExecutable(t *testing.T) {
	original := linuxLookPath
	defer func() { linuxLookPath = original }()
	linuxLookPath = func(name string) (string, error) {
		if name == "pw-cat" {
			return "/opt/bin/pw-cat", nil
		}
		return "", errCommandMissing
	}

	command, ok := linuxPlaybackCommand(&core.AudioFrame{SampleRate: 44100, Channels: 1})
	if !ok || command.name != "/opt/bin/pw-cat" {
		t.Fatalf("expected injected pw-cat path, got command=%+v ok=%v", command, ok)
	}
	if !containsArgPair(command.args, "--rate", "44100") || !containsArgPair(command.args, "--channels", "1") {
		t.Fatalf("expected frame format arguments, got %v", command.args)
	}
}

func containsArgPair(args []string, key, value string) bool {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == key && args[index+1] == value {
			return true
		}
	}
	return false
}

func TestLinuxAudioPlaybackCommandUsesFrameFormat(t *testing.T) {
	frame := &core.AudioFrame{SampleRate: 44100, Channels: 1}
	command, ok := linuxPlaybackCommand(frame)
	if !ok {
		t.Skip("no PipeWire or PulseAudio playback command installed")
	}
	if command.name == "" || len(command.args) == 0 {
		t.Fatalf("unexpected playback command: %+v", command)
	}
}

func TestLinuxAudioCaptureRestartsAfterProcessExit(t *testing.T) {
	originalLookPath, originalCommand := linuxLookPath, linuxExecCommand
	defer func() { linuxLookPath, linuxExecCommand = originalLookPath, originalCommand }()
	linuxLookPath = func(name string) (string, error) {
		if name == "arecord" {
			return "/test/arecord", nil
		}
		return "", errCommandMissing
	}
	linuxExecCommand = func(_ string, args ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=TestLinuxAudioHelperProcess", "--")
		cmd.Env = append(os.Environ(), "NEXLAB_AUDIO_HELPER=capture")
		return cmd
	}

	engine := NewAudioEngine()
	defer engine.Close()
	frames := make(chan struct{}, 4)
	if err := engine.StartCapture(false, func(frame *core.AudioFrame) {
		if len(frame.Data) > 0 {
			frames <- struct{}{}
		}
	}); err != nil {
		t.Fatalf("StartCapture: %v", err)
	}
	for index := 0; index < 2; index++ {
		select {
		case <-frames:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for capture restart frame %d", index+1)
		}
	}
}

func TestLinuxAudioPlaybackRebuildsAfterProcessExit(t *testing.T) {
	originalLookPath, originalCommand := linuxLookPath, linuxExecCommand
	defer func() { linuxLookPath, linuxExecCommand = originalLookPath, originalCommand }()
	linuxLookPath = func(name string) (string, error) {
		if name == "paplay" {
			return "/test/paplay", nil
		}
		return "", errCommandMissing
	}
	var starts atomic.Int32
	linuxExecCommand = func(_ string, args ...string) *exec.Cmd {
		starts.Add(1)
		cmd := exec.Command(os.Args[0], "-test.run=TestLinuxAudioHelperProcess", "--")
		cmd.Env = append(os.Environ(), "NEXLAB_AUDIO_HELPER=playback")
		return cmd
	}

	engine := NewAudioEngine()
	defer engine.Close()
	frame := &core.AudioFrame{SampleRate: 48000, Channels: 2, Data: []byte{1, 0, 2, 0}}
	if err := engine.PlayFrame(frame); err != nil {
		t.Fatalf("first PlayFrame: %v", err)
	}
	waitForLinuxAudioCondition(t, 2*time.Second, func() bool {
		return starts.Load() >= 1
	})
	waitForLinuxAudioCondition(t, 2*time.Second, func() bool {
		engine.mu.Lock()
		defer engine.mu.Unlock()
		return engine.playIn == nil
	})
	if err := engine.PlayFrame(frame); err != nil {
		t.Fatalf("second PlayFrame after playback exit: %v", err)
	}
	if starts.Load() < 2 {
		t.Fatalf("expected playback process rebuild, started %d times", starts.Load())
	}
}

func TestLinuxAudioUnavailableWhenNoCommandExists(t *testing.T) {
	original := linuxLookPath
	defer func() { linuxLookPath = original }()
	linuxLookPath = func(string) (string, error) { return "", errCommandMissing }
	engine := NewAudioEngine()
	defer engine.Close()
	if err := engine.StartCapture(false, func(*core.AudioFrame) {}); !errors.Is(err, ErrLinuxAudioUnavailable) {
		t.Fatalf("expected unavailable capture error, got %v", err)
	}
	if err := engine.PlayFrame(&core.AudioFrame{SampleRate: 48000, Channels: 2, Data: []byte{0, 0}}); !errors.Is(err, ErrLinuxAudioUnavailable) {
		t.Fatalf("expected unavailable playback error, got %v", err)
	}
}

func TestLinuxAudioHelperProcess(t *testing.T) {
	if os.Getenv("NEXLAB_AUDIO_HELPER") == "" {
		return
	}
	if os.Getenv("NEXLAB_AUDIO_HELPER") == "capture" {
		_, _ = os.Stdout.Write(make([]byte, 3840))
		return
	}
	if os.Getenv("NEXLAB_AUDIO_HELPER") == "playback" {
		buffer := make([]byte, 4)
		_, _ = os.Stdin.Read(buffer)
		return
	}
	if os.Getenv("NEXLAB_AUDIO_HELPER") == "playback-race" {
		buffer := make([]byte, 4)
		_, _ = os.Stdin.Read(buffer)
		time.Sleep(10 * time.Millisecond)
		return
	}
	t.Fatalf("unknown helper mode %q", os.Getenv("NEXLAB_AUDIO_HELPER"))
}

func TestLinuxAudioCommandArgumentsUsePortableFormats(t *testing.T) {
	original := linuxLookPath
	defer func() { linuxLookPath = original }()
	linuxLookPath = func(name string) (string, error) {
		if strings.Contains(name, "pw-cat") {
			return "/usr/bin/pw-cat", nil
		}
		return "", errCommandMissing
	}
	command, ok := linuxCaptureCommand(true)
	if !ok || !containsArgPair(command.args, "--format", "s16") || !containsArgPair(command.args, "--rate", "48000") {
		t.Fatalf("unexpected loopback capture command: %+v ok=%v", command, ok)
	}
}

func TestLinuxAudioPlayFrameAndCloseCanRaceWithPlaybackWatcher(t *testing.T) {
	originalLookPath, originalCommand := linuxLookPath, linuxExecCommand
	defer func() { linuxLookPath, linuxExecCommand = originalLookPath, originalCommand }()
	linuxLookPath = func(name string) (string, error) {
		if name == "paplay" {
			return "/test/paplay", nil
		}
		return "", errCommandMissing
	}
	linuxExecCommand = func(_ string, _ ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=TestLinuxAudioHelperProcess", "--")
		cmd.Env = append(os.Environ(), "NEXLAB_AUDIO_HELPER=playback-race")
		return cmd
	}

	engine := NewAudioEngine()
	frame := &core.AudioFrame{SampleRate: 48000, Channels: 2, Data: []byte{1, 0, 2, 0}}
	if err := engine.PlayFrame(frame); err != nil {
		t.Fatalf("initial PlayFrame: %v", err)
	}

	var calls atomic.Int32
	var wg sync.WaitGroup
	for index := 0; index < 32; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			calls.Add(1)
			_ = engine.PlayFrame(frame)
		}()
	}
	closeDone := make(chan struct{})
	go func() {
		_ = engine.Close()
		close(closeDone)
	}()
	wg.Wait()
	select {
	case <-closeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not finish while PlayFrame calls raced with playback watcher")
	}
	if calls.Load() != 32 {
		t.Fatalf("expected all PlayFrame calls to start, got %d", calls.Load())
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.playback != nil || engine.playIn != nil || engine.playDone != nil {
		t.Fatalf("playback state leaked after concurrent Close: playback=%v input=%v done=%v", engine.playback, engine.playIn, engine.playDone)
	}
}

func TestLinuxAudioPlaybackWatcherDoesNotInvalidateReplacement(t *testing.T) {
	originalLookPath, originalCommand := linuxLookPath, linuxExecCommand
	defer func() { linuxLookPath, linuxExecCommand = originalLookPath, originalCommand }()
	linuxLookPath = func(name string) (string, error) {
		if name == "paplay" {
			return "/test/paplay", nil
		}
		return "", errCommandMissing
	}
	var starts atomic.Int32
	linuxExecCommand = func(_ string, _ ...string) *exec.Cmd {
		starts.Add(1)
		cmd := exec.Command(os.Args[0], "-test.run=TestLinuxAudioHelperProcess", "--")
		cmd.Env = append(os.Environ(), "NEXLAB_AUDIO_HELPER=playback-race")
		return cmd
	}

	engine := NewAudioEngine()
	frame := &core.AudioFrame{SampleRate: 48000, Channels: 2, Data: []byte{1, 0, 2, 0}}
	if err := engine.PlayFrame(frame); err != nil {
		t.Fatalf("first PlayFrame: %v", err)
	}
	waitForLinuxAudioCondition(t, 3*time.Second, func() bool {
		return starts.Load() >= 1
	})
	waitForLinuxAudioCondition(t, 3*time.Second, func() bool {
		engine.mu.Lock()
		defer engine.mu.Unlock()
		return engine.playIn == nil && engine.playback == nil
	})
	if err := engine.PlayFrame(frame); err != nil {
		t.Fatalf("replacement PlayFrame: %v", err)
	}
	if starts.Load() < 2 {
		t.Fatalf("expected a replacement playback process, started %d times", starts.Load())
	}
	if err := engine.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestLinuxAudioCloseIsIdempotentAndPreventsRestart(t *testing.T) {
	originalLookPath := linuxLookPath
	defer func() { linuxLookPath = originalLookPath }()
	linuxLookPath = func(name string) (string, error) {
		if name == "paplay" || name == "arecord" {
			return "/test/" + name, nil
		}
		return "", errCommandMissing
	}

	engine := NewAudioEngine()
	if err := engine.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := engine.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := engine.PlayFrame(&core.AudioFrame{SampleRate: 48000, Channels: 2, Data: []byte{0, 0}}); !errors.Is(err, ErrLinuxAudioClosed) {
		t.Fatalf("expected closed playback error, got %v", err)
	}
	if err := engine.StartCapture(false, func(*core.AudioFrame) {}); !errors.Is(err, ErrLinuxAudioClosed) {
		t.Fatalf("expected closed capture error, got %v", err)
	}
}
