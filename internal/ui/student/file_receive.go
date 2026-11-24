package student

import (
	"fmt"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"nexlab/internal/core"
	"nexlab/internal/protocol"
	"nexlab/internal/ui/theme"
)

type receiveFileItem struct {
	Task      core.TransferTask
	UpdatedAt time.Time
}

func transferDirectionLabel(dir core.TransferDirection) string {
	switch dir {
	case core.TransferSend:
		return "接收"
	case core.TransferSubmit:
		return "提交"
	case core.TransferCollect:
		return "收取"
	default:
		return string(dir)
	}
}

func transferStatusLabel(status string) string {
	switch status {
	case "completed":
		return "已完成"
	case "running", "transferring":
		return "传输中"
	case "pending":
		return "等待中"
	case "failed":
		return "失败"
	case "cancelled":
		return "已取消"
	case "retrying":
		return "重试中"
	default:
		return status
	}
}

// RecordIncomingFile updates the student download queue for teacher-distributed files
// and automatically opens the standalone file receive window.
func (a *App) RecordIncomingFile(task core.TransferTask) {
	if task.Direction != core.TransferSend {
		return
	}

	a.mu.Lock()
	now := time.Now()
	updated := false
	for i := range a.fileReceiveItems {
		if a.fileReceiveItems[i].Task.ID == task.ID {
			a.fileReceiveItems[i].Task = task
			a.fileReceiveItems[i].UpdatedAt = now
			updated = true
			break
		}
	}
	if !updated {
		for i := range a.fileReceiveItems {
			existing := a.fileReceiveItems[i].Task
			if existing.FileName == task.FileName && existing.RemotePath == task.RemotePath &&
				(existing.Status == "failed" || existing.Status == "retrying") {
				a.fileReceiveItems[i].Task = task
				a.fileReceiveItems[i].UpdatedAt = now
				updated = true
				break
			}
		}
	}
	if !updated {
		a.fileReceiveItems = append(a.fileReceiveItems, receiveFileItem{
			Task:      task,
			UpdatedAt: now,
		})
	}
	if task.Status == "completed" {
		a.statusNote = "文件传输完成: " + task.FileName
		a.fileReceiveNote = "已接收: " + task.FileName
	} else if task.Status == "failed" {
		a.fileReceiveNote = "接收失败: " + task.FileName
	} else {
		a.fileReceiveNote = "正在接收教师下发文件..."
	}
	w := a.win
	frw := a.fileReceiveWin
	a.mu.Unlock()

	if w != nil {
		if frw == nil {
			w.Update(func() {
				a.ensureFileReceiveWindowOnMain()
			})
		} else {
			frw.Invalidate()
		}
	}
	a.Invalidate()
}

// OpenFileReceiveWindow opens or focuses the standalone file download queue window.
func (a *App) OpenFileReceiveWindow() {
	a.mu.Lock()
	w := a.win
	frw := a.fileReceiveWin
	a.mu.Unlock()

	if frw != nil {
		frw.SetAlwaysOnTop(true)
		frw.Show()
		frw.Focus()
		frw.Invalidate()
		return
	}
	if w != nil {
		w.Update(func() {
			a.ensureFileReceiveWindowOnMain()
		})
	}
}

func (a *App) ensureFileReceiveWindowOnMain() {
	a.mu.Lock()
	frw := a.fileReceiveWin
	a.mu.Unlock()
	if frw != nil {
		frw.SetAlwaysOnTop(true)
		frw.Show()
		frw.Focus()
		frw.Invalidate()
		return
	}
	frw = mygo.NewWindow(mygo.WindowOptions{
		Title:       "文件接收队列",
		Width:       520,
		Height:      420,
		MinWidth:    420,
		MinHeight:   320,
		Frameless:   false,
		AlwaysOnTop: true,
		Content:     ui.View(a.FileReceiveView),
	})
	frw.OnClosed(func() {
		a.mu.Lock()
		if a.fileReceiveWin == frw {
			a.fileReceiveWin = nil
		}
		a.mu.Unlock()
	})
	a.mu.Lock()
	a.fileReceiveWin = frw
	a.mu.Unlock()
}

// CloseFileReceiveWindow closes the standalone file receive window if open.
func (a *App) CloseFileReceiveWindow() {
	a.mu.Lock()
	frw := a.fileReceiveWin
	a.mu.Unlock()
	if frw != nil {
		frw.Close()
	}
}

// ClearCompletedReceiveFiles removes completed entries from the download queue.
func (a *App) ClearCompletedReceiveFiles() {
	a.mu.Lock()
	filtered := a.fileReceiveItems[:0]
	for _, it := range a.fileReceiveItems {
		if it.Task.Status != "completed" {
			filtered = append(filtered, it)
		}
	}
	a.fileReceiveItems = filtered
	if len(a.fileReceiveItems) == 0 {
		a.fileReceiveNote = ""
	}
	frw := a.fileReceiveWin
	a.mu.Unlock()
	if frw != nil {
		frw.Invalidate()
	}
	a.Invalidate()
}

