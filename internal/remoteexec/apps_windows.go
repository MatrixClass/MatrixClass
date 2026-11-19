//go:build windows

package remoteexec

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

var uninstallRoots = []struct {
	root   registry.Key
	path   string
	source string
}{
	{registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Uninstall`, "control-panel-user"},
	{registry.LOCAL_MACHINE, `Software\Microsoft\Windows\CurrentVersion\Uninstall`, "control-panel"},
	{registry.LOCAL_MACHINE, `Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`, "control-panel-32bit"},
}

func discoverApplications() []Application {
	seen := make(map[string]bool)
	var apps []Application
	for _, root := range uninstallRoots {
		key, err := registry.OpenKey(root.root, root.path, registry.READ)
		if err != nil {
			continue
		}
		names, _ := key.ReadSubKeyNames(-1)
		_ = key.Close()
		for _, name := range names {
			app, ok := readInstalledApplication(root.root, root.path+"\\"+name, root.source)
			if !ok {
				continue
			}
			identity := strings.ToLower(app.Name + "\x00" + app.Executable)
			if seen[identity] {
				continue
			}
			seen[identity] = true
			apps = append(apps, app)
		}
	}
	return apps
}

func readInstalledApplication(root registry.Key, path, source string) (Application, bool) {
	key, err := registry.OpenKey(root, path, registry.READ)
	if err != nil {
		return Application{}, false
	}
	defer key.Close()
	displayName, _, err := key.GetStringValue("DisplayName")
	if err != nil || strings.TrimSpace(displayName) == "" {
		return Application{}, false
	}
	icon, _, _ := key.GetStringValue("DisplayIcon")
	installDir, _, _ := key.GetStringValue("InstallLocation")
	executable, _ := normalizeDisplayIcon(icon)
	if executable == "" || !isExistingExecutable(executable) {
		executable = resolveInstalledExecutable(installDir, displayName)
	}
	return Application{ID: applicationID(displayName + "\x00" + executable), Name: strings.TrimSpace(displayName), Executable: executable, WorkingDir: installDir, Source: source}, true
}

func isExistingExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && strings.EqualFold(filepath.Ext(path), ".exe")
}

func resolveInstalledExecutable(installDir, displayName string) string {
	installDir = strings.TrimSpace(strings.Trim(installDir, "\""))
	if installDir == "" {
		return ""
	}
	entries, err := os.ReadDir(installDir)
	if err != nil {
		return ""
	}
	wanted := strings.ToLower(strings.NewReplacer(" ", "", "-", "", "_", "").Replace(displayName))
	var fallback string
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".exe") {
			continue
		}
		path := filepath.Join(installDir, entry.Name())
		if fallback == "" {
			fallback = path
		}
		name := strings.ToLower(strings.NewReplacer(" ", "", "-", "", "_", "").Replace(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))))
		if strings.Contains(name, wanted) || strings.Contains(wanted, name) {
			return path
		}
	}
	return fallback
}
