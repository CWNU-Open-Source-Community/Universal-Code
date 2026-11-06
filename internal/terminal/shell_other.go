//go:build !windows && !darwin && !linux

package terminal

func AvailableShells() []ShellInfo { return []ShellInfo{} }
