//go:build !windows

package desktop

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
)

func CopyNativeChatFile(string) error {
	return fmt.Errorf("file clipboard is currently supported on Windows; use Save as")
}
func OpenNativeChatFile(path, action string) error {
	if action == "open_with" {
		return fmt.Errorf("Open with is currently supported on Windows")
	}
	var command *exec.Cmd
	if runtime.GOOS == "darwin" {
		if action == "reveal" {
			command = exec.Command("open", "-R", path)
		} else {
			command = exec.Command("open", path)
		}
	} else {
		if action == "reveal" {
			path = filepath.Dir(path)
		}
		command = exec.Command("xdg-open", path)
	}
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}
