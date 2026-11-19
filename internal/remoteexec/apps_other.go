//go:build !windows && !linux

package remoteexec

func discoverApplications() []Application { return nil }
