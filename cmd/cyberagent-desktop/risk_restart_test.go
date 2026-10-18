//go:build desktop

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/desktop"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	riskRestartIntegrationParentEnvironment   = "TRAVERSE_TEST_RISK_RESTART_PARENT"
	riskRestartIntegrationBehaviorEnvironment = "TRAVERSE_TEST_RISK_RESTART_BEHAVIOR"
)

func TestMain(m *testing.M) {
	restart, restartMode, restartErr := parseRiskRestartHelperOptions(os.Args[1:])
	if restartMode {
		if restartErr != nil {
			_, _ = fmt.Fprintln(os.Stderr, restartErr)
			os.Exit(91)
		}
		behavior := os.Getenv(riskRestartIntegrationBehaviorEnvironment)
		if behavior == "invalid" || behavior == "silent" {
			waiter, err := prepareRiskRestartParent(restart.parentPID)
			if err != nil {
				_, _ = fmt.Fprintln(os.Stderr, err)
				os.Exit(92)
			}
			defer waiter.Close()
			if behavior == "invalid" {
				writer, err := openRiskRestartReadyWriter(restart.readyDescriptor)
				if err == nil {
					_, err = writer.Write([]byte(strings.Repeat("x",
						len(riskRestartReadyMessage(restart.readyToken)))))
					_ = writer.Close()
				}
				if err != nil {
					_, _ = fmt.Fprintln(os.Stderr, err)
					os.Exit(92)
				}
				_ = waiter.Wait()
				os.Exit(0)
			}
			time.Sleep(30 * time.Second)
			os.Exit(94)
		}
		if err := completeRiskRestartHelperHandshake(restart); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(92)
		}
		os.Exit(0)
	}
	if os.Getenv(riskRestartIntegrationParentEnvironment) == "1" {
		behavior := os.Getenv(riskRestartIntegrationBehaviorEnvironment)
		if behavior != "" {
			riskRestartReadyTimeout = 200 * time.Millisecond
		}
		err := startDesktopRiskRestartHelper(desktop.DesktopRiskProfileDebug,
			os.Getpid())
		wantError := behavior == "invalid" || behavior == "silent"
		if (err != nil) != wantError {
			_, _ = fmt.Fprintf(os.Stderr, "restart error = %v, want error %t\n", err, wantError)
			os.Exit(93)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestRiskRestartHelperArgumentsAreAnExactInternalSet(t *testing.T) {
	ready, err := newRiskRestartReadyChannel()
	if err != nil {
		t.Fatal(err)
	}
	defer ready.close()
	descriptor := ready.descriptor()
	arguments, err := riskRestartHelperArguments(desktop.DesktopRiskProfileDebug,
		4123, descriptor, ready.token)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--internal-risk-restart",
		"--internal-risk-profile=debug",
		"--internal-risk-parent-pid=4123",
		"--internal-risk-ready=" + descriptor,
		"--internal-risk-ready-token=" + ready.token,
	}
	if !reflect.DeepEqual(arguments, want) {
		t.Fatalf("helper arguments = %v, want %v", arguments, want)
	}
	parsed, matched, err := parseRiskRestartHelperOptions(arguments)
	if err != nil || !matched || parsed.profile != desktop.DesktopRiskProfileDebug ||
		parsed.parentPID != 4123 || parsed.readyDescriptor != descriptor ||
		parsed.readyToken != ready.token {
		t.Fatalf("parsed helper = %+v matched=%t err=%v", parsed, matched, err)
	}
	if _, err := riskRestartHelperArguments("shell", 4123, descriptor, ready.token); err == nil {
		t.Fatal("unknown risk profile produced helper arguments")
	}
}

func TestRiskRestartHelperParserRejectsPartialMixedAndForgedArguments(t *testing.T) {
	currentPID := os.Getpid()
	ready, err := newRiskRestartReadyChannel()
	if err != nil {
		t.Fatal(err)
	}
	defer ready.close()
	readyArgument := "--internal-risk-ready=" + ready.descriptor()
	readyTokenArgument := "--internal-risk-ready-token=" + ready.token
	tests := [][]string{
		{"--internal-risk-restart"},
		{"--internal-risk-restart", "--internal-risk-profile=full_access",
			"--internal-risk-parent-pid=4123", readyArgument, readyTokenArgument,
			"--enable-full-cdp-debug"},
		{"--internal-risk-restart", "--internal-risk-profile=shell",
			"--internal-risk-parent-pid=4123", readyArgument, readyTokenArgument},
		{"--internal-risk-restart", "--internal-risk-profile=debug",
			"--internal-risk-parent-pid=0", readyArgument, readyTokenArgument},
		{"--internal-risk-restart", "--internal-risk-profile=debug",
			"--internal-risk-parent-pid=" + strings.Repeat("9", 32), readyArgument,
			readyTokenArgument},
		{"--internal-risk-restart", "--internal-risk-profile=debug",
			"--internal-risk-parent-pid=4123", readyArgument},
		{"--internal-risk-restart", "--internal-risk-profile=debug",
			"--internal-risk-parent-pid=4123", "--internal-risk-ready=invalid",
			readyTokenArgument},
		{"--internal-risk-restart", "--internal-risk-profile=debug",
			"--internal-risk-parent-pid=4123", readyArgument,
			"--internal-risk-ready-token=invalid"},
	}
	tests = append(tests, []string{"--internal-risk-restart", "--internal-risk-profile=debug",
		"--internal-risk-parent-pid=" + strconv.Itoa(currentPID), readyArgument,
		readyTokenArgument})
	for _, arguments := range tests {
		if _, matched, err := parseRiskRestartHelperOptions(arguments); !matched || err == nil {
			t.Fatalf("unsafe helper arguments accepted: %v matched=%t err=%v", arguments, matched, err)
		}
	}
	if _, matched, err := parseRiskRestartHelperOptions([]string{"--enable-danger-full-access"}); err != nil || matched {
		t.Fatalf("ordinary desktop argument became helper mode: matched=%t err=%v", matched, err)
	}
}

func TestRiskRestartReadyHandshakeAcrossRealChildProcess(t *testing.T) {
	for _, behavior := range []string{"", "invalid", "silent"} {
		name := behavior
		if name == "" {
			name = "ready"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0])
			command.Env = append(os.Environ(),
				riskRestartIntegrationParentEnvironment+"=1",
				riskRestartIntegrationBehaviorEnvironment+"="+behavior)
			output, err := command.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("real restart handshake timed out: %v", ctx.Err())
			}
			if err != nil {
				t.Fatalf("real restart handshake failed: %v\n%s", err, output)
			}
		})
	}
}

