package theme

import (
	"bytes"
	"image"
	"image/color"
	"image/png"

	"github.com/egoist/mygo/ui"
)

// Palette defines the semantic colors used across Teacher and Student consoles,
// modeled after a classic blue-gray desktop chrome.
type Palette struct {
	Dark          bool
	ChromeBar     ui.Color
	ChromeRail    ui.Color
	ChromeHover   ui.Color
	ChromeActive  ui.Color
	Canvas        ui.Color
	Card          ui.Color
	CardBorder    ui.Color
	Selection     ui.Color
	Accent        ui.Color
	AccentText    ui.Color
	Success       ui.Color
	Warning       ui.Color
	Danger        ui.Color
	Info          ui.Color
	StatusBarBg   ui.Color
	StatusBarText ui.Color
	TheatreBg     ui.Color
	LockBg        ui.Color
	Text          ui.Color
	TextMuted     ui.Color
}

// LightPalette returns the light blue-gray desktop palette.
func LightPalette() Palette {
	return Palette{
		Dark:          false,
		ChromeBar:     ui.Hex("#EAF1FB"),
		ChromeRail:    ui.Hex("#F3F7FD"),
		ChromeHover:   ui.Hex("#DCE8F8"),
		ChromeActive:  ui.Hex("#D5E3F6"),
		Canvas:        ui.Hex("#FFFFFF"),
		Card:          ui.Hex("#F8FAFD"),
		CardBorder:    ui.Hex("#C7D6EA"),
		Selection:     ui.Hex("#D5E3F6"),
		Accent:        ui.Hex("#2B579A"),
		AccentText:    ui.Hex("#FFFFFF"),
		Success:       ui.Hex("#1E8E3E"),
		Warning:       ui.Hex("#D97706"),
		Danger:        ui.Hex("#C62828"),
		Info:          ui.Hex("#0284C7"),
		StatusBarBg:   ui.Hex("#DCE7F7"),
		StatusBarText: ui.Hex("#1E3A5F"),
		TheatreBg:     ui.Hex("#111827"),
		LockBg:        ui.Hex("#0B1120"),
		Text:          ui.Hex("#1E293B"),
		TextMuted:     ui.Hex("#5B6B82"),
	}
}

// DarkPalette returns the high-contrast dark variant for low-light classrooms.
func DarkPalette() Palette {
	return Palette{
		Dark:          true,
		ChromeBar:     ui.Hex("#182232"),
		ChromeRail:    ui.Hex("#141C29"),
		ChromeHover:   ui.Hex("#243247"),
		ChromeActive:  ui.Hex("#2B3D57"),
		Canvas:        ui.Hex("#0F172A"),
		Card:          ui.Hex("#1E293B"),
		CardBorder:    ui.Hex("#334155"),
		Selection:     ui.Hex("#263B59"),
		Accent:        ui.Hex("#3B82F6"),
		AccentText:    ui.Hex("#FFFFFF"),
		Success:       ui.Hex("#22C55E"),
		Warning:       ui.Hex("#F59E0B"),
		Danger:        ui.Hex("#EF4444"),
		Info:          ui.Hex("#38BDF8"),
		StatusBarBg:   ui.Hex("#151E2E"),
		StatusBarText: ui.Hex("#CBD5E1"),
		TheatreBg:     ui.Hex("#05080F"),
		LockBg:        ui.Hex("#030712"),
		Text:          ui.Hex("#E2E8F0"),
		TextMuted:     ui.Hex("#94A3B8"),
	}
}

// GetPalette returns the light or dark classroom palette.
func GetPalette(dark bool) Palette {
	if dark {
		return DarkPalette()
	}
	return LightPalette()
}

// Light returns a MyGo UI Theme configured with light desktop colors.
func Light() *ui.Theme {
	p := LightPalette()
	t := ui.LightTheme()
	t.Dark = false
	t.Background = p.Canvas
	t.Surface = p.ChromeBar
	t.SurfaceHover = p.ChromeHover
	t.SurfacePressed = p.ChromeActive
	t.Border = p.CardBorder
	t.Text = p.Text
	t.TextMuted = p.TextMuted
	t.Accent = p.Accent
	t.AccentHover = ui.Hex("#234982")
	t.AccentPressed = ui.Hex("#1B3966")
	t.AccentText = p.AccentText
	t.InactiveAccent = ui.Hex("#6482AD")
	t.Danger = p.Danger
	t.Warning = p.Warning
	t.Success = p.Success
	t.Selection = p.Selection
	t.Focus = p.Accent
	t.Radius = 6
	t.Spacing = 4
	t.FontSize = 13
	return t
}

