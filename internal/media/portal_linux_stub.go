//go:build !linux

package media

// Non-Linux counterpart of the Wayland controller lookup in wayland_linux.go.
// (The file name matches the Linux portal file family it belongs to; the build
// constraint above makes it compile everywhere except Linux.)

func waylandControllerOf(*ScreenCapturer) waylandController { return nil }