func TestDesktopRiskProfilesPreserveSafeProductAndAddOnlyTheirCeiling(t *testing.T) {
	if _, err := desktopOptionsForRiskProfile(
		desktop.DesktopRiskProfileFullAccess); err == nil {
		t.Fatal("Full Access unexpectedly remained a restart profile")
	}
	debug, err := desktopOptionsForRiskProfile(desktop.DesktopRiskProfileDebug)
	if err != nil {
		t.Fatal(err)
	}
	if !debug.riskProfileRestart || !debug.permissionControl || !debug.dangerFullAccess ||
		!debug.userTerminal || !debug.runExecution ||
		!debug.scheduledJobWorker || !debug.scheduledJobConfirmedOnly {
		t.Fatalf("debug product bundle is incomplete: %+v", debug)
	}
	if !debug.fullCDPDebug || debug.dockerExecution || debug.batchValidation ||
		debug.runWakeWorker {
		t.Fatalf("debug product bundle widened unrelated capabilities: %+v", debug)
	}
	if _, err := desktopOptionsForRiskProfile("shell"); err == nil {
		t.Fatal("unknown product risk profile was accepted")
	}
}

func TestDesktopRiskRestartNativeDialogOwnsPersistedTaskScopeAndSafeDefaults(t *testing.T) {
	tests := []struct {
		name     string
		profile  desktop.DesktopRiskProfile
		contains []string
	}{
		{
			name:    "debug",
			profile: desktop.DesktopRiskProfileDebug,
			contains: []string{
				"重启以启用用户终端",
				"当前任务的 Full 激活、Debug 交互和单独的限时终端授权",
				"重启不会恢复已保存任务的执行权",
			},
		},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			options, err := desktopRiskRestartDialogOptions(current.profile)
			if err != nil {
				t.Fatal(err)
			}
			if options.Type != runtime.WarningDialog ||
				options.DefaultButton != "取消" || options.CancelButton != "取消" ||
				!reflect.DeepEqual(options.Buttons, []string{"确认并重启", "取消"}) {
				t.Fatalf("unsafe native dialog defaults: %+v", options)
			}
			for _, expected := range current.contains {
				if !strings.Contains(options.Message, expected) {
					t.Fatalf("native dialog omitted %q: %q", expected, options.Message)
				}
			}
		})
	}
	for _, invalid := range []desktop.DesktopRiskProfile{
		desktop.DesktopRiskProfileFullAccess, "shell",
	} {
		if _, err := desktopRiskRestartDialogOptions(invalid); err == nil {
			t.Fatalf("native dialog accepted invalid restart profile %q", invalid)
		}
	}
}

