package student

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"nexlab/internal/core"
	"nexlab/internal/ui/theme"
)

func (a *App) buildHomeworkView(c *ui.Context, pal theme.Palette) {
	var fileTasks []*core.TransferTask
	if a.opts.Files != nil {
		fileTasks = a.opts.Files.ListTasks()
	}
	ui.Column(c).Fill().Padding(16).Gap(10).Background(pal.Canvas).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).DragWindow().Children(func() {
			ui.Text(c, "作业提交").FontSize(16).Bold().TextColor(pal.Accent)
			ui.Spacer(c)
			if ui.Button(c, "返回").Padding(4, 10).Clicked() {
				a.OpenMode(ModeBar)
			}
		})

		ui.Column(c).FillWidth().Padding(10).Radius(3).Background(pal.Card).Border(1, pal.CardBorder).Gap(6).Children(func() {
			ui.Text(c, "提交作业文件至教师端").FontSize(12).Bold()
			ui.Row(c).FillWidth().Gap(8).Children(func() {
				ui.TextInput(c, &a.homeworkPath).Grow(1).Placeholder("作业文件路径...")
				if ui.Button(c, "浏览").Padding(4, 10).Clicked() {
					a.openHomeworkPicker()
				}
				submitButton := ui.PrimaryButton(c, "上传作业").Disabled(a.homeworkSubmitting).Padding(4, 14)
				if submitButton.Clicked() {
					a.SubmitHomework()
				}
			})
		})

		if len(fileTasks) > 0 {
			ui.Scroll(c.Key("student-hw-history")).Grow(1).FillWidth().ScrollbarAxes(false, true).Scrollbars(true).KeepScrollbars(true).Children(func() {
				ui.Column(c).FillWidth().Padding(8).Radius(3).Background(pal.Card).Border(1, pal.CardBorder).Gap(4).Children(func() {
					ui.Text(c, fmt.Sprintf("提交记录 (%d)", len(fileTasks))).FontSize(11).Bold().TextColor(pal.TextMuted)
					for i := len(fileTasks) - 1; i >= 0; i-- {
						tk := fileTasks[i]
						ui.Row(c).Key("st-file-" + tk.ID).FillWidth().AlignItems(ui.Center).Gap(6).Children(func() {
							ui.Badge(c, transferDirectionLabel(tk.Direction))
							ui.Text(c, tk.FileName).FontSize(11).Grow(1).SingleLine()
							ui.Text(c, transferStatusLabel(tk.Status)).FontSize(10).TextColor(pal.TextMuted)
							if tk.Status == "completed" && tk.LocalPath != "" {
								if ui.Button(c, "打开").Padding(2, 8).Clicked() {
									_ = mygo.Shell.OpenPath(tk.LocalPath)
								}
							}
						})
					}
				})
			})
		} else {
			ui.Box(c).Grow(1).FillWidth().Center().Background(pal.Card).Border(1, pal.CardBorder).Radius(3).Children(func() {
				ui.Text(c, "选择作业文件后点击「上传作业」提交给教师").FontSize(12).TextColor(pal.TextMuted)
			})
		}
	})
}

func (a *App) SubmitHomework() {
	if a.opts.Files == nil {
		return
	}
	path := strings.TrimSpace(a.homeworkPath)
	if path == "" {
		a.statusNote = "请先选择作业文件"
		return
	}
	if a.homeworkSubmitting {
		return
	}
	a.homeworkSubmitting = true
	a.statusNote = "正在上传作业..."
	ctx, ok := a.lifecycleContext()
	if !ok {
		a.homeworkSubmitting = false
		return
	}
	go func() {
		task, err := a.opts.Files.SubmitHomework(ctx, a.studentID, path)
		if ctx.Err() != nil {
			return
		}
		note := ""
		if err != nil {
			note = "作业上传失败: " + err.Error()
		} else {
			note = "作业已提交: " + task.FileName
		}
		a.mu.Lock()
		if a.lifecycleClosed {
			a.mu.Unlock()
			return
		}
		a.homeworkSubmitting = false
		a.mu.Unlock()
		a.SetStatusNote(note)
	}()
}

func (a *App) openHomeworkPicker() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Title: "选择要提交的作业文件",
		})
		if err != nil || len(paths) == 0 {
			return
		}
		a.mu.Lock()
		a.homeworkPath = paths[0]
		a.mu.Unlock()
		a.Invalidate()
	}()
}

