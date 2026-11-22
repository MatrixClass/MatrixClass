//go:build !windows && !linux && !darwin

package remoteexec

func discoverApplications() []Application { return nil }
