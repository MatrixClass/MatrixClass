package media

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/klauspost/compress/zstd"
	"nexlab/internal/core"
)

const (
	audioMagic   = "NXAF"
	audioVersion = 1
	audioHdrSize = 36

	audioFlagZstd uint8 = 1 << 0

	// MinAudioSampleRate and MaxAudioSampleRate bound sample rates accepted from the network.
	MinAudioSampleRate = 8000
	MaxAudioSampleRate = 192000
	// MaxAudioChannels bounds the channel count of an encoded audio frame.
	MaxAudioChannels = 8
	// maxAudioFrameSeconds bounds one packet to at most this many seconds of audio.
	// Encoders produce 10-100ms packets (WASAPI 100ms buffer, Linux/synthetic 20ms).
	maxAudioFrameSeconds = 1
	maxAudioSampleCount  = MaxAudioSampleRate * maxAudioFrameSeconds
	// maxAudioADPCMBytes is the largest legal ADPCM payload (4-byte header + nibbles per channel).
	maxAudioADPCMBytes = MaxAudioChannels * (4 + maxAudioSampleCount/2)
)

var (
	ErrInvalidAudioData = errors.New("media: invalid encoded audio frame")
)

var imaIndexTable = [16]int{
	-1, -1, -1, -1, 2, 4, 6, 8,
	-1, -1, -1, -1, 2, 4, 6, 8,
}

var imaStepTable = [89]int{
	7, 8, 9, 10, 11, 12, 13, 14, 16, 17,
	19, 21, 23, 25, 28, 31, 34, 37, 41, 45,
	50, 55, 60, 66, 73, 80, 88, 97, 107, 118,
	130, 143, 157, 173, 190, 209, 230, 253, 279, 307,
	337, 371, 408, 449, 494, 544, 598, 658, 724, 796,
	876, 963, 1060, 1166, 1282, 1411, 1552, 1707, 1878, 2066,
	2272, 2499, 2749, 3024, 3327, 3660, 4026, 4428, 4871, 5358,
	5894, 6484, 7132, 7845, 8630, 9493, 10442, 11487, 12635, 13899,
	15289, 16818, 18500, 20350, 22385, 24623, 27086, 29794, 32767,
}

// AudioCodec compresses 16-bit PCM audio frames using IMA-ADPCM followed by Zstd.
type AudioCodec struct {
	mu  sync.Mutex
	enc *zstd.Encoder
	dec *zstd.Decoder
}

func NewAudioCodec() (*AudioCodec, error) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		return nil, fmt.Errorf("media: init audio zstd encoder: %w", err)
	}
	dec, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(maxAudioADPCMBytes))
	if err != nil {
		_ = enc.Close()
		return nil, fmt.Errorf("media: init audio zstd decoder: %w", err)
	}
	return &AudioCodec{
		enc: enc,
		dec: dec,
	}, nil
}

func (c *AudioCodec) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.enc != nil {
		_ = c.enc.Close()
		c.enc = nil
	}
	if c.dec != nil {
		c.dec.Close()
		c.dec = nil
	}
	return nil
}

func (c *AudioCodec) Encode(frame *core.AudioFrame) ([]byte, error) {
	if frame == nil || frame.SampleRate <= 0 || frame.Channels <= 0 || frame.Channels > 8 {
		return nil, ErrInvalidAudioData
	}
	bytesPerSample := frame.Channels * 2
	if len(frame.Data)%bytesPerSample != 0 {
		return nil, ErrInvalidAudioData
	}
	sampleCount := len(frame.Data) / bytesPerSample
	adpcm := encodeIMAADPCM(frame.Data, frame.Channels, sampleCount)

	c.mu.Lock()
	if c.enc == nil {
		c.mu.Unlock()
		return nil, ErrCodecClosed
	}
	compressed := c.enc.EncodeAll(adpcm, nil)
	c.mu.Unlock()

	flags := audioFlagZstd
	payload := compressed
	if len(adpcm) > 0 && len(compressed) >= len(adpcm) {
		flags = 0
		payload = adpcm
	}

	out := make([]byte, audioHdrSize+len(payload))
	copy(out[0:4], audioMagic)
	out[4] = audioVersion
	out[5] = uint8(frame.Channels)
	out[6] = flags
	out[7] = 0
	binary.LittleEndian.PutUint32(out[8:12], uint32(frame.SampleRate))
	binary.LittleEndian.PutUint64(out[12:20], frame.Seq)
	binary.LittleEndian.PutUint64(out[20:28], uint64(frame.Timestamp))
	binary.LittleEndian.PutUint32(out[28:32], uint32(sampleCount))
	binary.LittleEndian.PutUint32(out[32:36], uint32(len(payload)))
	copy(out[audioHdrSize:], payload)
	return out, nil
}

func (c *AudioCodec) Decode(data []byte) (*core.AudioFrame, error) {
	if len(data) < audioHdrSize || string(data[0:4]) != audioMagic || data[4] != audioVersion {
		return nil, ErrInvalidAudioData
	}
	channels := int(data[5])
	flags := data[6]
	sampleRate := int(binary.LittleEndian.Uint32(data[8:12]))
	seq := binary.LittleEndian.Uint64(data[12:20])
	ts := int64(binary.LittleEndian.Uint64(data[20:28]))
	sampleCount := int(binary.LittleEndian.Uint32(data[28:32]))
	pLen := int(binary.LittleEndian.Uint32(data[32:36]))

	if channels <= 0 || channels > MaxAudioChannels || sampleRate < MinAudioSampleRate || sampleRate > MaxAudioSampleRate || pLen < 0 || len(data) < audioHdrSize+pLen {
		return nil, ErrInvalidAudioData
	}
	if sampleCount < 0 || sampleCount > sampleRate*maxAudioFrameSeconds {
		return nil, ErrInvalidAudioData
	}
	payload := data[audioHdrSize : audioHdrSize+pLen]
	if flags&audioFlagZstd != 0 {
		c.mu.Lock()
		if c.dec == nil {
			c.mu.Unlock()
			return nil, ErrCodecClosed
		}
		raw, err := c.dec.DecodeAll(payload, nil)
		c.mu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("media: audio zstd decode failed: %w", err)
		}
		payload = raw
	}

	pcm, err := decodeIMAADPCM(payload, channels, sampleCount)
	if err != nil {
		return nil, err
	}
	return &core.AudioFrame{
		Seq:        seq,
		SampleRate: sampleRate,
		Channels:   channels,
		Timestamp:  ts,
		Data:       pcm,
	}, nil
}

