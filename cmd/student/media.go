package main

import (
	"context"
	"time"

	"nexlab/internal/media"
	"nexlab/internal/protocol"
)

func (rt *Runtime) broadcastDecodeLoop() {
	for {
		select {
		case <-rt.lifecycleCtx.Done():
			return
		case data := <-rt.bcastDecodeCh:
			frame, err := rt.Media.DecodeFrame(data)
			if err == nil && frame != nil {
				rt.App.UpdateVideoFrame(frame)
			}
		}
	}
}

func enqueueLatestFrame(queue chan []byte, data []byte) bool {
	select {
	case queue <- data:
		return true
	default:
	}
	select {
	case <-queue:
	default:
	}
	select {
	case queue <- data:
		return true
	default:
		return false
	}
}

func (rt *Runtime) startMonitorStream(maxW, maxH, fps, quality int) {
	rt.startMonitorStreamWithReset(maxW, maxH, fps, quality, false)
}

func shouldRestartMonitorStream(current, requested monitorStreamConfig, reset bool) bool {
	return reset || current != requested
}

func (rt *Runtime) startMonitorStreamWithReset(maxW, maxH, fps, quality int, reset bool) {
	if maxW <= 0 {
		maxW = 320
	}
	if maxH <= 0 {
		maxH = 180
	}
	if fps <= 0 {
		fps = 10
	}
	if fps > 30 {
		fps = 30
	}
	if quality <= 0 {
		quality = 65
	}
	cfg := monitorStreamConfig{maxW: maxW, maxH: maxH, fps: fps, quality: quality}

	rt.streamMu.Lock()
	if rt.monitorStop != nil && !shouldRestartMonitorStream(rt.monitorCfg, cfg, reset) {
		rt.streamMu.Unlock()
		return
	}
	parentCtx := rt.ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	rt.streamMu.Unlock()

	rt.stopMonitorStream()

	ctx, cancel := context.WithCancel(parentCtx)
	rt.streamMu.Lock()
	rt.monitorStop = cancel
	rt.monitorCfg = cfg
	rt.streamMu.Unlock()
	rt.goLifecycle(func(context.Context) { rt.runScreenStream(ctx, "monitor", maxW, maxH, fps, quality) })
}

func (rt *Runtime) stopMonitorStream() {
	rt.streamMu.Lock()
	cancel := rt.monitorStop
	rt.monitorStop = nil
	rt.monitorCfg = monitorStreamConfig{}
	rt.streamMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (rt *Runtime) startRemoteStream() {
	rt.stopRemoteStream()
	ctx, cancel := context.WithCancel(rt.ctx)
	rt.streamMu.Lock()
	rt.remoteStop = cancel
	rt.streamMu.Unlock()
	rt.goLifecycle(func(context.Context) { rt.runScreenStream(ctx, "remote", 1280, 720, 15, 75) })
}

func (rt *Runtime) stopRemoteStream() {
	rt.streamMu.Lock()
	cancel := rt.remoteStop
	rt.remoteStop = nil
	rt.streamMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (rt *Runtime) startDemoStream() {
	rt.stopDemoStream()
	ctx, cancel := context.WithCancel(rt.ctx)
	rt.streamMu.Lock()
	rt.demoActive = true
	rt.demoStop = cancel
	rt.streamMu.Unlock()
	rt.goLifecycle(func(context.Context) { rt.runScreenStream(ctx, "demo", 1280, 720, 15, 75) })
}

func (rt *Runtime) stopDemoStream() {
	rt.streamMu.Lock()
	rt.demoActive = false
	cancel := rt.demoStop
	rt.demoStop = nil
	rt.streamMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (rt *Runtime) runScreenStream(ctx context.Context, kind string, maxW, maxH, fps, quality int) {
	codec, err := media.NewHybridVideoCodec(false)
	if err != nil {
		return
	}
	defer codec.Close()

	interval := time.Second / time.Duration(fps)
	if interval < 33*time.Millisecond {
		interval = 33 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var seq uint64
	forceKeyFrame := true
	kfInterval := uint64(60)
	if kind == "monitor" {
		kfInterval = 5
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if len(rt.Transport.Sessions()) == 0 {
				seq = 0
				forceKeyFrame = true
				continue
			}
			frame, err := rt.Media.CaptureScreenMax(maxW, maxH)
			if err != nil || frame == nil {
				forceKeyFrame = true
				continue
			}
			seq++
			frame.Seq = seq
			frame.KeyFrame = forceKeyFrame || seq == 1 || seq%kfInterval == 0
			encoded, err := codec.Encode(frame, quality)
			if err != nil {
				forceKeyFrame = true
				continue
			}
			env, err := protocol.NewEnvelope(protocol.MsgVideoFrame, rt.studentID, videoStreamPacket{
				Kind: kind,
				Data: encoded,
			})
			if err != nil {
				forceKeyFrame = true
				continue
			}
			env.Channel = rt.channel
			env.Timestamp = time.Now().UnixMilli()
			if err := rt.Transport.BroadcastControl(env, nil); err != nil {
				forceKeyFrame = true
				continue
			}
			forceKeyFrame = false
		}
	}
}
