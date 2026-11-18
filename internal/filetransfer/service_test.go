package filetransfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"nexlab/internal/core"
	"nexlab/internal/protocol"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type pipeSession struct {
	id        string
	addr      string
	onStream  func(kind protocol.StreamKind, stream io.ReadWriteCloser)
	mu        sync.Mutex
	envelopes []protocol.Envelope
	closed    bool
}

func newPipeSession(id string, handler func(kind protocol.StreamKind, stream io.ReadWriteCloser)) *pipeSession {
	return &pipeSession{
		id:       id,
		addr:     "127.0.0.1:0",
		onStream: handler,
	}
}

func (p *pipeSession) ID() string         { return p.id }
func (p *pipeSession) RemoteAddr() string { return p.addr }

func (p *pipeSession) OpenStream(kind protocol.StreamKind) (io.ReadWriteCloser, error) {
	p.mu.Lock()
	closed := p.closed
	handler := p.onStream
	p.mu.Unlock()
	if closed {
		return nil, io.ErrClosedPipe
	}
	clientConn, serverConn := net.Pipe()
	if handler != nil {
		go handler(kind, serverConn)
	} else {
		_ = serverConn.Close()
	}
	return clientConn, nil
}

func (p *pipeSession) SendEnvelope(env protocol.Envelope) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return io.ErrClosedPipe
	}
	p.envelopes = append(p.envelopes, env)
	return nil
}

func (p *pipeSession) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	return nil
}

func makeDeterministicData(size int) []byte {
	out := make([]byte, size)
	for i := range out {
		out[i] = byte((i*31 + 17) & 0xFF)
	}
	return out
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestDistributeFileWithAutoOpenAndProgress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "lecture_notes.pdf")
	payload := makeDeterministicData(96 * 1024)
	if err := os.WriteFile(srcPath, payload, 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	var (
		autoOpenMu sync.Mutex
		opened     = make(map[string]string)
	)

	stu1Dir := filepath.Join(tmpDir, "stu1_rx")
	stu2Dir := filepath.Join(tmpDir, "stu2_rx")

	stu1Svc := NewService(Config{
		ReceiveDir: stu1Dir,
		ChunkSize:  8 * 1024,
		OnAutoOpen: func(path string) {
			autoOpenMu.Lock()
			opened["s1"] = path
			autoOpenMu.Unlock()
		},
	})
	stu2Svc := NewService(Config{
		ReceiveDir: stu2Dir,
		ChunkSize:  8 * 1024,
		OnAutoOpen: func(path string) {
			autoOpenMu.Lock()
			opened["s2"] = path
			autoOpenMu.Unlock()
		},
	})

	var progressEvents atomic.Int32
	teacherSvc := NewService(Config{
		ArchiveDir: filepath.Join(tmpDir, "archive"),
		ChunkSize:  8 * 1024,
		OnProgress: func(_ core.TransferTask) {
			progressEvents.Add(1)
		},
	})

	teacherSvc.RegisterSession("s1", newPipeSession("s1", func(_ protocol.StreamKind, stream io.ReadWriteCloser) {
		_ = stu1Svc.HandleStream(ctx, stream)
	}))
	teacherSvc.RegisterSession("s2", newPipeSession("s2", func(_ protocol.StreamKind, stream io.ReadWriteCloser) {
		_ = stu2Svc.HandleStream(ctx, stream)
	}))

	task, err := teacherSvc.DistributeFile(ctx, srcPath, "", []string{"s1", "s2"}, true)
	if err != nil {
		t.Fatalf("DistributeFile failed: %v", err)
	}
	if task.Status != "completed" || task.DoneBytes != int64(len(payload)) {
		t.Fatalf("unexpected task result: %+v", task)
	}
	if task.SHA256 != sha256Hex(payload) {
		t.Fatalf("unexpected SHA256: got %s want %s", task.SHA256, sha256Hex(payload))
	}
	if progressEvents.Load() < 2 {
		t.Fatalf("expected progress callbacks, got %d", progressEvents.Load())
	}

	for sid, rxDir := range map[string]string{"s1": stu1Dir, "s2": stu2Dir} {
		gotPath := filepath.Join(rxDir, "lecture_notes.pdf")
		gotBytes, err := os.ReadFile(gotPath)
		if err != nil {
			t.Fatalf("read received file for %s: %v", sid, err)
		}
		if !bytes.Equal(gotBytes, payload) {
			t.Fatalf("file content mismatch for %s", sid)
		}
		autoOpenMu.Lock()
		openPath := opened[sid]
		autoOpenMu.Unlock()
		if openPath != gotPath {
			t.Fatalf("expected auto-open path %q for %s, got %q", gotPath, sid, openPath)
		}
	}

	tasks := teacherSvc.ListTasks()
	if len(tasks) != 1 || tasks[0].ID != task.ID {
		t.Fatalf("unexpected teacher task list: %+v", tasks)
	}
}

