//go:build !windows

package store

import "os/exec"

func hideSubprocessWindow(*exec.Cmd) {}
