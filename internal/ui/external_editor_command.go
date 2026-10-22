package ui

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

func resolveExternalEditorCommand(parts []string, waitForClose bool) ([]string, error) {
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return nil, errors.New("编辑器命令为空")
	}

	commandPath, err := exec.LookPath(parts[0])
	if err != nil && runtime.GOOS == "darwin" {
		commandPath, err = resolveMacOSAppEditor(parts[0])
	}
	if err != nil {
		return nil, fmt.Errorf("找不到本地编辑器 %s；请检查应用是否已安装：%w", parts[0], err)
	}

	resolved := append([]string(nil), parts...)
	resolved[0] = commandPath
	if waitForClose && supportsEditorWaitOption(parts[0], commandPath) && !containsEditorWaitOption(resolved[1:]) {
		resolved = append(resolved[:1], append([]string{"--wait"}, resolved[1:]...)...)
	}
	return resolved, nil
}

func supportsEditorWaitOption(configured, resolved string) bool {
	for _, value := range []string{configured, resolved} {
		name := strings.ToLower(filepath.Base(strings.TrimSuffix(value, ".app")))
		if name == "goland" || name == "goland.sh" || name == "code" || name == "code-insiders" {
			return true
		}
	}
	return false
}

func containsEditorWaitOption(args []string) bool {
	for _, arg := range args {
		if arg == "--wait" {
			return true
		}
	}
	return false
}

func resolveMacOSAppEditor(command string) (string, error) {
	commandName := strings.ToLower(filepath.Base(command))
	if strings.HasSuffix(commandName, ".app") {
		commandName = strings.TrimSuffix(commandName, ".app")
	}
	if strings.HasSuffix(commandName, ".sh") {
		commandName = strings.TrimSuffix(commandName, ".sh")
	}
	switch commandName {
	case "goland":
		return resolveMacOSGoLandEditor()
	case "code", "code-insiders":
		return resolveMacOSVSCodeEditor(commandName)
	default:
		return "", exec.ErrNotFound
	}
}

func resolveMacOSGoLandEditor() (string, error) {
	bundles := make([]string, 0, 8)
	if home, err := os.UserHomeDir(); err == nil {
		bundles = append(bundles, macOSGoLandBundles(filepath.Join(home, "Applications"))...)
		toolbox := filepath.Join(home, "Library", "Application Support", "JetBrains", "Toolbox", "apps", "GoLand")
		bundles = append(bundles, macOSGoLandBundles(toolbox)...)
	}
	bundles = append(bundles, macOSGoLandBundles("/Applications")...)

	seen := make(map[string]struct{}, len(bundles))
	for _, bundle := range bundles {
		if _, duplicate := seen[bundle]; duplicate {
			continue
		}
		seen[bundle] = struct{}{}
		for _, candidate := range []string{
			filepath.Join(bundle, "Contents", "bin", "goland"),
			filepath.Join(bundle, "Contents", "bin", "goland.sh"),
			filepath.Join(bundle, "Contents", "MacOS", "goland"),
		} {
			info, err := os.Stat(candidate)
			if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("未在 /Applications 或用户应用目录中的 GoLand.app 找到启动器")
}

func resolveMacOSVSCodeEditor(command string) (string, error) {
	roots := []string{"/Applications"}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append([]string{filepath.Join(home, "Applications")}, roots...)
	}
	for _, root := range roots {
		for _, bundle := range macOSVSCodeBundles(root, command == "code-insiders") {
			candidate := filepath.Join(bundle, "Contents", "Resources", "app", "bin", "code")
			info, err := os.Stat(candidate)
			if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("未在 /Applications 或用户应用目录中的 Visual Studio Code.app 找到命令行启动器")
}

func macOSVSCodeBundles(root string, insiders bool) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var bundles []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := strings.ToLower(entry.Name())
		isInsiders := strings.Contains(name, "insiders")
		if !strings.HasSuffix(name, ".app") || !strings.Contains(name, "visual studio code") || isInsiders != insiders {
			continue
		}
		bundles = append(bundles, filepath.Join(root, entry.Name()))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(bundles)))
	return bundles
}

func macOSGoLandBundles(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	bundles := make([]string, 0, 2)
	for _, entry := range entries {
		if entry.IsDir() && isGoLandBundleName(entry.Name()) {
			bundles = append(bundles, filepath.Join(root, entry.Name()))
		}
	}
	if len(bundles) > 0 {
		return bundles
	}

	// JetBrains Toolbox nests versioned app bundles several directories below
	// its product folder. Stop descending once a matching .app bundle is found.
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() && isGoLandBundleName(entry.Name()) {
			bundles = append(bundles, path)
			return filepath.SkipDir
		}
		return nil
	})
	sort.Sort(sort.Reverse(sort.StringSlice(bundles)))
	return bundles
}

func isGoLandBundleName(name string) bool {
	name = strings.ToLower(name)
	return strings.HasPrefix(name, "goland") && strings.HasSuffix(name, ".app")
}
