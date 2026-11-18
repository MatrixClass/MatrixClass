//go:build windows

package media

import (
	"encoding/binary"
	"fmt"
	"math"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/windows"
	"nexlab/internal/core"
)

var procCoTaskMemFree = ole32DLL.NewProc("CoTaskMemFree")

const (
	eRender  = 0
	eCapture = 1
	eConsole = 0

	clsctxAll = 23

	audclntShareModeShared           = 0
	audclntStreamFlagsLoopback       = 0x00020000
	audclntStreamFlagsAutoConvertPCM = 0x80000000
	audclntStreamFlagsSrcDefaultQual = 0x08000000
	audclntBufferSilent              = 0x2

	waveFormatPCM        = 1
	waveFormatIEEEFloat  = 3
	waveFormatTagExtensible = 0xFFFE
)

var (
	clsidMMDeviceEnumerator = windows.GUID{Data1: 0xbcde0395, Data2: 0xe52f, Data3: 0x467c, Data4: [8]byte{0x8e, 0x3d, 0xc4, 0x57, 0x92, 0x91, 0x69, 0x2e}}
	iidIMMDeviceEnumerator  = windows.GUID{Data1: 0xa95664d2, Data2: 0x9614, Data3: 0x4f35, Data4: [8]byte{0xa7, 0x46, 0xde, 0x8d, 0xb6, 0x36, 0x17, 0xe6}}
	iidIAudioClient         = windows.GUID{Data1: 0x1cb9ad4c, Data2: 0xdbfa, Data3: 0x4c32, Data4: [8]byte{0xb1, 0x78, 0xc2, 0xf5, 0x68, 0xa7, 0x03, 0xb2}}
	iidIAudioCaptureClient  = windows.GUID{Data1: 0xc8adbd64, Data2: 0xe71e, Data3: 0x48a0, Data4: [8]byte{0xa4, 0xde, 0x18, 0x5c, 0x39, 0x5c, 0xd3, 0x17}}
	iidIAudioRenderClient   = windows.GUID{Data1: 0xf294acfc, Data2: 0x3146, Data3: 0x4483, Data4: [8]byte{0xa7, 0xbf, 0xad, 0xdc, 0xa7, 0xc2, 0x60, 0xe2}}
	iidISimpleAudioVolume   = windows.GUID{Data1: 0x87ce5498, Data2: 0x68d6, Data3: 0x44e5, Data4: [8]byte{0x92, 0x15, 0x6d, 0xa4, 0x7e, 0xf8, 0x83, 0xd8}}

	ksSubformatIEEEFloat = windows.GUID{Data1: 0x00000003, Data2: 0x0000, Data3: 0x0010, Data4: [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71}}
	ksSubformatPCM       = windows.GUID{Data1: 0x00000001, Data2: 0x0000, Data3: 0x0010, Data4: [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71}}
)

type waveFormatEx struct {
	WFormatTag      uint16
	NChannels       uint16
	NSamplesPerSec  uint32
	NAvgBytesPerSec uint32
	NBlockAlign     uint16
	WBitsPerSample  uint16
	CbSize          uint16
}

type waveFormatExtensible struct {
	Format        waveFormatEx
	Samples       uint16
	DwChannelMask uint32
	SubFormat     windows.GUID
}

type wasapiFormat struct {
	SampleRate int
	Channels   int
	Bits       int
	IsFloat    bool
}

type wasapiRenderState struct {
	client       uintptr
	renderClient uintptr
	volClient    uintptr
	bufferFrames uint32
	format       wasapiFormat
	started      bool
	headless     bool
}

// AudioEngine implements WASAPI audio capture (loopback and microphone)
// and WASAPI audio playback with safe silent fallback when headless.
type AudioEngine struct {
	mu     sync.Mutex
	volume float32
	stopCh chan struct{}
	doneCh chan struct{}
	seq    uint64
	render wasapiRenderState
}

func NewAudioEngine() *AudioEngine {
	return &AudioEngine{volume: 1.0}
}

func (a *AudioEngine) StartCapture(loopback bool, onFrame func(*core.AudioFrame)) error {
	if onFrame == nil {
		return nil
	}
	_ = a.StopCapture()

	a.mu.Lock()
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	a.stopCh = stopCh
	a.doneCh = doneCh
	a.mu.Unlock()

	go a.captureLoop(loopback, onFrame, stopCh, doneCh)
	return nil
}

