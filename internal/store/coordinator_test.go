package store

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/session"
)

func TestSQLiteUpgradesV18AndLazilyRegistersExistingRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v18.db")
	st := openHistoricalTestDatabase(t, filepath.Join(t.TempDir(), "seed.db"), 177)

	ctx := context.Background()
	_, run, err := newMigrationFixtureRunService(t, st).Create(ctx, application.CreateRunRequest{
		Goal: "upgrade coordinator", Profile: "review", Budget: domain.Budget{MaxTurns: 6},
	})
	if err != nil {
		t.Fatal(err)
	}
	st = historicalTestDatabaseFromSeed(t, st, path, 18)
	if _, err := st.db.ExecContext(ctx, `DELETE FROM run_events WHERE type LIKE 'agent.%'`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	version, err := st.SchemaVersion(ctx)
	if err != nil || version != LatestSchemaVersion {
		t.Fatalf("v18 database did not upgrade to v19: version=%d err=%v", version, err)
	}
	if _, found, err := st.GetRootAgent(ctx, run.ID); err != nil || found {
		t.Fatalf("migration unexpectedly invented a root outside a Run transaction: found=%t err=%v", found, err)
	}
	root, created, err := st.RegisterRootAgent(ctx, run.ID)
	if err != nil || !created || root.Status != domain.AgentReady {
		t.Fatalf("lazy root registration failed: root=%#v created=%t err=%v", root, created, err)
	}
	repeated, created, err := st.RegisterRootAgent(ctx, run.ID)
	if err != nil || created || repeated.ID != root.ID {
		t.Fatalf("lazy registration was not idempotent: root=%#v created=%t err=%v", repeated, created, err)
	}
	graph, err := st.RestoreAgentGraph(ctx, run.ID)
	if err != nil || graph.RootAgentID != root.ID || graph.LatestSnapshot.Version != 1 {
		t.Fatalf("upgraded graph is not recoverable: graph=%#v err=%v", graph, err)
	}
}

