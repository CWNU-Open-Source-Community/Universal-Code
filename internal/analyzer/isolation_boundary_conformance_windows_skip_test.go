//go:build windows

package analyzer

import (
	"strings"
	"testing"
)

func TestAnalyzerIsolationHostLimitationSkip(t *testing.T) {
	env := func(pairs map[string]string) func(string) string {
		return func(name string) string { return pairs[name] }
	}
	const closed = "product authority remains closed"
	if reason := analyzerIsolationHostLimitationSkip(0xc0000142, nil, env(nil)); reason != "" {
		t.Fatalf("unacknowledged host failure must stay loud, got skip reason %q", reason)
	}
	if reason := analyzerIsolationHostLimitationSkip(0, nil, env(nil)); reason != "" {
		t.Fatalf("non-DLL_INIT failure must stay loud, got skip reason %q", reason)
	}
	if reason := analyzerIsolationHostLimitationSkip(0xc0000142, []byte("partial"), env(nil));
		reason != "" {
		t.Fatalf("helper with output must stay loud, got skip reason %q", reason)
	}
	if reason := analyzerIsolationHostLimitationSkip(0xc0000142, nil,
		env(map[string]string{"GITHUB_ACTIONS": "true", "RUNNER_OS": "Windows"}));
		!strings.Contains(reason, closed) {
		t.Fatalf("GitHub-hosted Windows runner must skip, got %q", reason)
	}
	if reason := analyzerIsolationHostLimitationSkip(0xc0000142, nil,
		env(map[string]string{"GITHUB_ACTIONS": "true"})); reason != "" {
		t.Fatalf("GITHUB_ACTIONS alone must not skip, got %q", reason)
	}
	if reason := analyzerIsolationHostLimitationSkip(0xc0000142, nil,
		env(map[string]string{analyzerIsolationHostLimitationEnv: "1"}));
		!strings.Contains(reason, analyzerIsolationHostLimitationEnv) ||
		!strings.Contains(reason, closed) {
		t.Fatalf("explicit opt-in must skip with a documented reason, got %q", reason)
	}
	for _, unset := range []string{"", "0", "true", "yes", " 1 "} {
		if reason := analyzerIsolationHostLimitationSkip(0xc0000142, nil,
			env(map[string]string{analyzerIsolationHostLimitationEnv: unset}));
			reason != "" {
			t.Fatalf("opt-in value %q must not skip, got %q", unset, reason)
		}
	}
	// The opt-in only acknowledges this exact failure class; any other exit
	// code stays loud even with the environment variable set.
	if reason := analyzerIsolationHostLimitationSkip(1, nil,
		env(map[string]string{analyzerIsolationHostLimitationEnv: "1"}));
		reason != "" {
		t.Fatalf("opt-in must not widen past STATUS_DLL_INIT_FAILED, got %q", reason)
	}
}

