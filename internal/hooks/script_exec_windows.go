//go:build windows

package hooks

import (
	"context"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

func hiddenWindowsSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
}

// configureScriptProcess hides console windows on Windows so desktop GUI
// builds do not flash a visible console when running script hooks; the tree
// kill goes through the in-box taskkill below.
func configureScriptProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = hiddenWindowsSysProcAttr()
}

func newTaskkillTreeCommand(ctx context.Context, pid int) *exec.Cmd {
	tree := exec.CommandContext(ctx, "taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	tree.SysProcAttr = hiddenWindowsSysProcAttr()
	return tree
}

// killScriptProcessTree kills the hook's process tree. The interpreter may
// have spawned children (a daemon, a server) that would otherwise survive the
// declared timeout and keep running — and on a locked snapshot file, keep the
// per-run cleanup from removing it. taskkill /T walks the tree; if it is
// unavailable or fails, the direct kill is the fallback.
func killScriptProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tree := newTaskkillTreeCommand(ctx, cmd.Process.Pid)
	if err := tree.Run(); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
