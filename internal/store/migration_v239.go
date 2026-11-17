package store

import "strings"

// supervisorToolThroughputMigrationStatements widens supervisor batch position
// and round constraints to match the raised domain limits (8 calls/round with
// schema headroom up to 16 matching llm.MaxProviderToolCalls, 24 native rounds,
// and 217 physical execution rounds), preserving all historical rows, rowids,
// indexes, triggers, and external foreign-key bindings.
func supervisorToolThroughputMigrationStatements() []string {
	ledger := workspaceImageSupervisorLedgerStatements()
	if len(ledger) < 2 || ledger[0] != "PRAGMA legacy_alter_table=ON;" || ledger[len(ledger)-1] != "PRAGMA legacy_alter_table=OFF;" {
		panic("unexpected workspace image supervisor ledger statements")
	}
	statements := make([]string, 0, len(ledger)+32)
	posReplaced := 0
	for _, stmt := range ledger[:len(ledger)-1] {
		stmt = strings.ReplaceAll(stmt, "run_supervisor_tool_calls_v232", "run_supervisor_tool_calls_v239")
		stmt = strings.ReplaceAll(stmt, "run_supervisor_tool_calls_v231", "run_supervisor_tool_calls_v238")
		posReplaced += strings.Count(stmt, "CHECK(position BETWEEN 1 AND 4)")
		stmt = strings.ReplaceAll(stmt, "CHECK(position BETWEEN 1 AND 4)", "CHECK(position BETWEEN 1 AND 16)")
		statements = append(statements, stmt)
	}
	if posReplaced != 1 {
		panic("run_supervisor_tool_calls position constraint unavailable")
	}

	roundsCreate := requireMigrationStatement("CREATE TABLE run_supervisor_tool_rounds (", projectHookStatements)
	if strings.Count(roundsCreate, "CHECK(round BETWEEN 1 AND 21)") != 1 {
		panic("run_supervisor_tool_rounds round constraint unavailable")
	}
	roundsCreate = strings.Replace(roundsCreate, "CHECK(round BETWEEN 1 AND 21)", "CHECK(round BETWEEN 1 AND 217)", 1)

	nativeLimit := requireMigrationTrigger("trg_supervisor_native_round_limit", projectHookStatements)
	if strings.Count(nativeLimit, ">=4") != 1 {
		panic("trg_supervisor_native_round_limit threshold unavailable")
	}
	nativeLimit = strings.Replace(nativeLimit, ">=4", ">=24", 1)

	statements = append(statements,
		`DROP INDEX idx_supervisor_native_model_round;`,
		`DROP TRIGGER trg_supervisor_native_round_limit;`,
		`DROP TRIGGER trg_supervisor_tool_round_active_attempt;`,
		`DROP TRIGGER trg_supervisor_tool_round_completed_model;`,
		`DROP TRIGGER trg_supervisor_tool_round_completion;`,
		`ALTER TABLE run_supervisor_tool_rounds RENAME TO run_supervisor_tool_rounds_v238;`,
		roundsCreate,
		`INSERT INTO run_supervisor_tool_rounds(rowid,run_id,turn,attempt_id,round,model_attempt,origin,created_at,completed_at)
		 SELECT rowid,run_id,turn,attempt_id,round,model_attempt,origin,created_at,completed_at FROM run_supervisor_tool_rounds_v238;`,
		`DROP TABLE run_supervisor_tool_rounds_v238;`,
		requireMigrationStatement("CREATE UNIQUE INDEX idx_supervisor_native_model_round", projectHookStatements),
		nativeLimit,
		requireMigrationTrigger("trg_supervisor_tool_round_active_attempt", projectHookStatements),
		requireMigrationTrigger("trg_supervisor_tool_round_completed_model", projectHookStatements),
		requireMigrationTrigger("trg_supervisor_tool_round_completion", projectHookStatements),
	)

	for _, table := range []struct {
		name, columns string
		triggers      []string
	}{
		{
			name:     "run_supervisor_provider_replay",
			columns:  "run_id,turn,attempt_id,round,model_attempt,provider,model,replay_blob,replay_sha256,created_at",
			triggers: []string{"trg_supervisor_provider_replay_source", "trg_supervisor_provider_replay_immutable"},
		},
		{
			name:     "run_supervisor_context_recoveries",
			columns:  "run_id,turn,attempt_id,tool_round,protocol_repair,model_attempt,original_input_tokens,provider,model,created_at",
			triggers: []string{"trg_supervisor_context_recovery_source", "trg_supervisor_context_recovery_immutable"},
		},
		{
			name:     "run_supervisor_assistant_replay",
			columns:  "run_id,turn,attempt_id,model_attempt,tool_round,provider,model,replay_blob,replay_sha256,created_at",
			triggers: []string{"trg_supervisor_assistant_replay_source", "trg_supervisor_assistant_replay_immutable", "trg_supervisor_assistant_replay_delete"},
		},
	} {
		for _, trigger := range table.triggers {
			statements = append(statements, "DROP TRIGGER "+trigger+";")
		}
		create := requireMigrationStatement("CREATE TABLE "+table.name+" (", projectHookStatements)
		if strings.Count(create, "AND 21)") != 1 {
			panic(table.name + " round constraint unavailable")
		}
		create = strings.Replace(create, "AND 21)", "AND 217)", 1)
		statements = append(statements,
			"ALTER TABLE "+table.name+" RENAME TO "+table.name+"_v238;",
			create,
			"INSERT INTO "+table.name+"(rowid,"+table.columns+") SELECT rowid,"+table.columns+" FROM "+table.name+"_v238;",
			"DROP TABLE "+table.name+"_v238;",
		)
		for _, trigger := range table.triggers {
			statements = append(statements, requireMigrationTrigger(trigger, projectHookStatements))
		}
	}

	return append(statements, `PRAGMA legacy_alter_table=OFF;`)
}
