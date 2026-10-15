//go:build !windows

package desktop

import (
	"os/exec"
	"runtime"
)

func launchChatGPTBrowser(target string) error {
	launcher := "xdg-open"
	if runtime.GOOS == "darwin" {
		launcher = "open"
	}
	command := exec.Command(launcher, target)
	if err := command.Start(); err != nil {
		return err
	}
	// Some default browsers keep their launcher alive until the window closes.
	// Reap it independently; the login callback and cancellation remain usable.
	go func() { _ = command.Wait() }()
	return nil
}