func (a *AudioEngine) StopCapture() error {
	a.mu.Lock()
	stopCh := a.stopCh
	doneCh := a.doneCh
	a.stopCh = nil
	a.doneCh = nil
	a.mu.Unlock()

	if stopCh != nil {
		close(stopCh)
	}
	if doneCh != nil {
		<-doneCh
	}
	return nil
}

func (a *AudioEngine) captureLoop(loopback bool, onFrame func(*core.AudioFrame), stopCh <-chan struct{}, doneCh chan<- struct{}) {
	defer close(doneCh)

	if err := ole32DLL.Load(); err == nil {
		_, _, _ = procCoInitializeEx.Call(0, coinitMultithreaded)
		client, capClient, fmtInfo, err := initWASAPICapture(loopback)
		if err == nil {
			defer func() {
				comCall(client, 11)
				comRelease(capClient)
				comRelease(client)
			}()
			if r := comCall(client, 10); int32(r) >= 0 {
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-stopCh:
						return
					case <-ticker.C:
						for {
							var pktFrames uint32
							hr := comCall(capClient, 5, uintptr(unsafe.Pointer(&pktFrames)))
							if int32(hr) < 0 || pktFrames == 0 {
								break
							}
							var dataPtr uintptr
							var numFrames uint32
							var flags uint32
							var devPos, qpcPos uint64
							hr = comCall(capClient, 3,
								uintptr(unsafe.Pointer(&dataPtr)),
								uintptr(unsafe.Pointer(&numFrames)),
								uintptr(unsafe.Pointer(&flags)),
								uintptr(unsafe.Pointer(&devPos)),
								uintptr(unsafe.Pointer(&qpcPos)),
							)
							if int32(hr) < 0 {
								break
							}
							if numFrames == 0 {
								comCall(capClient, 4, 0)
								break
							}
							if numFrames > 0 {
								silentFrame := flags&audclntBufferSilent != 0 || dataPtr == 0
								if !silentFrame {
									frameBytes := int(numFrames) * fmtInfo.Channels * (fmtInfo.Bits / 8)
									raw := unsafe.Slice((*byte)(unsafe.Pointer(dataPtr)), frameBytes)
									pcm := convertToPCM16(raw, fmtInfo)
									if !isPCM16Silent(pcm) {
										a.mu.Lock()
										a.seq++
										seq := a.seq
										a.mu.Unlock()
										onFrame(&core.AudioFrame{
											Seq:        seq,
											SampleRate: fmtInfo.SampleRate,
											Channels:   fmtInfo.Channels,
											Timestamp:  time.Now().UnixMilli(),
											Data:       pcm,
										})
									}
								}
							}
							comCall(capClient, 4, uintptr(numFrames))
						}
					}
				}
			}
		}
	}

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	silent := make([]byte, 960*2*2)
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			a.mu.Lock()
			a.seq++
			seq := a.seq
			a.mu.Unlock()
			cp := make([]byte, len(silent))
			copy(cp, silent)
			onFrame(&core.AudioFrame{
				Seq:        seq,
				SampleRate: 48000,
				Channels:   2,
				Timestamp:  time.Now().UnixMilli(),
				Data:       cp,
			})
		}
	}
}