func encodeIMAADPCM(pcm []byte, channels, sampleCount int) []byte {
	if sampleCount == 0 {
		return nil
	}
	nibblesPerCh := sampleCount - 1
	nibbleBytesPerCh := (nibblesPerCh + 1) / 2
	perChSize := 4 + nibbleBytesPerCh
	out := make([]byte, channels*perChSize)

	for ch := 0; ch < channels; ch++ {
		chOff := ch * perChSize
		first := int16(binary.LittleEndian.Uint16(pcm[ch*2 : ch*2+2]))
		binary.LittleEndian.PutUint16(out[chOff:chOff+2], uint16(first))
		out[chOff+2] = 0
		out[chOff+3] = 0

		predictor := int(first)
		stepIndex := 0
		nibbleOut := out[chOff+4:]

		for i := 1; i < sampleCount; i++ {
			off := (i*channels + ch) * 2
			sample := int(int16(binary.LittleEndian.Uint16(pcm[off : off+2])))
			nibble := encodeIMANibble(sample, &predictor, &stepIndex)
			byteIdx := (i - 1) >> 1
			if (i-1)&1 == 0 {
				nibbleOut[byteIdx] = nibble
			} else {
				nibbleOut[byteIdx] |= nibble << 4
			}
		}
	}
	return out
}

func decodeIMAADPCM(adpcm []byte, channels, sampleCount int) ([]byte, error) {
	if sampleCount == 0 {
		return nil, nil
	}
	nibblesPerCh := sampleCount - 1
	nibbleBytesPerCh := (nibblesPerCh + 1) / 2
	perChSize := 4 + nibbleBytesPerCh
	if len(adpcm) < channels*perChSize {
		return nil, ErrInvalidAudioData
	}

	pcm := make([]byte, sampleCount*channels*2)
	for ch := 0; ch < channels; ch++ {
		chOff := ch * perChSize
		predictor := int(int16(binary.LittleEndian.Uint16(adpcm[chOff : chOff+2])))
		stepIndex := int(adpcm[chOff+2])
		if stepIndex < 0 {
			stepIndex = 0
		} else if stepIndex > 88 {
			stepIndex = 88
		}

		binary.LittleEndian.PutUint16(pcm[ch*2:ch*2+2], uint16(int16(predictor)))
		nibbleIn := adpcm[chOff+4:]

		for i := 1; i < sampleCount; i++ {
			b := nibbleIn[(i-1)>>1]
			var nibble byte
			if (i-1)&1 == 0 {
				nibble = b & 0x0F
			} else {
				nibble = (b >> 4) & 0x0F
			}
			sample := decodeIMANibble(nibble, &predictor, &stepIndex)
			off := (i*channels + ch) * 2
			binary.LittleEndian.PutUint16(pcm[off:off+2], uint16(sample))
		}
	}
	return pcm, nil
}

func encodeIMANibble(sample int, predictor, stepIndex *int) byte {
	diff := sample - *predictor
	var nibble byte
	if diff < 0 {
		nibble = 8
		diff = -diff
	}
	step := imaStepTable[*stepIndex]
	delta := step >> 3
	if diff >= step {
		nibble |= 4
		diff -= step
		delta += step
	}
	if diff >= step>>1 {
		nibble |= 2
		diff -= step >> 1
		delta += step >> 1
	}
	if diff >= step>>2 {
		nibble |= 1
		delta += step >> 2
	}
	if nibble&8 != 0 {
		*predictor -= delta
	} else {
		*predictor += delta
	}
	*predictor = clampInt(*predictor, -32768, 32767)
	*stepIndex = clampInt(*stepIndex+imaIndexTable[nibble], 0, 88)
	return nibble
}

func decodeIMANibble(nibble byte, predictor, stepIndex *int) int16 {
	step := imaStepTable[*stepIndex]
	delta := step >> 3
	if nibble&4 != 0 {
		delta += step
	}
	if nibble&2 != 0 {
		delta += step >> 1
	}
	if nibble&1 != 0 {
		delta += step >> 2
	}
	if nibble&8 != 0 {
		*predictor -= delta
	} else {
		*predictor += delta
	}
	*predictor = clampInt(*predictor, -32768, 32767)
	*stepIndex = clampInt(*stepIndex+imaIndexTable[nibble&0x0F], 0, 88)
	return int16(*predictor)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func scalePCM16(pcm []byte, volume float32) []byte {
	if volume >= 0.999 {
		return pcm
	}
	if volume <= 0 {
		return make([]byte, len(pcm))
	}
	out := make([]byte, len(pcm))
	for i := 0; i+1 < len(pcm); i += 2 {
		s := int16(binary.LittleEndian.Uint16(pcm[i : i+2]))
		scaled := clampInt(int(float32(s)*volume), -32768, 32767)
		binary.LittleEndian.PutUint16(out[i:i+2], uint16(int16(scaled)))
	}
	return out
}
