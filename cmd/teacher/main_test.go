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

func TestEndToEndBroadcastBurstLatestFrameOverKCP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	teacherAddr := freeUDPAddr(t)
	trRuntime, err := NewRuntime("Lab-Burst", "teacher-burst", "教师端", t.TempDir())
	if err != nil {
		t.Fatalf("NewRuntime teacher failed: %v", err)
	}
	defer trRuntime.Close()

	if err := trRuntime.Start(ctx, teacherAddr, "教师端"); err != nil {
		t.Fatalf("teacher Start failed: %v", err)
	}

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

	receivedPayloads := make(chan []byte, 64)
	stTransport.OnSession(func(sess core.Session) {
		env, _ := protocol.NewEnvelope(protocol.MsgStudentRegister, "stu-burst-1", core.StudentInfo{
			ID:          "stu-burst-1",
			StudentNo:   "202699",
			MachineName: "PC-BURST",
			Channel:     "Lab-Burst",
		})
		_ = sess.SendEnvelope(env)
	})
	stTransport.OnMulticastData(func(data []byte) {
		cp := make([]byte, len(data))
		copy(cp, data)
		select {
		case receivedPayloads <- cp:
		default:
		}
	})

	if err := stTransport.StartStudent(ctx, "Lab-Burst"); err != nil {
		t.Fatalf("student StartStudent failed: %v", err)
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && len(trRuntime.Classroom.ListStudents()) == 0 {
		time.Sleep(25 * time.Millisecond)
	}
	if len(trRuntime.Classroom.ListStudents()) == 0 {
		t.Fatalf("student failed to register on teacher")
	}

	// Prepare a valid encoded video frame and send a rapid burst with unique markers.
	baseFrame, err := trRuntime.Media.CaptureScreenMax(320, 180)
	if err != nil {
		t.Fatalf("CaptureScreenMax failed: %v", err)
	}
	encoded, err := trRuntime.Media.EncodeFrame(baseFrame, 70)
	if err != nil {
		t.Fatalf("EncodeFrame failed: %v", err)
	}

	const burstCount = 24
	const markerPrefix = "BURST-SEQ-"
	start := time.Now()
	for i := 1; i <= burstCount; i++ {
		pkt := make([]byte, len(encoded)+14)
		copy(pkt, encoded)
		copy(pkt[len(encoded):], []byte(markerPrefix))
		pkt[len(encoded)+10] = byte(i / 10 + '0')
		pkt[len(encoded)+11] = byte(i % 10 + '0')
		pkt[len(encoded)+12] = '!'
		pkt[len(encoded)+13] = '!'
		if err := trRuntime.Transport.BroadcastMulticast(pkt); err != nil {
			t.Fatalf("BroadcastMulticast frame %d failed: %v", i, err)
		}
	}
	sendElapsed := time.Since(start)
	if sendElapsed > 500*time.Millisecond {
		t.Fatalf("BroadcastMulticast burst stalled for %v", sendElapsed)
	}

	// Verify the latest frame in the burst arrives promptly without stalling.
	wantSuffix := []byte("BURST-SEQ-24!!")
	gotLatest := false
	gotCount := 0
	deadline = time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && !gotLatest {
		select {
		case pkt := <-receivedPayloads:
			gotCount++
			if len(pkt) >= len(wantSuffix) && string(pkt[len(pkt)-len(wantSuffix):]) == string(wantSuffix) {
				gotLatest = true
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	if !gotLatest {
		t.Fatalf("did not receive latest burst frame (%s) within timeout; received %d frames", wantSuffix, gotCount)
	}

	// Ensure the underlying encoded frame prefix remains decodable by the student media manager.
	decoded, err := stMedia.DecodeFrame(encoded)
	if err != nil || decoded == nil || decoded.Width <= 0 || decoded.Height <= 0 {
		t.Fatalf("student DecodeFrame failed: err=%v decoded=%+v", err, decoded)
	}
}

func TestMonitorDecodeErrorResetsDecoderRequestsRestartAndTouchesStudent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	teacherAddr := freeUDPAddr(t)
	trRuntime, err := NewRuntime("Lab-MonErr", "teacher-monerr", "教师端", t.TempDir())
	if err != nil {
		t.Fatalf("NewRuntime teacher failed: %v", err)
	}
	defer trRuntime.Close()

	if err := trRuntime.Start(ctx, teacherAddr, "教师端"); err != nil {
		t.Fatalf("teacher Start failed: %v", err)
	}

	stTransport := transport.NewManager(
		transport.WithDiscoveryConfig(transport.DiscoveryConfig{Port: 0}),
		transport.WithTeacherAddr(teacherAddr),
	)
	defer stTransport.Close()

	monitorCtrlCh := make(chan bool, 8)
	stTransport.OnSession(func(sess core.Session) {
		env, _ := protocol.NewEnvelope(protocol.MsgStudentRegister, "stu-mon-1", core.StudentInfo{
			ID:          "stu-mon-1",
			StudentNo:   "202601",
			MachineName: "PC-MON",
			Channel:     "Lab-MonErr",
		})
		_ = sess.SendEnvelope(env)
	})
	stTransport.OnEnvelope(func(_ core.Session, env protocol.Envelope) {
		if env.Type == protocol.MsgMonitorControl {
			var req struct {
				Active bool `json:"active"`
			}
			if json.Unmarshal(env.Payload, &req) == nil {
				select {
				case monitorCtrlCh <- req.Active:
				default:
				}
			}
		}
	})

	if err := stTransport.StartStudent(ctx, "Lab-MonErr"); err != nil {
		t.Fatalf("student StartStudent failed: %v", err)
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && len(trRuntime.Classroom.ListStudents()) == 0 {
		time.Sleep(25 * time.Millisecond)
	}
	if len(trRuntime.Classroom.ListStudents()) == 0 {
		t.Fatalf("student failed to register on teacher")
	}

	// Enable monitoring on teacher app.
	trRuntime.App.ToggleMonitor()
	select {
	case active := <-monitorCtrlCh:
		if !active {
			t.Fatalf("expected initial monitor control active=true")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for initial MsgMonitorControl")
	}

	// Mark student offline first so we can verify TouchStudent brings it back online even on decode error.
	if err := trRuntime.Classroom.SetStudentStatus("stu-mon-1", core.StatusOffline); err != nil {
		t.Fatalf("SetStudentStatus offline failed: %v", err)
	}

	// Seed a decoder entry for stu-mon-1:monitor and send a corrupted/delta frame.
	validCodec, err := media.NewHybridVideoCodec(false)
	if err != nil {
		t.Fatalf("NewHybridVideoCodec failed: %v", err)
	}
	trRuntime.decMu.Lock()
	trRuntime.studentDecoders["stu-mon-1:monitor"] = validCodec
	trRuntime.decMu.Unlock()

	badEnv, err := protocol.NewEnvelope(protocol.MsgVideoFrame, "stu-mon-1", videoStreamPacket{
		Kind: "monitor",
		Data: []byte("corrupted-delta-frame"),
	})
	if err != nil {
		t.Fatalf("NewEnvelope bad frame failed: %v", err)
	}
	badEnv.Channel = "Lab-MonErr"
	if err := stTransport.BroadcastControl(badEnv, nil); err != nil {
		t.Fatalf("BroadcastControl bad frame failed: %v", err)
	}

	// Expect teacher to send MsgMonitorControl(active=true) to restart stream/keyframe and mark student online.
	select {
	case active := <-monitorCtrlCh:
		if !active {
			t.Fatalf("expected recovery MsgMonitorControl active=true")
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for recovery MsgMonitorControl after decode error")
	}

	st, ok := trRuntime.Classroom.GetStudent("stu-mon-1")
	if !ok {
		t.Fatalf("student stu-mon-1 not found")
	}
	if st.Status != core.StatusOnline {
		t.Fatalf("expected stu-mon-1 status online after MsgVideoFrame, got %v", st.Status)
	}

	trRuntime.decMu.Lock()
	_, stillPresent := trRuntime.studentDecoders["stu-mon-1:monitor"]
	trRuntime.decMu.Unlock()
	if stillPresent {
		t.Fatalf("expected stale decoder stu-mon-1:monitor to be reset/removed after decode error")
	}

	// Also verify re-registering resets any existing monitor decoder.
	reCodec, err := media.NewHybridVideoCodec(false)
	if err != nil {
		t.Fatalf("NewHybridVideoCodec failed: %v", err)
	}
	trRuntime.decMu.Lock()
	trRuntime.studentDecoders["stu-mon-1:monitor"] = reCodec
	trRuntime.decMu.Unlock()

	regEnv, _ := protocol.NewEnvelope(protocol.MsgStudentRegister, "stu-mon-1", core.StudentInfo{
		ID:          "stu-mon-1",
		StudentNo:   "202601",
		MachineName: "PC-MON",
		Channel:     "Lab-MonErr",
	})
	if err := stTransport.BroadcastControl(regEnv, nil); err != nil {
		t.Fatalf("BroadcastControl re-register failed: %v", err)
	}

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		trRuntime.decMu.Lock()
		_, exists := trRuntime.studentDecoders["stu-mon-1:monitor"]
		trRuntime.decMu.Unlock()
		if !exists {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("expected stu-mon-1:monitor decoder to be reset on MsgStudentRegister")
}
