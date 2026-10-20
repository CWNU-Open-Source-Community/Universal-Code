//go:build darwin && cgo

package browserruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentBrowserDarwinRevocationBeforeResume(t *testing.T) {
	if os.Getenv("TRAVERSE_TEST_AGENT_BROWSER") != "1" {
		t.Skip("opt-in signed Chrome runtime")
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	denied := errors.New("authority revoked after dynamic identity verification")
	checks := 0
	request := AgentBrowserStartRequest{HomePath: home, Headless: true, Product: BrowserProductChrome,
		RuntimeDeadline: time.Now().Add(time.Minute), Authority: AgentBrowserAuthority{RunID: "revoked", ManagerBootID: "boot", SessionID: "no-resume", Generation: 1, PermissionSnapshotID: "p", PermissionRevision: 1, PermissionActivation: 1, RunAuthorizationFence: 1, PermissionMode: "full"}}
	request.CheckAuthority = func(context.Context, AgentBrowserAuthority) error {
		checks++
		if checks == 3 {
			endpoints, _ := filepath.Glob(filepath.Join(home, "runtime", "agent-browser", "profiles", "*", "Data", DevToolsActivePortFileName))
			if len(endpoints) != 0 {
				t.Fatal("Chrome executed before final live authority check")
			}
			return denied
		}
		return nil
	}
	_, err = LaunchAgentBrowser(t.Context(), request)
	var failure *AgentBrowserLaunchError
	if checks != 3 || !errors.Is(err, denied) || !errors.As(err, &failure) || failure.Cleanup.TreeReaped || failure.Cleanup.ProcessScope != DarwinAgentBrowserProcessScope || !failure.Cleanup.ProcessesStopped() || !failure.Cleanup.ProfileRemoved || failure.Cleanup.CleanupPending {
		t.Fatalf("pre-resume revocation cleanup checks=%d failure=%+v err=%v", checks, failure, err)
	}
}
