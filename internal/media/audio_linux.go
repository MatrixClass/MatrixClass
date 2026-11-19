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
var ErrLinuxAudioClosed = errors.New("media: audio engine is closed")

var linuxLookPath = exec.LookPath
var linuxExecCommand = exec.Command

const linuxAudioCaptureQueueCapacity = 8

type linuxAudioCommand struct {
	name string
	args []string
}

type AudioEngine struct {
	mu                sync.Mutex
	volume            float32
	stopCh            chan struct{}
	doneCh            chan struct{}
	capture           *exec.Cmd
	captureQueue      chan *core.AudioFrame
	captureWorkerDone chan struct{}
	playback          *exec.Cmd
	playIn            io.WriteCloser
	playDone          chan struct{}
	closed            bool
	captureDropped    uint64
}

func NewAudioEngine() *AudioEngine { return &AudioEngine{volume: 1.0} }

func (a *AudioEngine) StartCapture(loopback bool, onFrame func(*core.AudioFrame)) error {
	if onFrame == nil {
		return nil
	}
	_ = a.StopCapture()
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return ErrLinuxAudioClosed
	}
	a.mu.Unlock()
	command, ok := linuxCaptureCommand(loopback)
	if !ok {
		return ErrLinuxAudioUnavailable
	}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	workerDone := make(chan struct{})
	captureQueue := make(chan *core.AudioFrame, linuxAudioCaptureQueueCapacity)
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return ErrLinuxAudioClosed
	}
	a.stopCh = stopCh
	a.doneCh = doneCh
	a.capture = nil
	a.captureQueue = captureQueue
	a.captureWorkerDone = workerDone
	a.mu.Unlock()
	go linuxAudioCaptureWorker(stopCh, captureQueue, workerDone, onFrame)
	go func() {
		defer close(doneCh)
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
			cmd := linuxExecCommand(command.name, command.args...)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				return
			}
			if err := cmd.Start(); err != nil {
				if !waitLinuxAudioRetry(stopCh, 250*time.Millisecond) {
					return
				}
				continue
			}
			a.mu.Lock()
			if a.stopCh == stopCh {
				a.capture = cmd
			}
			a.mu.Unlock()
			for {
				if _, err := io.ReadFull(stdout, buffer); err != nil {
					break
				}
				sequence++
				data := append([]byte(nil), buffer...)
				frame := &core.AudioFrame{Seq: sequence, SampleRate: sampleRate, Channels: channels, Timestamp: time.Now().UnixMilli(), Data: data}
				select {
				case <-stopCh:
					return
				case captureQueue <- frame:
				default:
					a.mu.Lock()
					a.captureDropped++
					a.mu.Unlock()
				}
			}
			_ = cmd.Wait()
			a.mu.Lock()
			if a.capture == cmd {
				a.capture = nil
			}
			a.mu.Unlock()
			select {
			case <-stopCh:
				return
			default:
			}
			if !waitLinuxAudioRetry(stopCh, 250*time.Millisecond) {
				return
			}
		}
	}()
	return nil
}

func linuxAudioCaptureWorker(stopCh <-chan struct{}, queue <-chan *core.AudioFrame, doneCh chan<- struct{}, onFrame func(*core.AudioFrame)) {
	defer close(doneCh)
	for {
		select {
		case <-stopCh:
			return
		default:
		}
		select {
		case <-stopCh:
			return
		case frame, ok := <-queue:
			if !ok || frame == nil {
				return
			}
			onFrame(frame)
		}
	}
}

func waitLinuxAudioRetry(stopCh <-chan struct{}, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-stopCh:
		return false
	case <-timer.C:
		return true
	}
}

func (a *AudioEngine) StopCapture() error {
	a.mu.Lock()
	stopCh, doneCh, cmd := a.stopCh, a.doneCh, a.capture
	workerDone, queue := a.captureWorkerDone, a.captureQueue
	a.stopCh, a.doneCh, a.capture = nil, nil, nil
	a.captureWorkerDone, a.captureQueue = nil, nil
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
	if queue != nil {
		close(queue)
	}
	if workerDone != nil {
		<-workerDone
	}
	return nil
}

