package store

import (
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
		Channel:        "Lab-502",
		UseMulticast:   true,
		Dark:           true,
		CollectSaveDir: `D:\Homeworks`,
		MonitorFPS:     20,
		MonitorQual:    80,
		SpeakerVolume:  65,
		MicVolume:      90,
		SpeakerMuted:   false,
		MicMuted:       true,
		PolicyMode:     "whitelist",
		BlockedApps:    "steam.exe",
		AllowedApps:    "code.exe",
		BlockedSites:   "game.com",
		LockMessage:    "专注听讲",
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
	if gotCfg.Channel != "Lab-502" || !gotCfg.Dark || !gotCfg.MicMuted || gotCfg.SpeakerVolume != 65 {
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
