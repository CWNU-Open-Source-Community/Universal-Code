package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/skills"
)

func TestSpecialistSkillContextPreparesRecoversAndCommitsMetadataOnce(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := context.Background()
	registry, err := skills.BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	service := application.NewRunService(st)
	mission, run, err := service.Create(ctx, application.CreateRunRequest{
		Goal: "Specialist Skill provenance", Profile: "code", ModelRoute: "mock/model",
		Budget: domain.Budget{MaxTurns: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	selectionResult, err := application.NewSkillSelectionService(st, registry).Select(ctx,
		application.SelectSkillsRequest{
			RunID: run.ID, Names: []string{"code", "plan-delivery"},
			OperationKey: "specialist-skill-selection-0001", RequestedBy: "operator",
		})
	if err != nil {
		t.Fatal(err)
	}
	run, err = service.Start(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	root, found, err := st.GetRootAgent(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("root Agent missing: found=%t err=%v", found, err)
	}
	child, _, err := st.AdmitSpecialist(ctx, domain.SpecialistAdmission{
		AgentID: idgen.New("agent"), SessionID: idgen.New("sess"), RunID: run.ID,
		ParentAgentID: root.ID, Title: "Skill provenance Specialist",
		Skills: []string{"model.chat"}, TurnLimit: 2, TokenLimit: 128,
		MaxChildren: 1, CreatedAt: time.Now().UTC(),
	}, "specialist-skill-admission-0001")
	if err != nil {
		t.Fatal(err)
	}
	lease := acquireTestRunExecutionLease(t, ctx, st, run.ID)
	attempt, _, err := st.BeginSpecialistAttempt(ctx, domain.AgentAttemptStart{
		AttemptID: idgen.New("attempt"), RunID: run.ID, AgentID: child.ID,
		ParentAgentID: root.ID, Lease: lease, StartedAt: time.Now().UTC(),
	}, "specialist-skill-attempt-0001")
	if err != nil {
		t.Fatal(err)
	}
	child, err = st.GetAgentNode(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	mode, err := st.GetRunMode(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	assembly, err := registry.AssembleSpecialistContext(selectionResult.Selection, mode,
		child, attempt, 0)
	if err != nil {
		t.Fatalf("assemble Specialist Skill context: %v\nselection=%#v\nmode=%#v\nchild=%#v\nattempt=%#v",
			err, selectionResult.Selection, mode, child, attempt)
	}
	request := assembly.Preparation()
	forged := request
	forged.ItemCount = 0
	forged.TokenUpperBound = 0
	forged.RedactionCount = 0
	if _, err := st.PrepareSpecialistSkillContext(ctx, attemptRef(attempt), forged); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("forged empty Code subset was accepted: code=%s err=%v",
			apperror.CodeOf(err), err)
	}
	modelAttempt := llm.ModelAttempt{
		Number: 1, TransportAttempt: 1, MaxAttempts: 2,
		Provider: "mock", Model: "model",
	}
	if _, err := st.RecordSpecialistModelStarted(ctx, attemptRef(attempt), modelAttempt); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("unprepared selected Specialist model start error = %v", err)
	}
	assertTableCount(t, st, "specialist_model_calls", 0)
	assertTableCount(t, st, "specialist_skill_context_commits", 0)
	type prepareResult struct {
		value skills.SpecialistContextPreparation
		err   error
	}
	const callers = 8
	results := make(chan prepareResult, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := st.PrepareSpecialistSkillContext(ctx, attemptRef(attempt), request)
			results <- prepareResult{value: value, err: err}
		}()
	}
	wg.Wait()
	close(results)
	var prepared skills.SpecialistContextPreparation
	fresh := 0
	for result := range results {
		if result.err != nil || result.value.ItemCount != 1 {
			t.Fatalf("concurrent Specialist Skill preparation failed: %#v err=%v",
				result.value, result.err)
		}
		if prepared.ID == "" {
			prepared = result.value
		} else if result.value.ID != prepared.ID {
			t.Fatalf("concurrent preparation produced different IDs: %s != %s",
				result.value.ID, prepared.ID)
		}
		if !result.value.Recovered {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("concurrent preparation fresh count=%d want=1", fresh)
	}
	if _, err := st.db.ExecContext(ctx, `CREATE TRIGGER fail_specialist_skill_model_start
		BEFORE INSERT ON run_events WHEN NEW.type = 'model.started'
		BEGIN SELECT RAISE(ABORT, 'injected Specialist model start failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordSpecialistModelStarted(ctx, attemptRef(attempt), modelAttempt); err == nil {
		t.Fatal("injected Specialist model start failure unexpectedly committed")
	}
	assertTableCount(t, st, "specialist_model_calls", 0)
	assertTableCount(t, st, "specialist_skill_context_commits", 0)
	if _, err := st.db.ExecContext(ctx, `DROP TRIGGER fail_specialist_skill_model_start`); err != nil {
		t.Fatal(err)
	}
	inserted, err := st.RecordSpecialistModelStarted(ctx, attemptRef(attempt), modelAttempt)
	if err != nil || !inserted {
		t.Fatalf("model start did not commit Skill context: inserted=%t err=%v", inserted, err)
	}
	inserted, err = st.RecordSpecialistModelStarted(ctx, attemptRef(attempt), modelAttempt)
	if err != nil || inserted {
		t.Fatalf("model start replay was not idempotent: inserted=%t err=%v", inserted, err)
	}
	modelAttempt.Outcome, modelAttempt.Elapsed = llm.OutcomePermanent, time.Millisecond
	modelAttempt.ErrorText = "fixture model failure after usage"
	usage := llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}
	if _, err := st.RecordSpecialistModelFailed(ctx, attemptRef(attempt), modelAttempt, &usage); err != nil {
		t.Fatal(err)
	}
	recovered, err := st.PrepareSpecialistSkillContext(ctx, attemptRef(attempt), request)
	if err != nil || !recovered.Recovered || recovered.ID != prepared.ID || recovered.SpecialistContextPreparationRequest != prepared.SpecialistContextPreparationRequest {
		t.Fatalf("recorded usage prevented exact Skill recovery: %+v %v", recovered, err)
	}
	var preparationCount, commitCount int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM specialist_skill_context_preparations WHERE agent_attempt_id = ?`, attempt.ID).
		Scan(&preparationCount); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM specialist_skill_context_commits WHERE agent_attempt_id = ?`, attempt.ID).
		Scan(&commitCount); err != nil {
		t.Fatal(err)
	}
	if preparationCount != 1 || commitCount != 1 {
		t.Fatalf("Specialist Skill ledger duplicated: preparations=%d commits=%d",
			preparationCount, commitCount)
	}
	for _, mutation := range []string{
		`UPDATE specialist_skill_context_preparations SET token_budget = token_budget + 1 WHERE id = ?`,
		`DELETE FROM specialist_skill_context_preparations WHERE id = ?`,
		`UPDATE specialist_skill_context_commits SET model_attempt = model_attempt + 1 WHERE preparation_id = ?`,
		`DELETE FROM specialist_skill_context_commits WHERE preparation_id = ?`,
	} {
		if _, err := st.db.ExecContext(ctx, mutation, prepared.ID); err == nil {
			t.Fatalf("immutable Specialist Skill context mutation succeeded: %s", mutation)
		}
	}
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if countRunEventType(timeline, events.SpecialistSkillContextPreparedEvent) != 1 ||
		countRunEventType(timeline, events.SpecialistSkillContextCommittedEvent) != 1 {
		t.Fatalf("Specialist Skill audit events drifted: %#v", timeline)
	}
	preparedIndex, committedIndex, modelIndex := -1, -1, -1
	for index, event := range timeline {
		switch event.Type {
		case events.SpecialistSkillContextPreparedEvent:
			preparedIndex = index
			assertSpecialistSkillContextEventIsMetadataOnly(t, event, assembly, false)
		case events.SpecialistSkillContextCommittedEvent:
			committedIndex = index
			assertSpecialistSkillContextEventIsMetadataOnly(t, event, assembly, true)
		case events.ModelStartedEvent:
			modelIndex = index
		}
	}
	if preparedIndex < 0 || committedIndex <= preparedIndex || modelIndex <= committedIndex {
		t.Fatalf("Specialist Skill context event order drifted: prepared=%d committed=%d model=%d",
			preparedIndex, committedIndex, modelIndex)
	}
	assertSpecialistSkillContextSchemaIsMetadataOnly(t, st)
	if mission.ID != request.MissionID {
		t.Fatalf("preparation lost Mission binding: mission=%s request=%s",
			mission.ID, request.MissionID)
	}
}

func assertSpecialistSkillContextEventIsMetadataOnly(t testing.TB, event events.Event,
	assembly skills.SpecialistContextAssembly, committed bool,
) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 15 || payload["protocol"] != skills.SpecialistContextProtocolVersion ||
		payload["item_count"] != float64(assembly.ItemCount) ||
		payload["token_budget"] != float64(assembly.TokenBudget) ||
		payload["token_upper_bound"] != float64(assembly.TokenUpperBound) ||
		payload["redaction_count"] != float64(assembly.RedactionCount) ||
		payload["context_injection"] != committed ||
		payload["tool_capability_grant"] != false {
		t.Fatalf("unsafe or incomplete Specialist Skill context event: %#v", payload)
	}
	for key := range payload {
		lower := strings.ToLower(key)
		for _, forbidden := range []string{"name", "version", "path", "content", "hash", "sha", "selection_id"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("Specialist Skill context event exposed field %q: %s", key, event.PayloadJSON)
			}
		}
	}
	for _, forbidden := range []string{
		assembly.Items[0].Version, assembly.Items[0].Content,
		assembly.Items[0].SourceSHA256, assembly.Items[0].DeliveredSHA256,
	} {
		if forbidden != "" && strings.Contains(event.PayloadJSON, forbidden) {
			t.Fatalf("Specialist Skill context event exposed %q: %s", forbidden, event.PayloadJSON)
		}
	}
}

func assertSpecialistSkillContextSchemaIsMetadataOnly(t testing.TB, st *SQLiteStore) {
	t.Helper()
	for _, table := range []string{
		"specialist_skill_context_preparations", "specialist_skill_context_commits",
	} {
		rows, err := st.db.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var ordinal int
			var name, dataType string
			var notNull, primaryKey int
			var defaultValue any
			if err := rows.Scan(&ordinal, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			lower := strings.ToLower(name)
			for _, forbidden := range []string{
				"content", "path", "skill_name", "skill_version", "source_sha", "delivered_sha",
			} {
				if strings.Contains(lower, forbidden) {
					rows.Close()
					t.Fatalf("%s contains forbidden durable column %q", table, name)
				}
			}
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSchemaV46UpgradesToSpecialistSkillContextLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v46.db")
	st := openHistoricalTestDatabase(t, path, 46)

	ctx := context.Background()
	// The immutable historical prefix above is the upgrade input.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if version, err := st.SchemaVersion(ctx); err != nil || version != LatestSchemaVersion {
		t.Fatalf("v46 database did not upgrade: version=%d err=%v", version, err)
	}
	for _, table := range []string{
		"specialist_skill_context_preparations", "specialist_skill_context_commits",
	} {
		var count int
		if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
			WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("v47 table %s missing: count=%d err=%v", table, count, err)
		}
	}
}
