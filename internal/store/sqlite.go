package store

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"nexlab/internal/core"
)

// TeacherSettings holds persisted Teacher Console preferences and classroom policy.
type TeacherSettings struct {
	Channel          string
	AuthUsername     string
	AuthPasswordHash string
	UseMulticast     bool
	Dark             bool
	CollectSaveDir   string
	MonitorFPS       float64
	MonitorQual      float64
	SpeakerVolume    float64
	MicVolume        float64
	SpeakerMuted     bool
	MicMuted         bool
	PolicyMode       string
	BlockedApps      string
	AllowedApps      string
	BlockedSites     string
	LockMessage      string
}

// Store provides SQLite-backed persistence for the Teacher Console.
type Store struct {
	mu sync.Mutex
	db *sql.DB
}

// Open opens (or creates) the SQLite database at dbPath and initializes the schema.
func Open(dbPath string) (*Store, error) {
	if strings.TrimSpace(dbPath) == "" {
		dbPath = "nexlab-teacher.db"
	}
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=3000;`); err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := s.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func (s *Store) initSchema() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS teacher_settings (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	channel TEXT NOT NULL DEFAULT '',
	auth_username TEXT NOT NULL DEFAULT 'admin',
	auth_password_hash TEXT NOT NULL DEFAULT '',
	use_multicast INTEGER NOT NULL DEFAULT 1,
	dark INTEGER NOT NULL DEFAULT 0,
	collect_save_dir TEXT NOT NULL DEFAULT '',
	monitor_fps REAL NOT NULL DEFAULT 15,
	monitor_qual REAL NOT NULL DEFAULT 75,
	speaker_volume REAL NOT NULL DEFAULT 75,
	mic_volume REAL NOT NULL DEFAULT 80,
	speaker_muted INTEGER NOT NULL DEFAULT 0,
	mic_muted INTEGER NOT NULL DEFAULT 0,
	policy_mode TEXT NOT NULL DEFAULT 'blacklist',
	blocked_apps TEXT NOT NULL DEFAULT '',
	allowed_apps TEXT NOT NULL DEFAULT '',
	blocked_sites TEXT NOT NULL DEFAULT '',
	lock_message TEXT NOT NULL DEFAULT '',
	updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS channel_history (
	channel TEXT PRIMARY KEY,
	last_used_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS groups (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	leader_id TEXT NOT NULL DEFAULT '',
	members_json TEXT NOT NULL DEFAULT '[]',
	sort_order INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS students (
	id TEXT PRIMARY KEY,
	class_name TEXT NOT NULL DEFAULT '',
	student_no TEXT NOT NULL DEFAULT '',
	machine_name TEXT NOT NULL DEFAULT '',
	mac TEXT NOT NULL DEFAULT '',
	group_id TEXT NOT NULL DEFAULT '',
	seat_row INTEGER NOT NULL DEFAULT 0,
	seat_col INTEGER NOT NULL DEFAULT 0,
	checked_in INTEGER NOT NULL DEFAULT 0,
	last_seen INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS chat_messages (
	id TEXT PRIMARY KEY,
	sender_id TEXT NOT NULL DEFAULT '',
	sender TEXT NOT NULL DEFAULT '',
	text TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS exam_papers (
	id TEXT PRIMARY KEY,
	title TEXT NOT NULL,
	duration_sec INTEGER NOT NULL DEFAULT 0,
	questions_json TEXT NOT NULL DEFAULT '[]',
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS exam_submissions (
	paper_id TEXT NOT NULL,
	student_id TEXT NOT NULL,
	answers_json TEXT NOT NULL DEFAULT '{}',
	score INTEGER NOT NULL DEFAULT 0,
	max_score INTEGER NOT NULL DEFAULT 0,
	submitted_at INTEGER NOT NULL,
	essay_scores_json TEXT NOT NULL DEFAULT '{}',
	PRIMARY KEY (paper_id, student_id)
);`
	if _, err := s.db.Exec(ddl); err != nil {
		return err
	}
	// Safe migrations for existing databases created prior to auth columns.
	_, _ = s.db.Exec(`ALTER TABLE teacher_settings ADD COLUMN auth_username TEXT NOT NULL DEFAULT 'admin'`)
	_, _ = s.db.Exec(`ALTER TABLE students ADD COLUMN class_name TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE teacher_settings ADD COLUMN auth_password_hash TEXT NOT NULL DEFAULT ''`)
	// Manual (essay) grading results, persisted alongside the submission row.
	_, _ = s.db.Exec(`ALTER TABLE exam_submissions ADD COLUMN essay_scores_json TEXT NOT NULL DEFAULT '{}'`)
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) LoadSettings() (TeacherSettings, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out TeacherSettings
	var useMcast, dark, spkMuted, micMuted int
	row := s.db.QueryRow(`SELECT channel, auth_username, auth_password_hash, use_multicast, dark, collect_save_dir, monitor_fps, monitor_qual,
		speaker_volume, mic_volume, speaker_muted, mic_muted, policy_mode, blocked_apps, allowed_apps, blocked_sites, lock_message
		FROM teacher_settings WHERE id = 1`)
	err := row.Scan(
		&out.Channel, &out.AuthUsername, &out.AuthPasswordHash, &useMcast, &dark, &out.CollectSaveDir, &out.MonitorFPS, &out.MonitorQual,
		&out.SpeakerVolume, &out.MicVolume, &spkMuted, &micMuted,
		&out.PolicyMode, &out.BlockedApps, &out.AllowedApps, &out.BlockedSites, &out.LockMessage,
	)
	if err == sql.ErrNoRows {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	out.UseMulticast = useMcast != 0
	out.Dark = dark != 0
	out.SpeakerMuted = spkMuted != 0
	out.MicMuted = micMuted != 0
	return out, true, nil
}

func (s *Store) SaveSettings(cfg TeacherSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(cfg.AuthUsername) == "" {
		cfg.AuthUsername = "admin"
	}
	_, err := s.db.Exec(`INSERT INTO teacher_settings (
		id, channel, auth_username, auth_password_hash, use_multicast, dark, collect_save_dir, monitor_fps, monitor_qual,
		speaker_volume, mic_volume, speaker_muted, mic_muted,
		policy_mode, blocked_apps, allowed_apps, blocked_sites, lock_message, updated_at
	) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		channel=excluded.channel,
		auth_username=excluded.auth_username,
		auth_password_hash=excluded.auth_password_hash,
		use_multicast=excluded.use_multicast,
		dark=excluded.dark,
		collect_save_dir=excluded.collect_save_dir,
		monitor_fps=excluded.monitor_fps,
		monitor_qual=excluded.monitor_qual,
		speaker_volume=excluded.speaker_volume,
		mic_volume=excluded.mic_volume,
		speaker_muted=excluded.speaker_muted,
		mic_muted=excluded.mic_muted,
		policy_mode=excluded.policy_mode,
		blocked_apps=excluded.blocked_apps,
		allowed_apps=excluded.allowed_apps,
		blocked_sites=excluded.blocked_sites,
		lock_message=excluded.lock_message,
		updated_at=excluded.updated_at`,
		cfg.Channel, cfg.AuthUsername, cfg.AuthPasswordHash, boolToInt(cfg.UseMulticast), boolToInt(cfg.Dark), cfg.CollectSaveDir,
		cfg.MonitorFPS, cfg.MonitorQual, cfg.SpeakerVolume, cfg.MicVolume,
		boolToInt(cfg.SpeakerMuted), boolToInt(cfg.MicMuted),
		cfg.PolicyMode, cfg.BlockedApps, cfg.AllowedApps, cfg.BlockedSites, cfg.LockMessage,
		time.Now().UnixMilli(),
	)
	return err
}

func (s *Store) TouchChannel(channel string) error {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO channel_history (channel, last_used_at) VALUES (?, ?)
		ON CONFLICT(channel) DO UPDATE SET last_used_at=excluded.last_used_at`,
		channel, time.Now().UnixMilli())
	return err
}

func (s *Store) ListRecentChannels(limit int) ([]string, error) {
	if limit <= 0 {
		limit = 8
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT channel FROM channel_history ORDER BY last_used_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ch string
		if err := rows.Scan(&ch); err == nil && ch != "" {
			out = append(out, ch)
		}
	}
	return out, rows.Err()
}

func (s *Store) SaveGroups(groups []core.GroupInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM groups`); err != nil {
		_ = tx.Rollback()
		return err
	}
	for i, g := range groups {
		membersJSON, _ := json.Marshal(g.Members)
		if _, err := tx.Exec(`INSERT INTO groups (id, name, leader_id, members_json, sort_order) VALUES (?, ?, ?, ?, ?)`,
			g.ID, g.Name, g.LeaderID, string(membersJSON), i); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) LoadGroups() ([]core.GroupInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id, name, leader_id, members_json FROM groups ORDER BY sort_order ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.GroupInfo
	for rows.Next() {
		var g core.GroupInfo
		var membersJSON string
		if err := rows.Scan(&g.ID, &g.Name, &g.LeaderID, &membersJSON); err != nil {
			continue
		}
		_ = json.Unmarshal([]byte(membersJSON), &g.Members)
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) UpsertStudent(st core.StudentInfo) error {
	if st.ID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO students (id, class_name, student_no, machine_name, mac, group_id, seat_row, seat_col, checked_in, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			class_name=excluded.class_name,
			student_no=excluded.student_no,
			machine_name=excluded.machine_name,
			mac=excluded.mac,
			group_id=excluded.group_id,
			seat_row=excluded.seat_row,
			seat_col=excluded.seat_col,
			checked_in=excluded.checked_in,
			last_seen=excluded.last_seen`,
		st.ID, st.ClassName, st.StudentNo, st.MachineName, st.MAC, st.GroupID, st.Seat.Row, st.Seat.Col,
		boolToInt(st.CheckedIn), st.LastSeen.UnixMilli(),
	)
	return err
}

func (s *Store) LoadStudents() ([]core.StudentInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id, class_name, student_no, machine_name, mac, group_id, seat_row, seat_col, checked_in, last_seen FROM students`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.StudentInfo
	for rows.Next() {
		var st core.StudentInfo
		var checkedIn int
		var lastSeenMs int64
		if err := rows.Scan(&st.ID, &st.ClassName, &st.StudentNo, &st.MachineName, &st.MAC, &st.GroupID, &st.Seat.Row, &st.Seat.Col, &checkedIn, &lastSeenMs); err != nil {
			continue
		}
		st.CheckedIn = checkedIn != 0
		st.Status = core.StatusOffline
		if lastSeenMs > 0 {
			st.LastSeen = time.UnixMilli(lastSeenMs)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) DeleteStudents(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM students WHERE id = ?`, id); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SaveChat(msg core.ChatMessage) error {
	if msg.ID == "" {
		msg.ID = time.Now().Format("20060102150405.000000000")
	}
	createdAt := msg.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT OR IGNORE INTO chat_messages (id, sender_id, sender, text, created_at) VALUES (?, ?, ?, ?, ?)`,
		msg.ID, msg.SenderID, msg.Sender, msg.Text, createdAt.UnixMilli())
	return err
}

func (s *Store) LoadChats(limit int) ([]core.ChatMessage, error) {
	if limit <= 0 {
		limit = 200
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Select the newest `limit` messages, then return them in chronological order.
	rows, err := s.db.Query(`SELECT id, sender_id, sender, text, created_at FROM (
		SELECT id, sender_id, sender, text, created_at, rowid AS rid FROM chat_messages ORDER BY created_at DESC, rid DESC LIMIT ?
	) ORDER BY created_at ASC, rid ASC`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.ChatMessage
	for rows.Next() {
		var m core.ChatMessage
		var ts int64
		if err := rows.Scan(&m.ID, &m.SenderID, &m.Sender, &m.Text, &ts); err != nil {
			continue
		}
		m.CreatedAt = time.UnixMilli(ts)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) SaveExamPaper(p core.ExamPaper) error {
	qJSON, _ := json.Marshal(p.Questions)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO exam_papers (id, title, duration_sec, questions_json, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			title=excluded.title,
			duration_sec=excluded.duration_sec,
			questions_json=excluded.questions_json`,
		p.ID, p.Title, p.DurationSec, string(qJSON), time.Now().UnixMilli())
	return err
}

func (s *Store) LoadLatestExamPaper() (*core.ExamPaper, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var p core.ExamPaper
	var qJSON string
	err := s.db.QueryRow(`SELECT id, title, duration_sec, questions_json FROM exam_papers ORDER BY created_at DESC LIMIT 1`).
		Scan(&p.ID, &p.Title, &p.DurationSec, &qJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(qJSON), &p.Questions)
	return &p, nil
}

// SaveExamSubmission upserts a submission row. When sub.EssayScores is non-nil
// the persisted manual (essay) scores are replaced as well; a nil map leaves
// previously stored essay scores untouched.
func (s *Store) SaveExamSubmission(sub core.ExamSubmission) error {
	aJSON, _ := json.Marshal(sub.Answers)
	ts := sub.Submitted
	if ts.IsZero() {
		ts = time.Now()
	}
	essayJSON := "{}"
	keepEssay := 1
	if sub.EssayScores != nil {
		b, err := json.Marshal(sub.EssayScores)
		if err != nil {
			return err
		}
		essayJSON = string(b)
		keepEssay = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO exam_submissions (paper_id, student_id, answers_json, score, max_score, submitted_at, essay_scores_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(paper_id, student_id) DO UPDATE SET
			answers_json=excluded.answers_json,
			score=excluded.score,
			max_score=excluded.max_score,
			submitted_at=excluded.submitted_at,
			essay_scores_json=CASE WHEN ? = 1 THEN exam_submissions.essay_scores_json ELSE excluded.essay_scores_json END`,
		sub.PaperID, sub.StudentID, string(aJSON), sub.Score, sub.MaxScore, ts.UnixMilli(), essayJSON, keepEssay)
	return err
}

// SaveExamEssayScores updates only the manual grading results (and the
// recomputed score) of an existing submission row.
func (s *Store) SaveExamEssayScores(paperID, studentID string, essayScores map[string]int, score, maxScore int) error {
	if essayScores == nil {
		essayScores = map[string]int{}
	}
	b, err := json.Marshal(essayScores)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE exam_submissions SET essay_scores_json = ?, score = ?, max_score = ? WHERE paper_id = ? AND student_id = ?`,
		string(b), score, maxScore, paperID, studentID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) LoadExamSubmissions(paperID string) ([]core.ExamSubmission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT paper_id, student_id, answers_json, score, max_score, submitted_at, essay_scores_json FROM exam_submissions WHERE paper_id = ?`, paperID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.ExamSubmission
	for rows.Next() {
		var sub core.ExamSubmission
		var aJSON, essayJSON string
		var ts int64
		if err := rows.Scan(&sub.PaperID, &sub.StudentID, &aJSON, &sub.Score, &sub.MaxScore, &ts, &essayJSON); err != nil {
			continue
		}
		_ = json.Unmarshal([]byte(aJSON), &sub.Answers)
		var essay map[string]int
		if err := json.Unmarshal([]byte(essayJSON), &essay); err == nil && len(essay) > 0 {
			sub.EssayScores = essay
		}
		sub.Submitted = time.UnixMilli(ts)
		out = append(out, sub)
	}
	return out, rows.Err()
}
