package transport

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/klauspost/reedsolomon"
)

const (
	fecHeaderSize  = 12
	fecMagic       = uint16(0x4643) // "FC"
	fecMaxShardLen = 64 * 1024
	// fecMaxShards matches the GF(2^8) Reed-Solomon limit; larger stripes are
	// never produced by this package and are rejected on decode.
	fecMaxShards = 256
	// fecMaxGroups bounds the number of in-flight groups a decoder tracks.
	fecMaxGroups = 256
	// fecMaxCodecs bounds the cached Reed-Solomon codecs per decoder.
	fecMaxCodecs = 16
)

var (
	ErrFECInvalidConfig = errors.New("fec: dataShards and parityShards must be > 0")
	ErrFECInvalidPacket = errors.New("fec: invalid packet")
	ErrCipherKeyLength  = errors.New("transport: AES key must be 16, 24, or 32 bytes")
	ErrCipherShortData  = errors.New("transport: encrypted payload too short")
)

// FrameCipher wraps AES-GCM encryption for UDP/KCP or multicast payloads.
type FrameCipher struct {
	aead cipher.AEAD
}

func NewFrameCipher(key []byte) (*FrameCipher, error) {
	switch len(key) {
	case 16, 24, 32:
	default:
		return nil, ErrCipherKeyLength
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &FrameCipher{aead: aead}, nil
}

func (c *FrameCipher) Encrypt(plaintext []byte) ([]byte, error) {
	if c == nil || c.aead == nil {
		out := make([]byte, len(plaintext))
		copy(out, plaintext)
		return out, nil
	}
	ns := c.aead.NonceSize()
	out := make([]byte, ns, ns+len(plaintext)+c.aead.Overhead())
	if _, err := io.ReadFull(rand.Reader, out[:ns]); err != nil {
		return nil, err
	}
	return c.aead.Seal(out, out[:ns], plaintext, nil), nil
}

func (c *FrameCipher) Decrypt(ciphertext []byte) ([]byte, error) {
	if c == nil || c.aead == nil {
		out := make([]byte, len(ciphertext))
		copy(out, ciphertext)
		return out, nil
	}
	ns := c.aead.NonceSize()
	if len(ciphertext) < ns {
		return nil, ErrCipherShortData
	}
	nonce := ciphertext[:ns]
	return c.aead.Open(nil, nonce, ciphertext[ns:], nil)
}

// FECPacket represents one encoded shard in a Reed-Solomon stripe.
type FECPacket struct {
	GroupID uint32
	Index   uint16
	Total   uint16
	DataNum uint16
	Payload []byte
}

func EncodeFECPacket(p FECPacket) []byte {
	buf := make([]byte, fecHeaderSize+len(p.Payload))
	binary.BigEndian.PutUint16(buf[0:2], fecMagic)
	binary.BigEndian.PutUint32(buf[2:6], p.GroupID)
	binary.BigEndian.PutUint16(buf[6:8], p.Index)
	binary.BigEndian.PutUint16(buf[8:10], p.Total)
	binary.BigEndian.PutUint16(buf[10:12], p.DataNum)
	copy(buf[fecHeaderSize:], p.Payload)
	return buf
}

func DecodeFECPacket(raw []byte) (FECPacket, error) {
	if len(raw) < fecHeaderSize {
		return FECPacket{}, ErrFECInvalidPacket
	}
	if binary.BigEndian.Uint16(raw[0:2]) != fecMagic {
		return FECPacket{}, ErrFECInvalidPacket
	}
	p := FECPacket{
		GroupID: binary.BigEndian.Uint32(raw[2:6]),
		Index:   binary.BigEndian.Uint16(raw[6:8]),
		Total:   binary.BigEndian.Uint16(raw[8:10]),
		DataNum: binary.BigEndian.Uint16(raw[10:12]),
		Payload: make([]byte, len(raw)-fecHeaderSize),
	}
	copy(p.Payload, raw[fecHeaderSize:])
	if p.Total == 0 || p.Total > fecMaxShards || p.DataNum == 0 || p.DataNum > p.Total || p.Index >= p.Total {
		return FECPacket{}, ErrFECInvalidPacket
	}
	return p, nil
}

// FECEncoder encodes variable-length packets into data+parity shards.
type FECEncoder struct {
	mu           sync.Mutex
	dataShards   int
	parityShards int
	enc          reedsolomon.Encoder
	groupID      uint32
	pending      [][]byte
	maxShardSize int
}

func NewFECEncoder(dataShards, parityShards int) (*FECEncoder, error) {
	if dataShards <= 0 || parityShards <= 0 {
		return nil, ErrFECInvalidConfig
	}
	enc, err := reedsolomon.New(dataShards, parityShards)
	if err != nil {
		return nil, fmt.Errorf("fec: new encoder: %w", err)
	}
	return &FECEncoder{
		dataShards:   dataShards,
		parityShards: parityShards,
		enc:          enc,
		pending:      make([][]byte, 0, dataShards),
	}, nil
}

// EncodeBlock takes a complete payload (for example a video frame), splits it into
// dataShards equal-sized padded shards, computes parityShards, and returns all wired FEC packets.
func (e *FECEncoder) EncodeBlock(payload []byte) ([][]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	totalLen := 4 + len(payload)
	shardLen := (totalLen + e.dataShards - 1) / e.dataShards
	if shardLen == 0 {
		shardLen = 4
	}
	if shardLen > fecMaxShardLen {
		return nil, ErrFECInvalidPacket
	}

	totalShards := e.dataShards + e.parityShards
	backing := make([]byte, totalShards*shardLen)
	binary.BigEndian.PutUint32(backing[0:4], uint32(len(payload)))
	copy(backing[4:], payload)

	shards := make([][]byte, totalShards)
	for i := 0; i < totalShards; i++ {
		shards[i] = backing[i*shardLen : (i+1)*shardLen]
	}
	if err := e.enc.Encode(shards); err != nil {
		return nil, fmt.Errorf("fec: encode shards: %w", err)
	}

	e.groupID++
	gid := e.groupID
	out := make([][]byte, len(shards))
	for i, s := range shards {
		out[i] = EncodeFECPacket(FECPacket{
			GroupID: gid,
			Index:   uint16(i),
			Total:   uint16(len(shards)),
			DataNum: uint16(e.dataShards),
			Payload: s,
		})
	}
	return out, nil
}

// AddPacket buffers streaming packets and emits a full FEC stripe once dataShards arrive.
func (e *FECEncoder) AddPacket(pkt []byte) ([][]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	wrapped := make([]byte, 2+len(pkt))
	binary.BigEndian.PutUint16(wrapped[0:2], uint16(len(pkt)))
	copy(wrapped[2:], pkt)
	if len(wrapped) > e.maxShardSize {
		e.maxShardSize = len(wrapped)
	}
	e.pending = append(e.pending, wrapped)
	if len(e.pending) < e.dataShards {
		return nil, nil
	}
	return e.flushLocked()
}

func (e *FECEncoder) Flush() ([][]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.pending) == 0 {
		return nil, nil
	}
	for len(e.pending) < e.dataShards {
		e.pending = append(e.pending, []byte{0, 0})
	}
	return e.flushLocked()
}

