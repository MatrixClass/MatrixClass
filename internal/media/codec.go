package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/klauspost/compress/zstd"
	"nexlab/internal/core"
)

const (
	videoMagic   = "NXVF"
	videoVersion = 1
	videoHdrSize = 36
	tileSize     = 32

	codecIDFrameZstd uint8 = 1
	codecIDH264MFT   uint8 = 2

	videoFlagKeyFrame uint8 = 1 << 0
	videoFlagNV12     uint8 = 1 << 1
	videoFlagDelta    uint8 = 1 << 2
)

var (
	ErrInvalidFrameData = errors.New("media: invalid encoded video frame")
	ErrInvalidDimension = errors.New("media: invalid frame dimensions")
	ErrBufferTooSmall   = errors.New("media: input buffer too small")
	ErrMFTUnavailable   = errors.New("media: Media Foundation H.264 MFT unavailable")
)

// RGBAToNV12 converts tightly packed RGBA (4*width*height) to BT.709 full-range NV12 planes.
func RGBAToNV12(rgba []byte, width, height int) (yPlane, uvPlane []byte, strideY, strideUV int, err error) {
	if width <= 0 || height <= 0 {
		return nil, nil, 0, 0, ErrInvalidDimension
	}
	if len(rgba) < width*height*4 {
		return nil, nil, 0, 0, ErrBufferTooSmall
	}

	strideY = width
	cw := (width + 1) / 2
	ch := (height + 1) / 2
	strideUV = cw * 2

	yPlane = make([]byte, strideY*height)
	uvPlane = make([]byte, strideUV*ch)

	for y := 0; y < height; y++ {
		rowOff := y * width * 4
		yRow := y * strideY
		for x := 0; x < width; x++ {
			px := rowOff + x*4
			r := int32(rgba[px])
			g := int32(rgba[px+1])
			b := int32(rgba[px+2])
			// BT.709 full-range Y: 0.2126*R + 0.7152*G + 0.0722*B
			yv := (13933*r + 46871*g + 4732*b + 32768) >> 16
			yPlane[yRow+x] = clampByte(yv)
		}
	}

	for cy := 0; cy < ch; cy++ {
		y0 := cy * 2
		y1 := y0 + 1
		if y1 >= height {
			y1 = y0
		}
		uvRow := cy * strideUV
		for cx := 0; cx < cw; cx++ {
			x0 := cx * 2
			x1 := x0 + 1
			if x1 >= width {
				x1 = x0
			}
			p00 := (y0*width + x0) * 4
			p01 := (y0*width + x1) * 4
			p10 := (y1*width + x0) * 4
			p11 := (y1*width + x1) * 4

			r := (int32(rgba[p00]) + int32(rgba[p01]) + int32(rgba[p10]) + int32(rgba[p11]) + 2) >> 2
			g := (int32(rgba[p00+1]) + int32(rgba[p01+1]) + int32(rgba[p10+1]) + int32(rgba[p11+1]) + 2) >> 2
			b := (int32(rgba[p00+2]) + int32(rgba[p01+2]) + int32(rgba[p10+2]) + int32(rgba[p11+2]) + 2) >> 2

			// BT.709 full-range Cb/Cr:
			// Cb = 128 - 0.11457*R - 0.38543*G + 0.5*B
			// Cr = 128 + 0.5*R - 0.45415*G - 0.04585*B
			cb := 128 + ((-7509*r - 25259*g + 32768*b + 32768) >> 16)
			cr := 128 + ((32768*r - 29763*g - 3005*b + 32768) >> 16)
			uvPlane[uvRow+cx*2] = clampByte(cb)
			uvPlane[uvRow+cx*2+1] = clampByte(cr)
		}
	}

	return yPlane, uvPlane, strideY, strideUV, nil
}

