package teacher

import (
	"fmt"
	"image"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/video"

	"nexlab/internal/core"
	"nexlab/internal/protocol"
	"nexlab/internal/ui/theme"
)

// UpdateTheatreFrame updates the MyTheatre preview bitmap via YUV or RGBA data.
func (a *App) UpdateTheatreFrame(frame *core.VideoFrame) {
	if frame == nil || frame.Width <= 0 || frame.Height <= 0 {
		return
	}
	if a.theatreFrame == nil {
		a.theatreFrame = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, frame.Width, frame.Height)))
	}
	if len(frame.NV12Y) > 0 && len(frame.NV12UV) > 0 && frame.StrideY > 0 && frame.StrideUV > 0 {
		a.theatreFrame.UpdateYUV(&ui.YUVImage{
			Format:  ui.NV12,
			Width:   frame.Width,
			Height:  frame.Height,
			Planes:  [3][]byte{frame.NV12Y, frame.NV12UV},
			Strides: [3]int{frame.StrideY, frame.StrideUV},
			Matrix:  ui.BT709,
		})
		return
	}
	if len(frame.Data) >= frame.Width*frame.Height*4 {
		a.theatreFrame.Update(&image.RGBA{
			Pix:    frame.Data,
			Stride: frame.Width * 4,
			Rect:   image.Rect(0, 0, frame.Width, frame.Height),
		})
	}
}

func (a *App) buildMyTheatreTab(c *ui.Context, pal theme.Palette) {
	ui.Column(c).Grow(1).Padding(12).Gap(10).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "网络影院").FontSize(15).Bold()
			if len(a.theatreMedia) > 0 {
				ui.Select(c, &a.theatre.Media, a.theatreMedia).Width(260)
			} else {
				ui.Text(c, "尚未添加本地视频").FontSize(12).TextColor(pal.TextMuted)
			}
			ui.Spacer(c)
			if a.theatre.Playing {
				ui.Badge(c, "正在同步播放").Background(pal.Selection).TextColor(pal.Text)
			} else {
				ui.Badge(c, "就绪")
			}
		})

		// Video stage with a subtle idle-state label.
		ui.Box(c).Grow(1).MinHeight(280).Background(pal.TheatreBg).Radius(3).Center().Clip().Children(func() {
			if a.theatrePlayer != nil {
				if a.theatrePlayer.Ready() {
					a.theatre.Duration = a.theatrePlayer.Duration().Seconds()
					a.theatre.Position = a.theatrePlayer.Position().Seconds()
				}
				ui.Video(c, a.theatrePlayer).Fill().Fit(ui.Contain)
			} else if a.theatreFrame != nil {
				ui.Image(c, a.theatreFrame).Fill().Fit(ui.Contain)
			}
			if a.theatrePlayer == nil && (!a.theatre.Playing || a.theatreFrame == nil) {
				ui.Column(c).Center().Gap(6).Children(func() {
					ui.Text(c, "网络影院").FontSize(34).Bold().TextColor(ui.RGBA(255, 255, 255, 0.14))
					if a.theatre.Media == "" {
						ui.Text(c, "请点击下方「选择本地视频」添加媒体文件").FontSize(12).TextColor(ui.RGBA(212, 212, 212, 0.7))
					} else {
						ui.Text(c, fmt.Sprintf("当前影片：%s", a.theatre.Media)).FontSize(12).TextColor(ui.RGBA(212, 212, 212, 0.7))
					}
				})
			}
		})

		// Playback transport bar
		ui.Column(c).Background(pal.Card).Border(1, pal.CardBorder).Radius(3).Padding(10).Gap(8).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
				playLabel := "播放"
				if a.theatre.Playing {
					playLabel = "暂停"
				} else if a.theatre.Media != "" {
					playLabel = "继续播放"
				}
				playButton := ui.PrimaryButton(c, playLabel).Disabled(a.theatrePlayer == nil || strings.TrimSpace(a.theatre.Media) == "")
				if playButton.Clicked() {
					a.toggleTheatrePlayback()
				}
				if ui.Button(c, "停止").Clicked() {
					a.stopTheatrePlayback()
				}
				if ui.Button(c, "选择本地视频").Clicked() {
					a.openVideoPicker()
				}
				ui.Spacer(c)
				ui.Text(c, fmt.Sprintf("%02d:%02d / %02d:%02d",
					int(a.theatre.Position)/60, int(a.theatre.Position)%60,
					int(a.theatre.Duration)/60, int(a.theatre.Duration)%60,
				)).FontSize(12).TextColor(pal.TextMuted)
			})

			pos := a.theatre.Position
			if ui.Slider(c, &pos, 0, max(1, a.theatre.Duration)).Label("播放进度").Changed() {
				a.theatre.Position = pos
				if a.theatrePlayer != nil {
					a.theatrePlayer.Seek(time.Duration(pos * float64(time.Second)))
				}
				a.sendControlEnvelope(protocol.MsgTheatreControl, a.theatre, nil)
			}
		})
	})
}

