//go:build !windows

package application

import "os/exec"

func hideSubprocessWindow(*exec.Cmd) {}
