//go:build linux

package remoteexec

func supportedShell(shell Shell) bool {
	return shell == ShellSH || shell == ShellBash || shell == ShellPowerShell
}

func ShellOptions() []string {
	return []string{string(ShellSH), string(ShellBash), string(ShellPowerShell)}
}

func DefaultShell() Shell { return ShellSH }
