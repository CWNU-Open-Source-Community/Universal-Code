package application

import (
	"context"
	"runtime"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolgateway"
)

func TestSupervisorBrowserTimeoutIncludesDarwinLaunch(t *testing.T) {
	for _, goos := range []string{"darwin", "windows", "linux"} {
		for _, name := range []toolgateway.ToolName{
			toolgateway.BrowserNavigateTool, toolgateway.BrowserSnapshotTool,
			toolgateway.BrowserScreenshotTool, toolgateway.BrowserClickTool,
			toolgateway.BrowserTypeTool, toolgateway.BrowserKeyTool,
			toolgateway.BrowserScrollTool, toolgateway.BrowserStatusTool,
			toolgateway.WebFetchTool,
		} {
			t.Run(goos+"/"+string(name), func(t *testing.T) {
				want := 30 * time.Second
				if goos == "darwin" && name == toolgateway.BrowserNavigateTool {
					want = 3 * time.Minute
				}
				if got := supervisorToolExecutionTimeoutForPlatform(name, goos); got != want {
					t.Fatalf("execution timeout = %s, want %s", got, want)
				}
			})
		}
	}
}

func TestAgentBrowserLaunchReceivesBoundedSupervisorDeadline(t *testing.T) {
	for _, shorterParent := range []bool{false, true} {
		name := "tool deadline"
		if shorterParent {
			name = "shorter parent deadline"
		}
		t.Run(name, func(t *testing.T) {
			service, st, browser, supervisor, turn := newAgentBrowserFixture(t)
			call := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserNavigateTool,
				`{"version":"browser_navigate.v2","url":"https://example.org"}`)
			ctx := t.Context()
			var parentDeadline time.Time
			if shorterParent {
				parentDeadline = time.Now().Add(10 * time.Second)
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, parentDeadline)
				defer cancel()
			}
			launch := service.launch
			launched := false
			service.launch = func(ctx context.Context, req browserruntime.AgentBrowserStartRequest) (agentBrowserRuntime, error) {
				launched = true
				deadline, ok := ctx.Deadline()
				if !ok {
					t.Fatal("browser startup has no execution deadline")
				}
				if shorterParent {
					if !deadline.Equal(parentDeadline) {
						t.Fatalf("startup deadline = %s, want parent deadline %s", deadline, parentDeadline)
					}
				} else {
					want := 30 * time.Second
					if runtime.GOOS == "darwin" {
						want = 3 * time.Minute
					}
					if remaining := time.Until(deadline); remaining > want || remaining < want-5*time.Second {
						t.Fatalf("startup has %s remaining, want bounded %s budget", remaining, want)
					}
				}
				return launch(ctx, req)
			}
			_, waiting, err := supervisor.resumeSupervisorTools(ctx, turn,
				[]domain.SupervisorToolRound{{Calls: []domain.SupervisorToolCall{call}}})
			if err != nil || waiting || !launched || len(browser.actions) != 1 || st.calls[call.CallID].Status != domain.SupervisorToolCompleted {
				t.Fatalf("navigation did not complete: launched=%t actions=%v call=%+v waiting=%t err=%v",
					launched, browser.actions, st.calls[call.CallID], waiting, err)
			}
		})
	}
}

func TestAgentBrowserLaunchHonorsParentCancellation(t *testing.T) {
	service, st, browser, supervisor, turn := newAgentBrowserFixture(t)
	call := agentBrowserFixtureCall(t, service, st, toolgateway.BrowserNavigateTool,
		`{"version":"browser_navigate.v2","url":"https://example.org"}`)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	launched := false
	service.launch = func(ctx context.Context, _ browserruntime.AgentBrowserStartRequest) (agentBrowserRuntime, error) {
		launched = true
		cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	_, _, err := supervisor.resumeSupervisorTools(ctx, turn,
		[]domain.SupervisorToolRound{{Calls: []domain.SupervisorToolCall{call}}})
	if !launched || apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeCancelled || len(browser.actions) != 0 {
		t.Fatalf("cancelled launch continued: launched=%t actions=%v err=%v", launched, browser.actions, err)
	}
}