func (e *FECEncoder) flushLocked() ([][]byte, error) {
	total := e.dataShards + e.parityShards
	shardSize := e.maxShardSize
	if shardSize < 2 {
		shardSize = 2
	}
	shards := make([][]byte, total)
	for i := 0; i < e.dataShards; i++ {
		shards[i] = make([]byte, shardSize)
		copy(shards[i], e.pending[i])
	}
	for i := e.dataShards; i < total; i++ {
		shards[i] = make([]byte, shardSize)
	}
	if err := e.enc.Encode(shards); err != nil {
		return nil, fmt.Errorf("fec: encode stream stripe: %w", err)
	}

	e.groupID++
	gid := e.groupID
	out := make([][]byte, total)
	for i, s := range shards {
		out[i] = EncodeFECPacket(FECPacket{
			GroupID: gid,
			Index:   uint16(i),
			Total:   uint16(total),
			DataNum: uint16(e.dataShards),
			Payload: s,
		})
	}
	e.pending = e.pending[:0]
	e.maxShardSize = 0
	return out, nil
}

type fecGroup struct {
	shards     [][]byte
	received   int
	dataNum    int
	total      int
	shardLen   int
	completed  bool
	lastActive time.Time
}

// FECDecoder reassembles and reconstructs FEC blocks or packet stripes.
type FECDecoder struct {
	mu     sync.Mutex
	groups map[uint32]*fecGroup
	codecs map[uint32]reedsolomon.Encoder
	ttl    time.Duration
	lastGC time.Time
}