// RetryReceiveFile requests the teacher to re-distribute the specified file.
func (a *App) RetryReceiveFile(task core.TransferTask) {
	a.mu.Lock()
	for i := range a.fileReceiveItems {
		if a.fileReceiveItems[i].Task.ID == task.ID {
			a.fileReceiveItems[i].Task.Status = "retrying"
			a.fileReceiveItems[i].Task.Error = ""
			a.fileReceiveItems[i].UpdatedAt = time.Now()
			break
		}
	}
	a.fileReceiveNote = "正在向教师端请求重传: " + task.FileName
	frw := a.fileReceiveWin
	a.mu.Unlock()

	payload := map[string]any{
		"student_id":  a.studentID,
		"file_name":   task.FileName,
		"remote_path": task.RemotePath,
		"sha256":      task.SHA256,
		"auto_open":   task.AutoOpen,
	}
	sent := a.sendControl(protocol.MsgFileRetryReq, payload) == nil
	if !sent {
		a.mu.Lock()
		for i := range a.fileReceiveItems {
			if a.fileReceiveItems[i].Task.ID == task.ID {
				a.fileReceiveItems[i].Task.Status = "failed"
				a.fileReceiveItems[i].Task.Error = "未连接到教师端，无法发起重传"
				break
			}
		}
		a.fileReceiveNote = "重传请求发送失败：未连接到教师端"
		a.mu.Unlock()
	}
	if frw != nil {
		frw.Invalidate()
	}
	a.Invalidate()
}

func (a *App) FileReceiveView(c *ui.Context) {
	a.mu.Lock()
	dark := a.dark
	items := make([]receiveFileItem, len(a.fileReceiveItems))
	copy(items, a.fileReceiveItems)
	note := a.fileReceiveNote
	a.mu.Unlock()

	pal := theme.GetPalette(dark)
	c.SetTheme(theme.Theme(dark))

	doneCount, failCount, activeCount := 0, 0, 0
	for _, it := range items {
		switch it.Task.Status {
		case "completed":
			doneCount++
		case "failed":
			failCount++
		default:
			activeCount++
		}
	}

	ui.Column(c).Fill().Background(pal.Canvas).Padding(16).Gap(10).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).DragWindow().Children(func() {
			ui.Text(c, "接收教师下发文件").FontSize(15).Bold().TextColor(pal.Text)
			ui.Badge(c, fmt.Sprintf("共 %d 项", len(items)))
			if activeCount > 0 {
				ui.Badge(c, fmt.Sprintf("接收中 %d", activeCount)).Background(pal.Selection).TextColor(pal.Accent)
			}
			if doneCount > 0 {
				ui.Badge(c, fmt.Sprintf("完成 %d", doneCount))
			}
			if failCount > 0 {
				ui.Badge(c, fmt.Sprintf("失败 %d", failCount)).TextColor(pal.Danger)
			}
			ui.Spacer(c)
			if doneCount > 0 {
				if ui.Button(c, "清空已完成").Padding(4, 10).Clicked() {
					a.ClearCompletedReceiveFiles()
				}
			}
			if ui.Button(c, "关闭").Padding(4, 10).Clicked() {
				a.CloseFileReceiveWindow()
			}
		})

		if note != "" {
			ui.Text(c, note).FontSize(12).TextColor(pal.TextMuted).SingleLine()
		}

		if len(items) == 0 {
			ui.Box(c).Grow(1).Center().Background(pal.Card).Border(1, pal.CardBorder).Radius(4).Children(func() {
				ui.Text(c, "暂无教师下发文件").FontSize(13).TextColor(pal.TextMuted)
			})
			return
		}

		ui.Scroll(c).Grow(1).Gap(8).ScrollbarAxes(false, true).Scrollbars(true).KeepScrollbars(true).Children(func() {
			for _, it := range items {
				tk := it.Task
				ratio := 0.0
				if tk.TotalBytes > 0 {
					ratio = float64(tk.DoneBytes) / float64(tk.TotalBytes)
				} else if tk.Status == "completed" {
					ratio = 1.0
				}
				if ratio < 0 {
					ratio = 0
				}
				if ratio > 1 {
					ratio = 1
				}

				statusLabel := "等待接收"
				statusColor := pal.TextMuted
				switch tk.Status {
				case "running":
					statusLabel = fmt.Sprintf("接收中 %.0f%%", ratio*100)
					statusColor = pal.Accent
				case "completed":
					statusLabel = "已完成"
					statusColor = pal.Success
				case "retrying":
					statusLabel = "请求重传中..."
					statusColor = pal.Accent
				case "failed":
					statusLabel = "接收失败"
					statusColor = pal.Danger
				}

				savePath := tk.LocalPath
				if savePath == "" {
					savePath = tk.RemotePath
				}

				ui.Column(c).Key("recv-"+tk.ID).FillWidth().Padding(10).Gap(6).
					Background(pal.Card).Border(1, pal.CardBorder).Radius(4).Children(func() {
					ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
						ui.Text(c, tk.FileName).FontSize(13).Bold().TextColor(pal.Text).Grow(1).SingleLine()
						ui.Text(c, statusLabel).FontSize(11).Bold().TextColor(statusColor)
						if tk.Status == "completed" && tk.LocalPath != "" {
							if ui.Button(c, "打开").Padding(3, 10).Clicked() {
								_ = mygo.Shell.OpenPath(tk.LocalPath)
							}
						}
						if tk.Status == "failed" {
							if ui.PrimaryButton(c, "重试").Padding(3, 10).Clicked() {
								a.RetryReceiveFile(tk)
							}
						}
						if tk.Status == "running" || tk.Status == "transferring" {
							if ui.Button(c, "取消").Padding(3, 10).Clicked() {
								_ = a.opts.Files.CancelTask(tk.ID)
							}
						}
					})

					ui.Progress(c, ratio).Height(6)

					ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
						if savePath != "" {
							ui.Text(c, savePath).FontSize(11).TextColor(pal.TextMuted).Grow(1).SingleLine()
						} else {
							ui.Spacer(c)
						}
						ui.Text(c, fmt.Sprintf("%s / %s", formatStudentBytes(tk.DoneBytes), formatStudentBytes(tk.TotalBytes))).
							FontSize(11).TextColor(pal.TextMuted)
					})

					if tk.Error != "" {
						ui.Text(c, tk.Error).FontSize(11).TextColor(pal.Danger).SingleLine()
					}
				})
			}
		})
	})
}

func formatStudentBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	const unit = 1024
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
