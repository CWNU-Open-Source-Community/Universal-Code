package application_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/pricing"
	"cyberagent-workbench/internal/store"
)

func TestSpecialistReadRevocationPreservesRecoveryAndUsage(t *testing.T) {
	ctx := t.Context()
	root, home := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("review evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	usage := llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4}
	provider := &specialistTestProvider{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{scopedReadCall("read-notes", "notes.txt")}, Usage: usage},
		{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "Evidence reviewed."}), Usage: usage},
	}}
	st, run, child, _ := newScopedSubagentFixture(t, provider, domain.Budget{MaxTurns: 10, MaxToolCalls: 20}, 3, 128, false,
		domain.SpecialistAdmission{AllowedReadPaths: []string{"notes.txt"}, ToolCallLimit: 2, CostLimitMicros: 1000000}, home, root)
	prices := importSupervisorPriceSnapshot(t, ctx, st)
	prices.ID = "specialist-read-revoke-prices"
	prices.Entries[0].Provider = provider.Name()
	prices.Fingerprint = pricing.Fingerprint(prices)
	if _, _, err := st.ImportPriceSnapshot(ctx, prices); err != nil {
		t.Fatal(err)
	}
	if _, err := scopedToolService(st, provider).ExecuteSpecialist(ctx, application.ExecuteSpecialistRequest{
		RunID: run.ID, AgentID: child.ID, OperationKey: "read-revoke-usage-attempt-0001", WorkspaceTools: true,
	}); err != nil {
		t.Fatal(err)
	}
	before, err := st.GetAgentNode(ctx, child.ID)
	if err != nil || before.Status != domain.AgentReady || before.ToolCallsUsed != 1 || before.CostUsedMicros <= 0 {
		t.Fatalf("expected idle child with recorded tool and monetary usage: %+v %v", before, err)
	}
	moneyBefore, err := st.GetMonetaryUsage(ctx, run.ID)
	if err != nil || moneyBefore.SettledMicros <= 0 {
		t.Fatalf("expected settled model cost: %+v %v", moneyBefore, err)
	}
	if _, err := st.RestoreAgentGraph(ctx, run.ID); err != nil {
		t.Fatalf("graph before revocation: %v", err)
	}
	revoked, err := st.RevokeSpecialistReadTools(ctx, run.ID, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	checkRecovery := func(t *testing.T, st *store.SQLiteStore) domain.AgentGraph {
		t.Helper()
		graph, err := st.RestoreAgentGraph(ctx, run.ID)
		if err != nil {
			t.Fatalf("restore immediately after revocation: %v", err)
		}
		found := false
		for _, node := range graph.Nodes {
			if node.ID == child.ID {
				found = true
				if len(node.AllowedReadPaths) != 0 || node.ToolCallsUsed != before.ToolCallsUsed || node.CostUsedMicros != before.CostUsedMicros {
					t.Fatalf("recovery changed revoked scope or cumulative usage: %+v", node)
				}
			}
		}
		if !found {
			t.Fatal("restored graph omitted revoked child")
		}
		money, err := st.GetMonetaryUsage(ctx, run.ID)
		if err != nil || !reflect.DeepEqual(money, moneyBefore) {
			t.Fatalf("revocation changed monetary ledger: before=%+v after=%+v err=%v", moneyBefore, money, err)
		}
		return graph
	}
	t.Run("immediate", func(t *testing.T) { checkRecovery(t, st) })
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(filepath.Join(home, "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	t.Run("reopened", func(t *testing.T) {
		graph := checkRecovery(t, reopened)
		repeated, err := reopened.RevokeSpecialistReadTools(ctx, run.ID, child.ID)
		if err != nil || !reflect.DeepEqual(repeated, revoked) {
			t.Fatalf("repeated revocation changed child: before=%+v after=%+v err=%v", revoked, repeated, err)
		}
		if repeatedGraph := checkRecovery(t, reopened); !reflect.DeepEqual(repeatedGraph.LatestSnapshot, graph.LatestSnapshot) {
			t.Fatal("repeated revocation created another recovery snapshot")
		}
	})
}
