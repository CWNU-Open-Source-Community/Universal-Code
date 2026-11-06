//go:build windows

package terminal

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func AvailableShells() []ShellInfo {
	values := []ShellInfo{{ID: "pwsh", Name: "PowerShell 7"}, {ID: "powershell", Name: "Windows PowerShell 5.1"}, {ID: "cmd", Name: "CMD"}}
	for i := range values {
		resolved, err := resolveTerminalShell(values[i].ID)
		if err == nil {
			values[i] = resolved
		}
	}
	return values
}

func resolveTerminalShell(id string) (ShellInfo, error) {
	if id == "" {
		id = "auto"
	}
	if id == "auto" {
		if shell, err := resolveTerminalShell("pwsh"); err == nil {
			return shell, nil
		}
		return resolveTerminalShell("powershell")
	}
	shell := ShellInfo{ID: id}
	var paths []string
	switch id {
	case "pwsh":
		shell.Name = "PowerShell 7"
		if path := os.Getenv("CYBERAGENT_POWERSHELL_PATH"); path != "" {
			if !strings.EqualFold(filepath.Base(path), "pwsh.exe") {
				return ShellInfo{}, ErrTerminalBoundary
			}
			paths = []string{path}
		} else {
			if executable, err := os.Executable(); err == nil {
				paths = append(paths, filepath.Join(filepath.Dir(executable), "runtimes", "powershell7", "pwsh.exe"))
			}
			for _, folder := range []*windows.KNOWNFOLDERID{windows.FOLDERID_ProgramFiles, windows.FOLDERID_ProgramFilesX64, windows.FOLDERID_ProgramFilesX86} {
				if root, err := windows.KnownFolderPath(folder, 0); err == nil {
					paths = append(paths, filepath.Join(root, "PowerShell", "7", "pwsh.exe"))
				}
			}
		}
	case "powershell":
		shell.Name = "Windows PowerShell 5.1"
		root, err := windows.GetWindowsDirectory()
		if err != nil {
			return shell, err
		}
		paths = []string{filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")}
	case "cmd":
		shell.Name = "CMD"
		root, err := windows.GetSystemDirectory()
		if err != nil {
			return shell, err
		}
		paths = []string{filepath.Join(root, "cmd.exe")}
	default:
		return shell, ErrTerminalBoundary
	}
	for _, path := range paths {
		path = filepath.Clean(path)
		if !filepath.IsAbs(path) || strings.HasPrefix(path, `\\`) {
			continue
		}
		if err := validateTerminalShell(path); err != nil {
			continue
		}
		shell.Path = path
		shell.Version = terminalExecutableVersion(path)
		shell.Available = true
		return shell, nil
	}
	return shell, ErrTerminalUnavailable
}