func TestResumeInterruptedTransferAndSkipIdentical(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "dataset.bin")
	payload := makeDeterministicData(64 * 1024)
	if err := os.WriteFile(srcPath, payload, 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	remoteDir := filepath.Join(tmpDir, "student_desk")
	if err := os.MkdirAll(remoteDir, 0o755); err != nil {
		t.Fatalf("mkdir remoteDir: %v", err)
	}
	destPath := filepath.Join(remoteDir, "dataset.bin")
	partPath := destPath + ".part"

	// Pre-populate partial download (first 24 KiB).
	partialLen := 24 * 1024
	if err := os.WriteFile(partPath, payload[:partialLen], 0o644); err != nil {
		t.Fatalf("write partial file: %v", err)
	}

	var chunkBytesReceived atomic.Int64
	studentSvc := NewService(Config{
		ReceiveDir: remoteDir,
		ChunkSize:  8 * 1024,
	})
	teacherSvc := NewService(Config{
		ChunkSize: 8 * 1024,
	})

	// Wrap stream to count MsgFileChunk payload bytes sent from teacher after resume.
	teacherSvc.RegisterSession("s1", newPipeSession("s1", func(_ protocol.StreamKind, stream io.ReadWriteCloser) {
		counting := &chunkCountingRWC{ReadWriteCloser: stream, chunkBytes: &chunkBytesReceived}
		_ = studentSvc.HandleStream(ctx, counting)
	}))

	task, err := teacherSvc.DistributeFile(ctx, srcPath, remoteDir, []string{"s1"}, false)
	if err != nil {
		t.Fatalf("DistributeFile with resume failed: %v", err)
	}
	if task.Status != "completed" {
		t.Fatalf("expected completed status, got %s", task.Status)
	}

	expectedRemaining := int64(len(payload) - partialLen)
	if chunkBytesReceived.Load() != expectedRemaining {
		t.Fatalf("expected %d resumed bytes over wire, got %d", expectedRemaining, chunkBytesReceived.Load())
	}
	if _, err := os.Stat(partPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected .part file removed after completion")
	}
	gotBytes, err := os.ReadFile(destPath)
	if err != nil || !bytes.Equal(gotBytes, payload) {
		t.Fatalf("verified resumed file mismatch: err=%v", err)
	}

	// Re-distributing identical file should skip sending chunks completely (0 chunk bytes).
	chunkBytesReceived.Store(0)
	if _, err := teacherSvc.DistributeFile(ctx, srcPath, remoteDir, []string{"s1"}, false); err != nil {
		t.Fatalf("second DistributeFile failed: %v", err)
	}
	if chunkBytesReceived.Load() != 0 {
		t.Fatalf("expected 0 chunk bytes when target already has matching SHA256, got %d", chunkBytesReceived.Load())
	}
}

type chunkCountingRWC struct {
	io.ReadWriteCloser
	buf        bytes.Buffer
	mu         sync.Mutex
	chunkBytes *atomic.Int64
}

func (c *chunkCountingRWC) Read(p []byte) (int, error) {
	n, err := c.ReadWriteCloser.Read(p)
	if n > 0 {
		c.mu.Lock()
		c.buf.Write(p[:n])
		for c.buf.Len() >= protocol.HeaderSize {
			hdr := c.buf.Bytes()[:protocol.HeaderSize]
			msgType := protocol.MsgType(binary.BigEndian.Uint16(hdr[6:8]))
			length := int(binary.BigEndian.Uint32(hdr[8:12]))
			if c.buf.Len() < protocol.HeaderSize+length {
				break
			}
			frame := make([]byte, protocol.HeaderSize+length)
			_, _ = c.buf.Read(frame)
			if msgType == protocol.MsgFileChunk && length >= 8 {
				c.chunkBytes.Add(int64(length - 8))
			}
		}
		c.mu.Unlock()
	}
	return n, err
}

