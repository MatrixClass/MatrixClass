package exam

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nexlab/internal/core"
)

var (
	ErrPaperNotFound      = errors.New("exam: paper not found")
	ErrSubmissionNotFound = errors.New("exam: submission not found")
	ErrQuestionNotFound   = errors.New("exam: question not found")
)

type Service struct {
	mu          sync.RWMutex
	activeID    string
	papers      map[string]core.ExamPaper
	submissions map[string]map[string]core.ExamSubmission // paperID -> studentID -> submission
	essayScores map[string]map[string]map[string]int    // paperID -> studentID -> questionID -> points
	listeners   []func()
}

func NewService() *Service {
	return &Service{
		papers:      make(map[string]core.ExamPaper),
		submissions: make(map[string]map[string]core.ExamSubmission),
		essayScores: make(map[string]map[string]map[string]int),
	}
}

func (s *Service) OnChange(fn func()) {
	if fn == nil {
		return
	}
	s.mu.Lock()
	s.listeners = append(s.listeners, fn)
	s.mu.Unlock()
}

func (s *Service) notify() {
	s.mu.RLock()
	listeners := append([]func(){}, s.listeners...)
	s.mu.RUnlock()
	for _, fn := range listeners {
		fn()
	}
}

func (s *Service) PublishPaper(paper core.ExamPaper) {
	s.mu.Lock()
	cloned := clonePaper(paper)
	s.papers[cloned.ID] = cloned
	s.activeID = cloned.ID
	if _, ok := s.submissions[cloned.ID]; !ok {
		s.submissions[cloned.ID] = make(map[string]core.ExamSubmission)
	}
	if _, ok := s.essayScores[cloned.ID]; !ok {
		s.essayScores[cloned.ID] = make(map[string]map[string]int)
	}
	s.mu.Unlock()
	s.notify()
}

func (s *Service) GetActivePaper() *core.ExamPaper {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.activeID == "" {
		return nil
	}
	p, ok := s.papers[s.activeID]
	if !ok {
		return nil
	}
	cloned := clonePaper(p)
	return &cloned
}

func (s *Service) GetPaper(paperID string) (*core.ExamPaper, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.papers[paperID]
	if !ok {
		return nil, false
	}
	cloned := clonePaper(p)
	return &cloned, true
}

func (s *Service) SubmitAnswer(sub core.ExamSubmission) core.ExamSubmission {
	s.mu.Lock()
	paperID := sub.PaperID
	if paperID == "" {
		paperID = s.activeID
	}
	paper, hasPaper := s.papers[paperID]
	if sub.Submitted.IsZero() {
		sub.Submitted = time.Now()
	}
	sub.PaperID = paperID
	sub.Answers = cloneAnswers(sub.Answers)

	if hasPaper {
		if _, ok := s.submissions[paperID]; !ok {
			s.submissions[paperID] = make(map[string]core.ExamSubmission)
		}
		if _, ok := s.essayScores[paperID]; !ok {
			s.essayScores[paperID] = make(map[string]map[string]int)
		}
		score, maxScore := gradeSubmission(paper, sub.Answers, s.essayScores[paperID][sub.StudentID])
		sub.Score = score
		sub.MaxScore = maxScore
		s.submissions[paperID][sub.StudentID] = sub
	}
	s.mu.Unlock()
	s.notify()
	return sub
}

func (s *Service) GradeEssay(paperID, studentID, questionID string, points int) (core.ExamSubmission, error) {
	s.mu.Lock()
	paper, ok := s.papers[paperID]
	if !ok {
		s.mu.Unlock()
		return core.ExamSubmission{}, ErrPaperNotFound
	}
	subs, ok := s.submissions[paperID]
	if !ok {
		s.mu.Unlock()
		return core.ExamSubmission{}, ErrSubmissionNotFound
	}
	sub, ok := subs[studentID]
	if !ok {
		s.mu.Unlock()
		return core.ExamSubmission{}, ErrSubmissionNotFound
	}
	var targetQ *core.ExamQuestion
	for i := range paper.Questions {
		if paper.Questions[i].ID == questionID {
			targetQ = &paper.Questions[i]
			break
		}
	}
	if targetQ == nil {
		s.mu.Unlock()
		return core.ExamSubmission{}, ErrQuestionNotFound
	}
	if points < 0 {
		points = 0
	}
	if points > targetQ.Points {
		points = targetQ.Points
	}
	if _, ok := s.essayScores[paperID]; !ok {
		s.essayScores[paperID] = make(map[string]map[string]int)
	}
	if _, ok := s.essayScores[paperID][studentID]; !ok {
		s.essayScores[paperID][studentID] = make(map[string]int)
	}
	s.essayScores[paperID][studentID][questionID] = points
	score, maxScore := gradeSubmission(paper, sub.Answers, s.essayScores[paperID][studentID])
	sub.Score = score
	sub.MaxScore = maxScore
	subs[studentID] = sub
	s.mu.Unlock()
	s.notify()
	return sub, nil
}

