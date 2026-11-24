package teacher

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"nexlab/internal/core"
	"nexlab/internal/protocol"
	"nexlab/internal/ui/theme"
)

func (a *App) buildFilesTab(c *ui.Context, pal theme.Palette) {
	var tasks []*core.TransferTask
	if a.opts.Files != nil {
		tasks = a.opts.Files.ListTasks()
	}

	ui.Column(c).Grow(1).Padding(12).Gap(10).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "文件传输记录").FontSize(15).Bold()
			ui.Spacer(c)
			if ui.PrimaryButton(c, "打开文件分发窗口").Padding(5, 14).Clicked() {
				a.OpenFileDistributeWindow()
			}
			if a.collectingHomework {
				if ui.Button(c, "结束收取").Padding(5, 12).Clicked() {
					a.EndCollectHomework()
				}
				ui.Badge(c, "正在收取作业...").Background(pal.Selection).TextColor(pal.Accent)
			} else {
				if ui.Button(c, "收取作业").Padding(5, 12).Clicked() {
					a.CollectHomework()
				}
			}
		})

		if len(tasks) == 0 {
			ui.Box(c).Grow(1).Center().Children(func() {
				ui.Text(c, "暂无传输记录").FontSize(13).TextColor(pal.TextMuted)
			})
			return
		}

		ui.Scroll(c).Grow(1).Gap(6).ScrollbarAxes(false, true).Scrollbars(true).KeepScrollbars(true).Children(func() {
			for _, tk := range tasks {
				ratio := 0.0
				if tk.TotalBytes > 0 {
					ratio = float64(tk.DoneBytes) / float64(tk.TotalBytes)
				}
				statusLabel := tk.Status
				switch tk.Status {
				case "completed":
					statusLabel = "已完成"
				case "running":
					statusLabel = "传输中"
				case "failed":
					statusLabel = "失败"
				case "pending":
					statusLabel = "等待中"
				case "cancelled":
					statusLabel = "已取消"
				}
				dirLabel := "发送"
				if tk.Direction == core.TransferSubmit {
					dirLabel = "提交"
				} else if tk.Direction == core.TransferCollect {
					dirLabel = "收取"
				}
				ui.Column(c).Key("task-"+tk.ID).Background(pal.Card).Border(1, pal.CardBorder).Radius(3).Padding(10).Gap(6).Children(func() {
					ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
						ui.Badge(c, dirLabel)
						ui.Text(c, tk.FileName).FontSize(13).Bold().Grow(1).SingleLine()
						ui.Text(c, fmt.Sprintf("%s · %.0f%%", statusLabel, ratio*100)).FontSize(11).TextColor(pal.TextMuted)
					})
					ui.Progress(c, ratio).Height(6)
					if tk.RemotePath != "" {
						ui.Text(c, fmt.Sprintf("%s → %s", tk.LocalPath, tk.RemotePath)).FontSize(11).TextColor(pal.TextMuted).SingleLine()
					} else {
						ui.Text(c, tk.LocalPath).FontSize(11).TextColor(pal.TextMuted).SingleLine()
					}
				})
			}
		})
	})
}

func (a *App) DistributeFile() {
	if a.opts.Files == nil {
		return
	}
	targets := a.resolveFileTargets()
	if len(targets) == 0 {
		a.statusNote = "没有可分发的在线学生目标"
		return
	}
	localPath := a.distributePath
	remoteDir := a.remoteDir
	autoOpen := a.autoOpenFile
	a.statusNote = "正在分发文件..."
	ctx, ok := a.lifecycleContext()
	if !ok {
		return
	}
	go func() {
		task, err := a.opts.Files.DistributeFile(ctx, localPath, remoteDir, targets, autoOpen)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			a.SetStatusNote("文件分发失败: " + err.Error())
		} else {
			a.SetStatusNote("文件已分发: " + task.FileName)
		}
	}()
}