// NV12ToRGBA converts BT.709 full-range NV12 planes to tightly packed RGBA (4*width*height).
func NV12ToRGBA(yPlane, uvPlane []byte, width, height, strideY, strideUV int) ([]byte, error) {
	if width <= 0 || height <= 0 {
		return nil, ErrInvalidDimension
	}
	if strideY <= 0 {
		strideY = width
	}
	cw := (width + 1) / 2
	ch := (height + 1) / 2
	if strideUV <= 0 {
		strideUV = cw * 2
	}
	if strideY < width || len(yPlane) < strideY*(height-1)+width {
		return nil, ErrBufferTooSmall
	}
	if strideUV < cw*2 || len(uvPlane) < strideUV*(ch-1)+cw*2 {
		return nil, ErrBufferTooSmall
	}

	rgba := make([]byte, width*height*4)
	for y := 0; y < height; y++ {
		yRow := y * strideY
		uvRow := (y >> 1) * strideUV
		outRow := y * width * 4
		for x := 0; x < width; x++ {
			yv := int32(yPlane[yRow+x]) << 16
			uvIdx := uvRow + (x>>1)*2
			cb := int32(uvPlane[uvIdx]) - 128
			cr := int32(uvPlane[uvIdx+1]) - 128

			// Inverse BT.709 full-range:
			// R = Y + 1.5748*Cr
			// G = Y - 0.187324*Cb - 0.468124*Cr
			// B = Y + 1.8556*Cb
			r := (yv + 103206*cr + 32768) >> 16
			g := (yv - 12276*cb - 30679*cr + 32768) >> 16
			b := (yv + 121609*cb + 32768) >> 16

			px := outRow + x*4
			rgba[px] = clampByte(r)
			rgba[px+1] = clampByte(g)
			rgba[px+2] = clampByte(b)
			rgba[px+3] = 255
		}
	}
	return rgba, nil
}

// EnsureNV12 populates NV12Y, NV12UV, StrideY, and StrideUV on frame when Data is RGBA.
func EnsureNV12(frame *core.VideoFrame) error {
	if frame == nil {
		return ErrInvalidFrameData
	}
	if len(frame.NV12Y) > 0 && len(frame.NV12UV) > 0 && frame.StrideY >= frame.Width {
		return nil
	}
	y, uv, sy, suv, err := RGBAToNV12(frame.Data, frame.Width, frame.Height)
	if err != nil {
		return err
	}
	frame.NV12Y = y
	frame.NV12UV = uv
	frame.StrideY = sy
	frame.StrideUV = suv
	return nil
}

// ScaleRGBA downscales or resamples an RGBA buffer using fast nearest/box sampling.
func ScaleRGBA(src []byte, srcW, srcH, dstW, dstH int) []byte {
	return ScaleRGBABuf(nil, src, srcW, srcH, dstW, dstH)
}

// ScaleRGBABuf downscales or resamples an RGBA buffer into dst (reusing its capacity when sufficient).
func ScaleRGBABuf(dst, src []byte, srcW, srcH, dstW, dstH int) []byte {
	if srcW <= 0 || srcH <= 0 || dstW <= 0 || dstH <= 0 || len(src) < srcW*srcH*4 {
		return nil
	}
	need := dstW * dstH * 4
	if cap(dst) < need {
		dst = make([]byte, need)
	}
	dst = dst[:need]
	if srcW == dstW && srcH == dstH {
		copy(dst, src[:need])
		return dst
	}
	xMap := make([]int, dstW)
	for x := 0; x < dstW; x++ {
		sx := (x * srcW) / dstW
		if sx >= srcW {
			sx = srcW - 1
		}
		xMap[x] = sx * 4
	}
	for y := 0; y < dstH; y++ {
		sy := (y * srcH) / dstH
		if sy >= srcH {
			sy = srcH - 1
		}
		srcRow := sy * srcW * 4
		dstRow := y * dstW * 4
		for x := 0; x < dstW; x++ {
			sOff := srcRow + xMap[x]
			dOff := dstRow + x*4
			copy(dst[dOff:dOff+4], src[sOff:sOff+4])
		}
	}
	return dst
}

// DownscaleFrame scales frame proportionally so its dimensions do not exceed maxW x maxH (even-aligned).
func DownscaleFrame(frame *core.VideoFrame, maxW, maxH int) *core.VideoFrame {
	if frame == nil || frame.Width <= 0 || frame.Height <= 0 || (maxW <= 0 && maxH <= 0) {
		return frame
	}
	if (maxW <= 0 || frame.Width <= maxW) && (maxH <= 0 || frame.Height <= maxH) {
		return frame
	}
	dstW, dstH := fitEvenBounds(frame.Width, frame.Height, maxW, maxH)
	scaled := ScaleRGBA(frame.Data, frame.Width, frame.Height, dstW, dstH)
	if scaled == nil {
		return frame
	}
	return &core.VideoFrame{
		Seq:       frame.Seq,
		Codec:     frame.Codec,
		Width:     dstW,
		Height:    dstH,
		KeyFrame:  frame.KeyFrame,
		Timestamp: frame.Timestamp,
		Data:      scaled,
	}
}

