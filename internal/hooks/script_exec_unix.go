//go:build !windows

package hooks

import (
	"os/exec"
	"syscall"
)

// configureScriptProcess puts the hook process in its own group so the
// timeout can reach the whole tree, not just the interpreter.
func configureScriptProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killScriptProcessTree kills the hook's process group. The interpreter may
// have spawned children (a daemon, a server) that would otherwise survive the
// declared timeout and keep running — and on a locked snapshot file, keep the
// per-run cleanup from removing it.
func killScriptProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
