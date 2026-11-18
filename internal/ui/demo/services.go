package demo

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"path/filepath"
	"sync"
	"time"

	"nexlab/internal/core"
	"nexlab/internal/protocol"
)

type Transport struct {
	mu          sync.Mutex
	started     bool
	channel     string
	stats       core.TransportStats
	sent        []protocol.Envelope
	broadcasts  []protocol.Envelope
	multicasts  int
	onSession   func(core.Session)
	onEnvelope  func(core.Session, protocol.Envelope)
	onMulticast func([]byte)
}

func NewTransport(channel string, sessions int) *Transport {
	return &Transport{
		channel: channel,
		stats: core.TransportStats{
			ActiveSessions: sessions,
			TxBytesPerSec:  1240 * 1024,
			RxBytesPerSec:  320 * 1024,
			PacketLossRate: 0.002,
		},
	}
}

func (t *Transport) StartTeacher(_ context.Context, _ string, beacon protocol.DiscoveryBeacon) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.started = true
	if beacon.Channel != "" {
		t.channel = beacon.Channel
	}
	return nil
}

func (t *Transport) StartStudent(_ context.Context, channel string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.started = true
	t.channel = channel
	return nil
}

func (t *Transport) BroadcastMulticast(frame []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.multicasts++
	if t.onMulticast != nil {
		t.onMulticast(frame)
	}
	return nil
}

func (t *Transport) SendTo(studentID string, env protocol.Envelope) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	env.TargetIDs = []string{studentID}
	t.sent = append(t.sent, env)
	return nil
}

func (t *Transport) BroadcastControl(env protocol.Envelope, targetIDs []string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	env.TargetIDs = append([]string(nil), targetIDs...)
	t.broadcasts = append(t.broadcasts, env)
	return nil
}

func (t *Transport) OnSession(fn func(core.Session))                   { t.onSession = fn }
func (t *Transport) OnEnvelope(fn func(core.Session, protocol.Envelope)) { t.onEnvelope = fn }
func (t *Transport) OnMulticastData(fn func([]byte))                   { t.onMulticast = fn }

func (t *Transport) Stats() core.TransportStats {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stats
}

func (t *Transport) Close() error { return nil }

func (t *Transport) Broadcasts() []protocol.Envelope {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]protocol.Envelope(nil), t.broadcasts...)
}

type Media struct {
	mu     sync.Mutex
	volume float32
	seq    uint64
}

func NewMedia() *Media {
	return &Media{volume: 0.75}
}

func (m *Media) CaptureScreen() (*core.VideoFrame, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	return &core.VideoFrame{
		Seq:       m.seq,
		Codec:     core.CodecFrameZstd,
		Width:     320,
		Height:    180,
		KeyFrame:  true,
		Timestamp: time.Now().UnixMilli(),
	}, nil
}

func (m *Media) EncodeFrame(_ *core.VideoFrame, _ int) ([]byte, error) { return []byte{1, 2, 3}, nil }
func (m *Media) DecodeFrame(_ []byte) (*core.VideoFrame, error) {
	return &core.VideoFrame{Width: 320, Height: 180}, nil
}
func (m *Media) StartAudioCapture(_ bool, _ func(*core.AudioFrame)) error { return nil }
func (m *Media) StopAudioCapture() error                                  { return nil }
func (m *Media) PlayAudioFrame(_ *core.AudioFrame) error                  { return nil }
func (m *Media) SetVolume(vol float32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.volume = vol
}
func (m *Media) Volume() float32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.volume
}

type Control struct {
	mu          sync.Mutex
	Locked      bool
	LockMessage string
	Policy      core.PolicyRule
	Inputs      []core.RemoteInputEvent
	PowerCalls  []core.PowerAction
}

func NewControl() *Control { return &Control{} }

func (c *Control) InjectInput(ev core.RemoteInputEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Inputs = append(c.Inputs, ev)
	return nil
}

func (c *Control) SetScreenLock(locked bool, message string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Locked = locked
	c.LockMessage = message
	return nil
}

func (c *Control) ApplyPolicy(rule core.PolicyRule) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Policy = rule
	return nil
}