// OpenFileDistributeWindow opens the standalone File Distribution popup window.
func (a *App) OpenFileDistributeWindow() {
	a.mu.Lock()
	w := a.win
	fw := a.fileDistWin
	a.mu.Unlock()

	if w == nil {
		a.fileDistModal = true
		a.statusNote = "已打开文件分发窗口"
		return
	}
	if fw != nil {
		fw.Show()
		fw.Focus()
		return
	}
	fw = mygo.NewWindow(mygo.WindowOptions{
		Title:     "文件分发 - 上传队列",
		Width:     560,
		Height:    500,
		MinWidth:  460,
		MinHeight: 400,
		Frameless: false,
		Content:   ui.View(a.FileDistributeView),
	})
	fw.OnFileDrop(func(e *mygo.FileDropEvent) {
		if e == nil || len(e.Paths) == 0 {
			return
		}
		fw.Update(func() {
			a.AddQueuedFiles(e.Paths)
			a.statusNote = fmt.Sprintf("已拖入 %d 个文件到分发队列", len(e.Paths))
		})
	})
	fw.OnClosed(func() {
		a.mu.Lock()
		if a.fileDistWin == fw {
			a.fileDistWin = nil
		}
		a.mu.Unlock()
	})
	a.mu.Lock()
	a.fileDistWin = fw
	a.mu.Unlock()
	a.statusNote = "已打开文件分发窗口"
}

func (a *App) FileDistributeView(c *ui.Context) {
	a.mu.Lock()
	dark := a.dark
	a.mu.Unlock()
	pal := theme.GetPalette(dark)
	c.SetTheme(theme.Theme(dark))
	ui.Column(c).Fill().Background(pal.Canvas).Children(func() {
		a.buildFileDistributeContent(c, pal)
	})
}

func (a *App) buildFileDistributeContent(c *ui.Context, pal theme.Palette) {
	targets := a.resolveFileTargets()
	ui.Column(c).Fill().Padding(14).Gap(10).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "文件分发队列").FontSize(15).Bold()
			ui.Badge(c, fmt.Sprintf("目标: %d 台", len(targets))).Background(pal.Selection).TextColor(pal.Accent)
			ui.Spacer(c)
			ui.Checkbox(c, &a.fileTargetAll, "全部在线学生")
		})

		ui.Column(c).FillWidth().Padding(10).Radius(3).Background(pal.Card).Border(1, pal.CardBorder).Gap(6).Children(func() {
			ui.Text(c, "学生机接收路径（留空则默认发送到学生机桌面）").FontSize(11).TextColor(pal.TextMuted)
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.TextInput(c, &a.remoteDir).Placeholder("默认: 学生机桌面 (Desktop)").Grow(1)
				ui.Checkbox(c, &a.autoOpenFile, "到达后自动打开")
			})
		})

		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, fmt.Sprintf("待发送队列 (%d)", len(a.fileQueue))).FontSize(12).Bold()
			ui.Spacer(c)
			if ui.Button(c, "添加文件...").Padding(4, 12).Clicked() {
				a.openQueueFilePicker()
			}
			if ui.Button(c, "添加文件夹...").Padding(4, 12).Clicked() {
				a.openQueueFolderPicker()
			}
			if len(a.fileQueue) > 0 {
				if ui.Button(c, "清空队列").Padding(4, 10).Clicked() {
					a.fileQueue = nil
				}
			}
		})

		queueScroll := ui.Scroll(c.Key("file-dist-queue")).Grow(1).FillWidth().Background(pal.Card).Radius(3).Padding(8).Gap(6).ScrollbarAxes(false, true).Scrollbars(true).KeepScrollbars(true)
		if queueScroll.FileDragOver() {
			queueScroll.Border(2, pal.Accent)
		} else {
			queueScroll.Border(1, pal.CardBorder)
		}
		if dropped := queueScroll.DroppedFiles(); len(dropped) > 0 {
			a.AddQueuedFiles(dropped)
			a.statusNote = fmt.Sprintf("已拖入 %d 个文件到分发队列", len(dropped))
		}
		queueScroll.Children(func() {
			if len(a.fileQueue) == 0 {
				ui.Box(c).Fill().Center().Padding(20).Children(func() {
					ui.Text(c, "将文件或文件夹拖入此处，或点击“添加文件/文件夹...”加入队列").FontSize(12).TextColor(pal.TextMuted)
				})
				return
			}
			for idx, p := range a.fileQueue {
				idx, p := idx, p
				isDir := false
				if info, err := os.Stat(p); err == nil && info.IsDir() {
					isDir = true
				}
				ui.Row(c).Key("fq-"+p).FillWidth().Padding(6, 8).Radius(2).Background(pal.Canvas).Border(1, pal.CardBorder).AlignItems(ui.Center).Gap(8).Children(func() {
					if isDir {
						ui.Badge(c, "文件夹").Background(pal.Selection).TextColor(pal.Accent)
					}
					ui.Text(c, filepath.Base(p)).FontSize(12).Bold()
					ui.Text(c, p).FontSize(11).TextColor(pal.TextMuted).Grow(1).SingleLine()
					if ui.Button(c, "移除").Padding(2, 8).Clicked() {
						a.removeQueuedFile(idx)
					}
				})
			}
		})

		a.buildFileDistributeProgress(c, pal)

		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, a.statusNote).FontSize(11).TextColor(pal.TextMuted).Grow(1).SingleLine()
			if ui.Button(c, "关闭").Padding(5, 14).Clicked() {
				a.closeFileDistributeWindow()
			}
			sendLbl := fmt.Sprintf("发送 (%d)", len(a.fileQueue))
			if ui.PrimaryButton(c, sendLbl).Padding(5, 16).Clicked() {
				a.DistributeQueue()
			}
		})
	})
}

