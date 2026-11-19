package platform

type Desktop string

const (
	DesktopUnknown Desktop = "unknown"
	DesktopX11     Desktop = "x11"
	DesktopWayland Desktop = "wayland"
)

type Capabilities struct {
	OS                string  `json:"os"`
	Architecture      string  `json:"architecture"`
	Desktop           Desktop `json:"desktop"`
	ScreenCapture     bool    `json:"screen_capture"`
	AudioCapture      bool    `json:"audio_capture"`
	RemoteInput       bool    `json:"remote_input"`
	ScreenLock        bool    `json:"screen_lock"`
	PowerControl      bool    `json:"power_control"`
	ProcessControl    bool    `json:"process_control"`
	AppDiscovery      bool    `json:"app_discovery"`
	CommandExecution  bool    `json:"command_execution"`
	SystemTray        bool    `json:"system_tray"`
	WaylandRestricted bool    `json:"wayland_restricted"`
}
