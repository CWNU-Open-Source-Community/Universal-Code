//go:build !windows

package httpapi

import "os/exec"

func hideSubprocessWindow(*exec.Cmd) {}