func (c *Control) ExecutePower(action core.PowerAction, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.PowerCalls = append(c.PowerCalls, action)
	return nil
}

type Classroom struct {
	mu       sync.RWMutex
	students []core.StudentInfo
	groups   []core.GroupInfo
	chats    []core.ChatMessage
}

func NewClassroom() *Classroom {
	now := time.Now()
	return &Classroom{
		groups: []core.GroupInfo{
			{ID: "g1", Name: "第1小组 (前排)", LeaderID: "stu-01", Members: []string{"stu-01", "stu-02", "stu-03", "stu-04"}},
			{ID: "g2", Name: "第2小组 (后排)", LeaderID: "stu-05", Members: []string{"stu-05", "stu-06", "stu-07", "stu-08"}},
		},
		students: []core.StudentInfo{
			{ID: "stu-01", Name: "张伟", StudentNo: "20260101", MachineName: "PC-01", IP: "192.168.10.101", MAC: "00:11:22:33:44:01", Channel: "301机房", GroupID: "g1", Seat: core.SeatPos{Row: 0, Col: 0}, Status: core.StatusOnline, CheckedIn: true, LastSeen: now, RTTMs: 2, LossRate: 0.0},
			{ID: "stu-02", Name: "李娜", StudentNo: "20260102", MachineName: "PC-02", IP: "192.168.10.102", MAC: "00:11:22:33:44:02", Channel: "301机房", GroupID: "g1", Seat: core.SeatPos{Row: 0, Col: 1}, Status: core.StatusHandRaised, HandRaised: true, CheckedIn: true, LastSeen: now, RTTMs: 3, LossRate: 0.001},
			{ID: "stu-03", Name: "王磊", StudentNo: "20260103", MachineName: "PC-03", IP: "192.168.10.103", MAC: "00:11:22:33:44:03", Channel: "301机房", GroupID: "g1", Seat: core.SeatPos{Row: 0, Col: 2}, Status: core.StatusOnline, CheckedIn: true, LastSeen: now, RTTMs: 2, LossRate: 0.0},
			{ID: "stu-04", Name: "刘洋", StudentNo: "20260104", MachineName: "PC-04", IP: "192.168.10.104", MAC: "00:11:22:33:44:04", Channel: "301机房", GroupID: "g1", Seat: core.SeatPos{Row: 0, Col: 3}, Status: core.StatusOnline, CheckedIn: false, LastSeen: now, RTTMs: 4, LossRate: 0.002},
			{ID: "stu-05", Name: "陈敏", StudentNo: "20260105", MachineName: "PC-05", IP: "192.168.10.105", MAC: "00:11:22:33:44:05", Channel: "301机房", GroupID: "g2", Seat: core.SeatPos{Row: 1, Col: 0}, Status: core.StatusLocked, CheckedIn: true, LastSeen: now, RTTMs: 3, LossRate: 0.0},
			{ID: "stu-06", Name: "赵强", StudentNo: "20260106", MachineName: "PC-06", IP: "192.168.10.106", MAC: "00:11:22:33:44:06", Channel: "301机房", GroupID: "g2", Seat: core.SeatPos{Row: 1, Col: 1}, Status: core.StatusOnline, CheckedIn: true, LastSeen: now, RTTMs: 2, LossRate: 0.0},
			{ID: "stu-07", Name: "周静", StudentNo: "20260107", MachineName: "PC-07", IP: "192.168.10.107", MAC: "00:11:22:33:44:07", Channel: "301机房", GroupID: "g2", Seat: core.SeatPos{Row: 1, Col: 2}, Status: core.StatusOffline, CheckedIn: false, LastSeen: now.Add(-2 * time.Minute), RTTMs: 0, LossRate: 1.0},
			{ID: "stu-08", Name: "吴涛", StudentNo: "20260108", MachineName: "PC-08", IP: "192.168.10.108", MAC: "00:11:22:33:44:08", Channel: "301机房", GroupID: "g2", Seat: core.SeatPos{Row: 1, Col: 3}, Status: core.StatusOnline, CheckedIn: true, LastSeen: now, RTTMs: 5, LossRate: 0.004},
		},
		chats: []core.ChatMessage{
			{ID: "msg-1", SenderID: "teacher", Sender: "张老师", Text: "欢迎来到《计算机网络基础》实验课，请先完成点名签到。", CreatedAt: now.Add(-3 * time.Minute)},
			{ID: "msg-2", SenderID: "stu-02", Sender: "李娜", Text: "老师，我这边准备好了，想演示第2题配置。", CreatedAt: now.Add(-1 * time.Minute)},
		},
	}
}