func NewFECDecoder() *FECDecoder {
	return &FECDecoder{
		groups: make(map[uint32]*fecGroup),
		codecs: make(map[uint32]reedsolomon.Encoder),
		ttl:    5 * time.Second,
	}
}

func (d *FECDecoder) getCodec(dataNum, parityNum int) (reedsolomon.Encoder, error) {
	key := uint32(dataNum)<<16 | uint32(parityNum)
	if enc, ok := d.codecs[key]; ok {
		return enc, nil
	}
	enc, err := reedsolomon.New(dataNum, parityNum)
	if err != nil {
		return nil, err
	}
	if len(d.codecs) >= fecMaxCodecs {
		d.codecs = make(map[uint32]reedsolomon.Encoder)
	}
	d.codecs[key] = enc
	return enc, nil
}

func (d *FECDecoder) gcLocked(now time.Time) {
	if len(d.groups) < 64 || now.Sub(d.lastGC) < 500*time.Millisecond {
		return
	}
	d.lastGC = now
	for id, g := range d.groups {
		if now.Sub(g.lastActive) > d.ttl {
			delete(d.groups, id)
		}
	}
	if len(d.groups) > 128 {
		for id, g := range d.groups {
			if g.completed {
				delete(d.groups, id)
			}
		}
	}
}

// groupForPacketLocked returns the group pkt belongs to, creating it (and
// evicting old groups when at capacity) if needed. Packets whose Total or
// DataNum disagree with the group's first packet are rejected.
func (d *FECDecoder) groupForPacketLocked(pkt FECPacket, now time.Time) (*fecGroup, error) {
	grp, ok := d.groups[pkt.GroupID]
	if !ok {
		if len(d.groups) >= fecMaxGroups {
			d.evictGroupLocked()
		}
		grp = &fecGroup{
			shards:     make([][]byte, pkt.Total),
			dataNum:    int(pkt.DataNum),
			total:      int(pkt.Total),
			shardLen:   len(pkt.Payload),
			lastActive: now,
		}
		d.groups[pkt.GroupID] = grp
	}
	grp.lastActive = now
	if grp.completed {
		return grp, nil
	}
	if int(pkt.Total) != grp.total || int(pkt.DataNum) != grp.dataNum || int(pkt.Index) >= len(grp.shards) {
		return nil, ErrFECInvalidPacket
	}
	return grp, nil
}

// evictGroupLocked drops completed groups, or the least recently active one.
func (d *FECDecoder) evictGroupLocked() {
	var oldestID uint32
	var oldest *fecGroup
	for id, g := range d.groups {
		if g.completed {
			delete(d.groups, id)
			continue
		}
		if oldest == nil || g.lastActive.Before(oldest.lastActive) {
			oldestID, oldest = id, g
		}
	}
	if len(d.groups) >= fecMaxGroups && oldest != nil {
		delete(d.groups, oldestID)
	}
}