func fitEvenBounds(srcW, srcH, maxW, maxH int) (int, int) {
	if maxW <= 0 {
		maxW = srcW
	}
	if maxH <= 0 {
		maxH = srcH
	}
	scaleW := float64(maxW) / float64(srcW)
	scaleH := float64(maxH) / float64(srcH)
	scale := scaleW
	if scaleH < scale {
		scale = scaleH
	}
	if scale >= 1.0 {
		return srcW &^ 1, srcH &^ 1
	}
	w := int(float64(srcW)*scale) &^ 1
	h := int(float64(srcH)*scale) &^ 1
	if w < 2 {
		w = 2
	}
	if h < 2 {
		h = 2
	}
	return w, h
}

func bgraToRGBA(bgra, dst []byte, width, height, stride int) []byte {
	need := width * height * 4
	if cap(dst) < need {
		dst = make([]byte, need)
	}
	dst = dst[:need]
	rowBytes := width * 4
	for y := 0; y < height; y++ {
		srcRow := bgra[y*stride : y*stride+rowBytes]
		dstRow := dst[y*rowBytes : (y+1)*rowBytes]
		for x := 0; x < rowBytes; x += 4 {
			_ = srcRow[x+3]
			_ = dstRow[x+3]
			dstRow[x] = srcRow[x+2]
			dstRow[x+1] = srcRow[x+1]
			dstRow[x+2] = srcRow[x]
			dstRow[x+3] = 255
		}
	}
	return dst
}

func clampByte(v int32) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}

// ZstdVideoCodec implements fast tile-delta + Zstd encoding and decoding.
type ZstdVideoCodec struct {
	mu         sync.Mutex
	encFast    *zstd.Encoder
	encDef     *zstd.Encoder
	dec        *zstd.Decoder
	prevRGBA   []byte
	prevW      int
	prevH      int
	prevEncSeq uint64
	hasEncSeq  bool
	decRGBA    []byte
	decW       int
	decH       int
	decSeq     uint64
	hasDecSeq  bool
	scratch    []byte
	compBuf    []byte
	nv12RawBuf []byte
}

func NewZstdVideoCodec() (*ZstdVideoCodec, error) {
	encFast, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		return nil, fmt.Errorf("media: init zstd fast encoder: %w", err)
	}
	encDef, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		encFast.Close()
		return nil, fmt.Errorf("media: init zstd default encoder: %w", err)
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		encFast.Close()
		encDef.Close()
		return nil, fmt.Errorf("media: init zstd decoder: %w", err)
	}
	return &ZstdVideoCodec{
		encFast: encFast,
		encDef:  encDef,
		dec:     dec,
	}, nil
}

func (c *ZstdVideoCodec) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.encFast != nil {
		_ = c.encFast.Close()
		c.encFast = nil
	}
	if c.encDef != nil {
		_ = c.encDef.Close()
		c.encDef = nil
	}
	if c.dec != nil {
		c.dec.Close()
		c.dec = nil
	}
	return nil
}

