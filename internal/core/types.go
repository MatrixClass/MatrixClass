package core

import (
	"time"
)

type Role string

const (
	RoleTeacher Role = "teacher"
	RoleStudent Role = "student"
)

type StudentStatus string

const (
	StatusOffline     StudentStatus = "offline"
	StatusOnline      StudentStatus = "online"
	StatusBroadcast   StudentStatus = "broadcasting"
	StatusLocked      StudentStatus = "locked"
	StatusControlled  StudentStatus = "controlled"
	StatusExamining   StudentStatus = "examining"
	StatusHandRaised  StudentStatus = "hand_raised"
)

type SeatPos struct {
	Row int `json:"row"`
	Col int `json:"col"`
}

type StudentInfo struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	StudentNo   string        `json:"student_no"`
	MachineName string        `json:"machine_name"`
	IP          string        `json:"ip"`
	MAC         string        `json:"mac"`
	Channel     string        `json:"channel"`
	GroupID     string        `json:"group_id"`
	Seat        SeatPos       `json:"seat"`
	Status      StudentStatus `json:"status"`
	HandRaised  bool          `json:"hand_raised"`
	CheckedIn   bool          `json:"checked_in"`
	LastSeen    time.Time     `json:"last_seen"`
	RTTMs       int           `json:"rtt_ms"`
	LossRate    float64       `json:"loss_rate"`
}

type GroupInfo struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	LeaderID string   `json:"leader_id"`
	Members  []string `json:"members"`
}

type ChatMessage struct {
	ID        string    `json:"id"`
	SenderID  string    `json:"sender_id"`
	Sender    string    `json:"sender"`
	TargetID  string    `json:"target_id"`
	GroupID   string    `json:"group_id,omitempty"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

type BroadcastMode string

const (
	BroadcastFullScreen BroadcastMode = "fullscreen"
	BroadcastWindowed   BroadcastMode = "windowed"
)

type VideoCodec string

const (
	CodecH264MFT   VideoCodec = "h264_mft"
	CodecFrameZstd VideoCodec = "frame_zstd"
)

type VideoFrame struct {
	Seq       uint64     `json:"seq"`
	Codec     VideoCodec `json:"codec"`
	Width     int        `json:"width"`
	Height    int        `json:"height"`
	KeyFrame  bool       `json:"key_frame"`
	Timestamp int64      `json:"ts"`
	Data      []byte     `json:"data"`
	NV12Y     []byte     `json:"-"`
	NV12UV    []byte     `json:"-"`
	StrideY   int        `json:"-"`
	StrideUV  int        `json:"-"`
}

type AudioFrame struct {
	Seq        uint64 `json:"seq"`
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
	Timestamp  int64  `json:"ts"`
	Data       []byte `json:"data"`
}

type BroadcastConfig struct {
	Mode         BroadcastMode `json:"mode"`
	WithAudio    bool          `json:"with_audio"`
	FPS          int           `json:"fps"`
	Quality      int           `json:"quality"`
	UseMulticast bool          `json:"use_multicast"`
	SourceID     string        `json:"source_id"`
}

type MonitorConfig struct {
	TargetIDs []string `json:"target_ids"`
	Width     int      `json:"width"`
	Height    int      `json:"height"`
	FPS       int      `json:"fps"`
	Quality   int      `json:"quality"`
	Reset     bool     `json:"reset,omitempty"`
}

type TheatreState struct {
	Playing  bool    `json:"playing"`
	Media    string  `json:"media"`
	Position float64 `json:"position"`
	Duration float64 `json:"duration"`
	Volume   float32 `json:"volume"`
}

type InputEventKind string

const (
	InputMouseMove  InputEventKind = "mouse_move"
	InputMouseDown  InputEventKind = "mouse_down"
	InputMouseUp    InputEventKind = "mouse_up"
	InputMouseWheel InputEventKind = "mouse_wheel"
	InputKeyDown    InputEventKind = "key_down"
	InputKeyUp      InputEventKind = "key_up"
)

type RemoteInputEvent struct {
	Kind   InputEventKind `json:"kind"`
	X      float64        `json:"x"`
	Y      float64        `json:"y"`
	Button int            `json:"button"`
	Delta  int            `json:"delta"`
	VKCode uint16         `json:"vk_code"`
}

type PolicyRule struct {
	Mode           string   `json:"mode"`
	BlockedApps    []string `json:"blocked_apps"`
	AllowedApps    []string `json:"allowed_apps"`
	BlockedDomains []string `json:"blocked_domains"`
	AllowedDomains []string `json:"allowed_domains"`
	LockScreen     bool     `json:"lock_screen"`
	LockMessage    string   `json:"lock_message"`
}

type PowerAction string

const (
	PowerShutdown PowerAction = "shutdown"
	PowerReboot   PowerAction = "reboot"
	PowerLogoff   PowerAction = "logoff"
	PowerWake     PowerAction = "wake"
)

type TransferDirection string

const (
	TransferSend    TransferDirection = "send"
	TransferCollect TransferDirection = "collect"
	TransferSubmit  TransferDirection = "submit"
)

type TransferTask struct {
	ID         string            `json:"id"`
	Direction  TransferDirection `json:"direction"`
	FileName   string            `json:"file_name"`
	LocalPath  string            `json:"local_path"`
	RemotePath string            `json:"remote_path"`
	TotalBytes int64             `json:"total_bytes"`
	DoneBytes  int64             `json:"done_bytes"`
	SHA256     string            `json:"sha256"`
	AutoOpen   bool              `json:"auto_open"`
	StudentID  string            `json:"student_id"`
	Status     string            `json:"status"`
	Error      string            `json:"error,omitempty"`
}

type QuestionKind string

const (
	QuestionSingle QuestionKind = "single"
	QuestionMulti  QuestionKind = "multi"
	QuestionBool   QuestionKind = "bool"
	QuestionEssay  QuestionKind = "essay"
)

type ExamQuestion struct {
	ID      string       `json:"id"`
	Kind    QuestionKind `json:"kind"`
	Prompt  string       `json:"prompt"`
	Options []string     `json:"options,omitempty"`
	Answer  []string     `json:"answer,omitempty"`
	Points  int          `json:"points"`
}

type ExamPaper struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	DurationSec int            `json:"duration_sec"`
	Questions   []ExamQuestion `json:"questions"`
}

type ExamSubmission struct {
	PaperID   string              `json:"paper_id"`
	StudentID string              `json:"student_id"`
	Answers   map[string][]string `json:"answers"`
	Score     int                 `json:"score"`
	MaxScore  int                 `json:"max_score"`
	Submitted time.Time           `json:"submitted"`
}

type ExamStats struct {
	PaperID        string             `json:"paper_id"`
	TotalSubmitted int                `json:"total_submitted"`
	AverageScore   float64            `json:"average_score"`
	QuestionRates  map[string]float64 `json:"question_rates"`
}

type TransportStats struct {
	ActiveSessions int     `json:"active_sessions"`
	TxBytesPerSec  uint64  `json:"tx_bytes_per_sec"`
	RxBytesPerSec  uint64  `json:"rx_bytes_per_sec"`
	PacketLossRate float64 `json:"packet_loss_rate"`
}
