//go:build linux

package media

import (
	"testing"

	"nexlab/internal/core"
)

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