// OpenHomeworkSubmitWindow opens the standalone homework submission popup.
func (a *App) OpenHomeworkSubmitWindow() {
	a.mu.Lock()
	a.homeworkCollecting = true
	a.homeworkSubmitNote = "教师已发起作业收取，请提交作业"
	w := a.win
	hw := a.homeworkSubmitWin
	a.mu.Unlock()

	if w == nil {
		a.mu.Lock()
		a.mode = ModeHomeworkSubmit
		a.mu.Unlock()
		a.syncWindow()
		return
	}
	if hw != nil {
		hw.Update(func() {
			hw.SetAlwaysOnTop(true)
			hw.Show()
			hw.Focus()
		})
		return
	}
	w.Update(func() {
		a.mu.Lock()
		existing := a.homeworkSubmitWin
		a.mu.Unlock()
		if existing != nil {
			existing.SetAlwaysOnTop(true)
			existing.Show()
			existing.Focus()
			return
		}
		newHW := mygo.NewWindow(mygo.WindowOptions{
			Title:       "提交作业",
			Width:       480,
			Height:      420,
			MinWidth:    380,
			MinHeight:   340,
			Frameless:   false,
			AlwaysOnTop: true,
			Content:     ui.View(a.HomeworkSubmitView),
		})
		newHW.OnFileDrop(func(e *mygo.FileDropEvent) {
			if e == nil || len(e.Paths) == 0 {
				return
			}
			newHW.Update(func() {
				a.addHomeworkFiles(e.Paths)
				a.homeworkSubmitNote = fmt.Sprintf("已拖入 %d 个文件", len(e.Paths))
			})
		})
		newHW.OnClosed(func() {
			a.mu.Lock()
			if a.homeworkSubmitWin == newHW {
				a.homeworkSubmitWin = nil
			}
			a.mu.Unlock()
		})
		a.mu.Lock()
		a.homeworkSubmitWin = newHW
		a.mu.Unlock()
	})
}

// CloseHomeworkSubmitWindow closes the homework submission popup (called on teacher end).
func (a *App) CloseHomeworkSubmitWindow() {
	a.mu.Lock()
	a.homeworkCollecting = false
	a.homeworkSubmitNote = "教师已结束作业收取"
	hw := a.homeworkSubmitWin
	a.mu.Unlock()

	if hw != nil {
		hw.Update(func() {
			a.homeworkSubmitNote = "教师已结束作业收取，窗口将关闭"
		})
		ctx, ok := a.lifecycleContext()
		if ok {
			go func() {
				timer := time.NewTimer(1500 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
				}
				a.mu.Lock()
				curHW := a.homeworkSubmitWin
				closed := a.lifecycleClosed
				a.mu.Unlock()
				if !closed && curHW == hw {
					hw.Close()
				}
			}()
		}
	}

	a.mu.Lock()
	if a.mode == ModeHomeworkSubmit {
		a.mode = ModeBar
	}
	a.mu.Unlock()
	a.syncWindow()
}

func (a *App) HomeworkSubmitView(c *ui.Context) {
	a.mu.Lock()
	dark := a.dark
	a.mu.Unlock()
	pal := theme.GetPalette(dark)
	c.SetTheme(theme.Theme(dark))
	ui.Column(c).Fill().Background(pal.Canvas).Children(func() {
		a.buildHomeworkSubmitContent(c, pal)
	})
}