func (a *AudioEngine) PlayFrame(frame *core.AudioFrame) error {
	if frame == nil || len(frame.Data) == 0 || frame.Channels <= 0 || frame.SampleRate <= 0 {
		return nil
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if err := a.ensureRenderLocked(frame.SampleRate, frame.Channels); err != nil || a.render.headless {
		return nil
	}

	pcm := scalePCM16(frame.Data, a.volume)
	inFrames := len(pcm) / (frame.Channels * 2)
	if inFrames <= 0 {
		return nil
	}

	var padding uint32
	if hr := comCall(a.render.client, 6, uintptr(unsafe.Pointer(&padding))); int32(hr) < 0 {
		a.releaseRenderLocked()
		return nil
	}
	avail := int(a.render.bufferFrames) - int(padding)
	if avail <= 0 {
		return nil
	}

	outBytes := convertPCM16ToRender(pcm, frame.SampleRate, frame.Channels, a.render.format)
	outFrameStride := a.render.format.Channels * (a.render.format.Bits / 8)
	if outFrameStride <= 0 {
		return nil
	}
	writeFrames := len(outBytes) / outFrameStride
	if writeFrames > avail {
		writeFrames = avail
	}
	if writeFrames <= 0 {
		return nil
	}

	var dstPtr uintptr
	if hr := comCall(a.render.renderClient, 3, uintptr(writeFrames), uintptr(unsafe.Pointer(&dstPtr))); int32(hr) < 0 || dstPtr == 0 {
		a.releaseRenderLocked()
		return nil
	}
	copyBytes := writeFrames * outFrameStride
	copy(unsafe.Slice((*byte)(unsafe.Pointer(dstPtr)), copyBytes), outBytes[:copyBytes])
	comCall(a.render.renderClient, 4, uintptr(writeFrames), 0)

	if !a.render.started {
		if hr := comCall(a.render.client, 10); int32(hr) >= 0 {
			a.render.started = true
		}
	}
	return nil
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
	if a.render.volClient != 0 {
		var setMasterVol func(this uintptr, level float32, eventCtx uintptr) uintptr
		purego.RegisterFunc(&setMasterVol, comMethod(a.render.volClient, 3))
		_ = setMasterVol(a.render.volClient, vol, 0)
	}
}

func (a *AudioEngine) Volume() float32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.volume
}

func (a *AudioEngine) Close() error {
	err := a.StopCapture()
	a.mu.Lock()
	a.releaseRenderLocked()
	a.mu.Unlock()
	return err
}

func (a *AudioEngine) ensureRenderLocked(sampleRate, channels int) error {
	if a.render.headless {
		return nil
	}
	if a.render.client != 0 {
		return nil
	}
	if err := ole32DLL.Load(); err != nil {
		a.render.headless = true
		return err
	}
	_, _, _ = procCoInitializeEx.Call(0, coinitMultithreaded)

	client, fmtInfo, err := createAudioClient(eRender, 0, sampleRate, channels, true)
	if err != nil {
		a.render.headless = true
		return err
	}
	var bufFrames uint32
	if hr := comCall(client, 4, uintptr(unsafe.Pointer(&bufFrames))); int32(hr) < 0 || bufFrames == 0 {
		comRelease(client)
		a.render.headless = true
		return fmt.Errorf("media: GetBufferSize failed: 0x%08x", uint32(hr))
	}
	var renderClient uintptr
	if hr := comCall(client, 14, uintptr(unsafe.Pointer(&iidIAudioRenderClient)), uintptr(unsafe.Pointer(&renderClient))); int32(hr) < 0 || renderClient == 0 {
		comRelease(client)
		a.render.headless = true
		return fmt.Errorf("media: GetService(IAudioRenderClient) failed: 0x%08x", uint32(hr))
	}
	var volClient uintptr
	_ = comCall(client, 14, uintptr(unsafe.Pointer(&iidISimpleAudioVolume)), uintptr(unsafe.Pointer(&volClient)))

	a.render = wasapiRenderState{
		client:       client,
		renderClient: renderClient,
		volClient:    volClient,
		bufferFrames: bufFrames,
		format:       fmtInfo,
	}
	return nil
}

func (a *AudioEngine) releaseRenderLocked() {
	if a.render.volClient != 0 {
		comRelease(a.render.volClient)
	}
	if a.render.renderClient != 0 {
		comRelease(a.render.renderClient)
	}
	if a.render.client != 0 {
		if a.render.started {
			comCall(a.render.client, 11)
		}
		comRelease(a.render.client)
	}
	a.render = wasapiRenderState{}
}

