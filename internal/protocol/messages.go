package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	MagicBytes    uint32 = 0x4E584C42 // "NXLB"
	ProtocolVer   uint8  = 1
	HeaderSize           = 12
	MaxPacketSize        = 16 * 1024 * 1024
)

type StreamKind uint8

const (
	StreamControl   StreamKind = 1
	StreamMedia     StreamKind = 2
	StreamInput     StreamKind = 3
	StreamFile      StreamKind = 4
	StreamTelemetry StreamKind = 5
)

type MsgType uint16

const (
	MsgPing MsgType = 1
	MsgPong MsgType = 2

	MsgHello           MsgType = 10
	MsgHelloAck        MsgType = 11
	MsgStudentRegister MsgType = 12
	MsgRosterSync      MsgType = 13
	MsgGroupUpdate     MsgType = 14
	MsgSeatUpdate      MsgType = 15

	MsgCheckInStart  MsgType = 20
	MsgCheckInSubmit MsgType = 21
	MsgHandRaise     MsgType = 22
	MsgHandClear     MsgType = 23
	MsgChat          MsgType = 24

	MsgBroadcastStart  MsgType = 30
	MsgBroadcastStop   MsgType = 31
	MsgStudentDemoReq  MsgType = 32
	MsgTheatreControl  MsgType = 33
	MsgVoiceControl    MsgType = 34
	MsgMonitorControl  MsgType = 35
	MsgMediaNACK       MsgType = 36
	MsgVideoFrame      MsgType = 37
	MsgAudioFrame      MsgType = 38
	MsgAnnotationDraw  MsgType = 39

	MsgRemoteControlReq MsgType = 40
	MsgRemoteInputEvent MsgType = 41
	MsgScreenLock       MsgType = 42
	MsgPolicySync       MsgType = 43
	MsgPowerCommand     MsgType = 44

	MsgFileOffer    MsgType = 50
	MsgFileChunk    MsgType = 51
	MsgFileAck      MsgType = 52
	MsgFileComplete MsgType = 53
	MsgCollectReq   MsgType = 54

	MsgExamPublish MsgType = 60
	MsgExamAnswer  MsgType = 61
	MsgExamResult  MsgType = 62
)

var (
	ErrInvalidMagic   = errors.New("protocol: invalid magic header")
	ErrPacketTooLarge = errors.New("protocol: packet exceeds maximum size")
	ErrUnsupportedVer = errors.New("protocol: unsupported protocol version")
)

type Envelope struct {
	Type      MsgType         `json:"type"`
	RequestID string          `json:"request_id,omitempty"`
	SenderID  string          `json:"sender_id,omitempty"`
	TargetIDs []string        `json:"target_ids,omitempty"`
	Channel   string          `json:"channel,omitempty"`
	Timestamp int64           `json:"ts"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

func WriteFrame(w io.Writer, kind StreamKind, msgType MsgType, payload []byte) error {
	if len(payload) > MaxPacketSize {
		return ErrPacketTooLarge
	}
	var hdr [HeaderSize]byte
	binary.BigEndian.PutUint32(hdr[0:4], MagicBytes)
	hdr[4] = ProtocolVer
	hdr[5] = byte(kind)
	binary.BigEndian.PutUint16(hdr[6:8], uint16(msgType))
	binary.BigEndian.PutUint32(hdr[8:12], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := w.Write(payload)
		return err
	}
	return nil
}

func ReadFrame(r io.Reader) (StreamKind, MsgType, []byte, error) {
	var hdr [HeaderSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, 0, nil, err
	}
	if binary.BigEndian.Uint32(hdr[0:4]) != MagicBytes {
		return 0, 0, nil, ErrInvalidMagic
	}
	if hdr[4] != ProtocolVer {
		return 0, 0, nil, ErrUnsupportedVer
	}
	kind := StreamKind(hdr[5])
	msgType := MsgType(binary.BigEndian.Uint16(hdr[6:8]))
	length := binary.BigEndian.Uint32(hdr[8:12])
	if length > MaxPacketSize {
		return 0, 0, nil, ErrPacketTooLarge
	}
	if length == 0 {
		return kind, msgType, nil, nil
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, 0, nil, err
	}
	return kind, msgType, payload, nil
}

func EncodeEnvelope(env Envelope) ([]byte, error) {
	return json.Marshal(env)
}

func DecodeEnvelope(data []byte) (Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return Envelope{}, fmt.Errorf("protocol: decode envelope: %w", err)
	}
	return env, nil
}

func NewEnvelope(msgType MsgType, senderID string, payload any) (Envelope, error) {
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return Envelope{}, err
		}
		raw = b
	}
	return Envelope{
		Type:     msgType,
		SenderID: senderID,
		Payload:  raw,
	}, nil
}

type DiscoveryBeacon struct {
	Role        string `json:"role"`
	TeacherID   string `json:"teacher_id"`
	TeacherName string `json:"teacher_name"`
	Channel     string `json:"channel"`
	TCPPort     int    `json:"tcp_port,omitempty"`
	KCPPort     int    `json:"kcp_port"`
	MulticastIP string `json:"multicast_ip,omitempty"`
	McastPort   int    `json:"mcast_port,omitempty"`
	Version     string `json:"version"`
}
