package protocol

import (
	"bytes"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte(`{"hello":"world"}`)
	if err := WriteFrame(&buf, StreamControl, MsgChat, payload); err != nil {
		t.Fatalf("WriteFrame failed: %v", err)
	}
	kind, msgType, got, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame failed: %v", err)
	}
	if kind != StreamControl || msgType != MsgChat {
		t.Fatalf("unexpected header: kind=%d type=%d", kind, msgType)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch: got %q want %q", got, payload)
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	beacon := DiscoveryBeacon{
		Role:        "teacher",
		TeacherID:   "t-1",
		TeacherName: "Teacher Zhang",
		Channel:     "Lab-01",
		KCPPort:     39100,
		Version:     "0.1.0",
	}
	env, err := NewEnvelope(MsgHello, "t-1", beacon)
	if err != nil {
		t.Fatalf("NewEnvelope failed: %v", err)
	}
	raw, err := EncodeEnvelope(env)
	if err != nil {
		t.Fatalf("EncodeEnvelope failed: %v", err)
	}
	decoded, err := DecodeEnvelope(raw)
	if err != nil {
		t.Fatalf("DecodeEnvelope failed: %v", err)
	}
	if decoded.Type != MsgHello || decoded.SenderID != "t-1" {
		t.Fatalf("unexpected envelope: %+v", decoded)
	}
}

func TestInvalidMagic(t *testing.T) {
	buf := bytes.NewBuffer([]byte{0, 1, 2, 3, 1, 1, 0, 1, 0, 0, 0, 0})
	if _, _, _, err := ReadFrame(buf); err != ErrInvalidMagic {
		t.Fatalf("expected ErrInvalidMagic, got %v", err)
	}
}
