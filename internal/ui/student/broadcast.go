package student

import (
	"image"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/video"

	"nexlab/internal/core"
	"nexlab/internal/ui/theme"
)

// UpdateVideoFrame uploads an incoming RGBA or NV12 frame onto the GPU bitmap.
func (a *App) UpdateVideoFrame(frame *core.VideoFrame) {
	if frame == nil {
		return
	}
	a.mu.Lock()
	w := a.win
	if w == nil {
		a.applyVideoFrameLocked(frame)
		a.lastFrameAt = time.Now()
		a.mu.Unlock()
		return
	}
	a.pendingFrame = frame
	a.mu.Unlock()

	if a.frameScheduled.CompareAndSwap(false, true) {
		w.Update(func() {
			a.frameScheduled.Store(false)
			a.mu.Lock()
			f := a.pendingFrame
			a.pendingFrame = nil
			if f != nil {
				a.applyVideoFrameLocked(f)
				a.lastFrameAt = time.Now()
			}
			a.mu.Unlock()
		})
	}
}

func (a *App) applyVideoFrameLocked(frame *core.VideoFrame) {
	if len(frame.NV12Y) > 0 && len(frame.NV12UV) > 0 {
		if a.videoBitmap == nil {
			a.videoBitmap = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, frame.Width, frame.Height)))
		}
		a.videoBitmap.UpdateYUV(&ui.YUVImage{
			Format:  ui.NV12,
			Width:   frame.Width,
			Height:  frame.Height,
			Planes:  [3][]byte{frame.NV12Y, frame.NV12UV},
			Strides: [3]int{frame.StrideY, frame.StrideUV},
			Matrix:  ui.BT709,
		})
	} else if len(frame.Data) >= frame.Width*frame.Height*4 && frame.Width > 0 && frame.Height > 0 {
		rgba := &image.RGBA{
			Pix:    frame.Data,
			Stride: frame.Width * 4,
			Rect:   image.Rect(0, 0, frame.Width, frame.Height),
		}
		if a.videoBitmap == nil {
			a.videoBitmap = ui.NewBitmap(rgba)
		} else {
			a.videoBitmap.Update(rgba)
		}
	}
}

// StopBroadcast ends the teacher broadcast and returns to the floating bar.
func (a *App) StopBroadcast() {
	a.mu.Lock()
	a.broadcastActive = false
	a.mu.Unlock()
	a.ShowFloatingBar()
}

func (a *App) StartBroadcast(windowed bool) {
	a.mu.Lock()
	a.broadcastWindow = windowed
	a.broadcastActive = true
	a.mode = ModeBroadcast
	a.statusNote = "正在接收教师端屏幕广播"
	w := a.win
	a.mu.Unlock()
	if w != nil {
		w.Update(func() {
			a.syncWindowOnMain(w, true, true)
		})
	}
}

// StartTheatre switches the student viewer to the teacher's synchronized video.
// The underlying video stream is shared with screen broadcast, but the UI mode
// remains distinct so a network theatre session is not presented as a broadcast.
func (a *App) StartTheatre(windowed bool) {
	a.mu.Lock()
	a.broadcastWindow = windowed
	a.mode = ModeTheatre
	a.statusNote = "正在接收网络影院视频"
	w := a.win
	a.mu.Unlock()
	if w != nil {
		w.Update(func() {
			a.syncWindowOnMain(w, true, true)
		})
	}
}

// SetTheatreState records the synchronized network theatre state. The media
// file is opened when the corresponding file-transfer task completes.
func (a *App) SetTheatreState(state core.TheatreState) {
	a.mu.Lock()
	streamChanged := strings.TrimSpace(state.StreamURL) != "" && strings.TrimSpace(state.StreamURL) != a.theatreStreamURL
	wasPlaying := a.theatrePlaying
	a.theatreMedia = filepath.Base(strings.TrimSpace(state.Media))
	a.theatreStreamURL = strings.TrimSpace(state.StreamURL)
	a.theatrePlaying = state.Playing
	a.statusNote = "网络影院同步播放: " + state.Media
	if !state.Playing && !state.Stopped {
		a.statusNote = "网络影院已暂停"
	}
	p := a.theatrePlayer
	position := state.Position
	a.mu.Unlock()
	if p != nil {
		if state.Playing && math.Abs(p.Position().Seconds()-position) > 0.75 {
			p.Seek(time.Duration(position * float64(time.Second)))
		}
		if state.Playing {
			p.Play()
		} else {
			p.Pause()
		}
	}
	if state.Playing && (streamChanged || a.theatrePlayer == nil) {
		a.StartTheatre(true)
		if state.StreamURL != "" {
			a.OpenTheatreStream(state.StreamURL, state.Position)
		}
	} else if state.Playing && !wasPlaying {
		a.StartTheatre(true)
	} else {
		if state.Stopped {
			a.ShowFloatingBar()
		}
	}
}

