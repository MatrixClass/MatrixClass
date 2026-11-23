//go:build linux

package remoteexec

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

func discoverApplications() []Application {
	home, _ := os.UserHomeDir()
	roots := []struct{ path, source string }{
		{filepath.Join(home, ".local", "share", "applications"), "desktop-user"},
		{"/usr/local/share/applications", "desktop-local"},
		{"/usr/share/applications", "desktop-system"},
	}
	seen := make(map[string]bool)
	var apps []Application
	for _, root := range roots {
		entries, err := os.ReadDir(root.path)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".desktop") {
				continue
			}
			app, ok := parseDesktopFile(filepath.Join(root.path, entry.Name()), root.source)
			if !ok || seen[app.ID] {
				continue
			}
			seen[app.ID] = true
			apps = append(apps, app)
		}
	}
	return apps
}

func parseDesktopFile(path, source string) (Application, bool) {
	file, err := os.Open(path)
	if err != nil {
		return Application{}, false
	}
	defer file.Close()
	values := make(map[string]string)
	section := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "[Desktop Entry]" {
			section = line
			continue
		}
		if section != "[Desktop Entry]" || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	if values["Type"] != "" && values["Type"] != "Application" {
		return Application{}, false
	}
	if strings.EqualFold(values["Hidden"], "true") || strings.EqualFold(values["NoDisplay"], "true") {
		return Application{}, false
	}
	name := values["Name"]
	execLine := values["Exec"]
	if name == "" || execLine == "" {
		return Application{}, false
	}
	executable, args := parseDesktopExec(execLine)
	if executable == "" {
		return Application{}, false
	}
	workingDir := values["Path"]
	if workingDir == "" {
		workingDir = filepath.Dir(executable)
	}
	return Application{
		ID:         applicationID(path),
		Name:       name,
		Executable: executable,
		Arguments:  args,
		WorkingDir: workingDir,
		Source:     source,
	}, true
}

func parseDesktopExec(line string) (string, string) {
	parts := splitCommandLine(line, true)
	if len(parts) == 0 {
		return "", ""
	}
	args := make([]string, 0, len(parts)-1)
	for index, part := range parts {
		if strings.HasPrefix(part, "%") && len(part) == 2 {
			continue
		}
		if index == 0 {
			continue
		}
		args = append(args, part)
	}
	return parts[0], joinCommandLine(args)
}

func joinCommandLine(args []string) string {
	var b strings.Builder
	for index, arg := range args {
		if index > 0 {
			b.WriteByte(' ')
		}
		if strings.ContainsAny(arg, " \t\n\r'\"") {
			b.WriteByte('\'')
			b.WriteString(strings.ReplaceAll(arg, "'", "'\\''"))
			b.WriteByte('\'')
			continue
		}
		b.WriteString(arg)
	}
	return b.String()
}
