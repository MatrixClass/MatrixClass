//go:build windows

package remoteexec

import (
	"os"
	"path/filepath"
	"strings"
)

func discoverApplications() []Application {
	roots := []struct{ path, source string }{
		{os.Getenv("APPDATA") + `\Microsoft\Windows\Start Menu\Programs`, "start-menu"},
		{os.Getenv("ProgramData") + `\Microsoft\Windows\Start Menu\Programs`, "common-start-menu"},
		{os.Getenv("ProgramFiles"), "program-files"},
		{os.Getenv("ProgramFiles(x86)"), "program-files-x86"},
	}
	seen := make(map[string]bool)
	var apps []Application
	for _, root := range roots {
		if root.path == "" {
			continue
		}
		_ = filepath.Walk(root.path, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() || !strings.EqualFold(filepath.Ext(path), ".exe") {
				return nil
			}
			key := strings.ToLower(filepath.Clean(path))
			if seen[key] {
				return nil
			}
			seen[key] = true
			name := strings.TrimSuffix(info.Name(), filepath.Ext(info.Name()))
			apps = append(apps, Application{ID: applicationID(path), Name: name, Executable: path, Source: root.source})
			return nil
		})
	}
	return apps
}