func (s *Service) ListSubmissions(paperID string) []core.ExamSubmission {
	s.mu.RLock()
	defer s.mu.RUnlock()
	subs := s.submissions[paperID]
	out := make([]core.ExamSubmission, 0, len(subs))
	for _, sub := range subs {
		cloned := sub
		cloned.Answers = cloneAnswers(sub.Answers)
		out = append(out, cloned)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StudentID < out[j].StudentID
	})
	return out
}

func (s *Service) ComputeStats(paperID string) core.ExamStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stats := core.ExamStats{
		PaperID:       paperID,
		QuestionRates: make(map[string]float64),
	}
	paper, ok := s.papers[paperID]
	if !ok {
		return stats
	}
	subs := s.submissions[paperID]
	if len(subs) == 0 {
		for _, q := range paper.Questions {
			stats.QuestionRates[q.ID] = 0
		}
		return stats
	}
	stats.TotalSubmitted = len(subs)
	totalScore := 0
	correctCounts := make(map[string]int, len(paper.Questions))
	for studentID, sub := range subs {
		totalScore += sub.Score
		manual := s.essayScores[paperID][studentID]
		for _, q := range paper.Questions {
			if isQuestionCorrect(q, sub.Answers[q.ID], manual[q.ID]) {
				correctCounts[q.ID]++
			}
		}
	}
	stats.AverageScore = float64(totalScore) / float64(len(subs))
	for _, q := range paper.Questions {
		stats.QuestionRates[q.ID] = float64(correctCounts[q.ID]) / float64(len(subs))
	}
	return stats
}

func (s *Service) ExportPaperJSON(paperID string) ([]byte, error) {
	s.mu.RLock()
	paper, ok := s.papers[paperID]
	s.mu.RUnlock()
	if !ok {
		return nil, ErrPaperNotFound
	}
	return json.MarshalIndent(paper, "", "  ")
}

func (s *Service) ImportPaperJSON(data []byte) (core.ExamPaper, error) {
	var paper core.ExamPaper
	if err := json.Unmarshal(data, &paper); err != nil {
		return core.ExamPaper{}, fmt.Errorf("exam: decode paper json: %w", err)
	}
	if strings.TrimSpace(paper.ID) == "" {
		return core.ExamPaper{}, errors.New("exam: paper id required")
	}
	s.PublishPaper(paper)
	return paper, nil
}

func (s *Service) ExportResultsJSON(paperID string) ([]byte, error) {
	subs := s.ListSubmissions(paperID)
	return json.MarshalIndent(subs, "", "  ")
}

func (s *Service) ExportResultsCSV(paperID string) ([]byte, error) {
	subs := s.ListSubmissions(paperID)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write([]string{"paper_id", "student_id", "score", "max_score", "submitted"}); err != nil {
		return nil, err
	}
	for _, sub := range subs {
		row := []string{
			sub.PaperID,
			sub.StudentID,
			strconv.Itoa(sub.Score),
			strconv.Itoa(sub.MaxScore),
			sub.Submitted.Format(time.RFC3339),
		}
		if err := w.Write(row); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

func gradeSubmission(paper core.ExamPaper, answers map[string][]string, manual map[string]int) (int, int) {
	score := 0
	maxScore := 0
	for _, q := range paper.Questions {
		maxScore += q.Points
		if q.Kind == core.QuestionEssay {
			if manual != nil {
				pts := manual[q.ID]
				if pts < 0 {
					pts = 0
				}
				if pts > q.Points {
					pts = q.Points
				}
				score += pts
			}
			continue
		}
		if isObjectiveCorrect(q.Kind, q.Answer, answers[q.ID]) {
			score += q.Points
		}
	}
	return score, maxScore
}

func isQuestionCorrect(q core.ExamQuestion, got []string, manualPts int) bool {
	if q.Kind == core.QuestionEssay {
		return q.Points > 0 && manualPts >= q.Points
	}
	return isObjectiveCorrect(q.Kind, q.Answer, got)
}

func isObjectiveCorrect(kind core.QuestionKind, expected, got []string) bool {
	if len(expected) == 0 || len(got) == 0 {
		return false
	}
	switch kind {
	case core.QuestionSingle, core.QuestionBool:
		return strings.EqualFold(strings.TrimSpace(expected[0]), strings.TrimSpace(got[0])) && len(got) == 1
	case core.QuestionMulti:
		expSet := normalizeSet(expected)
		gotSet := normalizeSet(got)
		if len(expSet) != len(gotSet) {
			return false
		}
		for k := range expSet {
			if !gotSet[k] {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func normalizeSet(items []string) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, item := range items {
		trimmed := strings.ToUpper(strings.TrimSpace(item))
		if trimmed != "" {
			set[trimmed] = true
		}
	}
	return set
}

func clonePaper(p core.ExamPaper) core.ExamPaper {
	out := p
	out.Questions = make([]core.ExamQuestion, len(p.Questions))
	for i, q := range p.Questions {
		out.Questions[i] = q
		out.Questions[i].Options = append([]string(nil), q.Options...)
		out.Questions[i].Answer = append([]string(nil), q.Answer...)
	}
	return out
}

func cloneAnswers(src map[string][]string) map[string][]string {
	if src == nil {
		return nil
	}
	out := make(map[string][]string, len(src))
	for k, v := range src {
		out[k] = append([]string(nil), v...)
	}
	return out
}
