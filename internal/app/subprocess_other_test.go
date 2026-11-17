//go:build !windows

package app

import "os/exec"

func hideSubprocessWindow(*exec.Cmd) {}
