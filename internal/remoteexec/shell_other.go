//go:build !windows && !linux && !darwin

package remoteexec

func supportedShell(shell Shell) bool {
	return shell == ShellCMD || shell == ShellPowerShell
}

func ShellOptions() []string {
	return []string{string(ShellCMD), string(ShellPowerShell)}
}

func DefaultShell() Shell { return ShellCMD }
