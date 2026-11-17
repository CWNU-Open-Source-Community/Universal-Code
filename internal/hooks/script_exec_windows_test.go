//go:build windows

package hooks

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestScriptProcessHidesWindowsConsole(t *testing.T) {
	scriptCmd := exec.Command("cmd.exe", "/c", "exit", "0")
	configureScriptProcess(scriptCmd)
	if scriptCmd.SysProcAttr == nil || !scriptCmd.SysProcAttr.HideWindow ||
		scriptCmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatalf("script hook subprocess can create a visible console: %+v", scriptCmd.SysProcAttr)
	}

	killCmd := newTaskkillTreeCommand(t.Context(), 12345)
	if killCmd.SysProcAttr == nil || !killCmd.SysProcAttr.HideWindow ||
		killCmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatalf("taskkill tree subprocess can create a visible console: %+v", killCmd.SysProcAttr)
	}
}