func (c *Classroom) UpsertStudent(info core.StudentInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.students {
		if c.students[i].ID == info.ID {
			c.students[i] = info
			return
		}
	}
	c.students = append(c.students, info)
}

func (c *Classroom) RemoveStudent(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.students {
		if c.students[i].ID == id {
			c.students = append(c.students[:i], c.students[i+1:]...)
			return
		}
	}
}

func (c *Classroom) ListStudents() []core.StudentInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]core.StudentInfo, len(c.students))
	copy(out, c.students)
	return out
}

func (c *Classroom) SetGroups(groups []core.GroupInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.groups = append([]core.GroupInfo(nil), groups...)
}

func (c *Classroom) ListGroups() []core.GroupInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]core.GroupInfo, len(c.groups))
	copy(out, c.groups)
	return out
}

func (c *Classroom) UpdateSeat(studentID string, pos core.SeatPos) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.students {
		if c.students[i].ID == studentID {
			c.students[i].Seat = pos
			return
		}
	}
}

func (c *Classroom) StartCheckIn() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.students {
		c.students[i].CheckedIn = false
	}
}

func (c *Classroom) SubmitCheckIn(studentID, name, studentNo string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.students {
		if c.students[i].ID == studentID {
			c.students[i].CheckedIn = true
			if name != "" {
				c.students[i].Name = name
			}
			if studentNo != "" {
				c.students[i].StudentNo = studentNo
			}
			return
		}
	}
	c.students = append(c.students, core.StudentInfo{
		ID:        studentID,
		Name:      name,
		StudentNo: studentNo,
		Status:    core.StatusOnline,
		CheckedIn: true,
		LastSeen:  time.Now(),
	})
}

func (c *Classroom) RaiseHand(studentID string, raised bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.students {
		if c.students[i].ID == studentID {
			c.students[i].HandRaised = raised
			if raised {
				c.students[i].Status = core.StatusHandRaised
			} else if c.students[i].Status == core.StatusHandRaised {
				c.students[i].Status = core.StatusOnline
			}
			return
		}
	}
}

func (c *Classroom) AddChat(msg core.ChatMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if msg.ID == "" {
		msg.ID = fmt.Sprintf("msg-%d", len(c.chats)+1)
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now()
	}
	c.chats = append(c.chats, msg)
}

func (c *Classroom) ListChats() []core.ChatMessage {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]core.ChatMessage, len(c.chats))
	copy(out, c.chats)
	return out
}

type FileTransfer struct {
	mu    sync.Mutex
	tasks []*core.TransferTask
}

func NewFileTransfer() *FileTransfer {
	return &FileTransfer{
		tasks: []*core.TransferTask{
			{
				ID:         "tx-1",
				Direction:  core.TransferSend,
				FileName:   "实验1_子网划分指导书.pdf",
				LocalPath:  "D:\\Course\\实验1_子网划分指导书.pdf",
				RemotePath: "C:\\NexLab\\Recv\\实验1_子网划分指导书.pdf",
				TotalBytes: 2457600,
				DoneBytes:  2457600,
				AutoOpen:   true,
				Status:     "completed",
			},
			{
				ID:         "tx-2",
				Direction:  core.TransferSubmit,
				FileName:   "张伟_实验报告.docx",
				LocalPath:  "C:\\Users\\Student\\Desktop\\张伟_实验报告.docx",
				RemotePath: "D:\\Collect\\stu-01\\张伟_实验报告.docx",
				TotalBytes: 524288,
				DoneBytes:  393216,
				StudentID:  "stu-01",
				Status:     "transferring",
			},
		},
	}
}

