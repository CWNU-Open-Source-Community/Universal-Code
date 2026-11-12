package application_test

import (
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/store"
	"strings"
	"testing"
)

func postCompactionHookEngine(t *testing.T, state *store.SQLiteStore) *hooks.Engine {
	t.Helper()
	engine := hooks.NewEngine(state)
	if err := engine.Replace([]hooks.Registration{{PluginID: "watch", PluginFingerprint: strings.Repeat("b", 64), Declaration: hooks.Declaration{ProtocolVersion: hooks.ProtocolVersion, ID: "compact-tap", Event: hooks.PostCompaction, Action: hooks.ActionRecord, FailurePolicy: hooks.FailureContinue, TimeoutMillis: 100}}}); err != nil {
		t.Fatal(err)
	}
	return engine
}

func assertPostCompactionHookAudits(t *testing.T, state *store.SQLiteStore, runID string, count int) {
	t.Helper()
	audits, err := state.ListHookAudits(t.Context(), runID, "", 200)
	if err != nil || len(audits) != count {
		t.Fatalf("post-compaction audits=%+v want=%d err=%v", audits, count, err)
	}
	for _, audit := range audits {
		if audit.Event != hooks.PostCompaction || audit.Outcome != "completed" || audit.Rejected == nil || *audit.Rejected {
			t.Fatalf("compaction observation=%+v", audit)
		}
	}
}
