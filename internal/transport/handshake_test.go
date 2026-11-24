package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"nexlab/internal/core"
	"nexlab/internal/protocol"
)

func runHandshakePair(t *testing.T, id *TeacherIdentity, trust TeacherTrust) (net.Conn, net.Conn, error, error) {
	t.Helper()
	clientRaw, serverRaw := net.Pipe()
	type result struct {
		conn net.Conn
		err  error
	}
	serverCh := make(chan result, 1)
	go func() {
		c, err := ServerHandshake(serverRaw, id)
		if err != nil {
			_ = serverRaw.Close()
		}
		serverCh <- result{c, err}
	}()
	client, _, clientErr := ClientHandshake(clientRaw, trust)
	if clientErr != nil {
		_ = clientRaw.Close()
	}
	sr := <-serverCh
	return client, sr.conn, clientErr, sr.err
}

func TestHandshakeEncryptsAndPinsTeacher(t *testing.T) {
	id, err := GenerateTeacherIdentity()
	if err != nil {
		t.Fatal(err)
	}
	trust := NewMemoryTrust("")
	client, server, cErr, sErr := runHandshakePair(t, id, trust)
	if cErr != nil || sErr != nil {
		t.Fatalf("handshake: client=%v server=%v", cErr, sErr)
	}
	if trust.Pinned() != id.Fingerprint() {
		t.Fatalf("pinned %q, want %q", trust.Pinned(), id.Fingerprint())
	}
	payload := bytes.Repeat([]byte("matrixclass"), 4000)
	go func() { _, _ = client.Write(payload) }()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(server, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("payload mismatch")
	}
	go func() { _, _ = server.Write([]byte("ack")) }()
	ack := make([]byte, 3)
	if _, err := io.ReadFull(client, ack); err != nil || string(ack) != "ack" {
		t.Fatalf("reverse direction: %q %v", ack, err)
	}
}

func TestHandshakeRejectsImpostorTeacher(t *testing.T) {
	real, _ := GenerateTeacherIdentity()
	impostor, _ := GenerateTeacherIdentity()
	trust := NewMemoryTrust(real.Fingerprint())
	_, _, cErr, _ := runHandshakePair(t, impostor, trust)
	if !errors.Is(cErr, ErrUntrustedTeacher) {
		t.Fatalf("client err = %v, want ErrUntrustedTeacher", cErr)
	}
}

func TestSecureConnRejectsTamperedRecord(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	var wire bytes.Buffer
	writer, _ := newSecureConn(&bufConn{w: &wire}, key, key)
	if _, err := writer.Write([]byte("lock screen")); err != nil {
		t.Fatal(err)
	}
	raw := wire.Bytes()
	raw[len(raw)-1] ^= 0xFF
	reader, _ := newSecureConn(&bufConn{r: bytes.NewReader(raw)}, key, key)
	if _, err := reader.Read(make([]byte, 64)); err == nil {
		t.Fatal("expected tampered record to fail")
	}
}

func TestBeaconSignatureAndFilter(t *testing.T) {
	id, _ := GenerateTeacherIdentity()
	other, _ := GenerateTeacherIdentity()
	signed := SignBeacon(id, protocol.DiscoveryBeacon{Role: "teacher", Channel: "3", KCPPort: 39100})
	if _, err := VerifyBeacon(signed); err != nil {
		t.Fatalf("verify: %v", err)
	}
	forged := signed
	forged.KCPPort = 40000
	if _, err := VerifyBeacon(forged); err == nil {
		t.Fatal("modified beacon must fail verification")
	}
	filter := trustedBeaconFilter(NewMemoryTrust(id.Fingerprint()))
	if !filter(signed) {
		t.Fatal("trusted beacon rejected")
	}
	if filter(SignBeacon(other, protocol.DiscoveryBeacon{Role: "teacher", Channel: "3"})) {
		t.Fatal("beacon from another teacher accepted")
	}
	if filter(protocol.DiscoveryBeacon{Role: "teacher", Channel: "3"}) {
		t.Fatal("unsigned beacon accepted")
	}
}

func TestTeacherIdentityAndFileTrustPersist(t *testing.T) {
	dir := t.TempDir()
	idPath := filepath.Join(dir, "teacher.key")
	first, err := LoadOrCreateTeacherIdentity(idPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateTeacherIdentity(idPath)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint() != second.Fingerprint() {
		t.Fatal("identity not persisted")
	}
	trust := NewFileTrust(filepath.Join(dir, "trusted"))
	if err := trust.Accept(first.PublicKey()); err != nil {
		t.Fatal(err)
	}
	other, _ := GenerateTeacherIdentity()
	if err := NewFileTrust(filepath.Join(dir, "trusted")).Accept(other.PublicKey()); !errors.Is(err, ErrUntrustedTeacher) {
		t.Fatalf("accept other = %v", err)
	}
	if err := trust.SetPinned(""); err != nil {
		t.Fatal(err)
	}
	if trust.Pinned() != "" {
		t.Fatal("pin not cleared")
	}
	if _, err := NormalizeFingerprint("abc"); err == nil {
		t.Fatal("short fingerprint accepted")
	}
	lower := "1a2b3c4d 1a2b3c4d 1a2b3c4d 1a2b3c4d"
	if fp, err := NormalizeFingerprint(lower); err != nil || fp != "1A2B-3C4D-1A2B-3C4D-1A2B-3C4D-1A2B-3C4D" {
		t.Fatalf("normalize = %q %v", fp, err)
	}
}

func TestManagerSecureSessionAndImpostorRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	id, _ := GenerateTeacherIdentity()
	teacher := NewManager(WithTeacherIdentity(id), WithDiscoveryConfig(DiscoveryConfig{Port: 0}))
	defer teacher.Close()
	if err := teacher.StartTeacher(ctx, "127.0.0.1:0", protocol.DiscoveryBeacon{Channel: "sec"}); err != nil {
		t.Fatal(err)
	}
	addr := teacher.listener.Addr().String()
	got := make(chan protocol.Envelope, 1)
	teacher.OnEnvelope(func(_ core.Session, env protocol.Envelope) { got <- env })

	student := NewManager(WithTeacherAddr(addr), WithTeacherTrust(NewMemoryTrust(id.Fingerprint())), WithDiscoveryConfig(DiscoveryConfig{Port: 0}))
	defer student.Close()
	if err := student.StartStudent(ctx, "sec"); err != nil {
		t.Fatalf("trusted student: %v", err)
	}
	sessions := student.Sessions()
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d", len(sessions))
	}
	if err := sessions[0].SendEnvelope(protocol.Envelope{Type: protocol.MsgChat, SenderID: "s1"}); err != nil {
		t.Fatal(err)
	}
	select {
	case env := <-got:
		if env.Type != protocol.MsgChat {
			t.Fatalf("type = %v", env.Type)
		}
	case <-ctx.Done():
		t.Fatal("teacher did not receive envelope over secure session")
	}

	other, _ := GenerateTeacherIdentity()
	wary := NewManager(WithTeacherAddr(addr), WithTeacherTrust(NewMemoryTrust(other.Fingerprint())), WithDiscoveryConfig(DiscoveryConfig{Port: 0}))
	defer wary.Close()
	if err := wary.StartStudent(ctx, "sec"); !errors.Is(err, ErrUntrustedTeacher) {
		t.Fatalf("student pinned to another teacher err = %v", err)
	}
}

type bufConn struct {
	net.Conn
	r io.Reader
	w io.Writer
}

func (b *bufConn) Read(p []byte) (int, error)  { return b.r.Read(p) }
func (b *bufConn) Write(p []byte) (int, error) { return b.w.Write(p) }
