package browserruntime

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// Model a native process observation that cannot prove cleanup on its first
// attempt, then verifies the owned process scope on the explicit retry.
type retryableAgentBrowserTestProcess struct {
	attempts int
	done     chan struct{}
	exit     BrowserProcessExit
}

func (p *retryableAgentBrowserTestProcess) PID() int              { return 1 }
func (p *retryableAgentBrowserTestProcess) Done() <-chan struct{} { return p.done }
func (p *retryableAgentBrowserTestProcess) Exit() (BrowserProcessExit, bool) {
	return p.exit, true
}
func (p *retryableAgentBrowserTestProcess) Stop(context.Context) error {
	p.attempts++
	if p.attempts == 1 {
		close(p.done)
		return errors.New("process observation unavailable")
	}
	p.exit.ProcessScopeVerified = true
	return nil
}

func TestAgentBrowserDoneAllowsExplicitCleanupRetry(t *testing.T) {
	profile, err := prepareAgentBrowserProfile(t.TempDir(), AgentBrowserAuthority{SessionID: "retry"})
	if err != nil {
		t.Fatal(err)
	}
	life, cancel := context.WithCancel(t.Context())
	defer cancel()
	process := &retryableAgentBrowserTestProcess{
		done: make(chan struct{}), exit: BrowserProcessExit{ProcessScope: DarwinAgentBrowserProcessScope},
	}
	r := &AgentBrowserRuntime{
		request: AgentBrowserStartRequest{Authority: AgentBrowserAuthority{SessionID: "retry"}},
		profile: profile, process: process, life: life, cancel: cancel,
		done: make(chan struct{}), state: "ready",
	}
	go r.reapOnInvalidation()
	r.Cancel()
	select {
	case <-r.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("first cleanup attempt did not finish")
	}
	if status := r.Status(); status.State != "cleanup_pending" {
		t.Fatalf("unverified cleanup reported as complete: %+v", status)
	}
	if _, err := os.Stat(profile.path); err != nil {
		t.Fatalf("profile removed before process cleanup was verified: %v", err)
	}
	if _, err := r.Snapshot(t.Context()); !errors.Is(err, ErrAgentBrowserClosed) {
		t.Fatalf("pending cleanup allowed another action: %v", err)
	}
	result, err := r.Close(t.Context())
	if err != nil || !result.ProcessesStopped() || !result.ProfileRemoved || result.CleanupPending {
		t.Fatalf("explicit cleanup retry: %+v %v", result, err)
	}
	if status := r.Status(); status.State != "closed" || process.attempts != 2 {
		t.Fatalf("cleanup retry did not complete: status=%+v attempts=%d", status, process.attempts)
	}
	if _, err := os.Stat(profile.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("profile survived verified cleanup: %v", err)
	}
}

func TestAgentBrowserCleanupRequiresItsActualProcessScope(t *testing.T) {
	cases := []struct {
		name    string
		receipt AgentBrowserCleanup
		stopped bool
	}{
		{"windows job", AgentBrowserCleanup{TreeReaped: true}, true},
		{"unproven windows job", AgentBrowserCleanup{}, false},
		{"observed Darwin", AgentBrowserCleanup{ProcessScope: DarwinAgentBrowserProcessScope, ProcessScopeVerified: true}, true},
		{"unproven Darwin", AgentBrowserCleanup{ProcessScope: DarwinAgentBrowserProcessScope}, false},
		{"Darwin cannot claim job tree", AgentBrowserCleanup{ProcessScope: DarwinAgentBrowserProcessScope, ProcessScopeVerified: true, TreeReaped: true}, false},
		{"unknown scope", AgentBrowserCleanup{ProcessScope: "unknown", ProcessScopeVerified: true, TreeReaped: true}, false},
		{"unscoped verification", AgentBrowserCleanup{ProcessScopeVerified: true, TreeReaped: true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.receipt.ProcessesStopped(); got != c.stopped {
				t.Fatalf("stopped=%t want=%t receipt=%+v", got, c.stopped, c.receipt)
			}
		})
	}
	if !AgentBrowserNoProcessCleanup("empty-session").ProcessesStopped() {
		t.Fatal("empty resource receipt cannot close an unused session")
	}
}
