package theme

import (
	"bytes"
	"image"
	"image/color"
	"image/png"

	"github.com/egoist/mygo/ui"
)

// Palette defines the restrained monochrome (black/white/gray) desktop palette
// used across Teacher and Student consoles.
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

// LightPalette returns a crisp neutral monochrome light desktop palette.
func LightPalette() Palette {
	return Palette{
		Dark:          false,
		ChromeBar:     ui.Hex("#F2F2F2"),
		ChromeRail:    ui.Hex("#F7F7F7"),
		ChromeHover:   ui.Hex("#E6E6E6"),
		ChromeActive:  ui.Hex("#DCDCDC"),
		Canvas:        ui.Hex("#FFFFFF"),
		Card:          ui.Hex("#FAFAFA"),
		CardBorder:    ui.Hex("#D4D4D4"),
		Selection:     ui.Hex("#E4E4E4"),
		Accent:        ui.Hex("#1F1F1F"),
		AccentText:    ui.Hex("#FFFFFF"),
		Success:       ui.Hex("#262626"),
		Warning:       ui.Hex("#525252"),
		Danger:        ui.Hex("#141414"),
		Info:          ui.Hex("#404040"),
		StatusBarBg:   ui.Hex("#ECECEC"),
		StatusBarText: ui.Hex("#262626"),
		TheatreBg:     ui.Hex("#111111"),
		LockBg:        ui.Hex("#0A0A0A"),
		Text:          ui.Hex("#171717"),
		TextMuted:     ui.Hex("#6B6B6B"),
	}
}

// DarkPalette returns a neutral monochrome dark palette.
func DarkPalette() Palette {
	return Palette{
		Dark:          true,
		ChromeBar:     ui.Hex("#1D1D1D"),
		ChromeRail:    ui.Hex("#181818"),
		ChromeHover:   ui.Hex("#2A2A2A"),
		ChromeActive:  ui.Hex("#333333"),
		Canvas:        ui.Hex("#121212"),
		Card:          ui.Hex("#1B1B1B"),
		CardBorder:    ui.Hex("#323232"),
		Selection:     ui.Hex("#2D2D2D"),
		Accent:        ui.Hex("#EAEAEA"),
		AccentText:    ui.Hex("#111111"),
		Success:       ui.Hex("#D4D4D4"),
		Warning:       ui.Hex("#A3A3A3"),
		Danger:        ui.Hex("#F5F5F5"),
		Info:          ui.Hex("#BDBDBD"),
		StatusBarBg:   ui.Hex("#181818"),
		StatusBarText: ui.Hex("#D4D4D4"),
		TheatreBg:     ui.Hex("#0A0A0A"),
		LockBg:        ui.Hex("#050505"),
		Text:          ui.Hex("#ECECEC"),
		TextMuted:     ui.Hex("#8E8E8E"),
	}
}

// GetPalette returns the light or dark classroom palette.
func GetPalette(dark bool) Palette {
	if dark {
		return DarkPalette()
	}
	return LightPalette()
}

// Light returns a MyGo UI Theme configured with neutral light monochrome desktop colors.
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
	t.AccentHover = ui.Hex("#333333")
	t.AccentPressed = ui.Hex("#0D0D0D")
	t.AccentText = p.AccentText
	t.InactiveAccent = ui.Hex("#7A7A7A")
	t.Danger = p.Danger
	t.Warning = p.Warning
	t.Success = p.Success
	t.Selection = p.Selection
	t.Focus = p.Accent
	t.Radius = 3
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
	t.AccentHover = ui.Hex("#D4D4D4")
	t.AccentPressed = ui.Hex("#BDBDBD")
	t.AccentText = p.AccentText
	t.InactiveAccent = ui.Hex("#666666")
	t.Danger = p.Danger
	t.Warning = p.Warning
	t.Success = p.Success
	t.Selection = p.Selection
	t.Focus = p.Accent
	t.Radius = 3
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