func (c *ZstdVideoCodec) Encode(frame *core.VideoFrame, quality int) ([]byte, error) {
	if frame == nil || frame.Width <= 0 || frame.Height <= 0 {
		return nil, ErrInvalidDimension
	}
	if quality <= 0 {
		quality = 80
	}
	if quality > 100 {
		quality = 100
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	w, h := frame.Width, frame.Height
	expectedRGBA := w * h * 4

	// Use tile-delta first when we have RGBA data and a matching reference frame,
	// even at lower quality settings, because tile-delta is much faster and smaller
	// than full-frame RGBA->NV12 conversion on screen content.
	canDelta := len(frame.Data) >= expectedRGBA && !frame.KeyFrame && c.prevW == w && c.prevH == h && len(c.prevRGBA) == expectedRGBA

	// NV12-only source or low-quality non-delta fallback uses compact NV12 payload.
	if len(frame.Data) < expectedRGBA || (!canDelta && quality < 65 && !frame.KeyFrame) {
		var yPlane, uvPlane []byte
		var strideY, strideUV int
		if len(frame.NV12Y) >= w*h && len(frame.NV12UV) >= ((w+1)/2)*2*((h+1)/2) {
			yPlane, uvPlane = frame.NV12Y, frame.NV12UV
			strideY, strideUV = frame.StrideY, frame.StrideUV
			if strideY <= 0 {
				strideY = w
			}
			if strideUV <= 0 {
				strideUV = ((w + 1) / 2) * 2
			}
		} else {
			var err error
			yPlane, uvPlane, strideY, strideUV, err = RGBAToNV12(frame.Data, w, h)
			if err != nil {
				return nil, err
			}
		}
		ch := (h + 1) / 2
		cwBytes := ((w + 1) / 2) * 2
		rawNeed := w*h + cwBytes*ch
		if cap(c.nv12RawBuf) < rawNeed {
			c.nv12RawBuf = make([]byte, rawNeed)
		}
		raw := c.nv12RawBuf[:rawNeed]
		for row := 0; row < h; row++ {
			copy(raw[row*w:(row+1)*w], yPlane[row*strideY:row*strideY+w])
		}
		uvBase := w * h
		for row := 0; row < ch; row++ {
			copy(raw[uvBase+row*cwBytes:uvBase+(row+1)*cwBytes], uvPlane[row*strideUV:row*strideUV+cwBytes])
		}
		c.compBuf = c.selectEncoder(quality).EncodeAll(raw, c.compBuf[:0])
		flags := videoFlagNV12
		if frame.KeyFrame {
			flags |= videoFlagKeyFrame
		}
		return packVideoHeader(codecIDFrameZstd, flags, uint8(quality), w, h, frame.Seq, frame.Timestamp, c.compBuf), nil
	}

	rgba := frame.Data[:expectedRGBA]

	if canDelta {
		if c.hasEncSeq && frame.Seq != 0 && frame.Seq <= c.prevEncSeq {
			c.hasEncSeq = false
		}
		deltaBuf, changedTiles, totalTiles := c.buildTileDelta(rgba, c.prevRGBA, w, h)
		if changedTiles == 0 || changedTiles*100 <= totalTiles*65 {
			c.compBuf = c.selectEncoder(quality).EncodeAll(deltaBuf, c.compBuf[:0])
			copy(c.prevRGBA, rgba)
			c.prevEncSeq = frame.Seq
			c.hasEncSeq = true
			return packVideoHeader(codecIDFrameZstd, videoFlagDelta, uint8(quality), w, h, frame.Seq, frame.Timestamp, c.compBuf), nil
		}
	}

	if cap(c.prevRGBA) < expectedRGBA {
		c.prevRGBA = make([]byte, expectedRGBA)
	}
	c.prevRGBA = c.prevRGBA[:expectedRGBA]
	copy(c.prevRGBA, rgba)
	c.prevW = w
	c.prevH = h
	c.prevEncSeq = frame.Seq
	c.hasEncSeq = true

	c.compBuf = c.selectEncoder(quality).EncodeAll(rgba, c.compBuf[:0])
	return packVideoHeader(codecIDFrameZstd, videoFlagKeyFrame, uint8(quality), w, h, frame.Seq, frame.Timestamp, c.compBuf), nil
}

func (c *ZstdVideoCodec) selectEncoder(quality int) *zstd.Encoder {
	if quality >= 85 && c.encDef != nil {
		return c.encDef
	}
	return c.encFast
}

func (c *ZstdVideoCodec) buildTileDelta(curr, prev []byte, width, height int) ([]byte, int, int) {
	tilesX := (width + tileSize - 1) / tileSize
	tilesY := (height + tileSize - 1) / tileSize
	totalTiles := tilesX * tilesY

	buf := c.scratch[:0]
	var countBuf [4]byte
	buf = append(buf, countBuf[:]...) // placeholder for tile count

	changed := 0
	rowBytes := width * 4
	for ty := 0; ty < tilesY; ty++ {
		y0 := ty * tileSize
		th := tileSize
		if y0+th > height {
			th = height - y0
		}
		for tx := 0; tx < tilesX; tx++ {
			x0 := tx * tileSize
			tw := tileSize
			if x0+tw > width {
				tw = width - x0
			}
			if tileEqual(curr, prev, rowBytes, x0, y0, tw, th) {
				continue
			}
			changed++
			var hdr [8]byte
			binary.LittleEndian.PutUint16(hdr[0:2], uint16(x0))
			binary.LittleEndian.PutUint16(hdr[2:4], uint16(y0))
			binary.LittleEndian.PutUint16(hdr[4:6], uint16(tw))
			binary.LittleEndian.PutUint16(hdr[6:8], uint16(th))
			buf = append(buf, hdr[:]...)

			span := tw * 4
			tileBytes := span * th
			prevLen := len(buf)
			if cap(buf) < prevLen+tileBytes {
				grown := make([]byte, prevLen+tileBytes, (prevLen+tileBytes)*2)
				copy(grown, buf)
				buf = grown
			} else {
				buf = buf[:prevLen+tileBytes]
			}
			dstPos := prevLen
			for dy := 0; dy < th; dy++ {
				off := (y0+dy)*rowBytes + x0*4
				ca := curr[off : off+span]
				pa := prev[off : off+span]
				da := buf[dstPos : dstPos+span]
				xorBytes(da, ca, pa)
				dstPos += span
			}
		}
	}
	binary.LittleEndian.PutUint32(buf[0:4], uint32(changed))
	c.scratch = buf
	return buf, changed, totalTiles
}

func xorBytes(dst, a, b []byte) {
	n := len(dst)
	i := 0
	for ; i+8 <= n; i += 8 {
		va := binary.LittleEndian.Uint64(a[i : i+8])
		vb := binary.LittleEndian.Uint64(b[i : i+8])
		binary.LittleEndian.PutUint64(dst[i:i+8], va^vb)
	}
	for ; i < n; i++ {
		dst[i] = a[i] ^ b[i]
	}
}

func tileEqual(a, b []byte, rowBytes, x0, y0, tw, th int) bool {
	span := tw * 4
	for dy := 0; dy < th; dy++ {
		off := (y0+dy)*rowBytes + x0*4
		if !bytes.Equal(a[off:off+span], b[off:off+span]) {
			return false
		}
	}
	return true
}

func (c *ZstdVideoCodec) Decode(data []byte) (*core.VideoFrame, error) {
	codecID, flags, _, w, h, seq, ts, payload, err := parseVideoHeader(data)
	if err != nil {
		return nil, err
	}
	if codecID != codecIDFrameZstd {
		return nil, fmt.Errorf("%w: unexpected codec id %d", ErrInvalidFrameData, codecID)
	}
	if w <= 0 || h <= 0 {
		return nil, ErrInvalidDimension
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	raw, err := c.dec.DecodeAll(payload, nil)
	if err != nil {
		return nil, fmt.Errorf("media: zstd decode failed: %w", err)
	}

	if flags&videoFlagNV12 != 0 {
		cwBytes := ((w + 1) / 2) * 2
		ch := (h + 1) / 2
		need := w*h + cwBytes*ch
		if len(raw) < need {
			return nil, ErrInvalidFrameData
		}
		yPlane := make([]byte, w*h)
		uvPlane := make([]byte, cwBytes*ch)
		copy(yPlane, raw[:w*h])
		copy(uvPlane, raw[w*h:need])
		rgba, err := NV12ToRGBA(yPlane, uvPlane, w, h, w, cwBytes)
		if err != nil {
			return nil, err
		}
		if cap(c.decRGBA) < len(rgba) {
			c.decRGBA = make([]byte, len(rgba))
		}
		c.decRGBA = c.decRGBA[:len(rgba)]
		copy(c.decRGBA, rgba)
		c.decW, c.decH = w, h
		c.decSeq = seq
		c.hasDecSeq = true
		return &core.VideoFrame{
			Seq:       seq,
			Codec:     core.CodecFrameZstd,
			Width:     w,
			Height:    h,
			KeyFrame:  flags&videoFlagKeyFrame != 0,
			Timestamp: ts,
			Data:      rgba,
			NV12Y:     yPlane,
			NV12UV:    uvPlane,
			StrideY:   w,
			StrideUV:  cwBytes,
		}, nil
	}

	expectedRGBA := w * h * 4
	if flags&videoFlagDelta != 0 {
		if c.decW != w || c.decH != h || len(c.decRGBA) != expectedRGBA {
			return nil, fmt.Errorf("%w: missing reference frame for delta", ErrInvalidFrameData)
		}
		if c.hasDecSeq && seq != 0 && seq != c.decSeq+1 {
			c.hasDecSeq = false
			c.decW, c.decH = 0, 0
			return nil, fmt.Errorf("%w: delta seq gap (%d -> %d)", ErrInvalidFrameData, c.decSeq, seq)
		}
		if len(raw) < 4 {
			return nil, ErrInvalidFrameData
		}
		tileCount := int(binary.LittleEndian.Uint32(raw[0:4]))
		pos := 4
		rowBytes := w * 4
		for i := 0; i < tileCount; i++ {
			if pos+8 > len(raw) {
				return nil, ErrInvalidFrameData
			}
			x0 := int(binary.LittleEndian.Uint16(raw[pos : pos+2]))
			y0 := int(binary.LittleEndian.Uint16(raw[pos+2 : pos+4]))
			tw := int(binary.LittleEndian.Uint16(raw[pos+4 : pos+6]))
			th := int(binary.LittleEndian.Uint16(raw[pos+6 : pos+8]))
			pos += 8
			if x0+tw > w || y0+th > h {
				return nil, ErrInvalidFrameData
			}
			span := tw * 4
			if pos+span*th > len(raw) {
				return nil, ErrInvalidFrameData
			}
			for dy := 0; dy < th; dy++ {
				off := (y0+dy)*rowBytes + x0*4
				for k := 0; k < span; k++ {
					c.decRGBA[off+k] ^= raw[pos+k]
				}
				pos += span
			}
		}
		c.decSeq = seq
		c.hasDecSeq = true
	} else {
		if len(raw) < expectedRGBA {
			return nil, ErrInvalidFrameData
		}
		if cap(c.decRGBA) < expectedRGBA {
			c.decRGBA = make([]byte, expectedRGBA)
		}
		c.decRGBA = c.decRGBA[:expectedRGBA]
		copy(c.decRGBA, raw[:expectedRGBA])
		c.decW, c.decH = w, h
		c.decSeq = seq
		c.hasDecSeq = true
	}

	outRGBA := make([]byte, expectedRGBA)
	copy(outRGBA, c.decRGBA)

	return &core.VideoFrame{
		Seq:       seq,
		Codec:     core.CodecFrameZstd,
		Width:     w,
		Height:    h,
		KeyFrame:  flags&videoFlagKeyFrame != 0,
		Timestamp: ts,
		Data:      outRGBA,
	}, nil
}

func packVideoHeader(codecID, flags, quality uint8, width, height int, seq uint64, ts int64, payload []byte) []byte {
	out := make([]byte, videoHdrSize+len(payload))
	copy(out[0:4], videoMagic)
	out[4] = videoVersion
	out[5] = codecID
	out[6] = flags
	out[7] = quality
	binary.LittleEndian.PutUint32(out[8:12], uint32(width))
	binary.LittleEndian.PutUint32(out[12:16], uint32(height))
	binary.LittleEndian.PutUint64(out[16:24], seq)
	binary.LittleEndian.PutUint64(out[24:32], uint64(ts))
	binary.LittleEndian.PutUint32(out[32:36], uint32(len(payload)))
	copy(out[videoHdrSize:], payload)
	return out
}

func parseVideoHeader(data []byte) (codecID, flags, quality uint8, width, height int, seq uint64, ts int64, payload []byte, err error) {
	if len(data) < videoHdrSize {
		return 0, 0, 0, 0, 0, 0, 0, nil, ErrInvalidFrameData
	}
	if string(data[0:4]) != videoMagic {
		return 0, 0, 0, 0, 0, 0, 0, nil, ErrInvalidFrameData
	}
	if data[4] != videoVersion {
		return 0, 0, 0, 0, 0, 0, 0, nil, ErrInvalidFrameData
	}
	codecID = data[5]
	flags = data[6]
	quality = data[7]
	width = int(binary.LittleEndian.Uint32(data[8:12]))
	height = int(binary.LittleEndian.Uint32(data[12:16]))
	seq = binary.LittleEndian.Uint64(data[16:24])
	ts = int64(binary.LittleEndian.Uint64(data[24:32]))
	pLen := int(binary.LittleEndian.Uint32(data[32:36]))
	if pLen < 0 || len(data) < videoHdrSize+pLen {
		return 0, 0, 0, 0, 0, 0, 0, nil, ErrInvalidFrameData
	}
	payload = data[videoHdrSize : videoHdrSize+pLen]
	return codecID, flags, quality, width, height, seq, ts, payload, nil
}

// CaptureBackend identifies the active screen capture backend.
type CaptureBackend string

const (
	CaptureBackendDXGI      CaptureBackend = "dxgi"
	CaptureBackendGDI       CaptureBackend = "gdi"
	CaptureBackendSynthetic CaptureBackend = "synthetic"
)

type CaptureOptions struct {
	Width          int
	Height         int
	ForceSynthetic bool
}

func renderSyntheticRGBA(dst []byte, width, height int, seq uint64) []byte {
	need := width * height * 4
	if cap(dst) < need {
		dst = make([]byte, need)
	}
	dst = dst[:need]

	barOffset := int((seq * 11) % uint64(maxInt(1, width)))
	for y := 0; y < height; y++ {
		row := y * width * 4
		topBar := y < 32
		for x := 0; x < width; x++ {
			px := row + x*4
			if topBar {
				dst[px] = 28
				dst[px+1] = 56
				dst[px+2] = 104
				dst[px+3] = 255
				continue
			}
			r := uint8(24 + (x*35)/maxInt(1, width))
			g := uint8(42 + (y*60)/maxInt(1, height))
			b := uint8(88 + ((x+y)*45)/maxInt(1, width+height))
			if x >= barOffset && x < barOffset+48 && y >= 48 && y < height-24 {
				r = 76
				g = 175
				b = 240
			} else if x > 24 && x < width-24 && y > 48 && y < height-24 && ((x-24)%64 < 2 || (y-48)%64 < 2) {
				r = 55
				g = 90
				b = 140
			}
			dst[px] = r
			dst[px+1] = g
			dst[px+2] = b
			dst[px+3] = 255
		}
	}
	return dst
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

type videoMFTBackend interface {
	Available() bool
	Encode(frame *core.VideoFrame, quality int) ([]byte, error)
	Decode(data []byte) (*core.VideoFrame, error)
	Close() error
}

// HybridVideoCodec tries Windows Media Foundation H.264 MFT when enabled and available,
// falling back transparently to ZstdVideoCodec on any unsupported format or runtime failure.
type HybridVideoCodec struct {
	zstd      *ZstdVideoCodec
	mft       videoMFTBackend
	preferMFT bool
}

func NewHybridVideoCodec(preferH264MFT bool) (*HybridVideoCodec, error) {
	z, err := NewZstdVideoCodec()
	if err != nil {
		return nil, err
	}
	var mft videoMFTBackend
	if preferH264MFT {
		mft = newPlatformVideoMFT()
	}
	return &HybridVideoCodec{
		zstd:      z,
		mft:       mft,
		preferMFT: preferH264MFT,
	}, nil
}

func (h *HybridVideoCodec) MFTAvailable() bool {
	return h.mft != nil && h.mft.Available()
}

func (h *HybridVideoCodec) Encode(frame *core.VideoFrame, quality int) ([]byte, error) {
	if frame == nil {
		return nil, ErrInvalidDimension
	}
	wantMFT := (h.preferMFT || frame.Codec == core.CodecH264MFT) && h.mft != nil && h.mft.Available()
	if wantMFT {
		if out, err := h.mft.Encode(frame, quality); err == nil {
			return out, nil
		}
	}
	return h.zstd.Encode(frame, quality)
}

func (h *HybridVideoCodec) Decode(data []byte) (*core.VideoFrame, error) {
	codecID, _, _, _, _, _, _, _, err := parseVideoHeader(data)
	if err != nil {
		return nil, err
	}
	if codecID == codecIDH264MFT {
		if h.mft == nil {
			h.mft = newPlatformVideoMFT()
		}
		if h.mft != nil && h.mft.Available() {
			return h.mft.Decode(data)
		}
		return nil, ErrMFTUnavailable
	}
	return h.zstd.Decode(data)
}

func (h *HybridVideoCodec) Close() error {
	var firstErr error
	if h.mft != nil {
		firstErr = h.mft.Close()
	}
	if err := h.zstd.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}
