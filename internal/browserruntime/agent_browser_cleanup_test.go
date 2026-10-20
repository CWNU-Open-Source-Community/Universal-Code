package browserruntime

import "testing"

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
