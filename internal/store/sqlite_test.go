package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"nexlab/internal/core"
)

func TestSQLiteStoreRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "nexlab-teacher.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	cfg := TeacherSettings{
		Channel:          "5",
		AuthUsername:     "teacher01",
		AuthPasswordHash: "abcd1234",
		UseMulticast:     true,
		Dark:             true,
		CollectSaveDir:   `D:\Homeworks`,
		MonitorFPS:       20,
		MonitorQual:      80,
		SpeakerVolume:    65,
		MicVolume:        90,
		SpeakerMuted:     false,
		MicMuted:         true,
		PolicyMode:       "whitelist",
		BlockedApps:      "steam.exe",
		AllowedApps:      "code.exe",
		BlockedSites:     "game.com",
		LockMessage:      "专注听讲",
	}
	if err := s.SaveSettings(cfg); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if err := s.TouchChannel("Lab-502"); err != nil {
		t.Fatalf("TouchChannel: %v", err)
	}
	gotCfg, ok, err := s.LoadSettings()
	if err != nil || !ok {
		t.Fatalf("LoadSettings: ok=%v err=%v", ok, err)
	}
	if gotCfg.Channel != "5" || gotCfg.AuthUsername != "teacher01" || gotCfg.AuthPasswordHash != "abcd1234" || !gotCfg.Dark || !gotCfg.MicMuted || gotCfg.SpeakerVolume != 65 {
		t.Fatalf("unexpected loaded settings: %+v", gotCfg)
	}
	recent, err := s.ListRecentChannels(5)
	if err != nil || len(recent) != 1 || recent[0] != "Lab-502" {
		t.Fatalf("unexpected recent channels: %v err=%v", recent, err)
	}

	groups := []core.GroupInfo{{ID: "g1", Name: "一组", Members: []string{"stu-1"}}}
	if err := s.SaveGroups(groups); err != nil {
		t.Fatalf("SaveGroups: %v", err)
	}
	gotGroups, err := s.LoadGroups()
	if err != nil || len(gotGroups) != 1 || gotGroups[0].Name != "一组" {
		t.Fatalf("LoadGroups: %+v err=%v", gotGroups, err)
	}

	st := core.StudentInfo{ID: "stu-1", StudentNo: "202601", MachineName: "PC-01", Seat: core.SeatPos{Row: 1, Col: 2}, LastSeen: time.Now()}
	if err := s.UpsertStudent(st); err != nil {
		t.Fatalf("UpsertStudent: %v", err)
	}
	gotStu, err := s.LoadStudents()
	if err != nil || len(gotStu) != 1 || gotStu[0].MachineName != "PC-01" || gotStu[0].Status != core.StatusOffline {
		t.Fatalf("LoadStudents: %+v err=%v", gotStu, err)
	}

	paper := core.ExamPaper{ID: "ex-1", Title: "期中测验", DurationSec: 600, Questions: []core.ExamQuestion{{ID: "q1", Prompt: "Q1"}}}
	if err := s.SaveExamPaper(paper); err != nil {
		t.Fatalf("SaveExamPaper: %v", err)
	}
	gotPaper, err := s.LoadLatestExamPaper()
	if err != nil || gotPaper == nil || gotPaper.Title != "期中测验" {
		t.Fatalf("LoadLatestExamPaper: %+v err=%v", gotPaper, err)
	}
}

func TestLoadChatsReturnsNewestInChronologicalOrder(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "chat.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	base := time.UnixMilli(1_700_000_000_000)
	for i := 0; i < 10; i++ {
		msg := core.ChatMessage{ID: fmt.Sprintf("m%02d", i), Text: fmt.Sprintf("t%d", i), CreatedAt: base.Add(time.Duration(i) * time.Second)}
		if err := s.SaveChat(msg); err != nil {
			t.Fatalf("SaveChat: %v", err)
		}
	}
	got, err := s.LoadChats(3)
	if err != nil {
		t.Fatalf("LoadChats: %v", err)
	}
	if len(got) != 3 || got[0].ID != "m07" || got[1].ID != "m08" || got[2].ID != "m09" {
		ids := make([]string, 0, len(got))
		for _, m := range got {
			ids = append(ids, m.ID)
		}
		t.Fatalf("expected newest 3 in order [m07 m08 m09], got %v", ids)
	}
}

func TestExamEssayScoresPersist(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "exam.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	sub := core.ExamSubmission{PaperID: "p", StudentID: "s1", Answers: map[string][]string{"q4": {"x"}}, Score: 10, MaxScore: 50, Submitted: time.UnixMilli(1000)}
	if err := s.SaveExamSubmission(sub); err != nil {
		t.Fatalf("SaveExamSubmission: %v", err)
	}
	if err := s.SaveExamEssayScores("p", "s1", map[string]int{"q4": 25}, 35, 50); err != nil {
		t.Fatalf("SaveExamEssayScores: %v", err)
	}
	// Re-saving without EssayScores (e.g. raw resubmission) must keep the grade.
	if err := s.SaveExamSubmission(sub); err != nil {
		t.Fatalf("SaveExamSubmission: %v", err)
	}
	subs, err := s.LoadExamSubmissions("p")
	if err != nil || len(subs) != 1 {
		t.Fatalf("LoadExamSubmissions: %v %v", subs, err)
	}
	if subs[0].EssayScores["q4"] != 25 {
		t.Fatalf("essay scores not persisted: %+v", subs[0])
	}
	sub.EssayScores = map[string]int{"q4": 5}
	sub.Score = 15
	if err := s.SaveExamSubmission(sub); err != nil {
		t.Fatalf("SaveExamSubmission: %v", err)
	}
	subs, _ = s.LoadExamSubmissions("p")
	if subs[0].EssayScores["q4"] != 5 || subs[0].Score != 15 {
		t.Fatalf("essay scores not replaced: %+v", subs[0])
	}
	if err := s.SaveExamEssayScores("p", "missing", nil, 0, 0); err == nil {
		t.Fatalf("expected error for missing submission")
	}
}

func TestExamSubmissionMigrationAddsEssayColumn(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE exam_submissions (paper_id TEXT NOT NULL, student_id TEXT NOT NULL, answers_json TEXT NOT NULL DEFAULT '{}', score INTEGER NOT NULL DEFAULT 0, max_score INTEGER NOT NULL DEFAULT 0, submitted_at INTEGER NOT NULL, PRIMARY KEY (paper_id, student_id));
		INSERT INTO exam_submissions VALUES ('p','s','{}',1,2,3);`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = db.Close()
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	subs, err := s.LoadExamSubmissions("p")
	if err != nil || len(subs) != 1 || subs[0].Score != 1 {
		t.Fatalf("legacy row not loadable after migration: %v %v", subs, err)
	}
}
