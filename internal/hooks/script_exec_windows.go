//go:build windows

package hooks

import (
	"os/exec"
	"strconv"
)

// configureScriptProcess has no cross-process grouping hook in the Go
// runtime on Windows; the tree kill goes through the in-box taskkill below.
func configureScriptProcess(cmd *exec.Cmd) {
	_ = cmd
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
	tree := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	if err := tree.Run(); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
