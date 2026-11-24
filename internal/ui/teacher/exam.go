package teacher

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/chart"

	"nexlab/internal/core"
	"nexlab/internal/exam"
	"nexlab/internal/protocol"
	"nexlab/internal/ui/theme"
)

func (a *App) buildExamStatsTab(c *ui.Context, pal theme.Palette) {
	var paper *core.ExamPaper
	var stats core.ExamStats
	var subs []core.ExamSubmission
	if a.opts.Exam != nil {
		paper = a.opts.Exam.GetActivePaper()
		if paper != nil {
			stats = a.opts.Exam.ComputeStats(paper.ID)
			type subLister interface {
				ListSubmissions(paperID string) []core.ExamSubmission
			}
			if lister, ok := a.opts.Exam.(subLister); ok {
				subs = lister.ListSubmissions(paper.ID)
			}
		}
	}

	ui.Scroll(c).Grow(1).Padding(12).Gap(12).ScrollbarAxes(false, true).Scrollbars(true).KeepScrollbars(true).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Stretch).Children(func() {
			// Left: Quick paper builder & publisher
			ui.Column(c).Grow(1).Background(pal.Card).Border(1, pal.CardBorder).Radius(3).Padding(12).Gap(8).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "发布随堂测验").FontSize(14).Bold()
					ui.Spacer(c)
					ui.Badge(c, fmt.Sprintf("待发布题目: %d", len(a.examDraftQuestions)))
				})
				ui.Text(c, "测验标题").FontSize(12).TextColor(pal.TextMuted)
				ui.TextInput(c, &a.examTitleInput).Placeholder("输入试卷标题")
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Column(c).Width(130).Gap(4).Children(func() {
						ui.Text(c, "题型").FontSize(12).TextColor(pal.TextMuted)
						kindLabel := examKindDisplay(a.examKindInput)
						if ui.Select(c, &kindLabel, []string{"单选题", "多选题", "判断题", "简答题"}).Changed() {
							a.examKindInput = examKindValue(kindLabel)
						}
					})
					ui.Column(c).Width(110).Gap(4).Children(func() {
						ui.Text(c, "分值").FontSize(12).TextColor(pal.TextMuted)
						ui.NumberInput(c, &a.examPointsInput, 1, 100, 5)
					})
					ui.Column(c).Width(120).Gap(4).Children(func() {
						ui.Text(c, "时长(分钟)").FontSize(12).TextColor(pal.TextMuted)
						ui.NumberInput(c, &a.examDurInput, 1, 120, 1)
					})
				})
				ui.Text(c, "题目内容").FontSize(12).TextColor(pal.TextMuted)
				ui.TextInput(c, &a.examPromptInput).Placeholder("输入题干内容")
				switch core.QuestionKind(a.examKindInput) {
				case core.QuestionEssay:
					ui.Text(c, "简答题无需选项，提交后由教师评分").FontSize(11).TextColor(pal.TextMuted)
				case core.QuestionBool:
					ui.Text(c, "选项 (留空默认为：对,错)").FontSize(12).TextColor(pal.TextMuted)
					ui.TextInput(c, &a.examOptionsInput).Placeholder("对,错")
				default:
					ui.Text(c, "选项 (逗号分隔)").FontSize(12).TextColor(pal.TextMuted)
					ui.TextInput(c, &a.examOptionsInput).Placeholder("例如：A,B,C,D")
				}
				if core.QuestionKind(a.examKindInput) != core.QuestionEssay {
					ui.Text(c, "参考答案 (多选用逗号分隔)").FontSize(12).TextColor(pal.TextMuted)
					ui.TextInput(c, &a.examAnswerInput).Placeholder("必须与选项文字一致")
				}
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, "添加到试卷").Clicked() {
						a.addDraftQuestion()
					}
					if len(a.examDraftQuestions) > 0 {
						if ui.Button(c, "清空题目").Clicked() {
							a.examDraftQuestions = nil
						}
					}
					if ui.PrimaryButton(c, "下发随堂测验").Clicked() {
						a.publishExam()
					}
					if a.examPublished {
						ui.Text(c, "已下发至全班").FontSize(12).TextColor(pal.Success)
					}
				})
			})

			// Right: Live score & accuracy chart using mygo/ui/chart
			ui.Column(c).Grow(1).Background(pal.Card).Border(1, pal.CardBorder).Radius(3).Padding(12).Gap(8).Children(func() {
				title := "暂无已发布试卷"
				if paper != nil {
					title = paper.Title
				}
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "作答统计与正确率").FontSize(14).Bold()
					ui.Spacer(c)
					ui.Badge(c, fmt.Sprintf("已交 %d 份 · 均分 %.1f", stats.TotalSubmitted, stats.AverageScore))
					if paper != nil {
						if ui.Button(c, "导出CSV").Padding(2, 8).Clicked() {
							a.exportExamCSV(paper.ID)
						}
					}
				})
				ui.Text(c, title).FontSize(12).TextColor(pal.TextMuted)

				if paper != nil && len(paper.Questions) > 0 {
					labels := make([]string, len(paper.Questions))
					values := make([]float64, len(paper.Questions))
					for i, q := range paper.Questions {
						labels[i] = fmt.Sprintf("Q%d", i+1)
						values[i] = stats.QuestionRates[q.ID] * 100
					}
					xScale := chart.NewBand(labels...).Padding(0.3, 0.15)
					yScale := chart.NewLinear(0, 100).Nice(5)
					chart.Plot(c, chart.Axes{
						Left:   yScale.Ticks(5),
						Bottom: xScale.Ticks(),
					}, func(area ui.Element) {
						area.Background(pal.Canvas).Border(1, pal.CardBorder).Radius(2)
						for i, v := range values {
							barColor := pal.Accent
							if v < 60 {
								barColor = pal.TextMuted
							}
							chart.Rect(c, xScale.Band(i), yScale.Span(0, v)).Background(barColor).Radius(2)
						}
					}).Height(190).Gap(8).Label("随堂测验正确率图表")
				}

				if len(subs) > 0 {
					ui.Text(c, "学生答卷明细").FontSize(12).Bold()
					for _, sub := range subs {
						subLabel := sub.StudentID
						for _, st := range a.listStudents() {
							if st.ID == sub.StudentID {
								subLabel = st.MachineName
								if st.StudentNo != "" {
									subLabel += " (" + st.StudentNo + ")"
								}
								break
							}
						}
						ui.Row(c).Key("sub-"+sub.StudentID).Padding(4, 8).Background(pal.Canvas).Border(1, pal.CardBorder).Radius(2).AlignItems(ui.Center).Children(func() {
							ui.Text(c, subLabel).FontSize(12).Grow(1)
							ui.Text(c, fmt.Sprintf("%d / %d 分", sub.Score, sub.MaxScore)).FontSize(12).Bold()
						})
					}
				}
			})
		})
	})
}

