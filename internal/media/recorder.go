package media

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	nxlrMagic      = "NXLR"
	nxlrVersion    = uint16(1)
	nxlrFileHdrLen = 24
	nxlrChunkLen   = 24
)

type RecordKind uint8

const (
	RecordVideo  RecordKind = 1
	RecordAudio  RecordKind = 2
	RecordMarker RecordKind = 3
)

var (
	ErrInvalidNXLR    = errors.New("media: invalid .nxlr recording file")
	ErrRecorderClosed = errors.New("media: recorder is closed")
)

type LessonHeader struct {
	Version       uint16
	Flags         uint16
	CreatedUnixMs int64
	Width         int
	Height        int
}

type LessonChunk struct {
	Kind      RecordKind
	Flags     uint8
	Timestamp int64
	Seq       uint64
	Payload   []byte
}

type RecorderStats struct {
	Path         string
	VideoFrames  uint64
	AudioFrames  uint64
	BytesWritten uint64
}

// LessonRecorder writes thread-safe .nxlr lesson recording streams.
type LessonRecorder struct {
	mu     sync.Mutex
	file   *os.File
	bw     *bufio.Writer
	closed bool
	stats  RecorderStats
	header LessonHeader
}

func NewLessonRecorder(path string, width, height int) (*LessonRecorder, error) {
	if path == "" {
		return nil, fmt.Errorf("media: empty recording path")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("media: create recording directory: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("media: open recording file: %w", err)
	}

	hdr := LessonHeader{
		Version:       nxlrVersion,
		CreatedUnixMs: time.Now().UnixMilli(),
		Width:         width,
		Height:        height,
	}
	var raw [nxlrFileHdrLen]byte
	copy(raw[0:4], nxlrMagic)
	binary.LittleEndian.PutUint16(raw[4:6], hdr.Version)
	binary.LittleEndian.PutUint16(raw[6:8], hdr.Flags)
	binary.LittleEndian.PutUint64(raw[8:16], uint64(hdr.CreatedUnixMs))
	binary.LittleEndian.PutUint32(raw[16:20], uint32(maxInt(0, width)))
	binary.LittleEndian.PutUint32(raw[20:24], uint32(maxInt(0, height)))

	bw := bufio.NewWriterSize(f, 64*1024)
	if _, err := bw.Write(raw[:]); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("media: write nxlr header: %w", err)
	}
	if err := bw.Flush(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("media: flush nxlr header: %w", err)
	}

	return &LessonRecorder{
		file:   f,
		bw:     bw,
		header: hdr,
		stats: RecorderStats{
			Path:         path,
			BytesWritten: nxlrFileHdrLen,
		},
	}, nil
}

func (r *LessonRecorder) WriteVideo(seq uint64, ts int64, keyFrame bool, encoded []byte) error {
	var flags uint8
	if keyFrame {
		flags |= 1
	}
	return r.writeChunk(RecordVideo, flags, seq, ts, encoded, keyFrame)
}

func (r *LessonRecorder) WriteAudio(seq uint64, ts int64, encoded []byte) error {
	return r.writeChunk(RecordAudio, 0, seq, ts, encoded, false)
}

func (r *LessonRecorder) WriteMarker(ts int64, label string) error {
	return r.writeChunk(RecordMarker, 0, 0, ts, []byte(label), true)
}

func (r *LessonRecorder) writeChunk(kind RecordKind, flags uint8, seq uint64, ts int64, payload []byte, flush bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return ErrRecorderClosed
	}
	var hdr [nxlrChunkLen]byte
	hdr[0] = byte(kind)
	hdr[1] = flags
	binary.LittleEndian.PutUint16(hdr[2:4], 0)
	binary.LittleEndian.PutUint64(hdr[4:12], uint64(ts))
	binary.LittleEndian.PutUint64(hdr[12:20], seq)
	binary.LittleEndian.PutUint32(hdr[20:24], uint32(len(payload)))

	if _, err := r.bw.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := r.bw.Write(payload); err != nil {
			return err
		}
	}
	if flush {
		if err := r.bw.Flush(); err != nil {
			return err
		}
	}

	r.stats.BytesWritten += uint64(nxlrChunkLen + len(payload))
	switch kind {
	case RecordVideo:
		r.stats.VideoFrames++
	case RecordAudio:
		r.stats.AudioFrames++
	}
	return nil
}

func (r *LessonRecorder) Stats() RecorderStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stats
}

func (r *LessonRecorder) Flush() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRecorderClosed
	}
	return r.bw.Flush()
}

func (r *LessonRecorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	flushErr := r.bw.Flush()
	closeErr := r.file.Close()
	if flushErr != nil {
		return flushErr
	}
	return closeErr
}

// LessonReader sequentially reads chunks from an .nxlr recording file.
type LessonReader struct {
	mu     sync.Mutex
	file   *os.File
	br     *bufio.Reader
	header LessonHeader
}

func OpenLessonReader(path string) (*LessonReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(f, 64*1024)
	var raw [nxlrFileHdrLen]byte
	if _, err := io.ReadFull(br, raw[:]); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%w: %v", ErrInvalidNXLR, err)
	}
	if string(raw[0:4]) != nxlrMagic {
		_ = f.Close()
		return nil, ErrInvalidNXLR
	}
	ver := binary.LittleEndian.Uint16(raw[4:6])
	if ver != nxlrVersion {
		_ = f.Close()
		return nil, fmt.Errorf("%w: unsupported version %d", ErrInvalidNXLR, ver)
	}
	hdr := LessonHeader{
		Version:       ver,
		Flags:         binary.LittleEndian.Uint16(raw[6:8]),
		CreatedUnixMs: int64(binary.LittleEndian.Uint64(raw[8:16])),
		Width:         int(binary.LittleEndian.Uint32(raw[16:20])),
		Height:        int(binary.LittleEndian.Uint32(raw[20:24])),
	}
	return &LessonReader{
		file:   f,
		br:     br,
		header: hdr,
	}, nil
}

func (lr *LessonReader) Header() LessonHeader {
	return lr.header
}

func (lr *LessonReader) Next() (*LessonChunk, error) {
	lr.mu.Lock()
	defer lr.mu.Unlock()

	var hdr [nxlrChunkLen]byte
	_, err := io.ReadFull(lr.br, hdr[:])
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("%w: %v", ErrInvalidNXLR, err)
	}
	pLen := binary.LittleEndian.Uint32(hdr[20:24])
	if pLen > 64*1024*1024 {
		return nil, ErrInvalidNXLR
	}
	var payload []byte
	if pLen > 0 {
		payload = make([]byte, pLen)
		if _, err := io.ReadFull(lr.br, payload); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidNXLR, err)
		}
	}
	return &LessonChunk{
		Kind:      RecordKind(hdr[0]),
		Flags:     hdr[1],
		Timestamp: int64(binary.LittleEndian.Uint64(hdr[4:12])),
		Seq:       binary.LittleEndian.Uint64(hdr[12:20]),
		Payload:   payload,
	}, nil
}

func (lr *LessonReader) ReadAll() ([]*LessonChunk, error) {
	var chunks []*LessonChunk
	for {
		ch, err := lr.Next()
		if errors.Is(err, io.EOF) {
			return chunks, nil
		}
		if err != nil {
			return chunks, err
		}
		chunks = append(chunks, ch)
	}
}

func (lr *LessonReader) Close() error {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if lr.file == nil {
		return nil
	}
	err := lr.file.Close()
	lr.file = nil
	return err
}
