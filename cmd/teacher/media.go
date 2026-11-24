package main

import (
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"nexlab/internal/core"
	"nexlab/internal/media"
	"nexlab/internal/protocol"
)

func (rt *Runtime) resetStudentDecoder(key string) {
	rt.decMu.Lock()
	if dec, ok := rt.studentDecoders[key]; ok {
		if dec != nil {
			_ = dec.Close()
		}
		delete(rt.studentDecoders, key)
	}
	rt.decMu.Unlock()
}

func (rt *Runtime) decodeStudentFrame(studentID string, data []byte) (*core.VideoFrame, error) {
	rt.decMu.Lock()
	dec, ok := rt.studentDecoders[studentID]
	if !ok {
		var err error
		dec, err = media.NewHybridVideoCodec(false)
		if err != nil {
			rt.decMu.Unlock()
			return nil, err
		}
		rt.studentDecoders[studentID] = dec
	}
	rt.decMu.Unlock()
	return dec.Decode(data)
}

func (rt *Runtime) serveTheatreStream(w http.ResponseWriter, req *http.Request) {
	path := rt.App.TheatreMediaPath()
	if path == "" {
		http.Error(w, "network theatre is idle", http.StatusNotFound)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "media unavailable", http.StatusNotFound)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, "media unavailable", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, req, filepath.Base(path), info.ModTime(), f)
}

func localLANIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip4 := ip.To4(); ip4 != nil && !ip4.IsLoopback() {
				return ip4.String()
			}
		}
	}
	return ""
}

func (rt *Runtime) broadcastLoop(ctx context.Context) {
	ticker := time.NewTicker(66 * time.Millisecond)
	defer ticker.Stop()

	frameTrigger := make(chan struct{}, 1)
	var broadcastActive atomic.Bool
	go func() {
		var bcastSeq uint64
		var lastSentAt time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-frameTrigger:
				active := rt.App.Broadcasting() || rt.App.TheatrePlaying()
				if !active {
					broadcastActive.Store(false)
					continue
				}
				useMulticast := rt.App.UseMulticast()
				if !useMulticast && len(rt.Transport.Sessions()) == 0 {
					broadcastActive.Store(false)
					continue
				}
				frame, err := rt.Media.CaptureScreenMax(1280, 720)
				if err != nil {
					continue
				}
				changed := rt.Media.LastFrameChanged()
				explicitKF := !broadcastActive.Swap(true) || rt.forceKeyFrame.Swap(false)
				periodicKF := changed && bcastSeq > 0 && bcastSeq%150 == 0
				forceKF := explicitKF || periodicKF
				if !forceKF && !changed && time.Since(lastSentAt) < 2500*time.Millisecond {
					continue
				}
				bcastSeq++
				if forceKF {
					frame.KeyFrame = true
				}
				lastSentAt = time.Now()
				encoded, err := rt.Media.EncodeFrame(frame, int(rt.App.MonitorQual()))
				if err != nil {
					continue
				}
				_ = rt.Transport.BroadcastMulticast(encoded)
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rt.syncAudioCapture()
			rt.Transport.SetMulticastEnabled(rt.App.UseMulticast())
			if rt.App.Broadcasting() || rt.App.TheatrePlaying() {
				select {
				case frameTrigger <- struct{}{}:
				default:
				}
			} else {
				broadcastActive.Store(false)
			}
		}
	}
}

func (rt *Runtime) syncAudioCapture() {
	wantMode := ""
	if rt.App.VoiceActive() {
		wantMode = "voice"
	} else if rt.App.Broadcasting() || rt.App.TheatrePlaying() {
		wantMode = "loopback"
	}

	rt.audioMu.Lock()
	curMode := rt.audioMode
	if curMode == wantMode {
		rt.audioMu.Unlock()
		return
	}
	rt.audioMode = wantMode
	rt.audioMu.Unlock()

	if wantMode == "" {
		_ = rt.Media.StopAudioCapture()
		return
	}
	loopback := wantMode == "loopback"
	_ = rt.Media.StartAudioCapture(loopback, func(af *core.AudioFrame) {
		if wantMode == "voice" {
			gain := rt.App.EffectiveMicGain()
			if gain <= 0 {
				return
			}
			if gain != 1.0 && len(af.Data) >= 2 {
				for i := 0; i+1 < len(af.Data); i += 2 {
					s := int16(binary.LittleEndian.Uint16(af.Data[i : i+2]))
					v := float32(s) * gain
					if v > 32767 {
						v = 32767
					} else if v < -32768 {
						v = -32768
					}
					binary.LittleEndian.PutUint16(af.Data[i:i+2], uint16(int16(v)))
				}
			}
		}
		encoded, err := rt.Media.EncodeAudioFrame(af)
		if err != nil || len(encoded) == 0 {
			return
		}
		env, err := protocol.NewEnvelope(protocol.MsgAudioFrame, rt.teacherID, encoded)
		if err != nil {
			return
		}
		env.Channel = rt.channel
		env.Timestamp = time.Now().UnixMilli()
		_ = rt.Transport.BroadcastControl(env, nil)
	})
}
