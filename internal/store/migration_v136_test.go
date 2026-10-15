package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestSchemaV136AddsDurableRiskEscalationLedger(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "risk-escalation-v135.db")
	state := openHistoricalTestDatabase(t, path, 135)
	// The immutable historical prefix above is the upgrade input.
	if version, err := state.SchemaVersion(ctx); err != nil || version != 135 {
		state.Close()
		t.Fatalf("restored schema version=%d want=135 err=%v", version, err)
	}
	if _, err := state.db.ExecContext(ctx,
		`SELECT scope_fingerprint FROM approval_session_grants LIMIT 1`); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "no such column") {
		state.Close()
		t.Fatalf("v135 unexpectedly retained bounded grant columns: %v", err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if version, err := upgraded.SchemaVersion(ctx); err != nil || version != LatestSchemaVersion {
		t.Fatalf("upgraded schema version=%d want=%d err=%v", version,
			LatestSchemaVersion, err)
	}
	for _, table := range []string{
		"approval_grant_consumptions", "risk_escalation_proposals",
		"risk_escalation_operations", "risk_escalation_execution_intents",
		"risk_escalation_results", "risk_escalation_invalidations",
	} {
		var name string
		if err := upgraded.db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).
			Scan(&name); err != nil || name != table {
			t.Fatalf("v136 table %s missing: name=%q err=%v", table, name, err)
		}
	}
	var callsSQL string
	if err := upgraded.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master
		WHERE type = 'table' AND name = 'run_supervisor_tool_calls'`).Scan(&callsSQL); err != nil ||
		!strings.Contains(callsSQL,
			"tool_name IN ('host_command_propose', 'mcp_tool_call', 'mcp_tool_search', 'workspace_list'") {
		t.Fatalf("v136 Supervisor risk authority constraint is missing: %q err=%v",
			callsSQL, err)
	}
	for _, trigger := range []string{
		"trg_risk_escalation_supervisor_authority_insert",
		"trg_host_command_supervisor_envelope_immutable",
	} {
		var name string
		if err := upgraded.db.QueryRowContext(ctx, `SELECT name FROM sqlite_master
			WHERE type = 'trigger' AND name = ?`, trigger).Scan(&name); err != nil ||
			name != trigger {
			t.Fatalf("v136 Supervisor authority trigger %s missing: name=%q err=%v",
				trigger, name, err)
		}
	}
	var scope sql.NullString
	if err := upgraded.db.QueryRowContext(ctx,
		`SELECT scope_fingerprint FROM approval_session_grants LIMIT 1`).Scan(&scope); err != nil &&
		err != sql.ErrNoRows {
		t.Fatalf("v136 bounded grant columns are unavailable: %v", err)
	}
	assertNoForeignKeyViolations(t, upgraded.db)
}