// Dark returns the dark variant of the NexLab classroom theme.
func Dark() *ui.Theme {
	p := DarkPalette()
	t := ui.DarkTheme()
	t.Dark = true
	t.Background = p.Canvas
	t.Surface = p.ChromeBar
	t.SurfaceHover = p.ChromeHover
	t.SurfacePressed = p.ChromeActive
	t.Border = p.CardBorder
	t.Text = p.Text
	t.TextMuted = p.TextMuted
	t.Accent = p.Accent
	t.AccentHover = ui.Hex("#2563EB")
	t.AccentPressed = ui.Hex("#1D4ED8")
	t.AccentText = p.AccentText
	t.InactiveAccent = ui.Hex("#475569")
	t.Danger = p.Danger
	t.Warning = p.Warning
	t.Success = p.Success
	t.Selection = p.Selection
	t.Focus = p.Accent
	t.Radius = 6
	t.Spacing = 4
	t.FontSize = 13
	return t
}

// Theme returns the light or dark ui.Theme.
func Theme(dark bool) *ui.Theme {
	if dark {
		return Dark()
	}
	return Light()
}

// MustSVG parses an inline SVG string using ui.MustParseSVG.
func MustSVG(markup string) *ui.SVG {
	return ui.MustParseSVG([]byte(markup))
}

// Colorful SVG icons rendered via ui.Image(c, icon).
var (
	IconBroadcast = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <rect x="2" y="3" width="20" height="14" rx="2.5" fill="#2B579A"/>
  <rect x="4" y="5" width="16" height="10" rx="1" fill="#60A5FA"/>
  <path d="M9 17l-1.5 3.5h9L15 17z" fill="#64748B"/>
  <circle cx="12" cy="10" r="2.2" fill="#FFFFFF"/>
  <path d="M7.5 10a4.5 4.5 0 0 1 9 0" fill="none" stroke="#FBBF24" stroke-width="1.6" stroke-linecap="round"/>
</svg>`)

	IconVoice = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <rect x="9" y="2.5" width="6" height="11" rx="3" fill="#0EA5E9"/>
  <path d="M5.5 10.5a6.5 6.5 0 0 0 13 0" fill="none" stroke="#2B579A" stroke-width="2" stroke-linecap="round"/>
  <path d="M12 17v4M8.5 21h7" stroke="#475569" stroke-width="2" stroke-linecap="round"/>
  <circle cx="12" cy="6" r="1.5" fill="#BAE6FD"/>
</svg>`)

	IconStudentDemo = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <rect x="2" y="4" width="14" height="11" rx="2" fill="#10B981"/>
  <rect x="4" y="6" width="10" height="7" rx="1" fill="#D1FAE5"/>
  <circle cx="18.5" cy="8.5" r="3" fill="#F59E0B"/>
  <path d="M14 19c0-2.5 2-4 4.5-4s3.5 1.5 3.5 4" fill="#D97706"/>
  <path d="M9 15v4M6 19h6" stroke="#475569" stroke-width="1.8" stroke-linecap="round"/>
</svg>`)

	IconMyTheatre = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <rect x="2" y="4" width="20" height="16" rx="2.5" fill="#7C3AED"/>
  <rect x="4" y="6" width="16" height="12" rx="1.5" fill="#1E1B4B"/>
  <polygon points="10,8.5 16.5,12 10,15.5" fill="#F43F5E"/>
</svg>`)

	IconMonitor = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <rect x="2" y="3" width="9" height="8" rx="1.5" fill="#2563EB"/>
  <rect x="13" y="3" width="9" height="8" rx="1.5" fill="#0EA5E9"/>
  <rect x="2" y="13" width="9" height="8" rx="1.5" fill="#10B981"/>
  <rect x="13" y="13" width="9" height="8" rx="1.5" fill="#6366F1"/>
  <rect x="3.5" y="4.5" width="6" height="5" fill="#DBEAFE"/>
  <rect x="14.5" y="4.5" width="6" height="5" fill="#E0F2FE"/>
</svg>`)

	IconRemoteControl = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <rect x="2" y="3" width="18" height="13" rx="2" fill="#334155"/>
  <rect x="4" y="5" width="14" height="9" rx="1" fill="#38BDF8"/>
  <polygon points="13,10 21,15 16.5,16.5 15,21" fill="#F59E0B" stroke="#FFFFFF" stroke-width="1"/>
</svg>`)

	IconLockScreen = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <path d="M7 10V7a5 5 0 0 1 10 0v3" fill="none" stroke="#64748B" stroke-width="2.2" stroke-linecap="round"/>
  <rect x="4" y="10" width="16" height="11" rx="2.5" fill="#EF4444"/>
  <circle cx="12" cy="15" r="1.8" fill="#FFFFFF"/>
  <rect x="11.2" y="16" width="1.6" height="3" rx="0.8" fill="#FFFFFF"/>
