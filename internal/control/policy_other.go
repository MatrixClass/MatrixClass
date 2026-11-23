//go:build !windows && !linux && !darwin

package control

import "errors"

var ErrProcessControlUnsupportedPlatform = errors.New("control: process control is unsupported on this platform")

type noopProcessInspector struct{}

func newPlatformProcessInspector() ProcessInspector {
	return &noopProcessInspector{}
}

func (n *noopProcessInspector) ListProcesses() ([]ProcessInfo, error) {
	return nil, ErrProcessControlUnsupportedPlatform
}

func (n *noopProcessInspector) TerminateProcess(pid uint32) error {
	return ErrProcessControlUnsupportedPlatform
}

var platformProtectedProcesses = []string{"init", "launchd"}
