//go:build !windows

package application_test

import "os/exec"

func hideSubprocessWindow(*exec.Cmd) {}
