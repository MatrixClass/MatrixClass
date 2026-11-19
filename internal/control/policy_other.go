//go:build !windows && !linux && !darwin

package control

type noopProcessInspector struct{}

func newPlatformProcessInspector() ProcessInspector {
	return &noopProcessInspector{}
}

func (n *noopProcessInspector) ListProcesses() ([]ProcessInfo, error) {
	return nil, nil
}

func (n *noopProcessInspector) TerminateProcess(pid uint32) error {
	return nil
}