func TestAgentGraphSnapshotDetectsInboxTamperingAndRetainsBoundedHistory(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	_, run, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{
		Goal: "snapshot integrity", Profile: "code", Budget: domain.Budget{MaxTurns: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	root, found, err := st.GetRootAgent(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("root agent was not created: found=%t err=%v", found, err)
	}
	message, replayed, err := st.SendAgentMessage(ctx, domain.AgentMessage{
		ID: idgen.New("agentmsg"), RunID: run.ID, RecipientAgentID: root.ID,
		Kind: domain.AgentMessageInstruction, Semantic: domain.AgentMessageSemanticMessage,
		PayloadJSON: `{"goal":"inspect"}`,
		Status:      domain.AgentMessagePending,
	}, "snapshot-integrity-message")
	if err != nil {
		t.Fatal(err)
	}
	if replayed {
		t.Fatal("first agent message send was reported as a replay")
	}
	if _, err := st.RestoreAgentGraph(ctx, run.ID); err != nil {
		t.Fatalf("valid graph did not restore: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE agent_messages SET payload_json = ? WHERE id = ?`,
		`{"goal":"tampered"}`, message.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RestoreAgentGraph(ctx, run.ID); apperror.CodeOf(err) != apperror.CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "does not match") {
		t.Fatalf("tampered inbox was not rejected: code=%s err=%v", apperror.CodeOf(err), err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE agent_messages SET payload_json = ? WHERE id = ?`,
		message.PayloadJSON, message.ID); err != nil {
		t.Fatal(err)
	}
	for range domain.MaxAgentGraphSnapshots + 5 {
		if _, err := st.SnapshotAgentGraph(ctx, run.ID); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var minimum, maximum int64
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*), MIN(version), MAX(version)
		FROM agent_graph_snapshots WHERE run_id = ?`, run.ID).Scan(&count, &minimum, &maximum); err != nil {
		t.Fatal(err)
	}
	if count != domain.MaxAgentGraphSnapshots || maximum-minimum != int64(domain.MaxAgentGraphSnapshots-1) {
		t.Fatalf("snapshot retention is not bounded: count=%d min=%d max=%d", count, minimum, maximum)
	}
}

func TestAgentCoordinatorSchemaKeepsChildCreationDisabled(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	_, run, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{
		Goal: "no child execution", Profile: "code", Budget: domain.Budget{MaxTurns: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	root, found, err := st.GetRootAgent(ctx, run.ID)
	if err != nil || !found || root.ChildLimit != 0 {
		t.Fatalf("unexpected root: root=%#v found=%t err=%v", root, found, err)
	}
	childSession := session.New("", "blocked child", "code")
	if err := st.SaveSession(ctx, childSession); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	_, err = st.db.ExecContext(ctx, `INSERT INTO agent_nodes
		(id, run_id, parent_id, session_id, role, profile, skills_json, status, depth, child_limit,
		turn_limit, token_limit, turns_used, tokens_used, active_attempt_id, status_reason, version,
		created_at, updated_at, finished_at)
		VALUES (?, ?, ?, ?, 'specialist', 'code', '["model.chat"]', 'ready', 1, 0,
		1, 0, 0, 0, '', '', 1, ?, ?, NULL)`, idgen.New("agent"), run.ID, root.ID,
		childSession.ID, ts(now), ts(now))
	if err == nil || !strings.Contains(err.Error(), "agent child depth or active limit is invalid") {
		t.Fatalf("schema allowed a child while root child_limit=0: %v", err)
	}
	var count int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_nodes WHERE run_id = ?`, run.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("blocked child insert changed graph size to %d", count)
	}
}

func TestAgentInboxWakeIsIdempotentAndDoesNotLeakOperationKey(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	runService := application.NewRunService(st)
	_, run, err := runService.Create(ctx, application.CreateRunRequest{
		Goal: "wake a bounded specialist", Profile: "code",
		Budget: domain.Budget{MaxTurns: 8, MaxTokens: 1000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runService.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	root, found, err := st.GetRootAgent(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("root agent was not created: found=%t err=%v", found, err)
	}
	childSession := session.New("", "wake test specialist", "code")
	if err := st.SaveSession(ctx, childSession); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE agent_nodes SET child_limit = 1 WHERE id = ?`, root.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	child := domain.AgentNode{
		ID: idgen.New("agent"), RunID: run.ID, ParentID: root.ID, SessionID: childSession.ID,
		Role: domain.AgentRoleSpecialist, Profile: domain.ProfileCode, Skills: []string{"model.chat"},
		Status: domain.AgentWaiting, Depth: 1, ChildLimit: 0, TurnLimit: 2, TokenLimit: 200,
		StatusReason: "dependency pending", Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := child.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO agent_nodes
		(id, run_id, parent_id, session_id, role, profile, skills_json, status, depth, child_limit,
		turn_limit, token_limit, turns_used, tokens_used, active_attempt_id, status_reason, version,
		created_at, updated_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, '', ?, ?, ?, ?, NULL)`,
		child.ID, child.RunID, child.ParentID, child.SessionID, child.Role, child.Profile, `["model.chat"]`,
		child.Status, child.Depth, child.ChildLimit, child.TurnLimit, child.TokenLimit, child.StatusReason,
		child.Version, ts(child.CreatedAt), ts(child.UpdatedAt)); err != nil {
		t.Fatal(err)
	}

	operationKey := "wake-specialist-operation-0001"
	request := domain.AgentMessage{
		ID: idgen.New("agentmsg"), RunID: run.ID, SenderAgentID: root.ID,
		RecipientAgentID: child.ID, Kind: domain.AgentMessageControl,
		Semantic: domain.AgentMessageSemanticWake, PayloadJSON: `{"reason":"dependency resolved"}`,
	}
	message, replayed, err := st.SendAgentMessage(ctx, request, operationKey)
	if err != nil || replayed {
		t.Fatalf("first wake failed: message=%#v replayed=%t err=%v", message, replayed, err)
	}
	woken, err := st.GetAgentNode(ctx, child.ID)
	if err != nil || woken.Status != domain.AgentReady || woken.Version != 2 ||
		woken.StatusReason != "dependency resolved" {
		t.Fatalf("specialist was not woken exactly once: node=%#v err=%v", woken, err)
	}
	var eventsBefore, snapshotsBefore int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events WHERE run_id = ?`, run.ID).
		Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_graph_snapshots WHERE run_id = ?`, run.ID).
		Scan(&snapshotsBefore); err != nil {
		t.Fatal(err)
	}
	request.ID = idgen.New("agentmsg")
	repeated, replayed, err := st.SendAgentMessage(ctx, request, operationKey)
	if err != nil || !replayed || repeated.ID != message.ID {
		t.Fatalf("wake replay was not stable: message=%#v replayed=%t err=%v", repeated, replayed, err)
	}
	var eventsAfter, snapshotsAfter int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events WHERE run_id = ?`, run.ID).
		Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_graph_snapshots WHERE run_id = ?`, run.ID).
		Scan(&snapshotsAfter); err != nil {
		t.Fatal(err)
	}
	if eventsAfter != eventsBefore || snapshotsAfter != snapshotsBefore {
		t.Fatalf("wake replay duplicated durable effects: events %d->%d snapshots %d->%d",
			eventsBefore, eventsAfter, snapshotsBefore, snapshotsAfter)
	}
	request.PayloadJSON = `{"reason":"different intent"}`
	if _, _, err := st.SendAgentMessage(ctx, request, operationKey); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("changed wake intent did not conflict: code=%s err=%v", apperror.CodeOf(err), err)
	}
	var storedDigest string
	if err := st.db.QueryRowContext(ctx, `SELECT operation_key_digest FROM agent_message_operations
		WHERE message_id = ?`, message.ID).Scan(&storedDigest); err != nil {
		t.Fatal(err)
	}
	if storedDigest == operationKey || len(storedDigest) != 64 {
		t.Fatalf("raw operation key was persisted: %q", storedDigest)
	}
	var leaked int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events
		WHERE run_id = ? AND payload_json LIKE ?`, run.ID, "%"+operationKey+"%").Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatalf("raw operation key leaked into %d audit events", leaked)
	}

	rootWake := domain.AgentMessage{
		ID: idgen.New("agentmsg"), RunID: run.ID, RecipientAgentID: root.ID,
		Kind: domain.AgentMessageControl, Semantic: domain.AgentMessageSemanticWake,
		PayloadJSON: `{"reason":"must not resume root"}`,
	}
	if _, _, err := st.SendAgentMessage(ctx, rootWake, "root-wake-operation-0001"); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("root wake was not rejected: code=%s err=%v", apperror.CodeOf(err), err)
	}
	dependency := domain.AgentMessage{
		ID: idgen.New("agentmsg"), RunID: run.ID, RecipientAgentID: child.ID,
		Kind: domain.AgentMessageNotification, Semantic: domain.AgentMessageSemanticDependency,
		PayloadJSON: `{"dependency_id":"work-1","state":"satisfied"}`,
	}
	if _, _, err := st.SendAgentMessage(ctx, dependency, "dependency-operation-0001"); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("senderless dependency was not rejected: code=%s err=%v", apperror.CodeOf(err), err)
	}
	dependency.SenderAgentID = root.ID
	if _, replayed, err := st.SendAgentMessage(ctx, dependency, "dependency-operation-0002"); err != nil || replayed {
		t.Fatalf("valid dependency notification failed: replayed=%t err=%v", replayed, err)
	}
}

func TestSQLiteUpgradesV19InboxToSemanticProtocol(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v19.db")
	st := openHistoricalTestDatabase(t, filepath.Join(t.TempDir(), "seed.db"), 177)

	ctx := context.Background()
	_, run, err := newMigrationFixtureRunService(t, st).Create(ctx, application.CreateRunRequest{
		Goal: "preserve v19 inbox", Profile: "review", Budget: domain.Budget{MaxTurns: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	root, found, err := st.GetRootAgent(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("root agent was not created: found=%t err=%v", found, err)
	}
	message, _, err := st.SendAgentMessage(ctx, domain.AgentMessage{
		ID: idgen.New("agentmsg"), RunID: run.ID, RecipientAgentID: root.ID,
		Kind: domain.AgentMessageInstruction, Semantic: domain.AgentMessageSemanticMessage,
		PayloadJSON: `{"goal":"preserve"}`,
	}, "v19-preserved-message")
	if err != nil {
		t.Fatal(err)
	}
	before, found, err := st.GetLatestAgentGraphSnapshot(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("v19 snapshot was not created: found=%t err=%v", found, err)
	}
	st = historicalTestDatabaseFromSeed(t, st, path, 19)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	version, err := st.SchemaVersion(ctx)
	if err != nil || version != LatestSchemaVersion {
		t.Fatalf("v19 database did not upgrade to v20: version=%d err=%v", version, err)
	}
	messages, err := st.ListAgentMessages(ctx, root.ID, true, 10)
	if err != nil || len(messages) != 1 || messages[0].ID != message.ID ||
		messages[0].Semantic != domain.AgentMessageSemanticMessage {
		t.Fatalf("v19 message was not preserved: messages=%#v err=%v", messages, err)
	}
	after, found, err := st.GetLatestAgentGraphSnapshot(ctx, run.ID)
	if err != nil || !found || after.StateJSON != before.StateJSON {
		t.Fatalf("v19 snapshot compatibility changed: found=%t err=%v\nbefore=%s\nafter=%s",
			found, err, before.StateJSON, after.StateJSON)
	}
}

func TestSpecialistAdmissionIsAtomicPrivateAndReducesSupervisorBudget(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "admission.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	runService := application.NewRunService(st)
	_, run, err := runService.Create(ctx, application.CreateRunRequest{
		Goal: "reserve specialist budget", Profile: "code",
		Budget: domain.Budget{MaxTurns: 6, MaxTokens: 600},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runService.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	root, found, err := st.GetRootAgent(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("root was not created: found=%t err=%v", found, err)
	}
	admission := domain.SpecialistAdmission{
		AgentID: idgen.New("agent"), SessionID: idgen.New("sess"), RunID: run.ID,
		ParentAgentID: root.ID, Title: "analyst " + "sk-" + strings.Repeat("a", 32),
		Skills: []string{"model.chat"}, TurnLimit: 6, TokenLimit: 200,
		MaxChildren: 2, CreatedAt: time.Now().UTC(),
	}
	if _, _, err := st.AdmitSpecialist(ctx, admission, "oversized-admission-operation"); apperror.CodeOf(err) != apperror.CodeResourceExhausted {
		t.Fatalf("oversized turn reservation was not rejected: code=%s err=%v", apperror.CodeOf(err), err)
	}
	if _, err := st.db.ExecContext(ctx, `CREATE TRIGGER fail_specialist_registered
		BEFORE INSERT ON run_events WHEN NEW.type = 'agent.registered'
		BEGIN SELECT RAISE(ABORT, 'forced specialist event failure'); END;`); err != nil {
		t.Fatal(err)
	}
	admission.TurnLimit = 2
	operationKey := "private-admission-operation-0001"
	if _, _, err := st.AdmitSpecialist(ctx, admission, operationKey); err == nil {
		t.Fatal("forced event failure did not roll back specialist admission")
	}
	var children, sessions, operations int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_nodes WHERE run_id = ? AND parent_id = ?`,
		run.ID, root.ID).Scan(&children); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE id = ?`,
		admission.SessionID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_admission_operations`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	rolledBackRoot, err := st.GetAgentNode(ctx, root.ID)
	if err != nil || children != 0 || sessions != 0 || operations != 0 || rolledBackRoot.ChildLimit != 0 {
		t.Fatalf("failed admission left state behind: children=%d sessions=%d operations=%d root=%#v err=%v",
			children, sessions, operations, rolledBackRoot, err)
	}
	if _, err := st.db.ExecContext(ctx, `DROP TRIGGER fail_specialist_registered`); err != nil {
		t.Fatal(err)
	}
	child, replayed, err := st.AdmitSpecialist(ctx, admission, operationKey)
	if err != nil || replayed {
		t.Fatalf("valid specialist admission failed: child=%#v replayed=%t err=%v", child, replayed, err)
	}
	childSession, err := st.GetSession(ctx, child.SessionID)
	if err != nil || strings.Contains(childSession.Title, "sk-aaaaaaaa") ||
		!strings.Contains(childSession.Title, "[REDACTED:api-key]") {
		t.Fatalf("specialist title was not redacted: session=%#v err=%v", childSession, err)
	}
	var storedDigest string
	if err := st.db.QueryRowContext(ctx, `SELECT operation_key_digest FROM agent_admission_operations
		WHERE agent_id = ?`, child.ID).Scan(&storedDigest); err != nil {
		t.Fatal(err)
	}
	if storedDigest == operationKey || len(storedDigest) != 64 {
		t.Fatalf("raw specialist admission key was persisted: %q", storedDigest)
	}
	lease := acquireTestRunExecutionLease(t, ctx, st, run.ID)
	turn, err := st.BeginSupervisorTurn(ctx, lease, "")
	if err != nil {
		t.Fatal(err)
	}
	if turn.Run.Budget.MaxTurns != 4 || turn.Run.Budget.MaxTokens != 400 ||
		turn.Agent.TurnLimit != 4 || turn.Agent.TokenLimit != 400 {
		t.Fatalf("specialist reservation did not reduce root execution budget: turn=%#v agent=%#v",
			turn.Run.Budget, turn.Agent)
	}
}

func TestSQLiteUpgradesV20ToSpecialistAdmissionLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v20.db")
	st := openCurrentTestDatabase(t, filepath.Join(t.TempDir(), "seed.db"))

	ctx := context.Background()
	_, run, err := newMigrationFixtureRunService(t, st).Create(ctx, application.CreateRunRequest{
		Goal: "preserve v20 root", Profile: "learn", Budget: domain.Budget{MaxTurns: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	root, found, err := st.GetRootAgent(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("root was not created: found=%t err=%v", found, err)
	}
	st = historicalTestDatabaseFromSeed(t, st, path, 20)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	version, err := st.SchemaVersion(ctx)
	if err != nil || version != LatestSchemaVersion {
		t.Fatalf("v20 database did not upgrade to v21: version=%d err=%v", version, err)
	}
	preserved, found, err := st.GetRootAgent(ctx, run.ID)
	if err != nil || !found || preserved.ID != root.ID || preserved.ChildLimit != 0 {
		t.Fatalf("v20 root was not preserved: root=%#v found=%t err=%v", preserved, found, err)
	}
}

func TestConcurrentSpecialistAdmissionConvergesAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent-admission.db")
	firstStore, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.Close()
	ctx := context.Background()
	runService := application.NewRunService(firstStore)
	_, run, err := runService.Create(ctx, application.CreateRunRequest{
		Goal: "converge specialist admission", Profile: "review",
		Budget: domain.Budget{MaxTurns: 8, MaxTokens: 800},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runService.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	root, found, err := firstStore.GetRootAgent(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("root was not created: found=%t err=%v", found, err)
	}
	secondStore, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	stores := []*SQLiteStore{firstStore, secondStore}
	type result struct {
		agent    domain.AgentNode
		replayed bool
		err      error
	}
	results := make(chan result, len(stores))
	var wait sync.WaitGroup
	for index, current := range stores {
		wait.Add(1)
		go func(index int, current *SQLiteStore) {
			defer wait.Done()
			now := time.Now().UTC()
			agent, replayed, err := current.AdmitSpecialist(ctx, domain.SpecialistAdmission{
				AgentID: idgen.New("agent"), SessionID: idgen.New("sess"), RunID: run.ID,
				ParentAgentID: root.ID, Title: "concurrent reviewer", Skills: []string{"model.chat"},
				TurnLimit: 2, TokenLimit: 200, MaxChildren: 2, CreatedAt: now.Add(time.Duration(index)),
			}, "concurrent-admission-operation")
			results <- result{agent: agent, replayed: replayed, err: err}
		}(index, current)
	}
	wait.Wait()
	close(results)
	agentID := ""
	replays := 0
	for current := range results {
		if current.err != nil {
			t.Fatalf("concurrent specialist admission failed: %v", current.err)
		}
		if agentID == "" {
			agentID = current.agent.ID
		}
		if current.agent.ID != agentID {
			t.Fatalf("concurrent admission returned different Agents: %s and %s", agentID, current.agent.ID)
		}
		if current.replayed {
			replays++
		}
	}
	if replays != 1 {
		t.Fatalf("concurrent admission replay count = %d, want 1", replays)
	}
	var children, operations int
	if err := firstStore.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_nodes
		WHERE run_id = ? AND parent_id = ?`, run.ID, root.ID).Scan(&children); err != nil {
		t.Fatal(err)
	}
	if err := firstStore.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_admission_operations`).
		Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if children != 1 || operations != 1 {
		t.Fatalf("concurrent admission did not converge: children=%d operations=%d", children, operations)
	}
}
