//go:build darwin

package remoteexec

import (
	"os"
	"path/filepath"
)

func discoverApplications() []Application {
	return discoverAppBundles(darwinAppRoots())
}

func darwinAppRoots() []appBundleRoot {
	var roots []appBundleRoot
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		roots = append(roots, appBundleRoot{filepath.Join(home, "Applications"), "applications-user"})
	}
	return append(roots,
		appBundleRoot{"/Applications", "applications"},
		appBundleRoot{"/System/Applications", "applications-system"},
	)
}
