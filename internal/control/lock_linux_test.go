//go:build linux

package control

import "testing"

func TestLinuxLockCommandPrefersLoginctl(t *testing.T) {
	original := linuxLockLookPath
	defer func() { linuxLockLookPath = original }()
	linuxLockLookPath = func(name string) (string, error) {
		if name == "loginctl" {
			return "/usr/bin/loginctl", nil
		}
		return "", errLinuxCommandMissing
	}
	command, ok := linuxLockCommand()
	if !ok || command.name != "/usr/bin/loginctl" || len(command.args) != 1 || command.args[0] != "lock-session" {
		t.Fatalf("unexpected lock command: %+v ok=%v", command, ok)
	}
}
