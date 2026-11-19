//go:build linux

package media

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"nexlab/internal/core"
)

var ErrLinuxAudioUnavailable = errors.New("media: no PipeWire or PulseAudio command is available")

type linuxAudioCommand struct {
	name string
	args []string
}

type AudioEngine struct {
	mu       sync.Mutex
	volume   float32
	stopCh   chan struct{}
	doneCh   chan struct{}
	capture  *exec.Cmd
	playback *exec.Cmd
	playIn   io.WriteCloser
}

func NewAudioEngine() *AudioEngine { return &AudioEngine{volume: 1.0} }

func (a *AudioEngine) StartCapture(loopback bool, onFrame func(*core.AudioFrame)) error {
	if onFrame == nil {
		return nil
	}
	_ = a.StopCapture()
	command, ok := linuxCaptureCommand(loopback)
	if !ok {
		return ErrLinuxAudioUnavailable
	}
	cmd := exec.Command(command.name, command.args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	a.mu.Lock()
	a.stopCh = stopCh
	a.doneCh = doneCh
	a.capture = cmd
	a.mu.Unlock()
	go func() {
		defer close(doneCh)
		defer cmd.Wait()
		const sampleRate = 48000
		const channels = 2
		const bytesPerFrame = 2
		buffer := make([]byte, sampleRate/50*channels*bytesPerFrame)
		var sequence uint64
		for {
			select {
			case <-stopCh:
				return
			default:
			}
			if _, err := io.ReadFull(stdout, buffer); err != nil {
				return
			}
			sequence++
			data := append([]byte(nil), buffer...)
			onFrame(&core.AudioFrame{Seq: sequence, SampleRate: sampleRate, Channels: channels, Timestamp: time.Now().UnixMilli(), Data: data})
		}
	}()
	return nil
}

func (a *AudioEngine) StopCapture() error {
	a.mu.Lock()
	stopCh, doneCh, cmd := a.stopCh, a.doneCh, a.capture
	a.stopCh, a.doneCh, a.capture = nil, nil, nil
	a.mu.Unlock()
	if stopCh != nil {
		close(stopCh)
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	if doneCh != nil {
		<-doneCh
	}
	return nil
}

func (a *AudioEngine) PlayFrame(frame *core.AudioFrame) error {
	if frame == nil || len(frame.Data) == 0 {
		return nil
	}
	a.mu.Lock()
	if a.playIn == nil {
		command, ok := linuxPlaybackCommand(frame)
		if !ok {
			a.mu.Unlock()
			return ErrLinuxAudioUnavailable
		}
		cmd := exec.Command(command.name, command.args...)
		input, err := cmd.StdinPipe()
		if err != nil {
			a.mu.Unlock()
			return err
		}
		if err := cmd.Start(); err != nil {
			a.mu.Unlock()
			return err
		}
		a.playback, a.playIn = cmd, input
	}
	input := a.playIn
	volume := a.volume
	a.mu.Unlock()
	if volume <= 0 {
		return nil
	}
	if volume >= 0.999 {
		_, err := input.Write(frame.Data)
		return err
	}
	adjusted := make([]byte, len(frame.Data))
	for i := 0; i+1 < len(frame.Data); i += 2 {
		sample := int16(uint16(frame.Data[i]) | uint16(frame.Data[i+1])<<8)
		scaled := int(float32(sample) * volume)
		if scaled > 32767 {
			scaled = 32767
		} else if scaled < -32768 {
			scaled = -32768
		}
		adjusted[i], adjusted[i+1] = byte(scaled), byte(uint16(scaled)>>8)
	}
	_, err := input.Write(adjusted)
	return err
}

func (a *AudioEngine) SetVolume(vol float32) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if vol < 0 {
		vol = 0
	} else if vol > 1 {
		vol = 1
	}
	a.volume = vol
}

func (a *AudioEngine) Volume() float32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.volume
}

func (a *AudioEngine) Close() error {
	_ = a.StopCapture()
	a.mu.Lock()
	input, cmd := a.playIn, a.playback
	a.playIn, a.playback = nil, nil
	a.mu.Unlock()
	if input != nil {
		_ = input.Close()
	}
	if cmd != nil {
		_ = cmd.Wait()
	}
	return nil
}

func linuxCaptureCommand(loopback bool) (linuxAudioCommand, bool) {
	if _, err := exec.LookPath("pw-cat"); err == nil {
		args := []string{"--record", "--raw", "--format", "s16", "--rate", "48000", "--channels", "2"}
		if loopback {
			args = append(args, "--target", "@DEFAULT_SINK@")
		}
		return linuxAudioCommand{name: "pw-cat", args: args}, true
	}
	if _, err := exec.LookPath("parec"); err == nil {
		args := []string{"--raw", "--format=s16le", "--rate=48000", "--channels=2"}
		if loopback {
			args = append(args, "--device=@DEFAULT_SINK_MONITOR@")
		}
		return linuxAudioCommand{name: "parec", args: args}, true
	}
	if !loopback {
		if _, err := exec.LookPath("arecord"); err == nil {
			return linuxAudioCommand{name: "arecord", args: []string{"-q", "-t", "raw", "-f", "S16_LE", "-r", "48000", "-c", "2"}}, true
		}
	}
	return linuxAudioCommand{}, false
}

func linuxPlaybackCommand(frame *core.AudioFrame) (linuxAudioCommand, bool) {
	if frame.SampleRate <= 0 {
		frame.SampleRate = 48000
	}
	if frame.Channels <= 0 {
		frame.Channels = 2
	}
	if _, err := exec.LookPath("pw-cat"); err == nil {
		return linuxAudioCommand{name: "pw-cat", args: []string{"--playback", "--raw", "--format", "s16", "--rate", fmt.Sprint(frame.SampleRate), "--channels", fmt.Sprint(frame.Channels)}}, true
	}
	if _, err := exec.LookPath("paplay"); err == nil {
		return linuxAudioCommand{name: "paplay", args: []string{"--raw", "--format=s16le", "--rate", fmt.Sprint(frame.SampleRate), "--channels", fmt.Sprint(frame.Channels)}}, true
	}
	return linuxAudioCommand{}, false
}