func (f *FileTransfer) DistributeFile(_ context.Context, localPath, remoteDir string, _ []string, autoOpen bool) (*core.TransferTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := filepath.Base(localPath)
	if name == "." || name == "" {
		name = "教学资料.zip"
	}
	task := &core.TransferTask{
		ID:         fmt.Sprintf("tx-%d", len(f.tasks)+1),
		Direction:  core.TransferSend,
		FileName:   name,
		LocalPath:  localPath,
		RemotePath: filepath.Join(remoteDir, name),
		TotalBytes: 1024 * 1024,
		DoneBytes:  1024 * 1024,
		AutoOpen:   autoOpen,
		Status:     "completed",
	}
	f.tasks = append([]*core.TransferTask{task}, f.tasks...)
	return task, nil
}

func (f *FileTransfer) SubmitHomework(_ context.Context, studentID, localPath string) (*core.TransferTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := filepath.Base(localPath)
	if name == "." || name == "" {
		name = "homework.zip"
	}
	task := &core.TransferTask{
		ID:         fmt.Sprintf("tx-%d", len(f.tasks)+1),
		Direction:  core.TransferSubmit,
		FileName:   name,
		LocalPath:  localPath,
		RemotePath: filepath.Join("Submissions", studentID, name),
		TotalBytes: 512 * 1024,
		DoneBytes:  512 * 1024,
		StudentID:  studentID,
		Status:     "completed",
	}
	f.tasks = append([]*core.TransferTask{task}, f.tasks...)
	return task, nil
}

func (f *FileTransfer) CollectFiles(_ context.Context, remoteDir, saveDir string, targetIDs []string) ([]*core.TransferTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(targetIDs) == 0 {
		targetIDs = []string{"all"}
	}
	var created []*core.TransferTask
	for _, sid := range targetIDs {
		task := &core.TransferTask{
			ID:         fmt.Sprintf("tx-%d", len(f.tasks)+1),
			Direction:  core.TransferCollect,
			FileName:   fmt.Sprintf("作业汇总_%s.zip", sid),
			LocalPath:  saveDir,
			RemotePath: remoteDir,
			TotalBytes: 768 * 1024,
			DoneBytes:  768 * 1024,
			StudentID:  sid,
			Status:     "completed",
		}
		f.tasks = append([]*core.TransferTask{task}, f.tasks...)
		created = append(created, task)
	}
	return created, nil
}

func (f *FileTransfer) ListTasks() []*core.TransferTask {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*core.TransferTask, len(f.tasks))
	copy(out, f.tasks)
	return out
}

type Exam struct {
	mu          sync.Mutex
	active      *core.ExamPaper
	submissions []core.ExamSubmission
}

func NewExam() *Exam {
	paper := &core.ExamPaper{
		ID:          "exam-01",
		Title:       "第3章 局域网与TCP/IP随堂测验",
		DurationSec: 600,
		Questions: []core.ExamQuestion{
			{
				ID:      "q1",
				Kind:    core.QuestionSingle,
				Prompt:  "TCP 协议工作在 OSI 模型的哪一层？",
				Options: []string{"网络层", "传输层", "会话层", "应用层"},
				Answer:  []string{"传输层"},
				Points:  30,
			},
			{
				ID:      "q2",
				Kind:    core.QuestionBool,
				Prompt:  "KCP 协议基于 UDP 实现可靠传输，可降低首包与重传延迟。",
				Options: []string{"正确", "错误"},
				Answer:  []string{"正确"},
				Points:  30,
			},
			{
				ID:      "q3",
				Kind:    core.QuestionMulti,
				Prompt:  "以下属于局域网组播或广播应用场景的有？",
				Options: []string{"教师屏幕广播", "ARP 地址解析", "单点 SSH 登录", "网络影院同步播放"},
				Answer:  []string{"教师屏幕广播", "ARP 地址解析", "网络影院同步播放"},
				Points:  40,
			},
		},
	}
	return &Exam{
		active: paper,
		submissions: []core.ExamSubmission{
			{
				PaperID:   paper.ID,
				StudentID: "stu-01",
				Answers:   map[string][]string{"q1": {"传输层"}, "q2": {"正确"}, "q3": {"教师屏幕广播", "ARP 地址解析", "网络影院同步播放"}},
				Score:     100,
				MaxScore:  100,
				Submitted: time.Now().Add(-2 * time.Minute),
			},
			{
				PaperID:   paper.ID,
				StudentID: "stu-02",
				Answers:   map[string][]string{"q1": {"传输层"}, "q2": {"正确"}, "q3": {"教师屏幕广播"}},
				Score:     60,
				MaxScore:  100,
				Submitted: time.Now().Add(-1 * time.Minute),
			},
			{
				PaperID:   paper.ID,
				StudentID: "stu-03",
				Answers:   map[string][]string{"q1": {"网络层"}, "q2": {"正确"}, "q3": {"教师屏幕广播", "ARP 地址解析", "网络影院同步播放"}},
				Score:     70,
				MaxScore:  100,
				Submitted: time.Now().Add(-30 * time.Second),
			},
		},
	}
}