// DecodeBlock feeds one raw wired FEC packet and returns the reconstructed block payload once ready.
func (d *FECDecoder) DecodeBlock(raw []byte) ([]byte, bool, error) {
	pkt, err := DecodeFECPacket(raw)
	if err != nil {
		return nil, false, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	d.gcLocked(now)

	grp, err := d.groupForPacketLocked(pkt, now)
	if err != nil {
		return nil, false, err
	}
	if grp.completed {
		return nil, false, nil
	}
	if len(pkt.Payload) > grp.shardLen {
		grp.shardLen = len(pkt.Payload)
	}
	if grp.shards[pkt.Index] == nil {
		grp.shards[pkt.Index] = pkt.Payload
		grp.received++
	}
	if grp.received < grp.dataNum {
		return nil, false, nil
	}

	parityNum := grp.total - grp.dataNum
	if parityNum > 0 {
		// Pad any shorter shards before RS reconstruction (useful for stream mode).
		for i := range grp.shards {
			if grp.shards[i] != nil && len(grp.shards[i]) < grp.shardLen {
				padded := make([]byte, grp.shardLen)
				copy(padded, grp.shards[i])
				grp.shards[i] = padded
			}
		}
		needReconstruct := false
		for i := 0; i < grp.dataNum; i++ {
			if grp.shards[i] == nil {
				needReconstruct = true
				break
			}
		}
		if needReconstruct {
			enc, err := d.getCodec(grp.dataNum, parityNum)
			if err != nil {
				return nil, false, err
			}
			if err := enc.ReconstructData(grp.shards); err != nil {
				return nil, false, err
			}
		}
	} else {
		for i := 0; i < grp.dataNum; i++ {
			if grp.shards[i] == nil {
				return nil, false, nil
			}
		}
	}

	grp.completed = true
	joined := make([]byte, 0, grp.dataNum*grp.shardLen)
	for i := 0; i < grp.dataNum; i++ {
		joined = append(joined, grp.shards[i]...)
	}
	grp.shards = nil
	if len(joined) < 4 {
		return nil, false, ErrFECInvalidPacket
	}
	payloadLen := int(binary.BigEndian.Uint32(joined[0:4]))
	if payloadLen < 0 || 4+payloadLen > len(joined) {
		return nil, false, ErrFECInvalidPacket
	}
	out := make([]byte, payloadLen)
	copy(out, joined[4:4+payloadLen])
	return out, true, nil
}

// DecodeStreamPacket feeds a packet produced by FECEncoder.AddPacket and returns recovered packets when a stripe completes.
func (d *FECDecoder) DecodeStreamPacket(raw []byte) ([][]byte, error) {
	pkt, err := DecodeFECPacket(raw)
	if err != nil {
		return nil, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	d.gcLocked(now)

	grp, err := d.groupForPacketLocked(pkt, now)
	if err != nil {
		return nil, err
	}
	if grp.completed {
		return nil, nil
	}
	if len(pkt.Payload) > grp.shardLen {
		grp.shardLen = len(pkt.Payload)
	}
	if grp.shards[pkt.Index] == nil {
		grp.shards[pkt.Index] = pkt.Payload
		grp.received++
	}
	if grp.received < grp.dataNum {
		return nil, nil
	}

	for i := range grp.shards {
		if grp.shards[i] != nil && len(grp.shards[i]) < grp.shardLen {
			padded := make([]byte, grp.shardLen)
			copy(padded, grp.shards[i])
			grp.shards[i] = padded
		}
	}

	parityNum := grp.total - grp.dataNum
	if parityNum > 0 {
		enc, err := d.getCodec(grp.dataNum, parityNum)
		if err != nil {
			return nil, err
		}
		if err := enc.ReconstructData(grp.shards); err != nil {
			return nil, err
		}
	}

	grp.completed = true
	out := make([][]byte, 0, grp.dataNum)
	for i := 0; i < grp.dataNum; i++ {
		s := grp.shards[i]
		if len(s) < 2 {
			continue
		}
		n := int(binary.BigEndian.Uint16(s[0:2]))
		if n == 0 || 2+n > len(s) {
			continue
		}
		pktOut := make([]byte, n)
		copy(pktOut, s[2:2+n])
		out = append(out, pktOut)
	}
	grp.shards = nil
	return out, nil
}
