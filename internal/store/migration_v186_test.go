package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runmutation"
)

func TestSchemaV186UpgradePreservesLegacyCreationAndAdmitsBoundedBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v185.db")
	state := openHistoricalTestDatabase(t, path, 185)
	workspace := WorkspaceRecord{ID: "ws-v186-upgrade", Name: "v186-upgrade", RootPath: t.TempDir(), CreatedAt: time.Now().UTC()}
	if err := state.SaveWorkspace(t.Context(), workspace); err != nil {
		t.Fatal(err)
	}
	mission, legacy, err := newMigrationFixtureRunService(t, state).Create(t.Context(), application.CreateRunRequest{Goal: "preserve legacy creation", WorkspaceID: workspace.ID, Profile: "code", Interactive: true, Budget: domain.DefaultBudget(), RequestedBy: "http_control"})
	if err != nil {
		t.Fatal(err)
	}
	key := "v186-legacy-operation"
	insertCreationOperation(t, state, mission, legacy, key, false)
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	version, err := upgraded.SchemaVersion(t.Context())
	if err != nil || version != LatestSchemaVersion {
		t.Fatalf("schema=%d err=%v", version, err)
	}
	stored, err := upgraded.GetRun(t.Context(), legacy.ID)
	if err != nil || stored.Config.RequestedBudget != nil || stored.Budget != legacy.Budget {
		t.Fatalf("legacy snapshot altered: %#v %v", stored, err)
	}
	replayed, err := application.NewControlledRunCreationService(upgraded).Create(t.Context(), application.ControlledRunCreationRequest{Version: domain.RunCreationProtocolVersion, Goal: mission.Goal, WorkspaceID: workspace.ID, Profile: "code", OperationKey: key, RequestedBy: "http_control"})
	if err != nil || !replayed.Replayed || replayed.Run.ID != legacy.ID {
		t.Fatalf("legacy replay=%#v err=%v", replayed, err)
	}
	turns, tokens := 17, int64(700)
	created, err := application.NewControlledRunCreationService(upgraded).Create(t.Context(), application.ControlledRunCreationRequest{Version: domain.RunCreationProtocolVersion, Goal: "custom migrated budget", WorkspaceID: workspace.ID, Budget: &domain.TaskBudgetSettings{MaxTurns: &turns, MaxTokens: &tokens}, OperationKey: "v186-new-budget-operation"})
	if err != nil || created.Run.Budget.MaxTurns != turns || created.Run.Budget.MaxTokens != tokens {
		t.Fatalf("custom creation=%#v err=%v", created, err)
	}
	for _, statement := range []string{
		`UPDATE runs SET budget_json=json_set(budget_json,'$.max_turns',18) WHERE id=?`,
		`UPDATE runs SET config_json=json_remove(config_json,'$.requested_budget') WHERE id=?`,
		`UPDATE runs SET config_json=json_set(config_json,'$.project_config_fingerprint','bad') WHERE id=?`,
	} {
		if _, err := upgraded.db.Exec(statement, created.Run.ID); err == nil {
			t.Fatalf("immutable task configuration accepted %s", statement)
		}
	}
}

func insertCreationOperation(t *testing.T, state *SQLiteStore, mission domain.Mission, run domain.Run, key string, reject bool) {
	t.Helper()
	fingerprint := domain.ControlledCreationFingerprint(mission.Goal, mission.WorkspaceID, string(mission.Profile), "code", "deliver", "disabled", nil, "code", "http_control", run.Config.CreationBudget())
	_, err := state.db.Exec(`INSERT INTO run_creation_operations
		(operation_key_digest, request_fingerprint, protocol_version, mission_id, run_id, session_id, workspace_id, requested_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, runmutation.RunCreationOperationDigest(key), fingerprint, domain.RunCreationProtocolVersion,
		mission.ID, run.ID, run.SessionID, mission.WorkspaceID, "http_control", ts(run.CreatedAt))
	if reject && err == nil {
		t.Fatal("direct SQL admitted an invalid requested/effective budget binding")
	}
	if !reject && err != nil {
		t.Fatal(err)
	}
}

func TestSchemaV186DirectInsertRejectsForgedBudgetBindings(t *testing.T) {
	state := openCurrentTestDatabase(t, filepath.Join(t.TempDir(), "bad-bindings.db"))
	workspace := WorkspaceRecord{ID: "ws-v186-direct", Name: "v186-direct", RootPath: t.TempDir(), CreatedAt: time.Now().UTC()}
	if err := state.SaveWorkspace(t.Context(), workspace); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		requested string
		project   string
	}{
		{"turn widening", `{"max_turns":99,"max_tool_calls":100}`, ""},
		{"tool widening", `{"max_turns":100,"max_tool_calls":99}`, ""},
		{"invalid bound", `{"max_turns":10001,"max_tool_calls":100}`, ""},
		{"token mismatch", `{"max_turns":100,"max_tool_calls":100,"max_tokens":20}`, ""},
		{"extra field", `{"max_turns":100,"max_tool_calls":100,"credential":"secret"}`, ""},
		{"unbound project", `{"max_turns":100,"max_tool_calls":100}`, `{"protocol":"project_config.v1","max_turns":100,"max_tool_calls":100}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			mission, run, err := application.NewRunService(state).Create(t.Context(), application.CreateRunRequest{Goal: "reject forged budget", WorkspaceID: workspace.ID, Profile: "code", Interactive: true, Budget: domain.DefaultBudget(), RequestedBy: "http_control"})
			if err != nil {
				t.Fatal(err)
			}
			if test.project == "" {
				if _, err := state.db.Exec(`UPDATE runs SET config_json=json_set(config_json,'$.requested_budget',json(?)) WHERE id=?`, test.requested, run.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := state.db.Exec(`UPDATE runs SET config_json=json_set(config_json,'$.requested_budget',json(?),'$.project_config',json(?)) WHERE id=?`, test.requested, test.project, run.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := json.Unmarshal([]byte(test.requested), &run.Config.RequestedBudget); err != nil {
				t.Fatal(err)
			}
			insertCreationOperation(t, state, mission, run, "v186-forged-"+run.ID, true)
		})
	}
}
