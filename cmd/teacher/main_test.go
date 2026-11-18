package main

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"nexlab/internal/control"
	"nexlab/internal/core"
	"nexlab/internal/exam"
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
}