</svg>`)

	IconFileDistribute = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <path d="M3 6a2 2 0 0 1 2-2h5l2 2h7a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" fill="#F59E0B"/>
  <path d="M3 9h18v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" fill="#FBBF24"/>
  <path d="M12 11v6M9.5 14.5L12 17l2.5-2.5" fill="none" stroke="#1E293B" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>
</svg>`)

	IconCollectHomework = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <rect x="3" y="4" width="18" height="16" rx="2.5" fill="#14B8A6"/>
  <rect x="6" y="7" width="12" height="10" rx="1" fill="#CCFBF1"/>
  <path d="M12 15V9M9.5 11.5L12 9l2.5 2.5" fill="none" stroke="#0F766E" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>
</svg>`)

	IconExam = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <rect x="4" y="2.5" width="16" height="19" rx="2" fill="#3B82F6"/>
  <rect x="6.5" y="5" width="11" height="14" rx="1" fill="#FFFFFF"/>
  <path d="M8.5 9h7M8.5 12.5h5M8.5 16l1.5 1.5 3-3" fill="none" stroke="#1D4ED8" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/>
</svg>`)

	IconCheckIn = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <circle cx="12" cy="12" r="9.5" fill="#10B981"/>
  <path d="M7.5 12.5l3 3 6-6.5" fill="none" stroke="#FFFFFF" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"/>
</svg>`)

	IconHandRaise = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <path d="M7 13V6.5a1.2 1.2 0 0 1 2.4 0V11V4.5a1.2 1.2 0 0 1 2.4 0V11V5.5a1.2 1.2 0 0 1 2.4 0V11V7.5a1.2 1.2 0 0 1 2.4 0V15a5 5 0 0 1-5 5h-1a4.5 4.5 0 0 1-4.5-4.5V13z" fill="#F59E0B" stroke="#B45309" stroke-width="1.2"/>
</svg>`)

	IconChat = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <path d="M4 4h16a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H8l-4 3.5V6a2 2 0 0 1 2-2z" fill="#0284C7"/>
  <circle cx="8.5" cy="11" r="1.2" fill="#FFFFFF"/>
  <circle cx="12" cy="11" r="1.2" fill="#FFFFFF"/>
  <circle cx="15.5" cy="11" r="1.2" fill="#FFFFFF"/>
</svg>`)

	IconSettings = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <circle cx="12" cy="12" r="8.5" fill="#64748B"/>
  <circle cx="12" cy="12" r="5" fill="#E2E8F0"/>
  <circle cx="12" cy="12" r="2.2" fill="#2B579A"/>
</svg>`)

	IconSeat = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <rect x="3" y="4" width="18" height="12" rx="2" fill="#2B579A"/>
  <rect x="5" y="6" width="14" height="8" rx="1" fill="#93C5FD"/>
  <rect x="9" y="17" width="6" height="2.5" rx="1" fill="#64748B"/>
</svg>`)

	IconPower = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <circle cx="12" cy="12" r="9.5" fill="#DC2626"/>
  <path d="M12 6.5v5.5M8.5 8.8a5 5 0 1 0 7 0" fill="none" stroke="#FFFFFF" stroke-width="2" stroke-linecap="round"/>
</svg>`)

	IconThemeToggle = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <circle cx="12" cy="12" r="9" fill="#1E293B"/>
  <path d="M12 3a9 9 0 0 0 0 18z" fill="#FBBF24"/>
</svg>`)
)

// AppIconPNG generates an in-memory 32x32 PNG icon for windows and tray icons.
func AppIconPNG(teacher bool) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	bg := color.NRGBA{R: 0x2B, G: 0x57, B: 0x9A, A: 0xFF}
	if !teacher {
		bg = color.NRGBA{R: 0x02, G: 0x84, B: 0xC7, A: 0xFF}
	}
	inner := color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
	accent := color.NRGBA{R: 0x60, G: 0xA5, B: 0xFA, A: 0xFF}

	for y := range 32 {
		for x := range 32 {
			if x >= 2 && x < 30 && y >= 2 && y < 30 {
				img.SetNRGBA(x, y, bg)
			}
			if x >= 6 && x < 26 && y >= 7 && y < 21 {
				img.SetNRGBA(x, y, inner)
			}
			if x >= 8 && x < 24 && y >= 9 && y < 19 {
				img.SetNRGBA(x, y, accent)
			}
			if x >= 11 && x < 21 && y >= 23 && y < 26 {
				img.SetNRGBA(x, y, inner)
			}
		}
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