func (a *App) buildFileDistributeProgress(c *ui.Context, pal theme.Palette) {
	if a.opts.Files == nil {
		return
	}
	var sendTasks []*core.TransferTask
	var totalBytes, doneBytes int64
	var completedCount int
	for _, tk := range a.opts.Files.ListTasks() {
		if tk.Direction != core.TransferSend || a.fileDistHidden[tk.ID] {
			continue
		}
		sendTasks = append(sendTasks, tk)
		totalBytes += tk.TotalBytes
		doneBytes += tk.DoneBytes
		if tk.Status == "completed" {
			completedCount++
		}
	}
	if len(sendTasks) == 0 && !a.fileSending {
		return
	}
	overallRatio := 0.0
	if totalBytes > 0 {
		overallRatio = float64(doneBytes) / float64(totalBytes)
	} else if len(sendTasks) > 0 && completedCount == len(sendTasks) {
		overallRatio = 1.0
	}
	if overallRatio > 1 {
		overallRatio = 1
	}
	ui.Column(c).FillWidth().Padding(10).Radius(3).Background(pal.Card).Border(1, pal.CardBorder).Gap(6).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "本次分发进度").FontSize(12).Bold()
			ui.Spacer(c)
			ui.Text(c, fmt.Sprintf("已完成 %d/%d · %.0f%%", completedCount, len(sendTasks), overallRatio*100)).FontSize(11).TextColor(pal.TextMuted)
		})
		ui.Progress(c, overallRatio).Height(6)
		maxShow := len(sendTasks)
		if maxShow > 4 {
			maxShow = 4
		}
		active := make([]*core.TransferTask, 0, len(sendTasks))
		for _, tk := range sendTasks {
			if tk.Status != "completed" {
				active = append(active, tk)
			}
		}
		for _, tk := range sendTasks {
			if tk.Status == "completed" {
				active = append(active, tk)
			}
		}
		for _, tk := range active[:maxShow] {
			ratio := 0.0
			if tk.TotalBytes > 0 {
				ratio = float64(tk.DoneBytes) / float64(tk.TotalBytes)
			} else if tk.Status == "completed" {
				ratio = 1.0
			}
			if ratio > 1 {
				ratio = 1
			}
			statusText := "发送中"
			switch tk.Status {
			case "completed":
				statusText = "已完成"
			case "failed":
				statusText = "失败"
			case "cancelled":
				statusText = "已取消"
			}
			ui.Column(c).Key("fprog-" + tk.ID).FillWidth().Gap(3).Children(func() {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).Children(func() {
					ui.Text(c, tk.FileName).FontSize(11).Grow(1).SingleLine()
					ui.Text(c, fmt.Sprintf("%s · %.0f%%", statusText, ratio*100)).FontSize(11).TextColor(pal.TextMuted)
					if tk.Status == "running" || tk.Status == "transferring" {
						if ui.Button(c, "取消").Padding(2, 8).Clicked() {
							_ = a.opts.Files.CancelTask(tk.ID)
						}
					}
				})
				ui.Progress(c, ratio).Height(4)
			})
		}
		if hidden := len(sendTasks) - maxShow; hidden > 0 {
			ui.Text(c, fmt.Sprintf("还有 %d 个文件未显示", hidden)).FontSize(11).TextColor(pal.TextMuted)
		}
	})
}