func TestNativeRiskRestarterRequiresConfirmationBeforeExactStartAndQuit(t *testing.T) {
	var confirmed, started, quit int
	sequence := make([]string, 0, 3)
	var startedProfile desktop.DesktopRiskProfile
	var startedParent int
	restarter := &nativeRiskProfileRestarter{
		confirm: func(context.Context, desktop.DesktopRiskProfile) (bool, error) {
			confirmed++
			sequence = append(sequence, "confirm")
			return true, nil
		},
		start: func(profile desktop.DesktopRiskProfile, parentPID int) error {
			started++
			sequence = append(sequence, "ready")
			startedProfile, startedParent = profile, parentPID
			return nil
		},
		quit: func(context.Context) {
			quit++
			sequence = append(sequence, "quit")
		},
	}
	restarting, err := restarter.ConfirmAndRestart(context.Background(),
		desktop.DesktopRiskProfileDebug)
	if err != nil || !restarting {
		t.Fatalf("native restart = %t err=%v", restarting, err)
	}
	if confirmed != 1 || started != 1 || quit != 1 ||
		startedProfile != desktop.DesktopRiskProfileDebug || startedParent != os.Getpid() {
		t.Fatalf("native restart sequence confirm=%d start=%d quit=%d profile=%q parent=%d",
			confirmed, started, quit, startedProfile, startedParent)
	}
	if !reflect.DeepEqual(sequence, []string{"confirm", "ready", "quit"}) {
		t.Fatalf("native restart order = %v", sequence)
	}
}

func TestNativeRiskRestarterDoesNotStartOrQuitAfterCancelOrFailure(t *testing.T) {
	tests := []struct {
		name       string
		confirmed  bool
		confirmErr error
		startErr   error
	}{
		{name: "cancelled"},
		{name: "dialog failed", confirmErr: errors.New("dialog failed")},
		{name: "start failed", confirmed: true, startErr: errors.New("start failed")},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			started, quit := 0, 0
			restarter := &nativeRiskProfileRestarter{
				confirm: func(context.Context, desktop.DesktopRiskProfile) (bool, error) {
					return current.confirmed, current.confirmErr
				},
				start: func(desktop.DesktopRiskProfile, int) error {
					started++
					return current.startErr
				},
				quit: func(context.Context) { quit++ },
			}
			restarting, err := restarter.ConfirmAndRestart(context.Background(),
				desktop.DesktopRiskProfileDebug)
			if current.confirmErr != nil || current.startErr != nil {
				if err == nil {
					t.Fatal("native failure returned nil error")
				}
			} else if err != nil || restarting {
				t.Fatalf("native cancellation = %t err=%v", restarting, err)
			}
			wantStarted := 0
			if current.confirmed {
				wantStarted = 1
			}
			if started != wantStarted || quit != 0 {
				t.Fatalf("unsafe native effects: started=%d want=%d quit=%d",
					started, wantStarted, quit)
			}
		})
	}
}
