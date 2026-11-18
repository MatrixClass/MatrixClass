package exam

import (
	"encoding/json"
	"strings"
	"testing"

	"nexlab/internal/core"
)

func samplePaper() core.ExamPaper {
	return core.ExamPaper{
		ID:          "paper-1",
		Title:       "Computer Networks Quiz",
		DurationSec: 600,
		Questions: []core.ExamQuestion{
			{ID: "q1", Kind: core.QuestionSingle, Prompt: "KCP uses which transport?", Options: []string{"TCP", "UDP"}, Answer: []string{"UDP"}, Points: 20},
			{ID: "q2", Kind: core.QuestionMulti, Prompt: "Permissive licenses?", Options: []string{"MIT", "BSD", "GPL", "Apache-2.0"}, Answer: []string{"MIT", "BSD", "Apache-2.0"}, Points: 30},
			{ID: "q3", Kind: core.QuestionBool, Prompt: "FEC recovers lost shards.", Options: []string{"True", "False"}, Answer: []string{"True"}, Points: 20},
			{ID: "q4", Kind: core.QuestionEssay, Prompt: "Explain ARQ.", Points: 30},
		},
	}
}

func TestPublishGradeAndStats(t *testing.T) {
	svc := NewService()
	paper := samplePaper()
	svc.PublishPaper(paper)

	active := svc.GetActivePaper()
	if active == nil || active.ID != "paper-1" {
		t.Fatalf("unexpected active paper: %+v", active)
	}

	sub1 := svc.SubmitAnswer(core.ExamSubmission{
		PaperID:   "paper-1",
		StudentID: "stu-1",
		Answers: map[string][]string{
			"q1": {"UDP"},
			"q2": {"Apache-2.0", "MIT", "BSD"},
			"q3": {"True"},
			"q4": {"Automatic Repeat reQuest"},
		},
	})
	if sub1.Score != 70 || sub1.MaxScore != 100 {
		t.Fatalf("expected 70/100 before essay grading, got %d/%d", sub1.Score, sub1.MaxScore)
	}

	graded, err := svc.GradeEssay("paper-1", "stu-1", "q4", 30)
	if err != nil {
		t.Fatalf("GradeEssay failed: %v", err)
	}
	if graded.Score != 100 {
		t.Fatalf("expected 100 after essay grading, got %d", graded.Score)
	}

	sub2 := svc.SubmitAnswer(core.ExamSubmission{
		PaperID:   "paper-1",
		StudentID: "stu-2",
		Answers: map[string][]string{
			"q1": {"TCP"},
			"q2": {"MIT", "BSD"},
			"q3": {"true"},
		},
	})
	if sub2.Score != 20 {
		t.Fatalf("expected 20 for stu-2, got %d", sub2.Score)
	}

	stats := svc.ComputeStats("paper-1")
	if stats.TotalSubmitted != 2 || stats.AverageScore != 60 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if stats.QuestionRates["q1"] != 0.5 || stats.QuestionRates["q3"] != 1.0 {
		t.Fatalf("unexpected question rates: %+v", stats.QuestionRates)
	}
}

func TestImportExportJSONAndCSV(t *testing.T) {
	svc := NewService()
	raw, err := json.Marshal(samplePaper())
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	imported, err := svc.ImportPaperJSON(raw)
	if err != nil {
		t.Fatalf("ImportPaperJSON failed: %v", err)
	}
	if imported.Title != "Computer Networks Quiz" {
		t.Fatalf("unexpected title: %s", imported.Title)
	}
	exported, err := svc.ExportPaperJSON(imported.ID)
	if err != nil || !strings.Contains(string(exported), "paper-1") {
		t.Fatalf("ExportPaperJSON failed: err=%v", err)
	}

	svc.SubmitAnswer(core.ExamSubmission{
		PaperID:   "paper-1",
		StudentID: "stu-1",
		Answers:   map[string][]string{"q1": {"UDP"}},
	})
	csvBytes, err := svc.ExportResultsCSV("paper-1")
	if err != nil {
		t.Fatalf("ExportResultsCSV failed: %v", err)
	}
	if !strings.Contains(string(csvBytes), "paper-1,stu-1,20,100") {
		t.Fatalf("unexpected csv output:\n%s", string(csvBytes))
	}
	jsonBytes, err := svc.ExportResultsJSON("paper-1")
	if err != nil || !strings.Contains(string(jsonBytes), "stu-1") {
		t.Fatalf("ExportResultsJSON failed: %v", err)
	}
}
