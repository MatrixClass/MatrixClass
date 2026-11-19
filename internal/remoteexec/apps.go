package remoteexec

import "sort"

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
