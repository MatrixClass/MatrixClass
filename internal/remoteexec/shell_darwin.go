//go:build darwin

package remoteexec

func supportedShell(shell Shell) bool {
	return shell == ShellZsh || shell == ShellBash || shell == ShellSH
}

func ShellOptions() []string {
	return []string{string(ShellZsh), string(ShellBash), string(ShellSH)}
}

// DefaultShell is zsh, the login shell of every macOS release since 10.15.
func DefaultShell() Shell { return ShellZsh }