func TestSubmitHomeworkAndCollectFiles(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tmpDir := t.TempDir()
	archiveDir := filepath.Join(tmpDir, "teacher_archive")
	collectDir := filepath.Join(tmpDir, "teacher_collected")

	metaMap := map[string]StudentMeta{
		"s1": {ID: "s1", StudentNo: "2026001", Name: "Alice"},
		"s2": {ID: "s2", StudentNo: "2026002", Name: "Bob"},
	}

	teacherSvc := NewService(Config{
		ArchiveDir: archiveDir,
		ChunkSize:  16 * 1024,
		StudentResolver: func(studentID string) (StudentMeta, bool) {
			m, ok := metaMap[studentID]
			return m, ok
		},
	})

	stu1Svc := NewService(Config{ChunkSize: 16 * 1024})
	stu1Svc.RegisterStudentMeta(metaMap["s1"])
	stu1Svc.SetTeacherSession(newPipeSession(TeacherPeerID, func(_ protocol.StreamKind, stream io.ReadWriteCloser) {
		_ = teacherSvc.HandleStream(ctx, stream)
	}))

	stu2Svc := NewService(Config{ChunkSize: 16 * 1024})
	stu2Svc.RegisterStudentMeta(metaMap["s2"])

	// 1. Student s1 submits homework to teacher.
	hwPath := filepath.Join(tmpDir, "lab1_solution.go")
	hwData := []byte("package main\nfunc main() {}\n")
	if err := os.WriteFile(hwPath, hwData, 0o644); err != nil {
		t.Fatalf("write homework: %v", err)
	}

	subTask, err := stu1Svc.SubmitHomework(ctx, "s1", hwPath)
	if err != nil {
		t.Fatalf("SubmitHomework failed: %v", err)
	}
	expectedSaved := filepath.Join(archiveDir, "2026001_Alice", "lab1_solution.go")
	if subTask.RemotePath != expectedSaved {
		t.Fatalf("expected saved path %q, got %q", expectedSaved, subTask.RemotePath)
	}
	archivedBytes, err := os.ReadFile(expectedSaved)
	if err != nil || !bytes.Equal(archivedBytes, hwData) {
		t.Fatalf("archived homework mismatch: err=%v", err)
	}

	// 2. Teacher collects files from both s1 and s2 work directories.
	s1WorkDir := filepath.Join(tmpDir, "work_s1")
	s2WorkDir := filepath.Join(tmpDir, "work_s2")
	if err := os.MkdirAll(s1WorkDir, 0o755); err != nil {
		t.Fatalf("mkdir s1WorkDir: %v", err)
	}
	if err := os.MkdirAll(s2WorkDir, 0o755); err != nil {
		t.Fatalf("mkdir s2WorkDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(s1WorkDir, "report.md"), []byte("# Alice Report"), 0o644); err != nil {
		t.Fatalf("write s1 report: %v", err)
	}
	if err := os.WriteFile(filepath.Join(s1WorkDir, "temp.part"), []byte("incomplete"), 0o644); err != nil {
		t.Fatalf("write s1 temp.part: %v", err)
	}
	if err := os.WriteFile(filepath.Join(s2WorkDir, "report.md"), []byte("# Bob Report"), 0o644); err != nil {
		t.Fatalf("write s2 report: %v", err)
	}
	if err := os.WriteFile(filepath.Join(s2WorkDir, "diagram.svg"), []byte("<svg/>"), 0o644); err != nil {
		t.Fatalf("write s2 diagram: %v", err)
	}

	// Route each student's collect request to their respective workspace directory.
	teacherSvc.RegisterSession("s1", newPipeSession("s1", func(_ protocol.StreamKind, stream io.ReadWriteCloser) {
		rewriteCollectStream(ctx, t, stream, s1WorkDir, stu1Svc)
	}))
	teacherSvc.RegisterSession("s2", newPipeSession("s2", func(_ protocol.StreamKind, stream io.ReadWriteCloser) {
		rewriteCollectStream(ctx, t, stream, s2WorkDir, stu2Svc)
	}))

	collected, err := teacherSvc.CollectFiles(ctx, "student_workspace", collectDir, []string{"s1", "s2"})
	if err != nil {
		t.Fatalf("CollectFiles failed: %v", err)
	}
	if len(collected) != 3 {
		t.Fatalf("expected 3 collected files (ignoring .part), got %d", len(collected))
	}

	gotS1, err := os.ReadFile(filepath.Join(collectDir, "2026001_Alice", "report.md"))
	if err != nil || string(gotS1) != "# Alice Report" {
		t.Fatalf("s1 collected report mismatch: %q err=%v", string(gotS1), err)
	}
	gotS2, err := os.ReadFile(filepath.Join(collectDir, "2026002_Bob", "diagram.svg"))
	if err != nil || string(gotS2) != "<svg/>" {
		t.Fatalf("s2 collected diagram mismatch: %q err=%v", string(gotS2), err)
	}
}

func rewriteCollectStream(ctx context.Context, t *testing.T, stream io.ReadWriteCloser, actualDir string, svc *Service) {
	t.Helper()
	defer stream.Close()
	_, msgType, payload, err := protocol.ReadFrame(stream)
	if err != nil {
		return
	}
	if msgType != protocol.MsgCollectReq {
		t.Errorf("expected MsgCollectReq, got %d", msgType)
		return
	}
	var req CollectRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Errorf("unmarshal CollectRequest: %v", err)
		return
	}
	req.RemoteDir = actualDir
	_ = svc.handleCollectRequest(ctx, stream, req)
}

func TestChecksumMismatchRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tmpDir := t.TempDir()
	rxSvc := NewService(Config{ReceiveDir: tmpDir})

	clientConn, serverConn := net.Pipe()
	errCh := make(chan error, 1)
	go func() {
		errCh <- rxSvc.HandleStream(ctx, serverConn)
	}()

	data := []byte("corrupted payload over wire")
	offerBytes, _ := json.Marshal(FileOffer{
		TaskID:     "bad-hash-1",
		Direction:  core.TransferSend,
		FileName:   "corrupt.bin",
		TotalBytes: int64(len(data)),
		SHA256:     strings_RepeatZeroHash(),
	})
	if err := protocol.WriteFrame(clientConn, protocol.StreamFile, protocol.MsgFileOffer, offerBytes); err != nil {
		t.Fatalf("write offer: %v", err)
	}
	if _, msgType, _, err := protocol.ReadFrame(clientConn); err != nil || msgType != protocol.MsgFileAck {
		t.Fatalf("read ack failed: type=%d err=%v", msgType, err)
	}

	chunk := make([]byte, 8+len(data))
	binary.BigEndian.PutUint64(chunk[:8], 0)
	copy(chunk[8:], data)
	if err := protocol.WriteFrame(clientConn, protocol.StreamFile, protocol.MsgFileChunk, chunk); err != nil {
		t.Fatalf("write chunk: %v", err)
	}

	doneBytes, _ := json.Marshal(FileComplete{
		TaskID:    "bad-hash-1",
		DoneBytes: int64(len(data)),
		Success:   true,
	})
	if err := protocol.WriteFrame(clientConn, protocol.StreamFile, protocol.MsgFileComplete, doneBytes); err != nil {
		t.Fatalf("write complete: %v", err)
	}

	_, respType, respPayload, err := protocol.ReadFrame(clientConn)
	if err != nil || respType != protocol.MsgFileComplete {
		t.Fatalf("read final complete: type=%d err=%v", respType, err)
	}
	var resp FileComplete
	_ = json.Unmarshal(respPayload, &resp)
	if resp.Success {
		t.Fatalf("expected checksum verification failure, got success")
	}
	_ = clientConn.Close()

	if recvErr := <-errCh; !errors.Is(recvErr, ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch, got %v", recvErr)
	}
}

func strings_RepeatZeroHash() string {
	return "0000000000000000000000000000000000000000000000000000000000000000"
}
