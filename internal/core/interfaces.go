package core

import (
	"context"
	"io"
	"nexlab/internal/protocol"
)

type Session interface {
	ID() string
	RemoteAddr() string
	OpenStream(kind protocol.StreamKind) (io.ReadWriteCloser, error)
	SendEnvelope(env protocol.Envelope) error
	Close() error
}

type TransportManager interface {
	StartTeacher(ctx context.Context, listenAddr string, beacon protocol.DiscoveryBeacon) error
	StartStudent(ctx context.Context, channel string) error
	BroadcastMulticast(frame []byte) error
	SendTo(studentID string, env protocol.Envelope) error
	BroadcastControl(env protocol.Envelope, targetIDs []string) error
	OnSession(fn func(Session))
	OnEnvelope(fn func(Session, protocol.Envelope))
	OnMulticastData(fn func([]byte))
	Stats() TransportStats
	Close() error
}

type MediaEngine interface {
	CaptureScreen() (*VideoFrame, error)
	EncodeFrame(frame *VideoFrame, quality int) ([]byte, error)
	DecodeFrame(data []byte) (*VideoFrame, error)
	StartAudioCapture(loopback bool, onFrame func(*AudioFrame)) error
	StopAudioCapture() error
	PlayAudioFrame(frame *AudioFrame) error
	SetVolume(vol float32)
}

type ControlEngine interface {
	InjectInput(ev RemoteInputEvent) error
	SetScreenLock(locked bool, message string) error
	ApplyPolicy(rule PolicyRule) error
	ExecutePower(action PowerAction, macAddr string) error
}

type ClassroomService interface {
	UpsertStudent(info StudentInfo)
	RemoveStudent(id string)
	ListStudents() []StudentInfo
	SetGroups(groups []GroupInfo)
	ListGroups() []GroupInfo
	UpdateSeat(studentID string, pos SeatPos)
	StartCheckIn()
	SubmitCheckIn(studentID, name, studentNo string)
	RaiseHand(studentID string, raised bool)
	AddChat(msg ChatMessage)
	ListChats() []ChatMessage
}

type FileTransferService interface {
	DistributeFile(ctx context.Context, localPath, remoteDir string, targetIDs []string, autoOpen bool) (*TransferTask, error)
	SubmitHomework(ctx context.Context, studentID, localPath string) (*TransferTask, error)
	CollectFiles(ctx context.Context, remoteDir, saveDir string, targetIDs []string) ([]*TransferTask, error)
	ListTasks() []*TransferTask
}

type ExamService interface {
	PublishPaper(paper ExamPaper)
	GetActivePaper() *ExamPaper
	SubmitAnswer(sub ExamSubmission) ExamSubmission
	ComputeStats(paperID string) ExamStats
}
