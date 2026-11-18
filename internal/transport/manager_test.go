package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"nexlab/internal/core"
	"nexlab/internal/protocol"
)

func freeUDPPort(t *testing.T) int {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket free port: %v", err)
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	_ = pc.Close()
	return port
}

func TestManagerDiscoveryAndEnvelopeDelivery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	discPort := freeUDPPort(t)
	discCfg := DiscoveryConfig{
		Port:          discPort,
		BroadcastAddr: fmt.Sprintf("127.0.0.1:%d", discPort),
		Interval:      80 * time.Millisecond,
	}

	teacher := NewManager(WithDiscoveryConfig(discCfg))
	defer teacher.Close()

	teacherSessionCh := make(chan core.Session, 2)
	teacherEnvCh := make(chan protocol.Envelope, 8)
	teacher.OnSession(func(s core.Session) {
		teacherSessionCh <- s
	})
	teacher.OnEnvelope(func(_ core.Session, env protocol.Envelope) {
		teacherEnvCh <- env
	})

	beacon := protocol.DiscoveryBeacon{
		Role:        "teacher",
		TeacherID:   "teacher-01",
		TeacherName: "Ms. Lin",
		Channel:     "Lab-302",
		Version:     "1.0.0",
	}
	if err := teacher.StartTeacher(ctx, "127.0.0.1:0", beacon); err != nil {
		t.Fatalf("StartTeacher: %v", err)
	}

	student := NewManager(WithDiscoveryConfig(discCfg))
	defer student.Close()

	studentEnvCh := make(chan protocol.Envelope, 8)
	student.OnEnvelope(func(_ core.Session, env protocol.Envelope) {
		studentEnvCh <- env
	})

	if err := student.StartStudent(ctx, "Lab-302"); err != nil {
		t.Fatalf("StartStudent: %v", err)
	}

	// Student sends registration envelope to Teacher
	regEnv, err := protocol.NewEnvelope(protocol.MsgStudentRegister, "stu-42", core.StudentInfo{
		ID:   "stu-42",
		Name: "Bob",
	})
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	if err := student.SendTo("", regEnv); err != nil {
		t.Fatalf("student.SendTo: %v", err)
	}

	select {
	case <-teacherSessionCh:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for teacher OnSession")
	}

	select {
	case got := <-teacherEnvCh:
		if got.Type != protocol.MsgStudentRegister || got.SenderID != "stu-42" {
			t.Fatalf("unexpected envelope on teacher: %+v", got)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for teacher OnEnvelope")
	}

	// Teacher sends targeted envelope back using student alias "stu-42"
	chatEnv, err := protocol.NewEnvelope(protocol.MsgChat, "teacher-01", core.ChatMessage{
		ID:   "m-1",
		Text: "Welcome to Lab 302!",
	})
	if err != nil {
		t.Fatalf("NewEnvelope chat: %v", err)
	}
	if err := teacher.SendTo("stu-42", chatEnv); err != nil {
		t.Fatalf("teacher.SendTo(stu-42): %v", err)
	}

	select {
	case got := <-studentEnvCh:
		if got.Type != protocol.MsgChat || got.SenderID != "teacher-01" {
			t.Fatalf("unexpected envelope on student: %+v", got)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for student OnEnvelope")
	}

	// BroadcastControl to all
	bcastEnv, _ := protocol.NewEnvelope(protocol.MsgCheckInStart, "teacher-01", nil)
	if err := teacher.BroadcastControl(bcastEnv, nil); err != nil {
		t.Fatalf("BroadcastControl: %v", err)
	}
	select {
	case got := <-studentEnvCh:
		if got.Type != protocol.MsgCheckInStart {
			t.Fatalf("expected MsgCheckInStart, got %v", got.Type)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for BroadcastControl")
	}

	time.Sleep(120 * time.Millisecond)
	stats := teacher.Stats()
	if stats.ActiveSessions != 1 {
		t.Fatalf("expected 1 active session on teacher, got %d", stats.ActiveSessions)
	}
}

func TestManagerMulticastFECAndEncryptedDelivery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	discPort := freeUDPPort(t)
	mcastPort := freeUDPPort(t)
	key := make([]byte, 32)
	_, _ = rand.Read(key)

	discCfg := DiscoveryConfig{
		Port:          discPort,
		BroadcastAddr: fmt.Sprintf("127.0.0.1:%d", discPort),
		Interval:      80 * time.Millisecond,
	}

	teacher := NewManager(
		WithDiscoveryConfig(discCfg),
		WithMulticastFEC(4, 2),
		WithCipherKey(key),
	)
	defer teacher.Close()

	beacon := protocol.DiscoveryBeacon{
		Role:        "teacher",
		TeacherID:   "teacher-fec",
		Channel:     "Lab-FEC",
		MulticastIP: "127.0.0.1",
		McastPort:   mcastPort,
		Version:     "1.0.0",
	}
	if err := teacher.StartTeacher(ctx, "127.0.0.1:0", beacon); err != nil {
		t.Fatalf("StartTeacher: %v", err)
	}

	student := NewManager(
		WithDiscoveryConfig(discCfg),
		WithMulticastFEC(4, 2),
		WithCipherKey(key),
		WithMulticastListenAddr(fmt.Sprintf("127.0.0.1:%d", mcastPort)),
	)
	defer student.Close()

	framesCh := make(chan []byte, 8)
	student.OnMulticastData(func(frame []byte) {
		cp := make([]byte, len(frame))
		copy(cp, frame)
		framesCh <- cp
	})

	if err := student.StartStudent(ctx, "Lab-FEC"); err != nil {
		t.Fatalf("StartStudent: %v", err)
	}

	// Send 3 multi-KB video frames over FEC+AES-GCM UDP stream
	for i := 0; i < 3; i++ {
		frame := make([]byte, 6000+i*1200)
		_, _ = rand.Read(frame)
		if err := teacher.BroadcastMulticast(frame); err != nil {
			t.Fatalf("BroadcastMulticast(%d): %v", i, err)
		}
		select {
		case got := <-framesCh:
			if !bytes.Equal(got, frame) {
				t.Fatalf("frame %d payload mismatch: got %d want %d", i, len(got), len(frame))
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for multicast FEC frame %d", i)
		}
	}
}

func TestManagerMulticastNACKFallbackAndPureUnicastFallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Set up a UDP proxy between teacher multicast output and student multicast input
	// that intentionally drops frame #2 so student triggers KCP MsgMediaNACK retransmission!
	proxyPort := freeUDPPort(t)
	studentMcastPort := freeUDPPort(t)

	proxyConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: proxyPort})
	if err != nil {
		t.Fatalf("proxy ListenUDP: %v", err)
	}
	defer proxyConn.Close()

	studentTarget := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: studentMcastPort}
	go func() {
		buf := make([]byte, 65535)
		for {
			n, _, err := proxyConn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n >= mcastHeaderSize {
				// Drop all chunks of seq == 2 and seq == 4 to force NACK fallback over KCP
				seq := uint64(buf[2])<<56 | uint64(buf[3])<<48 | uint64(buf[4])<<40 | uint64(buf[5])<<32 |
					uint64(buf[6])<<24 | uint64(buf[7])<<16 | uint64(buf[8])<<8 | uint64(buf[9])
				if seq == 2 || seq == 4 {
					continue
				}
			}
			_, _ = proxyConn.WriteToUDP(buf[:n], studentTarget)
		}
	}()

	teacher := NewManager(WithDiscoveryConfig(DiscoveryConfig{Port: 0}))
	defer teacher.Close()

	teacherConnected := make(chan struct{}, 1)
	teacher.OnSession(func(_ core.Session) {
		select {
		case teacherConnected <- struct{}{}:
		default:
		}
	})

	beacon := protocol.DiscoveryBeacon{
		Role:        "teacher",
		TeacherID:   "teacher-nack",
		Channel:     "Lab-NACK",
		MulticastIP: "127.0.0.1",
		McastPort:   proxyPort,
		Version:     "1.0.0",
	}
	if err := teacher.StartTeacher(ctx, "127.0.0.1:0", beacon); err != nil {
		t.Fatalf("StartTeacher: %v", err)
	}

	student := NewManager(
		WithDiscoveryConfig(DiscoveryConfig{Port: 0}),
		WithTeacherAddr(teacher.listener.Addr().String()),
		WithMulticastListenAddr(fmt.Sprintf("127.0.0.1:%d", studentMcastPort)),
	)
	defer student.Close()

	var mu sync.Mutex
	receivedMap := make(map[string]bool)
	allDone := make(chan struct{})
	const totalFrames = 6

	student.OnMulticastData(func(frame []byte) {
		mu.Lock()
		receivedMap[string(frame)] = true
		count := len(receivedMap)
		mu.Unlock()
		if count == totalFrames {
			select {
			case allDone <- struct{}{}:
			default:
			}
		}
	})

	if err := student.StartStudent(ctx, "Lab-NACK"); err != nil {
		t.Fatalf("StartStudent: %v", err)
	}

	// Trigger KCP handshake so session is established on teacher before multicast test
	pingEnv, _ := protocol.NewEnvelope(protocol.MsgPing, "stu-nack", nil)
	if err := student.SendTo("", pingEnv); err != nil {
		t.Fatalf("student ping: %v", err)
	}
	select {
	case <-teacherConnected:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for KCP session before multicast")
	}

	expectedFrames := make([]string, totalFrames)
	for i := 0; i < totalFrames; i++ {
		// Multi-chunk frame (> 1200 bytes)
		tag := fmt.Sprintf("FRAME-%d-%s", i+1, string(bytes.Repeat([]byte{'X'}, 2600)))
		expectedFrames[i] = tag
		if err := teacher.BroadcastMulticast([]byte(tag)); err != nil {
			t.Fatalf("BroadcastMulticast(%d): %v", i+1, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case <-allDone:
	case <-ctx.Done():
		mu.Lock()
		gotCount := len(receivedMap)
		mu.Unlock()
		t.Fatalf("timed out waiting for NACK fallback recovery: got %d/%d frames", gotCount, totalFrames)
	}

	mu.Lock()
	for _, exp := range expectedFrames {
		if !receivedMap[exp] {
			t.Fatalf("missing expected frame prefix %q", exp[:10])
		}
	}
	mu.Unlock()

	// Also verify pure unicast fallback when multicast is not configured
	teacherNoMcast := NewManager(WithDiscoveryConfig(DiscoveryConfig{Port: 0}))
	defer teacherNoMcast.Close()

	tSessCh := make(chan struct{}, 1)
	teacherNoMcast.OnSession(func(_ core.Session) {
		select {
		case tSessCh <- struct{}{}:
		default:
		}
	})
	if err := teacherNoMcast.StartTeacher(ctx, "127.0.0.1:0", protocol.DiscoveryBeacon{
		Role:      "teacher",
		TeacherID: "t-unicast",
		Channel:   "Lab-Unicast",
	}); err != nil {
		t.Fatalf("StartTeacher unicast: %v", err)
	}

	studentNoMcast := NewManager(
		WithDiscoveryConfig(DiscoveryConfig{Port: 0}),
		WithTeacherAddr(teacherNoMcast.listener.Addr().String()),
	)
	defer studentNoMcast.Close()

	unicastFrameCh := make(chan []byte, 2)
	studentNoMcast.OnMulticastData(func(frame []byte) {
		cp := make([]byte, len(frame))
		copy(cp, frame)
		unicastFrameCh <- cp
	})
	if err := studentNoMcast.StartStudent(ctx, "Lab-Unicast"); err != nil {
		t.Fatalf("StartStudent unicast: %v", err)
	}
	_ = studentNoMcast.SendTo("", pingEnv)
	select {
	case <-tSessCh:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for unicast teacher session")
	}

	wantPayload := []byte("unicast-fallback-media-frame")
	if err := teacherNoMcast.BroadcastMulticast(wantPayload); err != nil {
		t.Fatalf("BroadcastMulticast unicast: %v", err)
	}
	select {
	case got := <-unicastFrameCh:
		if !bytes.Equal(got, wantPayload) {
			t.Fatalf("unicast fallback payload mismatch: got %q want %q", got, wantPayload)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for unicast fallback media")
	}
}

func TestControlEnvelopeNotBlockedByMediaBurst(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	teacher := NewManager(WithDiscoveryConfig(DiscoveryConfig{Port: 0}))
	defer teacher.Close()

	tSessCh := make(chan struct{}, 1)
	teacher.OnSession(func(_ core.Session) {
		select {
		case tSessCh <- struct{}{}:
		default:
		}
	})
	if err := teacher.StartTeacher(ctx, "127.0.0.1:0", protocol.DiscoveryBeacon{
		Role:      "teacher",
		TeacherID: "t-hol",
		Channel:   "Lab-HOL",
	}); err != nil {
		t.Fatalf("StartTeacher: %v", err)
	}

	student := NewManager(
		WithDiscoveryConfig(DiscoveryConfig{Port: 0}),
		WithTeacherAddr(teacher.listener.Addr().String()),
	)
	defer student.Close()

	ctrlCh := make(chan protocol.MsgType, 8)
	student.OnEnvelope(func(_ core.Session, env protocol.Envelope) {
		ctrlCh <- env.Type
	})
	if err := student.StartStudent(ctx, "Lab-HOL"); err != nil {
		t.Fatalf("StartStudent: %v", err)
	}
	pingEnv, _ := protocol.NewEnvelope(protocol.MsgPing, "stu-hol", nil)
	if err := student.SendTo("", pingEnv); err != nil {
		t.Fatalf("student ping: %v", err)
	}
	select {
	case <-tSessCh:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for session")
	}

	// Flood large video fallback frames rapidly; control envelope must still enqueue & deliver promptly.
	bigFrame := make([]byte, 256*1024)
	for i := 0; i < 20; i++ {
		_ = teacher.BroadcastMulticast(bigFrame)
	}

	stopEnv, _ := protocol.NewEnvelope(protocol.MsgBroadcastStop, "t-hol", nil)
	start := time.Now()
	if err := teacher.BroadcastControl(stopEnv, nil); err != nil {
		t.Fatalf("BroadcastControl stop: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("BroadcastControl took too long under media burst: %v", elapsed)
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case mt := <-ctrlCh:
			if mt == protocol.MsgBroadcastStop {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for MsgBroadcastStop during media burst")
		}
	}
}

func TestStudentAutoReconnectOnSessionDrop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	teacher := NewManager(WithDiscoveryConfig(DiscoveryConfig{Port: 0}))
	defer teacher.Close()
	tSessCh := make(chan core.Session, 4)
	teacher.OnSession(func(s core.Session) {
		tSessCh <- s
	})
	if err := teacher.StartTeacher(ctx, "127.0.0.1:0", protocol.DiscoveryBeacon{Channel: "Lab-Reconn", KCPPort: 1}); err != nil {
		t.Fatalf("StartTeacher: %v", err)
	}

	student := NewManager(
		WithDiscoveryConfig(DiscoveryConfig{Port: 0}),
		WithTeacherAddr(teacher.listener.Addr().String()),
	)
	defer student.Close()
	sSessCh := make(chan core.Session, 4)
	student.OnSession(func(s core.Session) {
		sSessCh <- s
	})
	if err := student.StartStudent(ctx, "Lab-Reconn"); err != nil {
		t.Fatalf("StartStudent: %v", err)
	}
	pingEnv, _ := protocol.NewEnvelope(protocol.MsgPing, "stu-reconn", nil)
	if err := student.SendTo("", pingEnv); err != nil {
		t.Fatalf("initial student ping: %v", err)
	}

	select {
	case <-tSessCh:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for initial teacher session")
	}
	var firstStudentSess core.Session
	select {
	case firstStudentSess = <-sSessCh:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for initial student session")
	}

	// Simulate unexpected session drop on student side.
	_ = firstStudentSess.Close()

	// Student should automatically reconnect and invoke OnSession again.
	select {
	case <-sSessCh:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for student auto-reconnect")
	}

	ping2, _ := protocol.NewEnvelope(protocol.MsgPing, "stu-reconn", nil)
	if err := student.SendTo("", ping2); err != nil {
		t.Fatalf("reconnected student ping: %v", err)
	}
	select {
	case <-tSessCh:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for reconnected teacher session")
	}
}
