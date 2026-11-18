package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nexlab/internal/control"
	"nexlab/internal/core"
	"nexlab/internal/exam"
	"nexlab/internal/filetransfer"
	"nexlab/internal/media"
	"nexlab/internal/protocol"
	"nexlab/internal/transport"
	"nexlab/internal/ui/student"
)

func freeUDPAddr(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freeUDPAddr: %v", err)
	}
	addr := pc.LocalAddr().String()
	_ = pc.Close()
	return addr
}

func TestEndToEndTeacherStudentKCPIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	teacherAddr := freeUDPAddr(t)
	mcastAddr := freeUDPAddr(t)

	trRuntime, err := NewRuntime("Lab-E2E", "teacher-e2e", "教师端", t.TempDir())
	if err != nil {
		t.Fatalf("NewRuntime teacher failed: %v", err)
	}
	defer trRuntime.Close()

	if err := trRuntime.Start(ctx, teacherAddr, "教师端"); err != nil {
		t.Fatalf("teacher Start failed: %v", err)
	}

	// Build a student node connecting directly via KCP to teacherAddr
	stTransport := transport.NewManager(
		transport.WithTeacherAddr(teacherAddr),
		transport.WithMulticastListenAddr(mcastAddr),
	)
	defer stTransport.Close()

	stMedia, err := media.NewManager(media.ManagerOptions{})
	if err != nil {
		t.Fatalf("student media init: %v", err)
	}
	stControl := control.NewManager(control.ManagerOptions{})
	defer stControl.Close()
	stExam := exam.NewService()
	stApp := student.New(student.Options{
		StudentID:   "stu-e2e-1",
		StudentNo:   "202688",
		Channel:     "Lab-E2E",
		Services: student.Services{
			Transport: stTransport,
			Media:     stMedia,
			Control:   stControl,
			Exam:      stExam,
		},
	})

	stTransport.OnSession(func(sess core.Session) {
		env, _ := protocol.NewEnvelope(protocol.MsgStudentRegister, "stu-e2e-1", core.StudentInfo{
			ID:          "stu-e2e-1",
			StudentNo:   "202688",
			MachineName: "PC-88",
			Channel:     "Lab-E2E",
		})
		_ = sess.SendEnvelope(env)
	})

	stTransport.OnEnvelope(func(_ core.Session, env protocol.Envelope) {
		switch env.Type {
		case protocol.MsgBroadcastStart:
			var cfg core.BroadcastConfig
			_ = json.Unmarshal(env.Payload, &cfg)
			stApp.StartBroadcast(cfg.Mode == core.BroadcastWindowed)
		case protocol.MsgBroadcastStop:
			stApp.ShowFloatingBar()
		case protocol.MsgScreenLock:
			var req struct {
				Locked  bool   `json:"locked"`
				Message string `json:"message"`
			}
			if json.Unmarshal(env.Payload, &req) == nil {
				stApp.SetLocked(req.Locked, req.Message)
			}
		case protocol.MsgExamPublish:
			var p core.ExamPaper
			if json.Unmarshal(env.Payload, &p) == nil {
				stExam.PublishPaper(p)
			}
		}
	})

	if err := stTransport.StartStudent(ctx, "Lab-E2E"); err != nil {
		t.Fatalf("student StartStudent failed: %v", err)
	}

	// Wait for teacher roster to receive student registration over KCP
	deadline := time.Now().Add(4 * time.Second)
	registered := false
	for time.Now().Before(deadline) {
		if len(trRuntime.Classroom.ListStudents()) > 0 {
			registered = true
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !registered {
		t.Fatalf("student failed to register on teacher roster over KCP")
	}

	// Teacher locks student screen over KCP
	trRuntime.App.ToggleScreenLock()
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !stApp.ScreenLocked() {
		time.Sleep(25 * time.Millisecond)
	}
	if !stApp.ScreenLocked() {
		t.Fatalf("expected student screen lock propagated over KCP")
	}

	// Teacher publishes exam -> student answers -> teacher grades
	paper := core.ExamPaper{
		ID:    "e2e-quiz",
		Title: "E2E KCP Quiz",
		Questions: []core.ExamQuestion{
			{ID: "q1", Kind: core.QuestionSingle, Prompt: "1+1=?", Options: []string{"1", "2"}, Answer: []string{"2"}, Points: 100},
		},
	}
	trRuntime.Exam.PublishPaper(paper)
	env, _ := protocol.NewEnvelope(protocol.MsgExamPublish, "teacher-e2e", paper)
	_ = trRuntime.Transport.BroadcastControl(env, nil)

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && stExam.GetActivePaper() == nil {
		time.Sleep(25 * time.Millisecond)
	}
	if stExam.GetActivePaper() == nil {
		t.Fatalf("student did not receive exam paper over KCP")
	}
	sub := stExam.SubmitAnswer(core.ExamSubmission{
		PaperID:   "e2e-quiz",
		StudentID: "stu-e2e-1",
		Answers:   map[string][]string{"q1": {"2"}},
	})
	subEnv, _ := protocol.NewEnvelope(protocol.MsgExamAnswer, "stu-e2e-1", sub)
	_ = stTransport.BroadcastControl(subEnv, nil)

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && trRuntime.Exam.ComputeStats("e2e-quiz").TotalSubmitted == 0 {
		time.Sleep(25 * time.Millisecond)
	}
	stats := trRuntime.Exam.ComputeStats("e2e-quiz")
	if stats.TotalSubmitted != 1 || stats.AverageScore != 100 {
		t.Fatalf("unexpected teacher exam stats: %+v", stats)
	}

	// Media capture + encode + decode smoke test
	frame, err := trRuntime.Media.CaptureScreen()
	if err != nil {
		t.Fatalf("teacher CaptureScreen failed: %v", err)
	}
	pkt, err := trRuntime.Media.EncodeFrame(frame, 75)
	if err != nil {
		t.Fatalf("teacher EncodeFrame failed: %v", err)
	}
	decoded, err := stMedia.DecodeFrame(pkt)
	if err != nil || decoded.Width != frame.Width || decoded.Height != frame.Height {
		t.Fatalf("student DecodeFrame failed: err=%v decoded=%+v", err, decoded)
	}

	// Monitor frame round-trip from student to teacher over KCP
	monFrame, err := stMedia.CaptureScreenMax(320, 180)
	if err != nil {
		t.Fatalf("student CaptureScreenMax failed: %v", err)
	}
	monBytes, err := stMedia.EncodeFrame(monFrame, 70)
	if err != nil {
		t.Fatalf("student EncodeFrame failed: %v", err)
	}
	monEnv, err := protocol.NewEnvelope(protocol.MsgVideoFrame, "stu-e2e-1", videoStreamPacket{
		Kind: "monitor",
		Data: monBytes,
	})
	if err != nil {
		t.Fatalf("NewEnvelope MsgVideoFrame failed: %v", err)
	}
	if err := stTransport.BroadcastControl(monEnv, nil); err != nil {
		t.Fatalf("BroadcastControl MsgVideoFrame failed: %v", err)
	}

	// File distribution over real KCP StreamFile
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "lab_handout.txt")
	fileBytes := []byte("NexLab KCP real file transfer verification payload\n")
	if err := os.WriteFile(srcFile, fileBytes, 0o644); err != nil {
		t.Fatalf("write srcFile: %v", err)
	}
	stFiles := filetransfer.NewService(filetransfer.Config{
		ReceiveDir: dstDir,
		ArchiveDir: dstDir,
	})
	stSess := stTransport.Sessions()[0]
	stFiles.SetTeacherSession(stSess)
	go func() {
		type accepter interface {
			AcceptStream() (protocol.StreamKind, io.ReadWriteCloser, error)
		}
		ac, ok := stSess.(accepter)
		if !ok {
			return
		}
		for {
			kind, stream, err := ac.AcceptStream()
			if err != nil {
				return
			}
			if kind == protocol.StreamFile {
				go func() { _ = stFiles.HandleStream(ctx, stream) }()
			} else {
				_ = stream.Close()
			}
		}
	}()

	task, err := trRuntime.Files.DistributeFile(ctx, srcFile, "", []string{"stu-e2e-1"}, false)
	if err != nil {
		t.Fatalf("DistributeFile over KCP failed: %v", err)
	}
	if task.Status != "completed" {
		t.Fatalf("expected completed task, got %+v", task)
	}
	gotFile, err := os.ReadFile(filepath.Join(dstDir, "lab_handout.txt"))
	if err != nil {
		t.Fatalf("read received file: %v", err)
	}
	sumA := sha256.Sum256(fileBytes)
	sumB := sha256.Sum256(gotFile)
	if hex.EncodeToString(sumA[:]) != hex.EncodeToString(sumB[:]) {
		t.Fatalf("received file SHA-256 mismatch")
	}
}

