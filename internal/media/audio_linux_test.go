//go:build linux

package media

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"nexlab/internal/core"
)

var errCommandMissing = errors.New("command missing")

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
	starts := 0
	linuxExecCommand = func(_ string, args ...string) *exec.Cmd {
		starts++
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
	deadline := time.Now().Add(2 * time.Second)
	for starts < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	for engine.playIn != nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := engine.PlayFrame(frame); err != nil {
		t.Fatalf("second PlayFrame after playback exit: %v", err)
	}
	if starts < 2 {
		t.Fatalf("expected playback process rebuild, started %d times", starts)
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