func (a *App) publishExam() {
	if a.opts.Exam == nil {
		return
	}
	questions := append([]core.ExamQuestion(nil), a.examDraftQuestions...)
	if len(questions) == 0 {
		q := a.buildCurrentDraftQuestion("q1")
		if strings.TrimSpace(q.Prompt) == "" {
			a.statusNote = "请先填写题干或添加题目后再发布"
			return
		}
		questions = []core.ExamQuestion{q}
	}
	title := strings.TrimSpace(a.examTitleInput)
	if title == "" {
		title = "随堂测验"
	}
	paper := core.ExamPaper{
		ID:          fmt.Sprintf("exam-%d", time.Now().Unix()),
		Title:       title,
		DurationSec: int(a.examDurInput) * 60,
		Questions:   questions,
	}
	a.opts.Exam.PublishPaper(paper)
	if a.store != nil {
		_ = a.store.SaveExamPaper(paper)
	}
	a.examPublished = true
	// Students get the paper without correct answers; grading happens here.
	a.sendControlEnvelope(protocol.MsgExamPublish, exam.RedactedPaper(paper), nil)
	a.statusNote = fmt.Sprintf("已发布随堂测验: %s (%d 题)", paper.Title, len(questions))
}

func (a *App) buildCurrentDraftQuestion(id string) core.ExamQuestion {
	kind := core.QuestionKind(a.examKindInput)
	if kind == "" {
		kind = core.QuestionSingle
	}
	pts := int(a.examPointsInput)
	if pts <= 0 {
		pts = 20
	}
	var opts []string
	if kind != core.QuestionEssay {
		opts = splitCSV(a.examOptionsInput)
	}
	if kind == core.QuestionBool && len(opts) == 0 {
		opts = []string{"对", "错"}
	}
	ans := splitCSV(a.examAnswerInput)
	return core.ExamQuestion{
		ID:      id,
		Kind:    kind,
		Prompt:  a.examPromptInput,
		Options: opts,
		Answer:  ans,
		Points:  pts,
	}
}

func (a *App) addDraftQuestion() {
	id := fmt.Sprintf("q%d", len(a.examDraftQuestions)+1)
	q := a.buildCurrentDraftQuestion(id)
	if strings.TrimSpace(q.Prompt) == "" {
		a.statusNote = "请先填写题干内容"
		return
	}
	a.examDraftQuestions = append(a.examDraftQuestions, q)
	a.statusNote = fmt.Sprintf("已添加第 %d 题到待发布试卷", len(a.examDraftQuestions))
}

func (a *App) exportExamCSV(paperID string) {
	type csvExporter interface {
		ExportResultsCSV(paperID string) ([]byte, error)
	}
	exp, ok := a.opts.Exam.(csvExporter)
	if !ok {
		return
	}
	data, err := exp.ExportResultsCSV(paperID)
	if err != nil {
		a.statusNote = "导出成绩失败: " + err.Error()
		return
	}
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
			Title:       "导出随堂测验成绩 (CSV)",
			DefaultPath: paperID + "_results.csv",
			Filters:     []mygo.FileFilter{{Name: "CSV 表格", Extensions: []string{"csv"}}},
		})
		if err != nil || path == "" {
			return
		}
		note := "成绩已导出: " + path
		if werr := os.WriteFile(path, data, 0o644); werr != nil {
			note = "导出写入失败: " + werr.Error()
		}
		a.SetStatusNote(note)
		a.Invalidate()
	}()
}

func examKindDisplay(kind string) string {
	switch core.QuestionKind(kind) {
	case core.QuestionMulti:
		return "多选题"
	case core.QuestionBool:
		return "判断题"
	case core.QuestionEssay:
		return "简答题"
	default:
		return "单选题"
	}
}

func examKindValue(label string) string {
	switch label {
	case "多选题":
		return string(core.QuestionMulti)
	case "判断题":
		return string(core.QuestionBool)
	case "简答题":
		return string(core.QuestionEssay)
	default:
		return string(core.QuestionSingle)
	}
}