func (a *App) buildHomeworkSubmitContent(c *ui.Context, pal theme.Palette) {
	ui.Column(c).Fill().Padding(14).Gap(10).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "提交作业").FontSize(15).Bold()
			ui.Badge(c, "教师正在收取").Background(pal.Selection).TextColor(pal.Accent)
		})

		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, fmt.Sprintf("待提交文件 (%d)", len(a.homeworkQueue))).FontSize(12).Bold()
			ui.Spacer(c)
			if ui.Button(c, "添加文件...").Padding(4, 12).Clicked() {
				a.openHomeworkQueuePicker()
			}
			if len(a.homeworkQueue) > 0 {
				if ui.Button(c, "清空").Padding(4, 10).Clicked() {
					a.homeworkQueue = nil
				}
			}
		})

		queueScroll := ui.Scroll(c.Key("hw-queue")).Grow(1).FillWidth().Background(pal.Card).Radius(3).Padding(8).Gap(6).ScrollbarAxes(false, true).Scrollbars(true).KeepScrollbars(true)
		if queueScroll.FileDragOver() {
			queueScroll.Border(2, pal.Accent)
		} else {
			queueScroll.Border(1, pal.CardBorder)
		}
		if dropped := queueScroll.DroppedFiles(); len(dropped) > 0 {
			a.addHomeworkFiles(dropped)
			a.homeworkSubmitNote = fmt.Sprintf("已拖入 %d 个文件", len(dropped))
		}
		queueScroll.Children(func() {
			if len(a.homeworkQueue) == 0 {
				ui.Box(c).Fill().Center().Padding(20).Children(func() {
					ui.Text(c, "将作业文件拖入此处，或点击「添加文件...」").FontSize(12).TextColor(pal.TextMuted)
				})
				return
			}
			for idx, p := range a.homeworkQueue {
				idx, p := idx, p
				ui.Row(c).Key("hq-"+p).FillWidth().Padding(6, 8).Radius(2).Background(pal.Canvas).Border(1, pal.CardBorder).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, filepath.Base(p)).FontSize(12).Bold()
					ui.Text(c, p).FontSize(11).TextColor(pal.TextMuted).Grow(1).SingleLine()
					if ui.Button(c, "移除").Padding(2, 8).Clicked() {
						a.removeHomeworkFile(idx)
					}
				})
			}
		})

		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, a.homeworkSubmitNote).FontSize(11).TextColor(pal.TextMuted).Grow(1).SingleLine()
			submitLabel := fmt.Sprintf("提交 (%d)", len(a.homeworkQueue))
			if a.homeworkQueueBusy {
				submitLabel = "正在提交..."
			}
			if ui.PrimaryButton(c, submitLabel).Disabled(a.homeworkQueueBusy || len(a.homeworkQueue) == 0).Padding(5, 16).Clicked() {
				a.SubmitHomeworkQueue()
			}
		})
	})
}

func (a *App) addHomeworkFiles(paths []string) {
	existing := make(map[string]bool, len(a.homeworkQueue))
	for _, p := range a.homeworkQueue {
		existing[p] = true
	}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p != "" && !existing[p] {
			existing[p] = true
			a.homeworkQueue = append(a.homeworkQueue, p)
		}
	}
}

func (a *App) removeHomeworkFile(idx int) {
	if idx < 0 || idx >= len(a.homeworkQueue) {
		return
	}
	a.homeworkQueue = append(a.homeworkQueue[:idx], a.homeworkQueue[idx+1:]...)
}

func (a *App) openHomeworkQueuePicker() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Title:    "选择要提交的作业文件",
			Multiple: true,
		})
		if err != nil || len(paths) == 0 {
			return
		}
		a.mu.Lock()
		hw := a.homeworkSubmitWin
		w := a.win
		a.mu.Unlock()
		apply := func() {
			a.addHomeworkFiles(paths)
			a.homeworkSubmitNote = fmt.Sprintf("已添加 %d 个文件", len(paths))
		}
		if hw != nil {
			hw.Update(apply)
		} else if w != nil {
			w.Update(apply)
		} else {
			apply()
		}
	}()
}

func (a *App) SubmitHomeworkQueue() {
	if a.homeworkQueueBusy {
		return
	}
	if a.opts.Files == nil || len(a.homeworkQueue) == 0 {
		a.homeworkSubmitNote = "请先添加作业文件"
		return
	}
	queue := append([]string(nil), a.homeworkQueue...)
	a.homeworkSubmitNote = fmt.Sprintf("正在上传 %d 个作业文件...", len(queue))
	ctx, ok := a.lifecycleContext()
	if !ok {
		return
	}
	a.homeworkQueueBusy = true
	go func() {
		sent := 0
		var lastErr error
		for _, p := range queue {
			if _, err := a.opts.Files.SubmitHomework(ctx, a.studentID, p); err != nil {
				if ctx.Err() != nil {
					a.mu.Lock()
					a.homeworkQueueBusy = false
					a.mu.Unlock()
					return
				}
				lastErr = err
			} else {
				sent++
			}
		}
		a.mu.Lock()
		a.homeworkQueueBusy = false
		if a.lifecycleClosed {
			a.mu.Unlock()
			return
		}
		if lastErr != nil {
			a.homeworkSubmitNote = fmt.Sprintf("提交完成 %d/%d（部分失败: %v）", sent, len(queue), lastErr)
		} else {
			a.homeworkSubmitNote = fmt.Sprintf("已成功提交 %d 个作业文件", sent)
			a.homeworkQueue = nil
		}
		hw := a.homeworkSubmitWin
		a.mu.Unlock()
		if hw != nil {
			hw.Invalidate()
		}
		a.Invalidate()
	}()
}
