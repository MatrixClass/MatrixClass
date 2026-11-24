package teacher

import (
	"image"
	"image/color"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"nexlab/internal/core"
)

// UpdateMonitorFrame pushes a decoded student monitor video frame into the Monitor Wall tile.
func (a *App) UpdateMonitorFrame(studentID string, frame *core.VideoFrame) {
	if studentID == "" || frame == nil || frame.Width <= 0 || frame.Height <= 0 {
		return
	}
	a.mu.Lock()
	w := a.win
	if w == nil {
		bmp := a.ensureTileBitmap(studentID, frame.Width, frame.Height)
		updateBitmapFromFrame(bmp, frame)
		a.mu.Unlock()
		return
	}
	a.pendingMonitor[studentID] = frame
	a.mu.Unlock()
	a.scheduleVideoFramesUpdate(w)
}

// UpdateRemoteFrame updates the live Remote Control interactive screen bitmap.
func (a *App) UpdateRemoteFrame(studentID string, frame *core.VideoFrame) {
	if studentID == "" || frame == nil || frame.Width <= 0 || frame.Height <= 0 {
		return
	}
	a.mu.Lock()
	if studentID != a.remoteTargetSnap {
		// Late frames from a previous (or no) remote-control target.
		a.mu.Unlock()
		return
	}
	w := a.win
	if w == nil {
		if a.remoteBitmap == nil {
			a.remoteBitmap = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, frame.Width, frame.Height)))
		}
		bmp := a.remoteBitmap
		updateBitmapFromFrame(bmp, frame)
		a.mu.Unlock()
		return
	}
	a.pendingRemote = frame
	a.mu.Unlock()
	a.scheduleVideoFramesUpdate(w)
}

// UpdateDemoFrame updates the Student Demo preview bitmap on the Teacher Console.
func (a *App) UpdateDemoFrame(studentID string, frame *core.VideoFrame) {
	if studentID == "" || frame == nil || frame.Width <= 0 || frame.Height <= 0 {
		return
	}
	a.mu.Lock()
	w := a.win
	if w == nil {
		if a.demoBitmap == nil {
			a.demoBitmap = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, frame.Width, frame.Height)))
		}
		bmp := a.demoBitmap
		updateBitmapFromFrame(bmp, frame)
		a.mu.Unlock()
		return
	}
	a.pendingDemo = frame
	a.mu.Unlock()
	a.scheduleVideoFramesUpdate(w)
}

func (a *App) scheduleVideoFramesUpdate(w *mygo.Window) {
	if w == nil || !a.videoUpdateScheduled.CompareAndSwap(false, true) {
		return
	}
	w.Update(func() {
		for {
			a.mu.Lock()
			var monBatch map[string]*core.VideoFrame
			if len(a.pendingMonitor) > 0 {
				monBatch = a.pendingMonitor
				a.pendingMonitor = make(map[string]*core.VideoFrame, len(monBatch))
			}
			remFrame := a.pendingRemote
			a.pendingRemote = nil
			demoFrame := a.pendingDemo
			a.pendingDemo = nil

			for id, f := range monBatch {
				bmp := a.ensureTileBitmap(id, f.Width, f.Height)
				updateBitmapFromFrame(bmp, f)
			}
			if remFrame != nil {
				if a.remoteBitmap == nil {
					a.remoteBitmap = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, remFrame.Width, remFrame.Height)))
				}
				updateBitmapFromFrame(a.remoteBitmap, remFrame)
			}
			if demoFrame != nil {
				if a.demoBitmap == nil {
					a.demoBitmap = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, demoFrame.Width, demoFrame.Height)))
				}
				updateBitmapFromFrame(a.demoBitmap, demoFrame)
			}
			hasMore := len(a.pendingMonitor) > 0 || a.pendingRemote != nil || a.pendingDemo != nil
			if !hasMore {
				a.videoUpdateScheduled.Store(false)
			}
			a.mu.Unlock()
			if !hasMore {
				break
			}
		}
	})
}

func updateBitmapFromFrame(bmp *ui.Bitmap, frame *core.VideoFrame) {
	if bmp == nil || frame == nil {
		return
	}
	if len(frame.NV12Y) > 0 && len(frame.NV12UV) > 0 && frame.StrideY > 0 && frame.StrideUV > 0 {
		cw, ch := (frame.Width+1)/2, (frame.Height+1)/2
		minY := frame.StrideY*(frame.Height-1) + frame.Width
		minUV := frame.StrideUV*(ch-1) + 2*cw
		if frame.StrideY >= frame.Width && frame.StrideUV >= 2*cw && frame.StrideUV%2 == 0 && len(frame.NV12Y) >= minY && len(frame.NV12UV) >= minUV {
			bmp.UpdateYUV(&ui.YUVImage{
				Format:  ui.NV12,
				Width:   frame.Width,
				Height:  frame.Height,
				Planes:  [3][]byte{frame.NV12Y, frame.NV12UV},
				Strides: [3]int{frame.StrideY, frame.StrideUV},
				Matrix:  ui.BT709,
			})
			return
		}
	}
	if len(frame.Data) >= frame.Width*frame.Height*4 {
		img := &image.RGBA{
			Pix:    frame.Data,
			Stride: frame.Width * 4,
			Rect:   image.Rect(0, 0, frame.Width, frame.Height),
		}
		bmp.Update(img)
	}
}

func (a *App) ensureTileBitmap(studentID string, w, h int) *ui.Bitmap {
	if bmp, ok := a.monitorTiles[studentID]; ok && bmp != nil {
		return bmp
	}
	img := placeholderScreenRGBA(w, h, len(a.monitorTiles)+1)
	bmp := ui.NewBitmap(img)
	a.monitorTiles[studentID] = bmp
	return bmp
}

func placeholderScreenRGBA(w, h, seed int) *image.RGBA {
	if w <= 0 {
		w = 240
	}
	if h <= 0 {
		h = 135
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	shade := uint8(36 + (seed*11)%24)
	for y := range h {
		for x := range w {
			c := color.RGBA{R: shade, G: shade, B: shade, A: 255}
			if y < 18 {
				c = color.RGBA{R: 26, G: 26, B: 26, A: 255}
			} else if x > 14 && x < w-14 && y > 26 && y < h-18 {
				c = color.RGBA{R: 242, G: 242, B: 242, A: 255}
				if y < 40 {
					c = color.RGBA{R: 64, G: 64, B: 64, A: 255}
				}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}