// Restrainted monochrome line/surface SVG icons rendered via ui.Image(c, icon).
var (
	IconBroadcast = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <rect x="2.5" y="4" width="19" height="13" rx="1.5" fill="#FFFFFF" stroke="#262626" stroke-width="1.7"/>
  <rect x="4.5" y="6" width="15" height="9" fill="#E5E5E5"/>
  <path d="M9 17l-1 3h8l-1-3" fill="none" stroke="#262626" stroke-width="1.7" stroke-linejoin="round"/>
  <circle cx="12" cy="10.5" r="1.6" fill="#262626"/>
  <path d="M8.5 10.5a3.5 3.5 0 0 1 7 0" fill="none" stroke="#525252" stroke-width="1.5" stroke-linecap="round"/>
</svg>`)

	IconVoice = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <rect x="9" y="3" width="6" height="10" rx="3" fill="#E5E5E5" stroke="#262626" stroke-width="1.7"/>
  <path d="M6 10.5a6 6 0 0 0 12 0" fill="none" stroke="#262626" stroke-width="1.7" stroke-linecap="round"/>
  <path d="M12 16.5v4M8.5 20.5h7" stroke="#262626" stroke-width="1.7" stroke-linecap="round"/>
</svg>`)

	IconStudentDemo = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <rect x="2.5" y="4.5" width="13.5" height="10" rx="1.5" fill="#FFFFFF" stroke="#262626" stroke-width="1.7"/>
  <rect x="4.5" y="6.5" width="9.5" height="6" fill="#E5E5E5"/>
  <circle cx="19" cy="8.5" r="2.2" fill="#D4D4D4" stroke="#262626" stroke-width="1.6"/>
  <path d="M15.5 19c0-2.2 1.6-3.5 3.5-3.5s3 1.3 3 3.5" fill="none" stroke="#262626" stroke-width="1.6" stroke-linecap="round"/>
  <path d="M9.2 14.5v4M6.5 18.5h5.5" stroke="#262626" stroke-width="1.7" stroke-linecap="round"/>
</svg>`)

	IconMyTheatre = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <rect x="2.5" y="4.5" width="19" height="15" rx="1.5" fill="#FFFFFF" stroke="#262626" stroke-width="1.7"/>
  <rect x="4.5" y="6.5" width="15" height="11" fill="#E5E5E5"/>
  <polygon points="10,9 16,12 10,15" fill="#262626"/>
</svg>`)

	IconMonitor = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <rect x="3" y="3.5" width="8" height="7.5" rx="1" fill="#E5E5E5" stroke="#262626" stroke-width="1.6"/>
  <rect x="13" y="3.5" width="8" height="7.5" rx="1" fill="#FFFFFF" stroke="#262626" stroke-width="1.6"/>
  <rect x="3" y="13" width="8" height="7.5" rx="1" fill="#FFFFFF" stroke="#262626" stroke-width="1.6"/>
  <rect x="13" y="13" width="8" height="7.5" rx="1" fill="#E5E5E5" stroke="#262626" stroke-width="1.6"/>
</svg>`)

	IconRemoteControl = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <rect x="2.5" y="3.5" width="17" height="12" rx="1.5" fill="#FFFFFF" stroke="#262626" stroke-width="1.7"/>
  <rect x="4.5" y="5.5" width="13" height="8" fill="#E5E5E5"/>
  <polygon points="13,10.5 21,15 16.8,16.4 15.2,20.5" fill="#262626" stroke="#FFFFFF" stroke-width="1"/>
</svg>`)

	IconLockScreen = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <path d="M7.5 10V7a4.5 4.5 0 0 1 9 0v3" fill="none" stroke="#262626" stroke-width="1.8" stroke-linecap="round"/>
  <rect x="4.5" y="10" width="15" height="10.5" rx="1.5" fill="#262626"/>
  <circle cx="12" cy="14.5" r="1.5" fill="#FFFFFF"/>
  <rect x="11.3" y="15.5" width="1.4" height="2.8" fill="#FFFFFF"/>
</svg>`)

	IconFileDistribute = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <path d="M3 6.5A1.5 1.5 0 0 1 4.5 5h4.8l1.8 2h8.4A1.5 1.5 0 0 1 21 8.5v9a1.5 1.5 0 0 1-1.5 1.5h-15A1.5 1.5 0 0 1 3 17.5z" fill="#E5E5E5" stroke="#262626" stroke-width="1.7" stroke-linejoin="round"/>
  <path d="M12 10.5v5.5M9.5 13.8L12 16.3l2.5-2.5" fill="none" stroke="#262626" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/>
</svg>`)

	IconCollectHomework = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <rect x="3.5" y="4.5" width="17" height="15" rx="1.5" fill="#FFFFFF" stroke="#262626" stroke-width="1.7"/>
  <rect x="5.5" y="6.5" width="13" height="11" fill="#F0F0F0"/>
  <path d="M12 15.5V9.5M9.5 11.8L12 9.3l2.5 2.5" fill="none" stroke="#262626" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/>
