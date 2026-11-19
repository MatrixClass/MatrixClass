//go:build !linux && !windows

package platform

import "runtime"

func Detect() Capabilities {
	return Capabilities{OS: runtime.GOOS, Architecture: runtime.GOARCH}
}