func TestEndToEndScreenBroadcastOverKCPFallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	teacherAddr := freeUDPAddr(t)
	trRuntime, err := NewRuntime("Lab-Bcast", "teacher-bcast", "教师端", t.TempDir())
	if err != nil {
		t.Fatalf("NewRuntime teacher failed: %v", err)
	}
	defer trRuntime.Close()

	if err := trRuntime.Start(ctx, teacherAddr, "教师端"); err != nil {
		t.Fatalf("teacher Start failed: %v", err)
	}

	// Connect student using --teacher explicit address (no WithMulticastListenAddr, no discovery),
	// matching `nexlab-student.exe --teacher 127.0.0.1:39100`.
	stTransport := transport.NewManager(
		transport.WithDiscoveryConfig(transport.DiscoveryConfig{Port: 0}),
		transport.WithTeacherAddr(teacherAddr),
	)
	defer stTransport.Close()

	stMedia, err := media.NewManager(media.ManagerOptions{PreferH264MFT: false})
	if err != nil {
		t.Fatalf("student media init: %v", err)
	}
	defer stMedia.Close()

	stApp := student.New(student.Options{
		StudentID: "stu-bcast-1",
		StudentNo: "202601",
		Channel:   "Lab-Bcast",
		Services: student.Services{
			Transport: stTransport,
			Media:     stMedia,
		},
	})

	stTransport.OnSession(func(sess core.Session) {
		env, _ := protocol.NewEnvelope(protocol.MsgStudentRegister, "stu-bcast-1", core.StudentInfo{
			ID:          "stu-bcast-1",
			StudentNo:   "202601",
			MachineName: "PC-BCAST",
			Channel:     "Lab-Bcast",
		})
		_ = sess.SendEnvelope(env)
	})

	stTransport.OnMulticastData(func(data []byte) {
		if frame, err := stMedia.DecodeFrame(data); err == nil && frame != nil {
			stApp.UpdateVideoFrame(frame)
		}
	})

	stTransport.OnEnvelope(func(_ core.Session, env protocol.Envelope) {
		switch env.Type {
		case protocol.MsgBroadcastStart:
			var cfg core.BroadcastConfig
			_ = json.Unmarshal(env.Payload, &cfg)
			stApp.StartBroadcast(cfg.Mode == core.BroadcastWindowed)
		case protocol.MsgBroadcastStop:
			stApp.ShowFloatingBar()
		}
	})

	if err := stTransport.StartStudent(ctx, "Lab-Bcast"); err != nil {
		t.Fatalf("student StartStudent failed: %v", err)
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && len(trRuntime.Classroom.ListStudents()) == 0 {
		time.Sleep(25 * time.Millisecond)
	}
	if len(trRuntime.Classroom.ListStudents()) == 0 {
		t.Fatalf("student failed to register on teacher")
	}

	// Start teacher screen broadcast -> student must switch to ModeBroadcast and decode a real frame
	trRuntime.App.ToggleBroadcast()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if stApp.Mode() == student.ModeBroadcast && stApp.HasVideoFrame() {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if stApp.Mode() != student.ModeBroadcast {
		t.Fatalf("expected student ModeBroadcast (%d), got %d", student.ModeBroadcast, stApp.Mode())
	}
	if !stApp.HasVideoFrame() {
		t.Fatalf("expected student to receive and decode real broadcast video frame")
	}

	// Stop teacher screen broadcast -> student returns to floating bar
	trRuntime.App.ToggleBroadcast()
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && stApp.Mode() != student.ModeBar {
		time.Sleep(25 * time.Millisecond)
	}
	if stApp.Mode() != student.ModeBar {
		t.Fatalf("expected student ModeBar (%d) after broadcast stop, got %d", student.ModeBar, stApp.Mode())
	}
}