// OpenTheatreStream opens the teacher-hosted media stream without downloading
// the complete video to the student machine.
func (a *App) OpenTheatreStream(streamURL string, position float64) {
	streamURL = strings.TrimSpace(streamURL)
	if streamURL == "" {
		return
	}
	a.mu.Lock()
	w := a.win
	playing := a.theatrePlaying
	a.mu.Unlock()
	if w == nil {
		return
	}
	w.Update(func() {
		p, err := video.OpenURL(streamURL)
		if err != nil {
			a.SetStatusNote("网络影院流打开失败: " + err.Error())
			return
		}
		if a.theatrePlayer != nil {
			a.theatrePlayer.Close()
		}
		a.theatrePlayer = p.SetLoop(false)
		if position > 0 {
			a.theatrePlayer.Seek(time.Duration(position * float64(time.Second)))
		}
		if playing {
			a.theatrePlayer.Play()
		} else {
			a.theatrePlayer.Pause()
		}
		a.Invalidate()
	})
}

func (a *App) SetBroadcastWindowed(windowed bool) {
	a.mu.Lock()
	a.broadcastWindow = windowed
	w := a.win
	a.mu.Unlock()
	if w != nil {
		w.Update(func() {
			a.syncWindowOnMain(w, false, false)
		})
	}
}

func (a *App) buildBroadcastView(c *ui.Context, pal theme.Palette) {
	ui.Column(c).Fill().Background(pal.TheatreBg).Children(func() {
		ui.Row(c).FillWidth().Padding(6, 12).Background(pal.ChromeBar).AlignItems(ui.Center).Gap(8).DragWindow().Children(func() {
			a.mu.Lock()
			mode := a.mode
			a.mu.Unlock()
			title := "屏幕广播"
			if mode == ModeTheatre {
				title = "网络影院"
			}
			ui.Text(c, title).FontSize(12).Bold().TextColor(pal.Text)
			if a.broadcastWindow {
				ui.Badge(c, "窗口模式").Background(pal.Selection).TextColor(pal.Text)
			} else {
				ui.Badge(c, "全屏演示").Background(pal.Selection).TextColor(pal.Text)
			}
			ui.Spacer(c)
			if ui.Button(c, "切换窗口/全屏").Padding(3, 8).Clicked() {
				a.mu.Lock()
				a.broadcastWindow = !a.broadcastWindow
				a.mu.Unlock()
				a.syncWindow()
			}
			handLabel := "举手"
			if a.handRaised {
				handLabel = "取消举手"
			}
			if ui.Button(c, handLabel).Padding(3, 8).Clicked() {
				a.ToggleHandRaise()
			}
			if ui.Button(c, "返回悬浮条").Padding(3, 8).Clicked() {
				a.mu.Lock()
				a.mode = ModeBar
				a.mu.Unlock()
				a.syncWindow()
			}
		})

		ui.Column(c).Grow(1).FillWidth().Center().Padding(12).Children(func() {
			a.mu.Lock()
			mode := a.mode
			player := a.theatrePlayer
			a.mu.Unlock()
			if mode == ModeTheatre && player != nil {
				ui.Video(c, player).Fill().Fit(ui.Contain)
				return
			}
			a.mu.Lock()
			bmp := a.videoBitmap
			a.mu.Unlock()
			if bmp != nil {
				ui.Image(c, bmp).Fill().Fit(ui.Contain).Radius(2)
			} else {
				ui.Text(c, "等待教师端屏幕或网络影院画面...").FontSize(14).TextColor(ui.Hex("#D4D4D4"))
				ui.Text(c, "当前无视频信号").FontSize(12).TextColor(ui.Hex("#8E8E8E"))
			}
		})
	})
}

func (a *App) HasVideoFrame() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.videoBitmap != nil
}
