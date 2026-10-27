package application

import (
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/outputsafe"
	terminalruntime "cyberagent-workbench/internal/terminal"
	"cyberagent-workbench/internal/toolgateway"
)

func TestDebugTerminalInterruptForegroundHelper(t *testing.T) {
	if os.Args[len(os.Args)-1] != "uc-terminal-interrupt-helper" {
		t.Skip("foreground subprocess helper")
	}
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	fmt.Println("UC_INTERRUPT_READY")
	select {
	case <-interrupts:
		fmt.Println("UC_INTERRUPT_RECEIVED")
	case <-time.After(30 * time.Second):
		t.Fatal("foreground subprocess received no interrupt")
	}
}

func TestDebugTerminalInterruptNativeForegroundPreservesShell(t *testing.T) {
	switch runtime.GOOS {
	case "windows":
		if os.Getenv("CYBERAGENT_TEST_WINDOWS_CONPTY") != "1" {
			t.Skip("set CYBERAGENT_TEST_WINDOWS_CONPTY=1 to run the real ConPTY interrupt test")
		}
	case "darwin", "linux":
	default:
		t.Skip("native terminal is unavailable on this platform")
	}
	f := newDebugTerminalAgentFixtureWithManager(t, false, true)
	binding := grantDebugTerminalInterruptFixture(t, f)
	executor, err := NewDebugTerminalToolExecutor(f.service)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quotedExecutable := "'" + strings.ReplaceAll(executable, "'", "'\"'\"'") + "'"
	command := "PS1='UC_INTERRUPT_'\"PROMPT> \"; " + quotedExecutable + " '-test.run=^TestDebugTerminalInterruptForegroundHelper$' '--' 'uc-terminal-interrupt-helper'"
	shellCheck := "printf '%s%s\\n' 'UC_INTERRUPT_' 'SHELL_USABLE'"
	if runtime.GOOS == "windows" {
		command = "function prompt { 'UC_INTERRUPT_' + 'PROMPT> ' }; & '" + strings.ReplaceAll(executable, "'", "''") + "' '-test.run=^TestDebugTerminalInterruptForegroundHelper$' '--' 'uc-terminal-interrupt-helper'"
		shellCheck = "Write-Output ('UC_INTERRUPT_' + 'SHELL_USABLE')"
	}
	runInput := func(action, command, operation string) toolgateway.DebugTerminalExecutionResult {
		t.Helper()
		result, err := executor.ExecuteDebugTerminal(t.Context(), toolgateway.DebugTerminalContext{
			RunID: f.run.ID, OperationKey: operation,
		}, toolgateway.DebugTerminalInput{
			Version: toolgateway.DebugTerminalProtocolVersion, Action: action,
			Command: command, MaxBytes: 4096,
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	waitForMarker := func(result toolgateway.DebugTerminalExecutionResult, marker string) {
		t.Helper()
		output, cursor := string(result.Output), result.NextCursor
		deadline := time.Now().Add(10 * time.Second)
		for !strings.Contains(output, marker) && time.Now().Before(deadline) {
			page, err := f.service.Read(t.Context(), ReadDebugTerminalAgentOutputRequest{
				ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
				BindingID:       binding.ID, Cursor: cursor, MaxBytes: 4096,
			})
			if err != nil {
				t.Fatal(err)
			}
			output += string(page.Data)
			cursor = page.NextCursor
			time.Sleep(10 * time.Millisecond)
		}
		if !strings.Contains(output, marker) {
			t.Fatalf("terminal omitted %s: %q", marker, output)
		}
	}
	waitForMarker(runInput(toolgateway.DebugTerminalActionWrite, command, "native-start-foreground"), "UC_INTERRUPT_READY")
	interrupted := runInput(toolgateway.DebugTerminalActionInterrupt, "", "native-interrupt-foreground")
	if !interrupted.InputSubmitted || interrupted.BytesWritten != 1 || interrupted.TerminalSessionID != f.sessionID {
		t.Fatalf("unexpected native interrupt result: %+v", interrupted)
	}
	waitForMarker(interrupted, "UC_INTERRUPT_RECEIVED")
	// Shell prompts have no newline and intentionally stay out of the model's
	// line-buffered stream. Wait for the real prompt before sending another command.
	var promptOutput []byte
	var promptCursor uint64
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(outputsafe.Sanitize(promptOutput), "UC_INTERRUPT_PROMPT> ") && time.Now().Before(deadline) {
		page, err := f.manager.Read(f.sessionID, promptCursor, 4096)
		if err != nil {
			t.Fatal(err)
		}
		promptOutput = append(promptOutput, page.Data...)
		promptCursor = page.NextCursor
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(outputsafe.Sanitize(promptOutput), "UC_INTERRUPT_PROMPT> ") {
		t.Fatalf("shell prompt did not return after interrupt: %q", outputsafe.Sanitize(promptOutput))
	}
	// Both pieces are separate in the command, so terminal echo cannot satisfy this check.
	waitForMarker(runInput(toolgateway.DebugTerminalActionWrite, shellCheck, "native-check-shell"), "UC_INTERRUPT_SHELL_USABLE")
	current, err := f.manager.Get(f.sessionID)
	if err != nil || current.State != terminalruntime.SessionRunning {
		t.Fatalf("shell did not survive foreground interrupt: %+v err=%v", current, err)
	}
	if _, err := f.manager.WriteUser(t.Context(), terminalruntime.UserInputRequest{
		SessionID: f.sessionID, Data: []byte("exit\r"), RequestedBy: "test_operator", UserConfirmed: true,
	}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, err = f.manager.Get(f.sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State == terminalruntime.SessionExited {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("test shell did not exit")
}
