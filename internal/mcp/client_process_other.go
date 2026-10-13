//go:build !windows

package mcp

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
)

type clientProcessGroup struct{ pgid int }

func startClientProcessTree(ctx context.Context, cmd *exec.Cmd) (clientProcessTree, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	attributes := syscall.SysProcAttr{}
	if cmd.SysProcAttr != nil {
		attributes = *cmd.SysProcAttr
	}
	attributes.Setpgid, attributes.Pgid = true, 0
	cmd.SysProcAttr = &attributes
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	tree := &clientProcessGroup{pgid: cmd.Process.Pid}
	if err := ctx.Err(); err != nil {
		_ = tree.Close()
		_ = cmd.Wait()
		return nil, err
	}
	return tree, nil
}

func (p *clientProcessGroup) Close() error {
	err := syscall.Kill(-p.pgid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
