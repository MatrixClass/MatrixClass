//go:build linux

package media

import (
	"errors"
	"testing"

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