func (a *App) closeFileDistributeWindow() {
	a.fileDistModal = false
	a.mu.Lock()
	fw := a.fileDistWin
	a.mu.Unlock()
	if fw != nil {
		fw.Close()
	}
}

func (a *App) AddQueuedFiles(paths []string) {
	if len(paths) == 0 {
		return
	}
	existing := make(map[string]bool, len(a.fileQueue))
	for _, p := range a.fileQueue {
		existing[p] = true
	}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p != "" && !existing[p] {
			existing[p] = true
			a.fileQueue = append(a.fileQueue, p)
		}
	}
}

func (a *App) removeQueuedFile(idx int) {
	if idx < 0 || idx >= len(a.fileQueue) {
		return
	}
	a.fileQueue = append(a.fileQueue[:idx], a.fileQueue[idx+1:]...)
}

func (a *App) openQueueFilePicker() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Title:    "选择要添加到分发队列的文件",
			Multiple: true,
		})
		if err != nil || len(paths) == 0 {
			return
		}
		a.mu.Lock()
		fw := a.fileDistWin
		w := a.win
		a.mu.Unlock()
		apply := func() {
			a.AddQueuedFiles(paths)
			a.statusNote = fmt.Sprintf("已添加 %d 个文件到分发队列", len(paths))
		}
		if fw != nil {
			fw.Update(apply)
		} else if w != nil {
			w.Update(apply)
		} else {
			apply()
		}
	}()
}

func (a *App) openQueueFolderPicker() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Title:     "选择要添加到分发队列的文件夹",
			Directory: true,
			Multiple:  true,
		})
		if err != nil || len(paths) == 0 {
			return
		}
		a.mu.Lock()
		fw := a.fileDistWin
		w := a.win
		a.mu.Unlock()
		apply := func() {
			a.AddQueuedFiles(paths)
			a.statusNote = fmt.Sprintf("已添加 %d 个文件夹到分发队列", len(paths))
		}
		if fw != nil {
			fw.Update(apply)
		} else if w != nil {
			w.Update(apply)
		} else {
			apply()
		}
	}()
}

type queuedTransferItem struct {
	localPath string
	remoteDir string
}

func expandQueueEntries(queue []string, baseRemoteDir string) []queuedTransferItem {
	var items []queuedTransferItem
	for _, p := range queue {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		info, err := os.Stat(p)
		if err != nil || !info.IsDir() {
			items = append(items, queuedTransferItem{
				localPath: p,
				remoteDir: baseRemoteDir,
			})
			continue
		}
		parentDir := filepath.Dir(p)
		_ = filepath.WalkDir(p, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || d == nil || d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(parentDir, path)
			if err != nil {
				return nil
			}
			subDir := filepath.Dir(rel)
			targetDir := baseRemoteDir
			if subDir != "." && subDir != "" {
				targetDir = filepath.Join(baseRemoteDir, subDir)
			}
			items = append(items, queuedTransferItem{
				localPath: path,
				remoteDir: targetDir,
			})
			return nil
		})
	}
	return items
}

