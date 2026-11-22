package filetransfer

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nexlab/internal/core"
	"nexlab/internal/protocol"
)

// sendRawOffer writes an offer on a fresh pipe served by svc.HandleStreamFrom
// and returns the decoded ack, final completion and the handler error.
func sendRawOffer(t *testing.T, svc *Service, sessionID string, offer FileOffer, data []byte) (FileAck, FileComplete, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, server := net.Pipe()
	defer client.Close()
	errCh := make(chan error, 1)
	go func() { errCh <- svc.HandleStreamFrom(ctx, sessionID, server) }()

	payload, _ := json.Marshal(offer)
	if err := protocol.WriteFrame(client, protocol.StreamFile, protocol.MsgFileOffer, payload); err != nil {
		t.Fatalf("write offer: %v", err)
	}
	_, msgType, ackPayload, err := protocol.ReadFrame(client)
	if err != nil || msgType != protocol.MsgFileAck {
		t.Fatalf("read ack: type=%d err=%v", msgType, err)
	}
	var ack FileAck
	_ = json.Unmarshal(ackPayload, &ack)
	var final FileComplete
	if ack.Accepted {
		chunk := make([]byte, 8+len(data))
		binary.BigEndian.PutUint64(chunk[:8], 0)
		copy(chunk[8:], data)
		if err := protocol.WriteFrame(client, protocol.StreamFile, protocol.MsgFileChunk, chunk); err != nil {
			t.Fatalf("write chunk: %v", err)
		}
		done, _ := json.Marshal(FileComplete{TaskID: offer.TaskID, DoneBytes: int64(len(data)), Success: true})
		if err := protocol.WriteFrame(client, protocol.StreamFile, protocol.MsgFileComplete, done); err != nil {
			t.Fatalf("write complete: %v", err)
		}
		_, _, finalPayload, err := protocol.ReadFrame(client)
		if err != nil {
			t.Fatalf("read final: %v", err)
		}
		_ = json.Unmarshal(finalPayload, &final)
	}
	_ = client.Close()
	return ack, final, <-errCh
}

func TestTeacherRoleRejectsSendOffersAndCollectRequests(t *testing.T) {
	tmp := t.TempDir()
	outside := filepath.Join(tmp, "outside")
	svc := NewService(Config{Role: RoleTeacher, ArchiveDir: filepath.Join(tmp, "archive"), ReceiveDir: filepath.Join(tmp, "recv")})
	data := []byte("evil")
	ack, _, err := sendRawOffer(t, svc, "sess-1", FileOffer{
		TaskID: "x", Direction: core.TransferSend, FileName: "evil.txt", RemoteDir: outside,
		TotalBytes: int64(len(data)), SHA256: sha256Hex(data),
	}, data)
	if ack.Accepted || !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("expected send offer rejected, ack=%+v err=%v", ack, err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "evil.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("file written outside archive: %v", statErr)
	}
	ack, _, err = sendRawOffer(t, svc, "sess-1", FileOffer{
		TaskID: "y", Direction: core.TransferCollect, FileName: "evil.txt", RemoteDir: outside, TotalBytes: 4,
	}, data)
	if ack.Accepted || !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("expected collect-direction offer rejected, ack=%+v err=%v", ack, err)
	}

	// Collect requests must be refused (teacher files are not readable by students).
	client, server := net.Pipe()
	errCh := make(chan error, 1)
	go func() { errCh <- svc.HandleStreamFrom(context.Background(), "sess-1", server) }()
	req, _ := json.Marshal(CollectRequest{TaskID: "c", RemoteDir: tmp})
	if err := protocol.WriteFrame(client, protocol.StreamFile, protocol.MsgCollectReq, req); err != nil {
		t.Fatalf("write collect: %v", err)
	}
	_, msgType, payload, err := protocol.ReadFrame(client)
	if err != nil || msgType != protocol.MsgFileComplete {
		t.Fatalf("expected MsgFileComplete refusal, type=%d err=%v", msgType, err)
	}
	var done FileComplete
	_ = json.Unmarshal(payload, &done)
	if done.Success {
		t.Fatalf("collect request should be refused")
	}
	_ = client.Close()
	if err := <-errCh; !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("expected ErrNotPermitted, got %v", err)
	}
}

