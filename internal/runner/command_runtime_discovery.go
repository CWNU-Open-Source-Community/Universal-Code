package runner

import (
	"context"
	"os/exec"
	"runtime"
)

type CommandRuntimeExecutableInfo struct {
	Name       string `json:"name"`
	Executable string `json:"executable"`
}

func DefaultCommandRuntimeShellProfile() CommandRuntimeProfile {
	if runtime.GOOS == "windows" {
		return CommandRuntimePowerShell
	}
	return CommandRuntimeBash
}

// CommandRuntimeHostEnvironment is bounded, read-only discovery, not execution
// authority. It never reads executable output or publishes environment values.
type CommandRuntimeHostEnvironment struct {
	OS                    string                         `json:"os"`
	Architecture          string                         `json:"architecture"`
	ShellProfiles         []CommandRuntimeProfile        `json:"shell_profiles"`
	PreferredShellProfile CommandRuntimeProfile          `json:"preferred_shell_profile,omitempty"`
	ProcessExecutables    []CommandRuntimeExecutableInfo `json:"process_executables"`
}

func DiscoverCommandRuntimeHost(ctx context.Context, workspaceRoot string) (CommandRuntimeHostEnvironment, error) {
	info := CommandRuntimeHostEnvironment{OS: runtime.GOOS, Architecture: runtime.GOARCH,
		ShellProfiles: []CommandRuntimeProfile{}, ProcessExecutables: []CommandRuntimeExecutableInfo{}}
	if err := ctx.Err(); err != nil {
		return info, err
	}
	root, _, _, err := resolveCommandRuntimeDirectory(workspaceRoot, ".")
	if err != nil {
		return info, err
	}
	usable := func(path string) bool {
		outside, err := commandRuntimeExecutableOutsideWorkspace(path, root)
		return err == nil && outside && commandRuntimeExecutableAttributes(path) == nil
	}
	profiles := []CommandRuntimeProfile{CommandRuntimePowerShell, CommandRuntimeBash}
	if runtime.GOOS != "windows" {
		profiles[0], profiles[1] = profiles[1], profiles[0]
	}
	for _, profile := range profiles {
		if path, err := resolveCommandRuntimeShell(profile); err == nil && usable(path) {
			info.ShellProfiles = append(info.ShellProfiles, profile)
			if profile == DefaultCommandRuntimeShellProfile() {
				info.PreferredShellProfile = profile
			}
		}
	}
	seen := make(map[string]bool)
	for _, name := range []string{"node", "python3", "python", "go"} {
		if err := ctx.Err(); err != nil {
			return info, err
		}
		found, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		path, err := resolveCommandRuntimeProcess(found)
		if err != nil || len(path) > MaxCommandRuntimePathBytes || !commandRuntimeNativeExecutableAllowed(path) || !usable(path) || seen[path] {
			continue
		}
		seen[path] = true
		info.ProcessExecutables = append(info.ProcessExecutables, CommandRuntimeExecutableInfo{Name: name, Executable: path})
	}
	return info, nil
}