func initWASAPICapture(loopback bool) (uintptr, uintptr, wasapiFormat, error) {
	flow := uintptr(eCapture)
	flags := uint32(audclntStreamFlagsAutoConvertPCM | audclntStreamFlagsSrcDefaultQual)
	allowAutoConvert := true
	if loopback {
		flow = uintptr(eRender)
		flags = audclntStreamFlagsLoopback
		allowAutoConvert = false
	}
	client, fmtInfo, err := createAudioClient(flow, flags, 48000, 2, allowAutoConvert)
	if err != nil {
		return 0, 0, wasapiFormat{}, err
	}
	var capClient uintptr
	if hr := comCall(client, 14, uintptr(unsafe.Pointer(&iidIAudioCaptureClient)), uintptr(unsafe.Pointer(&capClient))); int32(hr) < 0 || capClient == 0 {
		comRelease(client)
		return 0, 0, wasapiFormat{}, fmt.Errorf("media: GetService(IAudioCaptureClient) failed: 0x%08x", uint32(hr))
	}
	return client, capClient, fmtInfo, nil
}

func createAudioClient(dataFlow uintptr, extraFlags uint32, desiredRate, desiredChannels int, allowAutoConvert bool) (uintptr, wasapiFormat, error) {
	var enum uintptr
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)),
		0,
		clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)),
		uintptr(unsafe.Pointer(&enum)),
	)
	if int32(hr) < 0 || enum == 0 {
		return 0, wasapiFormat{}, fmt.Errorf("media: CoCreateInstance(MMDeviceEnumerator) failed: 0x%08x", uint32(hr))
	}
	defer comRelease(enum)

	var dev uintptr
	if r := comCall(enum, 4, dataFlow, eConsole, uintptr(unsafe.Pointer(&dev))); int32(r) < 0 || dev == 0 {
		return 0, wasapiFormat{}, fmt.Errorf("media: GetDefaultAudioEndpoint failed: 0x%08x", uint32(r))
	}
	defer comRelease(dev)

	var client uintptr
	if r := comCall(dev, 3, uintptr(unsafe.Pointer(&iidIAudioClient)), clsctxAll, 0, uintptr(unsafe.Pointer(&client))); int32(r) < 0 || client == 0 {
		return 0, wasapiFormat{}, fmt.Errorf("media: IMMDevice::Activate(IAudioClient) failed: 0x%08x", uint32(r))
	}

	const bufDuration100ns = 1_000_000 // 100ms
	if allowAutoConvert {
		ch := uint16(desiredChannels)
		if ch <= 0 {
			ch = 2
		}
		sr := uint32(desiredRate)
		if sr == 0 {
			sr = 48000
		}
		wfx := waveFormatEx{
			WFormatTag:      waveFormatPCM,
			NChannels:       ch,
			NSamplesPerSec:  sr,
			WBitsPerSample:  16,
			NBlockAlign:     ch * 2,
			NAvgBytesPerSec: sr * uint32(ch) * 2,
		}
		initFlags := extraFlags | audclntStreamFlagsAutoConvertPCM | audclntStreamFlagsSrcDefaultQual
		if r := comCall(client, 3, audclntShareModeShared, uintptr(initFlags), bufDuration100ns, 0, uintptr(unsafe.Pointer(&wfx)), 0); int32(r) >= 0 {
			return client, wasapiFormat{SampleRate: int(sr), Channels: int(ch), Bits: 16, IsFloat: false}, nil
		}
	}

	var mixPtr uintptr
	if r := comCall(client, 8, uintptr(unsafe.Pointer(&mixPtr))); int32(r) < 0 || mixPtr == 0 {
		comRelease(client)
		return 0, wasapiFormat{}, fmt.Errorf("media: GetMixFormat failed: 0x%08x", uint32(r))
	}
	defer procCoTaskMemFree.Call(mixPtr)

	fmtInfo := parseWaveFormat(mixPtr)
	initFlags := extraFlags &^ (audclntStreamFlagsAutoConvertPCM | audclntStreamFlagsSrcDefaultQual)
	if r := comCall(client, 3, audclntShareModeShared, uintptr(initFlags), bufDuration100ns, 0, mixPtr, 0); int32(r) < 0 {
		comRelease(client)
		return 0, wasapiFormat{}, fmt.Errorf("media: IAudioClient::Initialize failed: 0x%08x", uint32(r))
	}
	return client, fmtInfo, nil
}

