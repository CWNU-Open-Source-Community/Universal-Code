//go:build windows

package terminal

import (
	"bytes"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTerminalShellChoicesRejectArbitraryCommands(t *testing.T) {
	for _, id := range []string{"cmd /c echo injected", "../pwsh.exe", "C:\\temp\\pwsh.exe", "bash"} {
		if _, err := resolveTerminalShell(id); err == nil {
			t.Fatalf("accepted shell %q", id)
		}
	}
	for _, id := range []string{"cmd", "powershell"} {
		shell, err := resolveTerminalShell(id)
		if err != nil || !shell.Available || shell.Path == "" || shell.Version == "" {
			t.Fatalf("shell %s: %+v %v", id, shell, err)
		}
	}
	t.Setenv("CYBERAGENT_POWERSHELL_PATH", `relative\pwsh.exe`)
	if _, err := resolveTerminalShell("pwsh"); err == nil {
		t.Fatal("accepted relative host shell")
	}
}

func TestWindowsConPTYCloseInteractiveShellsOptIn(t *testing.T) {
	if os.Getenv("CYBERAGENT_TEST_WINDOWS_CONPTY") != "1" {
		t.Skip("real ConPTY opt-in")
	}
	for _, id := range []string{"cmd", "powershell", "pwsh"} {
		t.Run(id, func(t *testing.T) {
			if _, err := resolveTerminalShell(id); err != nil {
				t.Skipf("shell unavailable: %v", err)
			}
			manager, err := NewPlatformManager(nil)
			if err != nil {
				t.Fatal(err)
			}
			request := terminalStartTestRequestFor(t, "close-interactive-"+id)
			request.ShellID = id
			current, err := manager.Start(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				page, _ := manager.Read(current.ID, 0, MaxTerminalOutputReadBytes)
				if len(page.Data) > 0 {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			done := make(chan error, 1)
			go func() { done <- manager.Close(current.ID, "test_operator", true) }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				stack := make([]byte, 64*1024)
				n := runtime.Stack(stack, true)
				t.Fatalf("interactive close hung:\n%s", stack[:n])
			}
		})
	}
}

func TestWindowsTerminalSelectedShellConPTYOptIn(t *testing.T) {
	if os.Getenv("CYBERAGENT_TEST_WINDOWS_CONPTY") != "1" {
		t.Skip("real ConPTY opt-in")
	}
	for _, id := range []string{"cmd", "powershell", "pwsh"} {
		t.Run(id, func(t *testing.T) {
			if _, err := resolveTerminalShell(id); err != nil {
				t.Skipf("shell unavailable: %v", err)
			}
			manager, err := NewPlatformManager(nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = manager.Shutdown() })
			request := terminalStartTestRequestFor(t, "selected-shell-"+id)
			request.ShellID = id
			session, err := manager.Start(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if session.Shell == nil || session.Shell.ID != id {
				t.Fatalf("wrong actual shell: %+v", session.Shell)
			}
			command := "Write-Output ('UC_SHELL_' + 'READY'); exit\r"
			if id == "cmd" {
				command = "echo UC_SHELL_READY & exit\r"
			}
			if _, err = manager.WriteUser(t.Context(), UserInputRequest{SessionID: session.ID, Data: []byte(command), RequestedBy: "test_operator", UserConfirmed: true}); err != nil {
				t.Fatal(err)
			}
			var output []byte
			cursor := uint64(0)
			deadline := time.Now().Add(12 * time.Second)
			for time.Now().Before(deadline) {
				page, err := manager.Read(session.ID, cursor, MaxTerminalOutputReadBytes)
				if err != nil {
					t.Fatal(err)
				}
				output = append(output, page.Data...)
				cursor = page.NextCursor
				current, err := manager.Get(session.ID)
				if err != nil {
					t.Fatal(err)
				}
				if current.State == SessionExited {
					if current.ExitCode != 0 || !bytes.Contains(output, []byte("UC_SHELL_READY")) {
						t.Fatalf("shell failed: %d %s", current.ExitCode, strings.TrimSpace(string(output)))
					}
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Fatalf("shell did not finish: %s", output)
		})
	}
}