</svg>`)

	IconExam = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <rect x="4.5" y="3" width="15" height="18" rx="1.5" fill="#FFFFFF" stroke="#262626" stroke-width="1.7"/>
  <path d="M8 8h8M8 12h6M8 16l1.6 1.6 3.2-3.2" fill="none" stroke="#262626" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/>
</svg>`)

	IconCheckIn = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <circle cx="12" cy="12" r="9" fill="#E5E5E5" stroke="#262626" stroke-width="1.7"/>
  <path d="M8 12.5l2.8 2.8 5.4-5.8" fill="none" stroke="#262626" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"/>
</svg>`)

	IconHandRaise = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <path d="M7 13V6.8a1.1 1.1 0 0 1 2.2 0V11V4.8a1.1 1.1 0 0 1 2.2 0V11V5.8a1.1 1.1 0 0 1 2.2 0V11V7.8a1.1 1.1 0 0 1 2.2 0V15a4.8 4.8 0 0 1-4.8 4.8h-.8A4.2 4.2 0 0 1 7 15.6V13z" fill="#E5E5E5" stroke="#262626" stroke-width="1.5"/>
</svg>`)

	IconChat = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <path d="M4 4.5h16a1.5 1.5 0 0 1 1.5 1.5v9.5A1.5 1.5 0 0 1 20 17H8l-4 3.5V6A1.5 1.5 0 0 1 5.5 4.5z" fill="#FFFFFF" stroke="#262626" stroke-width="1.7" stroke-linejoin="round"/>
  <circle cx="8.5" cy="10.8" r="1.1" fill="#262626"/>
  <circle cx="12" cy="10.8" r="1.1" fill="#262626"/>
  <circle cx="15.5" cy="10.8" r="1.1" fill="#262626"/>
</svg>`)

	IconSettings = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <circle cx="12" cy="12" r="7.5" fill="#E5E5E5" stroke="#262626" stroke-width="1.7"/>
  <circle cx="12" cy="12" r="3" fill="#FFFFFF" stroke="#262626" stroke-width="1.6"/>
</svg>`)

	IconSeat = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <rect x="3" y="4.5" width="18" height="11.5" rx="1.2" fill="#FFFFFF" stroke="#262626" stroke-width="1.7"/>
  <rect x="5" y="6.5" width="14" height="7.5" fill="#E5E5E5"/>
  <path d="M9 16v3h6v-3M7.5 19.5h9" fill="none" stroke="#262626" stroke-width="1.6" stroke-linecap="round"/>
</svg>`)

	IconPower = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <circle cx="12" cy="12" r="9" fill="#FFFFFF" stroke="#262626" stroke-width="1.7"/>
  <path d="M12 6.5v5.5M8.6 8.8a4.8 4.8 0 1 0 6.8 0" fill="none" stroke="#262626" stroke-width="1.8" stroke-linecap="round"/>
</svg>`)

	IconThemeToggle = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <circle cx="12" cy="12" r="8.5" fill="#FFFFFF" stroke="#262626" stroke-width="1.7"/>
  <path d="M12 3.5a8.5 8.5 0 0 0 0 17z" fill="#262626"/>
</svg>`)

	IconChevronUp = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <path d="M6.5 14.5L12 9l5.5 5.5" fill="none" stroke="#262626" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
</svg>`)

	IconChevronDown = MustSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <path d="M6.5 9.5L12 15l5.5-5.5" fill="none" stroke="#262626" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
</svg>`)
)

// AppIconPNG generates an in-memory 32x32 PNG icon for windows and tray icons.
func AppIconPNG(teacher bool) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	bg := color.NRGBA{R: 0x1F, G: 0x1F, B: 0x1F, A: 0xFF}
	if !teacher {
		bg = color.NRGBA{R: 0x45, G: 0x45, B: 0x45, A: 0xFF}
	}
	inner := color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
	accent := color.NRGBA{R: 0xD4, G: 0xD4, B: 0xD4, A: 0xFF}

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
