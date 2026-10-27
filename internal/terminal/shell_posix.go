//go:build darwin || linux

package terminal

func AvailableShells() []ShellInfo {
	value, err := resolveTerminalShell("bash")
	if err != nil {
		return []ShellInfo{{ID: "bash", Name: "Bash"}}
	}
	return []ShellInfo{value}
}

func resolveTerminalShell(id string) (ShellInfo, error) {
	if id != "" && id != "auto" && id != "bash" {
		return ShellInfo{}, ErrTerminalBoundary
	}
	path, err := trustedPOSIXBashPath()
	if err != nil {
		return ShellInfo{}, err
	}
	return ShellInfo{ID: "bash", Name: "Bash", Path: path, Available: true}, nil
}