func TestTeacherRoleSubmitUsesArchiveAndSessionIdentity(t *testing.T) {
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "archive")
	svc := NewService(Config{
		Role:       RoleTeacher,
		ArchiveDir: archive,
		SessionIdentity: func(sessionID string) (StudentMeta, bool) {
			if sessionID == "sess-real" {
				return StudentMeta{ID: "stu-real", StudentNo: "2026009"}, true
			}
			return StudentMeta{}, false
		},
	})
	data := []byte("homework")
	ack, final, err := sendRawOffer(t, svc, "sess-real", FileOffer{
		TaskID: "s", Direction: core.TransferSubmit, FileName: "../../hw.txt",
		RemoteDir: filepath.Join(tmp, "elsewhere"),
		StudentID: "victim", StudentNo: "2026001", StudentName: "Mallory",
		TotalBytes: int64(len(data)), SHA256: sha256Hex(data),
	}, data)
	if err != nil || !ack.Accepted || !final.Success {
		t.Fatalf("submit failed: ack=%+v final=%+v err=%v", ack, final, err)
	}
	want := filepath.Join(archive, "2026009_Mallory", "hw.txt")
	if final.SavedPath != want {
		t.Fatalf("saved path = %q, want %q", final.SavedPath, want)
	}
	if _, err := os.Stat(filepath.Join(tmp, "elsewhere")); !os.IsNotExist(err) {
		t.Fatalf("peer-supplied RemoteDir must be ignored")
	}
	if tasks := svc.ListTasks(); len(tasks) != 1 || tasks[0].StudentID != "stu-real" {
		t.Fatalf("task must carry session-bound student ID: %+v", tasks)
	}
}

func TestStudentRoleRejectsSubmitOffersButAcceptsSend(t *testing.T) {
	tmp := t.TempDir()
	svc := NewService(Config{Role: RoleStudent, ReceiveDir: tmp, ArchiveDir: tmp})
	data := []byte("x")
	ack, _, err := sendRawOffer(t, svc, "", FileOffer{TaskID: "a", Direction: core.TransferSubmit, FileName: "a.txt", TotalBytes: 1}, data)
	if ack.Accepted || !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("expected submit offer rejected on student, ack=%+v err=%v", ack, err)
	}
	dest := filepath.Join(tmp, "lesson")
	ack, final, err := sendRawOffer(t, svc, "", FileOffer{TaskID: "b", Direction: core.TransferSend, FileName: "b.txt", RemoteDir: dest, TotalBytes: 1, SHA256: sha256Hex(data)}, data)
	if err != nil || !ack.Accepted || final.SavedPath != filepath.Join(dest, "b.txt") {
		t.Fatalf("send offer should be accepted: ack=%+v final=%+v err=%v", ack, final, err)
	}
}

func TestUnspecifiedRoleKeepsLegacyBehaviour(t *testing.T) {
	tmp := t.TempDir()
	svc := NewService(Config{ReceiveDir: tmp})
	dest := filepath.Join(tmp, "anywhere")
	data := []byte("legacy")
	_, final, err := sendRawOffer(t, svc, "", FileOffer{TaskID: "l", Direction: core.TransferCollect, FileName: "l.txt", RemoteDir: dest, TotalBytes: int64(len(data))}, data)
	if err != nil || final.SavedPath != filepath.Join(dest, "l.txt") {
		t.Fatalf("legacy offer: final=%+v err=%v", final, err)
	}
}