func (a *App) DistributeQueue() {
	if a.opts.Files == nil {
		return
	}
	targets := a.resolveFileTargets()
	if len(targets) == 0 {
		a.statusNote = "没有可分发的在线学生目标"
		return
	}
	queue := append([]string(nil), a.fileQueue...)
	if len(queue) == 0 && strings.TrimSpace(a.distributePath) != "" {
		queue = []string{strings.TrimSpace(a.distributePath)}
	}
	if len(queue) == 0 {
		a.statusNote = "请先添加文件到上传队列"
		return
	}
	remoteDir := strings.TrimSpace(a.remoteDir)
	items := expandQueueEntries(queue, remoteDir)
	if len(items) == 0 {
		a.statusNote = "队列中没有可发送的文件（空文件夹）"
		return
	}
	autoOpen := a.autoOpenFile
	a.mu.Lock()
	if a.fileSending {
		a.statusNote = "已有文件分发任务正在进行"
		a.mu.Unlock()
		return
	}
	a.fileSending = true
	a.statusNote = fmt.Sprintf("正在向 %d 台学生分发 %d 个文件...", len(targets), len(items))
	a.mu.Unlock()
	a.fileDistHidden = make(map[string]bool)
	for _, tk := range a.opts.Files.ListTasks() {
		if tk.Direction == core.TransferSend {
			a.fileDistHidden[tk.ID] = true
		}
	}
	ctx, ok := a.lifecycleContext()
	if !ok {
		a.mu.Lock()
		a.fileSending = false
		a.mu.Unlock()
		return
	}
	go func() {
		defer a.runOnUI(func() { a.fileSending = false })
		sent := 0
		var lastErr error
		for _, it := range items {
			if _, err := a.opts.Files.DistributeFile(ctx, it.localPath, it.remoteDir, targets, autoOpen); err != nil {
				if ctx.Err() != nil {
					return
				}
				lastErr = err
			} else {
				sent++
			}
		}
		if lastErr != nil {
			a.SetStatusNote(fmt.Sprintf("分发完成 %d/%d（部分失败: %v）", sent, len(items), lastErr))
		} else {
			a.SetStatusNote(fmt.Sprintf("已成功发送 %d 个文件至 %d 台学生", sent, len(targets)))
		}
		a.Invalidate()
	}()
}

func (a *App) CollectHomework() {
	if a.opts.Files == nil {
		return
	}
	if a.collectingHomework {
		a.EndCollectHomework()
		return
	}
	a.mu.Lock()
	w := a.win
	defaultDir := strings.TrimSpace(a.collectSaveDir)
	a.mu.Unlock()
	if defaultDir == "" {
		defaultDir = defaultDownloadsDir()
	}
	if w == nil {
		a.startCollectHomework(defaultDir)
		return
	}
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Title:       "选择作业保存目录（取消则保存到默认作业存放目录）",
			DefaultPath: defaultDir,
			Directory:   true,
		})
		chosen := defaultDir
		if err == nil && len(paths) > 0 && strings.TrimSpace(paths[0]) != "" {
			chosen = strings.TrimSpace(paths[0])
		}
		w.Update(func() {
			a.startCollectHomework(chosen)
		})
	}()
}

func (a *App) startCollectHomework(saveDir string) {
	if strings.TrimSpace(saveDir) == "" {
		saveDir = defaultDownloadsDir()
	}
	a.applyArchiveDir(saveDir)
	a.collectingHomework = true
	a.statusNote = fmt.Sprintf("已发起作业收取，保存目录: %s", saveDir)
	a.sendControlEnvelope(protocol.MsgHomeworkCollectStart, map[string]any{
		"channel": a.channel,
	}, nil)
}

func (a *App) applyArchiveDir(dir string) {
	dir = strings.TrimSpace(dir)
	if dir == "" || a.opts.Files == nil {
		return
	}
	if setter, ok := a.opts.Files.(interface{ SetArchiveDir(string) }); ok {
		setter.SetArchiveDir(dir)
	}
}

func defaultDownloadsDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, "Downloads")
	}
	return "submissions"
}

func (a *App) EndCollectHomework() {
	a.collectingHomework = false
	a.statusNote = "已结束作业收取"
	a.sendControlEnvelope(protocol.MsgHomeworkCollectEnd, map[string]any{
		"channel": a.channel,
	}, nil)
}

func (a *App) CollectingHomework() bool { return a.collectingHomework }

func (a *App) resolveFileTargets() []string {
	if !a.fileTargetAll {
		if targets := a.targetStudentIDs(); len(targets) > 0 {
			return targets
		}
	}
	var targets []string
	for _, st := range a.listStudents() {
		if st.Status != core.StatusOffline {
			targets = append(targets, st.ID)
		}
	}
	if len(targets) == 0 && a.selectedID != "" {
		targets = []string{a.selectedID}
	}
	return targets
}

func (a *App) openDistributeFilePicker() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Title: "选择要分发给学生的教学文件",
		})
		if err != nil || len(paths) == 0 {
			return
		}
		a.runOnUI(func() { a.distributePath = paths[0] })
	}()
}

func (a *App) openCollectDirPicker() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Title:     "选择作业回收保存目录",
			Directory: true,
		})
		if err != nil || len(paths) == 0 {
			return
		}
		a.runOnUI(func() { a.collectSaveDir = paths[0] })
	}()
}