func (a *AudioEngine) PlayFrame(frame *core.AudioFrame) error {
	if frame == nil || len(frame.Data) == 0 {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return ErrLinuxAudioClosed
	}
	if err := a.ensurePlaybackLocked(frame); err != nil {
		return err
	}
	volume := a.volume
	if volume <= 0 {
		return nil
	}
	adjusted := make([]byte, len(frame.Data))
	if volume >= 0.999 {
		adjusted = frame.Data
	} else {
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
	}
	_, err := a.playIn.Write(adjusted)
	if err != nil {
		a.resetPlaybackLocked()
		retryErr := a.ensurePlaybackLocked(frame)
		if retryErr == nil {
			_, retryErr = a.playIn.Write(adjusted)
		}
		return retryErr
	}
	return err
}

func (a *AudioEngine) ensurePlaybackLocked(frame *core.AudioFrame) error {
	if a.playIn != nil {
		return nil
	}
	command, ok := linuxPlaybackCommand(frame)
	if !ok {
		return ErrLinuxAudioUnavailable
	}
	cmd := linuxExecCommand(command.name, command.args...)
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	a.playback, a.playIn = cmd, input
	done := make(chan struct{})
	a.playDone = done
	go a.watchPlayback(cmd, done)
	return nil
}

func (a *AudioEngine) watchPlayback(cmd *exec.Cmd, done chan struct{}) {
	_ = cmd.Wait()
	a.mu.Lock()
	if a.playback == cmd {
		a.playback = nil
		a.playIn = nil
		a.playDone = nil
	}
	a.mu.Unlock()
	close(done)
}

func (a *AudioEngine) resetPlaybackLocked() {
	if a.playIn != nil {
		_ = a.playIn.Close()
	}
	if a.playback != nil && a.playback.Process != nil {
		_ = a.playback.Process.Kill()
	}
	a.playIn, a.playback, a.playDone = nil, nil, nil
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
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	input, cmd, done := a.playIn, a.playback, a.playDone
	a.playIn, a.playback, a.playDone = nil, nil, nil
	a.mu.Unlock()
	if input != nil {
		_ = input.Close()
	}
	if cmd != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	if done != nil {
		<-done
	}
	return nil
}

func linuxCaptureCommand(loopback bool) (linuxAudioCommand, bool) {
	if path, err := linuxLookPath("pw-cat"); err == nil {
		args := []string{"--record", "--raw", "--format", "s16", "--rate", "48000", "--channels", "2"}
		if loopback {
			args = append(args, "--target", "@DEFAULT_SINK@")
		}
		return linuxAudioCommand{name: path, args: args}, true
	}
	if path, err := linuxLookPath("parec"); err == nil {
		args := []string{"--raw", "--format=s16le", "--rate=48000", "--channels=2"}
		if loopback {
			args = append(args, "--device=@DEFAULT_SINK_MONITOR@")
		}
		return linuxAudioCommand{name: path, args: args}, true
	}
	if !loopback {
		if path, err := linuxLookPath("arecord"); err == nil {
			return linuxAudioCommand{name: path, args: []string{"-q", "-t", "raw", "-f", "S16_LE", "-r", "48000", "-c", "2"}}, true
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
	if path, err := linuxLookPath("pw-cat"); err == nil {
		return linuxAudioCommand{name: path, args: []string{"--playback", "--raw", "--format", "s16", "--rate", fmt.Sprint(frame.SampleRate), "--channels", fmt.Sprint(frame.Channels)}}, true
	}
	if path, err := linuxLookPath("paplay"); err == nil {
		return linuxAudioCommand{name: path, args: []string{"--raw", "--format=s16le", "--rate", fmt.Sprint(frame.SampleRate), "--channels", fmt.Sprint(frame.Channels)}}, true
	}
	return linuxAudioCommand{}, false
}