func TestSanitizeFileNameAndSegments(t *testing.T) {
	cases := map[string]string{
		"":                  "unnamed.bin",
		".":                 "unnamed.bin",
		"..":                "unnamed.bin",
		"...":               "unnamed.bin",
		"   ":               "unnamed.bin",
		"/":                 "unnamed.bin",
		`..\..\evil.exe`:    "evil.exe",
		"../../etc/passwd":  "passwd",
		"report.docx:evil":  "report.docx_evil",
		"a\x00b\x1fc.txt":   "a_b_c.txt",
		"CON":               "_CON",
		"con.txt":           "_con.txt",
		"Nul.tar.gz":        "_Nul.tar.gz",
		"COM1.log":          "_COM1.log",
		"lpt9":              "_lpt9",
		"COM0.txt":          "COM0.txt",
		"CONSOLE.txt":       "CONSOLE.txt",
		"notes.txt. . ":     "notes.txt",
		"name.":             "name",
		`what?<>|*".txt`:    "what______.txt",
		"normal file.pdf":   "normal file.pdf",
		"作业 1.docx":         "作业 1.docx",
		"aux .txt":          "_aux .txt",
		"C:secret.txt":      "C_secret.txt",
		`dir\sub\file.txt`:  "file.txt",
		"a..b.txt":          "a_b.txt",
		"trailing space.  ": "trailing space",
	}
	for in, want := range cases {
		if got := sanitizeFileName(in); got != want {
			t.Errorf("sanitizeFileName(%q) = %q, want %q", in, got, want)
		}
	}
	folders := []struct{ no, name, id, want string }{
		{"..", "..", "..", "___"},
		{"", "", "", "unknown_student"},
		{".", " ", ". ", "unknown_student"},
		{"CON", "", "", "_CON"},
		{"2026:01", "A/B", "", "2026_01_A_B"},
		{"", "Bob.", "id1", "id1_Bob"},
		{"", "", "LPT1", "_LPT1"},
	}
	for _, f := range folders {
		got := formatStudentFolder(f.no, f.name, f.id)
		if got != f.want {
			t.Errorf("formatStudentFolder(%q,%q,%q) = %q, want %q", f.no, f.name, f.id, got, f.want)
		}
		if strings.ContainsAny(got, `/\:`) || got == "." || got == ".." {
			t.Errorf("unsafe folder %q", got)
		}
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

type countingCloser struct{ closed int }

func (c *countingCloser) Close() error { c.closed++; return nil }

func TestFlushAndCloseFileClosesEvenWhenFlushFails(t *testing.T) {
	bw := bufio.NewWriterSize(failingWriter{}, 16)
	_, _ = bw.Write([]byte("pending"))
	closer := &countingCloser{}
	err := flushAndCloseFile(bw, closer)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("expected flush error, got %v", err)
	}
	if closer.closed != 1 {
		t.Fatalf("file must be closed even when flush fails, closed=%d", closer.closed)
	}
}

func TestReceiveOfferRejectsShortCompleteWithoutHash(t *testing.T) {
	tmp := t.TempDir()
	dest := filepath.Join(tmp, "short.bin")
	svc := NewService(Config{ReceiveDir: tmp})
	offer := FileOffer{TaskID: "short", FileName: "short.bin", TotalBytes: 10}
	client, server := net.Pipe()
	defer client.Close()
	resultCh := make(chan error, 1)
	go func() {
		_, err := svc.receiveOffer(context.Background(), server, offer, dest, core.TransferSend)
		resultCh <- err
	}()
	if _, _, _, err := protocol.ReadFrame(client); err != nil {
		t.Fatalf("read ack: %v", err)
	}
	chunk := make([]byte, 8+4)
	copy(chunk[8:], "abcd")
	_ = protocol.WriteFrame(client, protocol.StreamFile, protocol.MsgFileChunk, chunk)
	done, _ := json.Marshal(FileComplete{TaskID: "short", DoneBytes: 4, Success: true})
	_ = protocol.WriteFrame(client, protocol.StreamFile, protocol.MsgFileComplete, done)
	_, _, finalPayload, err := protocol.ReadFrame(client)
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	var final FileComplete
	_ = json.Unmarshal(finalPayload, &final)
	if final.Success {
		t.Fatalf("short transfer must not succeed")
	}
	if err := <-resultCh; !errors.Is(err, ErrIncompleteTransfer) {
		t.Fatalf("expected ErrIncompleteTransfer, got %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("incomplete file must not be published: %v", err)
	}
}

func TestCancelTaskInterruptsBlockedReceive(t *testing.T) {
	tmp := t.TempDir()
	svc := NewService(Config{ReceiveDir: tmp})
	offer := FileOffer{TaskID: "blocked", FileName: "blocked.bin", TotalBytes: 100}
	client, server := net.Pipe()
	defer client.Close()
	resultCh := make(chan error, 1)
	go func() {
		_, err := svc.receiveOffer(context.Background(), server, offer, filepath.Join(tmp, "blocked.bin"), core.TransferSend)
		resultCh <- err
	}()
	if _, _, _, err := protocol.ReadFrame(client); err != nil {
		t.Fatalf("read ack: %v", err)
	}
	// Receiver is now blocked reading the next frame; nothing more is sent.
	tasks := svc.ListTasks()
	if len(tasks) != 1 {
		t.Fatalf("expected one task, got %d", len(tasks))
	}
	if err := svc.CancelTask(tasks[0].ID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	select {
	case err := <-resultCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("CancelTask did not interrupt blocked receive")
	}
	if got := svc.ListTasks()[0]; got.Status != "cancelled" {
		t.Fatalf("expected cancelled status, got %+v", got)
	}
}

func TestSendOverStreamCancelInterruptsBlockedIO(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.bin")
	if err := os.WriteFile(src, make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := NewService(Config{})
	client, server := net.Pipe()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan error, 1)
	go func() {
		_, err := svc.sendOverStream(ctx, client, src, FileOffer{TaskID: "c", FileName: "src.bin", TotalBytes: 1024}, nil)
		resultCh <- err
	}()
	// Read the offer but never ack: sender blocks on ReadFrame.
	if _, _, _, err := protocol.ReadFrame(server); err != nil {
		t.Fatalf("read offer: %v", err)
	}
	cancel()
	select {
	case err := <-resultCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not interrupt blocked send")
	}
	_, _ = io.Copy(io.Discard, server)
}