func parseWaveFormat(ptr uintptr) wasapiFormat {
	wfx := (*waveFormatEx)(unsafe.Pointer(ptr))
	info := wasapiFormat{
		SampleRate: int(wfx.NSamplesPerSec),
		Channels:   int(wfx.NChannels),
		Bits:       int(wfx.WBitsPerSample),
		IsFloat:    wfx.WFormatTag == waveFormatIEEEFloat,
	}
	if wfx.WFormatTag == waveFormatTagExtensible && wfx.CbSize >= 22 {
		ext := (*waveFormatExtensible)(unsafe.Pointer(ptr))
		if ext.SubFormat == ksSubformatIEEEFloat {
			info.IsFloat = true
		} else if ext.SubFormat == ksSubformatPCM {
			info.IsFloat = false
		}
	}
	if info.Channels <= 0 {
		info.Channels = 2
	}
	if info.SampleRate <= 0 {
		info.SampleRate = 48000
	}
	if info.Bits <= 0 {
		info.Bits = 16
	}
	return info
}

func isPCM16Silent(pcm []byte) bool {
	for i := 0; i+1 < len(pcm); i += 2 {
		if pcm[i] != 0 || pcm[i+1] != 0 {
			return false
		}
	}
	return true
}

func convertToPCM16(raw []byte, fmtInfo wasapiFormat) []byte {
	if !fmtInfo.IsFloat && fmtInfo.Bits == 16 {
		out := make([]byte, len(raw))
		copy(out, raw)
		return out
	}
	if fmtInfo.IsFloat && fmtInfo.Bits == 32 {
		samples := len(raw) / 4
		out := make([]byte, samples*2)
		for i := 0; i < samples; i++ {
			f := math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4 : i*4+4]))
			if f > 1 {
				f = 1
			} else if f < -1 {
				f = -1
			}
			s := int16(f * 32767)
			binary.LittleEndian.PutUint16(out[i*2:i*2+2], uint16(s))
		}
		return out
	}
	if !fmtInfo.IsFloat && fmtInfo.Bits == 32 {
		samples := len(raw) / 4
		out := make([]byte, samples*2)
		for i := 0; i < samples; i++ {
			s32 := int32(binary.LittleEndian.Uint32(raw[i*4 : i*4+4]))
			s16 := int16(s32 >> 16)
			binary.LittleEndian.PutUint16(out[i*2:i*2+2], uint16(s16))
		}
		return out
	}
	return nil
}

func convertPCM16ToRender(pcm []byte, inRate, inCh int, dstFmt wasapiFormat) []byte {
	if !dstFmt.IsFloat && dstFmt.Bits == 16 && dstFmt.Channels == inCh && dstFmt.SampleRate == inRate {
		return pcm
	}
	inFrames := len(pcm) / (inCh * 2)
	if inFrames <= 0 {
		return nil
	}
	outFrames := inFrames
	if dstFmt.SampleRate > 0 && inRate > 0 && dstFmt.SampleRate != inRate {
		outFrames = int(int64(inFrames) * int64(dstFmt.SampleRate) / int64(inRate))
		if outFrames <= 0 {
			outFrames = 1
		}
	}
	outFrameBytes := dstFmt.Channels * (dstFmt.Bits / 8)
	out := make([]byte, outFrames*outFrameBytes)
	for of := 0; of < outFrames; of++ {
		srcFrame := of
		if dstFmt.SampleRate != inRate && outFrames > 0 {
			srcFrame = (of * inFrames) / outFrames
			if srcFrame >= inFrames {
				srcFrame = inFrames - 1
			}
		}
		for ch := 0; ch < dstFmt.Channels; ch++ {
			srcCh := ch
			if srcCh >= inCh {
				srcCh = 0
			}
			sOff := (srcFrame*inCh + srcCh) * 2
			s := int16(binary.LittleEndian.Uint16(pcm[sOff : sOff+2]))
			dOff := of*outFrameBytes + ch*(dstFmt.Bits/8)
			if dstFmt.IsFloat && dstFmt.Bits == 32 {
				f := float32(s) / 32768.0
				binary.LittleEndian.PutUint32(out[dOff:dOff+4], math.Float32bits(f))
			} else if dstFmt.Bits == 32 {
				binary.LittleEndian.PutUint32(out[dOff:dOff+4], uint32(int32(s)<<16))
			} else {
				binary.LittleEndian.PutUint16(out[dOff:dOff+2], uint16(s))
			}
		}
	}
	return out
}
