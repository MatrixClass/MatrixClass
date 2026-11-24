package student

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"nexlab/internal/core"
	"nexlab/internal/protocol"
	"nexlab/internal/ui/theme"
)

func (a *App) buildExamView(c *ui.Context, pal theme.Palette) {
	a.loadActivePaper()
	ui.Column(c).Fill().Padding(16).Gap(10).Background(pal.Canvas).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).DragWindow().Children(func() {
			title := "随堂测验答题卡"
			if a.activePaper != nil {
				title = a.activePaper.Title
			}
			ui.Text(c, title).FontSize(16).Bold().TextColor(pal.Accent)
			ui.Spacer(c)
			if a.activePaper == nil || a.examResult != nil {
				if ui.Button(c, "完成").Padding(4, 10).Clicked() {
					a.mu.Lock()
					a.mode = ModeBar
					a.mu.Unlock()
					a.syncWindow()
				}
			}
		})

		if a.activePaper == nil {
			ui.Text(c, "教师端暂未下发随堂测验试卷。").FontSize(13).TextColor(pal.TextMuted)
			return
		}

		ui.Scroll(c.Key("student-exam-scroll")).Grow(1).FillWidth().ScrollbarAxes(false, true).Scrollbars(true).KeepScrollbars(true).Children(func() {
			ui.Column(c).FillWidth().Gap(10).Children(func() {
				for idx, q := range a.activePaper.Questions {
					q := q
					ui.Column(c).Key(q.ID).FillWidth().Padding(10).Radius(3).Background(pal.Card).Border(1, pal.CardBorder).Gap(6).Children(func() {
						kindLabel := examKindLabel(q.Kind)
						ui.Text(c, fmt.Sprintf("%d. [%s] %s (%d分)", idx+1, kindLabel, q.Prompt, q.Points)).FontSize(13).Bold()
						cur := a.examAnswers[q.ID]
						options := q.Options
						if q.Kind == core.QuestionBool && len(options) == 0 {
							options = []string{"对", "错"}
						}
						if q.Kind != core.QuestionEssay && len(options) > 0 {
							ui.Row(c).Wrap().Gap(8).Children(func() {
								for _, opt := range options {
									opt := opt
									selected := examAnswerHas(cur, opt)
									lbl := opt
									if selected {
										lbl = "✓ " + opt
									}
									btn := ui.Button(c, lbl).Padding(4, 10).Radius(2)
									if selected {
										btn.Background(pal.Selection).Border(1, pal.Accent)
									}
									if btn.Clicked() {
										if q.Kind == core.QuestionMulti {
											a.examAnswers[q.ID] = toggleExamAnswer(cur, opt)
										} else {
											a.examAnswers[q.ID] = opt
										}
									}
								}
							})
							if q.Kind == core.QuestionMulti {
								ui.Text(c, "多选题：可选择多个选项，再次点击取消").FontSize(11).TextColor(pal.TextMuted)
							}
						} else {
							val := cur
							inp := ui.TextInput(c, &val).FillWidth().Placeholder("请输入作答...")
							if inp.Changed() {
								a.examAnswers[q.ID] = val
							}
						}
					})
				}
			})
		})

		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			if ui.PrimaryButton(c, "提交答卷").Padding(6, 18).Clicked() {
				a.SubmitExam()
			}
			if a.examResult != nil {
				// Papers arrive without answer keys, so the score is computed
				// by the teacher; don't show a misleading local score.
				ui.Badge(c, "已提交 · 由教师评分").Background(pal.Selection).TextColor(pal.Text)
			}
		})
	})
}

func (a *App) SubmitExam() {
	a.submitExamAnswers()
}

// examAnswerSep joins multi-select answers inside one stored string; it is a
// control character so it never collides with option text or essay answers.
const examAnswerSep = "\x1f"

func splitExamAnswer(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, examAnswerSep) {
		if strings.TrimSpace(part) != "" {
			out = append(out, part)
		}
	}
	return out
}

func examAnswerHas(raw, opt string) bool {
	for _, part := range splitExamAnswer(raw) {
		if part == opt {
			return true
		}
	}
	return false
}

func toggleExamAnswer(raw, opt string) string {
	parts := splitExamAnswer(raw)
	out := parts[:0]
	found := false
	for _, part := range parts {
		if part == opt {
			found = true
			continue
		}
		out = append(out, part)
	}
	if !found {
		out = append(out, opt)
	}
	return strings.Join(out, examAnswerSep)
}

func examKindLabel(kind core.QuestionKind) string {
	switch kind {
	case core.QuestionMulti:
		return "多选"
	case core.QuestionBool:
		return "判断"
	case core.QuestionEssay:
		return "简答"
	default:
		return "单选"
	}
}

func (a *App) submitExamAnswers() {
	a.loadActivePaper()
	if a.activePaper == nil || a.opts.Exam == nil {
		return
	}
	ans := make(map[string][]string, len(a.examAnswers))
	keys := make([]string, 0, len(a.examAnswers))
	for k := range a.examAnswers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if parts := splitExamAnswer(a.examAnswers[k]); len(parts) > 0 {
			ans[k] = parts
		}
	}
	sub := a.opts.Exam.SubmitAnswer(core.ExamSubmission{
		PaperID:   a.activePaper.ID,
		StudentID: a.studentID,
		Answers:   ans,
		Submitted: time.Now(),
	})
	if err := a.sendControl(protocol.MsgExamAnswer, sub); err != nil {
		a.statusNote = "测验提交失败，请重试: " + err.Error()
		return
	}
	a.examResult = &sub
	a.statusNote = "测验已提交，等待教师评分"
}

func (a *App) loadActivePaper() {
	if a.opts.Exam == nil {
		return
	}
	p := a.opts.Exam.GetActivePaper()
	if p != nil {
		a.activePaper = p
	}
}

func (a *App) ExamResult() *core.ExamSubmission { return a.examResult }
