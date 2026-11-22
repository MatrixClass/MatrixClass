package remoteexec

import (
	"os"
	"path/filepath"
	"strings"
)

// macOS application bundle discovery. Kept free of build tags so it can be
// exercised by unit tests on every OS; apps_darwin.go wires it to the real
// /Applications roots.

// appBundleRoot is a directory scanned for *.app bundles.
type appBundleRoot struct {
	path   string
	source string
}

// maxAppBundleScanDepth bounds how deep we look for bundles below a root
// (e.g. /Applications/Utilities/Terminal.app is depth 2).
const maxAppBundleScanDepth = 3

const maxInfoPlistSize = 4 << 20

func isAppBundlePath(path string) bool {
	return strings.EqualFold(filepath.Ext(strings.TrimRight(path, `/\`)), ".app")
}

// discoverAppBundles scans roots in order; the first bundle seen for a given
// bundle identifier (or path when no identifier is present) wins, so user
// installs shadow system ones when the user root is listed first.
func discoverAppBundles(roots []appBundleRoot) []Application {
	seen := make(map[string]bool)
	var apps []Application
	for _, root := range roots {
		for _, bundle := range findAppBundles(root.path, maxAppBundleScanDepth) {
			app, identity, ok := readAppBundle(bundle, root.source)
			if !ok || seen[identity] {
				continue
			}
			seen[identity] = true
			apps = append(apps, app)
		}
	}
	return apps
}

// findAppBundles returns *.app directories under root without descending into
// bundles themselves (nested helper apps are not user-facing).
func findAppBundles(root string, depth int) []string {
	if depth <= 0 {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var bundles []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		path := filepath.Join(root, name)
		isDir := entry.IsDir()
		if entry.Type()&os.ModeSymlink != 0 {
			// Follow symlinks to bundles (common for Homebrew casks), but do
			// not recurse through symlinked directories to avoid loops.
			if info, err := os.Stat(path); err == nil && info.IsDir() && isAppBundlePath(name) {
				bundles = append(bundles, path)
			}
			continue
		}
		if !isDir {
			continue
		}
		if isAppBundlePath(name) {
			bundles = append(bundles, path)
			continue
		}
		bundles = append(bundles, findAppBundles(path, depth-1)...)
	}
	return bundles
}

// readAppBundle parses Contents/Info.plist and resolves the bundle's main
// executable. It returns the application and a de-duplication identity.
func readAppBundle(bundle, source string) (Application, string, bool) {
	infoPath := filepath.Join(bundle, "Contents", "Info.plist")
	info, err := os.Stat(infoPath)
	if err != nil || info.IsDir() || info.Size() > maxInfoPlistSize {
		return Application{}, "", false
	}
	data, err := os.ReadFile(infoPath)
	if err != nil {
		return Application{}, "", false
	}
	dict, err := plistDict(data)
	if err != nil {
		return Application{}, "", false
	}
	if plistBool(dict, "LSBackgroundOnly") {
		return Application{}, "", false
	}
	if packageType := plistString(dict, "CFBundlePackageType"); packageType != "" && packageType != "APPL" {
		return Application{}, "", false
	}
	base := strings.TrimSuffix(filepath.Base(bundle), filepath.Ext(bundle))
	name := firstNonEmpty(plistString(dict, "CFBundleDisplayName"), plistString(dict, "CFBundleName"), base)
	executableName := firstNonEmpty(plistString(dict, "CFBundleExecutable"), base)
	if strings.ContainsAny(executableName, `/\`) || executableName == "." || executableName == ".." {
		return Application{}, "", false
	}
	executable := filepath.Join(bundle, "Contents", "MacOS", executableName)
	if st, err := os.Stat(executable); err != nil || st.IsDir() {
		return Application{}, "", false
	}
	identity := strings.ToLower(plistString(dict, "CFBundleIdentifier"))
	if identity == "" {
		identity = strings.ToLower(filepath.Clean(bundle))
	}
	return Application{
		ID:         applicationID(bundle),
		Name:       name,
		Executable: executable,
		Source:     source,
	}, identity, true
}

// appBundleForExecutable returns the enclosing "X.app" directory when path is
// either a bundle itself or the main binary inside X.app/Contents/MacOS.
func appBundleForExecutable(path string) (string, bool) {
	clean := filepath.Clean(path)
	if isAppBundlePath(clean) {
		return clean, true
	}
	macOSDir := filepath.Dir(clean)
	contentsDir := filepath.Dir(macOSDir)
	bundle := filepath.Dir(contentsDir)
	if filepath.Base(macOSDir) == "MacOS" && filepath.Base(contentsDir) == "Contents" && isAppBundlePath(bundle) {
		return bundle, true
	}
	return "", false
}

// darwinLaunchCommand builds the command used to launch an application on
// macOS. Bundles go through LaunchServices (`open -a`) so they activate and
// behave like a Finder launch; plain executables run directly.
func darwinLaunchCommand(executable string, args []string, wait bool) (string, []string) {
	bundle, ok := appBundleForExecutable(executable)
	if !ok {
		return executable, args
	}
	cmdArgs := []string{"-a", bundle}
	if wait {
		cmdArgs = append(cmdArgs, "-W")
	}
	if len(args) > 0 {
		cmdArgs = append(cmdArgs, "--args")
		cmdArgs = append(cmdArgs, args...)
	}
	return "/usr/bin/open", cmdArgs
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