func (a *App) toggleTheatrePlayback() {
	if a.theatrePlayer == nil || strings.TrimSpace(a.theatre.Media) == "" {
		a.theatre.Playing = false
		a.statusNote = "请先选择本地视频，再播放并分发"
		return
	}
	a.theatre.Playing = !a.theatre.Playing
	a.theatre.Stopped = false
	if a.theatrePlayer != nil {
		if a.theatre.Playing {
			a.theatrePlayer.Play()
		} else {
			a.theatrePlayer.Pause()
		}
	}
	if a.theatre.Playing {
		a.activeModeLabel = "网络影院"
		a.statusNote = "网络影院正在同步播放: " + a.theatre.Media
	} else {
		a.activeModeLabel = "待机监听"
		a.statusNote = "网络影院已暂停"
	}
	a.sendControlEnvelope(protocol.MsgTheatreControl, a.theatre, nil)
}

func (a *App) stopTheatrePlayback() {
	a.theatre.Playing = false
	a.theatre.Stopped = true
	a.theatre.Position = 0
	if a.theatrePlayer != nil {
		a.theatrePlayer.Pause()
		a.theatrePlayer.Seek(0)
	}
	a.activeModeLabel = "待机监听"
	a.statusNote = "网络影院已停止"
	a.sendControlEnvelope(protocol.MsgTheatreControl, a.theatre, nil)
}

func (a *App) openVideoPicker() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Title: "选择网络影院教学视频",
			Filters: []mygo.FileFilter{
				{Name: "视频文件", Extensions: []string{"mp4", "mkv", "avi", "mov"}},
			},
		})
		if err != nil || len(paths) == 0 {
			return
		}
		path := paths[0]
		a.runOnUI(func() { a.loadLocalVideo(path) })
	}()
}

func (a *App) loadLocalVideo(path string) {
	if a.theatrePlayer != nil {
		a.theatrePlayer.Close()
		a.theatrePlayer = nil
	}
	p, err := video.Open(path)
	if err != nil {
		a.theatre.Media = path
		a.statusNote = "视频打开失败 (改用屏幕演示): " + err.Error()
		return
	}
	p.SetVolume(a.volume / 100.0)
	a.theatrePlayer = p
	a.theatre.Media = path
	a.theatre.Playing = true
	a.activeModeLabel = "网络影院"
	a.statusNote = "网络影院正在播放: " + path
	a.theatre.StreamURL = a.theatreStreamURL
	a.sendControlEnvelope(protocol.MsgTheatreControl, a.theatre, nil)
}

func (a *App) TheatrePlaying() bool {
	return a.theatreFlag.Load()
}

func (a *App) TheatreMediaPath() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.theatre.Media
}

func (a *App) SetTheatreStreamURL(url string) {
	a.mu.Lock()
	a.theatreStreamURL = strings.TrimSpace(url)
	a.theatre.StreamURL = a.theatreStreamURL
	a.mu.Unlock()
}