func (e *Exam) PublishPaper(paper core.ExamPaper) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if paper.ID == "" {
		paper.ID = fmt.Sprintf("exam-%d", time.Now().Unix())
	}
	cp := paper
	e.active = &cp
	e.submissions = nil
}

func (e *Exam) GetActivePaper() *core.ExamPaper {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active == nil {
		return nil
	}
	cp := *e.active
	return &cp
}

func (e *Exam) SubmitAnswer(sub core.ExamSubmission) core.ExamSubmission {
	e.mu.Lock()
	defer e.mu.Unlock()
	if sub.Submitted.IsZero() {
		sub.Submitted = time.Now()
	}
	if e.active != nil && e.active.ID == sub.PaperID {
		score, maxScore := 0, 0
		for _, q := range e.active.Questions {
			maxScore += q.Points
			ans := sub.Answers[q.ID]
			if equalStringSlices(ans, q.Answer) {
				score += q.Points
			}
		}
		sub.Score = score
		sub.MaxScore = maxScore
	}
	e.submissions = append(e.submissions, sub)
	return sub
}

func (e *Exam) ComputeStats(paperID string) core.ExamStats {
	e.mu.Lock()
	defer e.mu.Unlock()
	stats := core.ExamStats{
		PaperID:       paperID,
		QuestionRates: make(map[string]float64),
	}
	if e.active == nil || e.active.ID != paperID || len(e.submissions) == 0 {
		return stats
	}
	var totalScore float64
	correctCounts := make(map[string]int)
	for _, sub := range e.submissions {
		stats.TotalSubmitted++
		totalScore += float64(sub.Score)
		for _, q := range e.active.Questions {
			if equalStringSlices(sub.Answers[q.ID], q.Answer) {
				correctCounts[q.ID]++
			}
		}
	}
	stats.AverageScore = totalScore / float64(stats.TotalSubmitted)
	for _, q := range e.active.Questions {
		stats.QuestionRates[q.ID] = float64(correctCounts[q.ID]) / float64(stats.TotalSubmitted)
	}
	return stats
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]int, len(a))
	for _, v := range a {
		set[v]++
	}
	for _, v := range b {
		if set[v] == 0 {
			return false
		}
		set[v]--
	}
	return true
}

// SampleDesktopImage generates a crisp preview image for monitor wall tiles or theatre demos.
func SampleDesktopImage(width, height int, seed int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	bgR := uint8(24 + (seed*29)%40)
	bgG := uint8(48 + (seed*17)%55)
	bgB := uint8(90 + (seed*37)%80)
	for y := range height {
		for x := range width {
			c := color.RGBA{R: bgR, G: bgG, B: bgB, A: 255}
			if y < 22 {
				c = color.RGBA{R: 35, G: 65, B: 115, A: 255}
			} else if x > 18 && x < width-18 && y > 34 && y < height-24 {
				c = color.RGBA{R: 240, G: 245, B: 252, A: 255}
				if y < 52 {
					c = color.RGBA{R: 43, G: 87, B: 154, A: 255}
				}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}
