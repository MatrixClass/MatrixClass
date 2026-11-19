package remoteexec

import (
	"sort"
	"strings"
)

func (s *Service) ListApplications() []Application {
	apps := discoverApplications()
	sort.Slice(apps, func(i, j int) bool {
		if apps[i].Name != apps[j].Name {
			return apps[i].Name < apps[j].Name
		}
		return apps[i].Executable < apps[j].Executable
	})
	return apps
}

func normalizeDisplayIcon(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	if value[0] == '"' {
		if end := strings.Index(value[1:], "\""); end >= 0 {
			value = value[1 : end+1]
		}
	}
	if comma := strings.LastIndex(value, ","); comma > 0 {
		value = value[:comma]
	}
	return strings.Trim(strings.TrimSpace(value), "\""), true
}
