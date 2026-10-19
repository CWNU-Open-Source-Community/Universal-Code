package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/testfixtures/legacyskill"
	"cyberagent-workbench/internal/toolgateway"
)

const testAccessToken = "api-test-token-0123456789-abcdefghijk"

const testControlToken = "api-control-token-0123456789-abcdefgh"

type apiTestEnvelope struct {
	Version   string          `json:"version"`
	RequestID string          `json:"request_id"`
	Data      json.RawMessage `json:"data"`
	Page      *Page           `json:"page,omitempty"`
	Error     *apiErrorView   `json:"error,omitempty"`
}

type apiFixture struct {
	api               *API
	store             *store.SQLiteStore
	dbPath            string
	run               domain.Run
	root              domain.AgentNode
	workItems         []domain.WorkItem
	notes             []domain.Note
	artifactID        string
	secret            string
	leaseID           string
	checkpoint        domain.SupervisorCheckpoint
	attempt           llm.ModelAttempt
	externalSelection skills.ExternalSelection
	workspace         store.WorkspaceRecord
}

func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "http-api.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	workspace := store.WorkspaceRecord{ID: "workspace-http-api", Name: "http-api",
		RootPath: t.TempDir(), CreatedAt: time.Now().UTC()}
	if err := st.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	runs := application.NewRunService(st)
	_, run, err := runs.Create(ctx, application.CreateRunRequest{
		Goal: "inspect durable agent state", Profile: "review", ModelRoute: "review",
		WorkspaceID: workspace.ID, Budget: domain.Budget{MaxTurns: 8, MaxToolCalls: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	externalSelection := prepareAPIExternalSkillProjection(t, st, dbPath, run)
	run, err = runs.Start(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	root, _, err := st.RegisterRootAgent(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []session.Message{
		session.NewMessage(run.SessionID, "user", "inspect the project"),
		session.NewMessage(run.SessionID, "assistant", "inspection started"),
	} {
		if _, err := st.SaveSessionMessage(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
	compacted := session.NewMessage(run.SessionID, "assistant", "old compacted context")
	compacted.Compacted = true
	if _, err := st.SaveSessionMessage(ctx, compacted); err != nil {
		t.Fatal(err)
	}

	workService := application.NewWorkItemService(st)
	workItems := make([]domain.WorkItem, 0, 3)
	for index := 1; index <= 3; index++ {
		item, err := workService.Create(ctx, application.CreateWorkItemRequest{
			RunID: run.ID, Title: fmt.Sprintf("API work item %d", index), Owner: "root",
			OwnerAgentID:       root.ID,
			AcceptanceCriteria: []string{fmt.Sprintf("criterion %d", index)},
		})
		if err != nil {
			t.Fatal(err)
		}
		workItems = append(workItems, item)
	}

	secret := "sk-" + strings.Repeat("q", 30)
	noteService := application.NewNoteService(st)
	notes := make([]domain.Note, 0, 3)
	for index := 1; index <= 3; index++ {
		content := fmt.Sprintf("durable observation %d", index)
		if index == 1 {
			content += " token=" + secret
		}
		note, err := noteService.Create(ctx, application.CreateNoteRequest{
			RunID: run.ID, Title: fmt.Sprintf("API note %d", index), Content: content,
			OwnerAgentID: root.ID, Tags: []string{"api", fmt.Sprintf("page-%d", index)}, Pinned: index == 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		notes = append(notes, note)
	}

	gateway := toolgateway.New(st, policy.NewDefaultChecker())
	proposed, err := gateway.Invoke(ctx, toolgateway.ToolCall{
		Name: toolgateway.ShellTool, Arguments: map[string]string{"command": "echo api evidence"},
		RunID: run.ID, SessionID: run.SessionID, WorkspaceID: workspace.ID,
		WorkspaceRoot: workspace.RootPath, RequestedBy: "http_api_test",
	})
	if err != nil || proposed.Proposal == nil {
		t.Fatalf("artifact proposal failed: %#v err=%v", proposed, err)
	}
	reviewed, err := gateway.Review(ctx, toolgateway.ReviewRequest{
		Action: toolgateway.ReviewApprove, Tool: toolgateway.ShellTool,
		ProposalID: proposed.Proposal.ID, ReviewedBy: "http_api_test",
	})
	if err != nil || reviewed.Result == nil || reviewed.Result.Metadata["artifact_stdout_id"] == "" {
		t.Fatalf("artifact capture failed: %#v err=%v", reviewed, err)
	}
	acquiredLease, err := st.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{
		RunID: run.ID, OwnerID: "http-api-test-worker", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := st.BeginSupervisorTurn(ctx, acquiredLease.Lease, "pending input is deliberately private")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PrepareExternalRootSkillContext(ctx, turn.Checkpoint,
		skills.ExternalRootContextPreparationRequest{
			RunID: run.ID, MissionID: run.MissionID, RootAgentID: root.ID,
			SupervisorAttemptID: turn.Checkpoint.AttemptID,
			Turn:                turn.Checkpoint.NextTurn, SelectionID: externalSelection.ID,
			ProtocolVersion: skills.ExternalContextProtocolVersion,
			Surface:         externalSelection.Surface, Profile: externalSelection.Profile,
			SelectionFingerprint: externalSelection.Fingerprint,
			ContextFingerprint: runmutation.Fingerprint("http-api-external-context",
				externalSelection.Fingerprint),
			ItemCount: externalSelection.ItemCount, TokenBudget: externalSelection.TokenBudget,
			TokenUpperBound: externalSelection.TokenUpperBound,
		}); err != nil {
		t.Fatal(err)
	}
	attempt := llm.ModelAttempt{
		Number: 1, TransportAttempt: 1, MaxAttempts: 3, Provider: "http-api-test", Model: "test-model",
	}
	if inserted, err := st.RecordSupervisorModelStarted(ctx, turn.Checkpoint, attempt); err != nil || !inserted {
		t.Fatalf("fixture model start inserted=%t err=%v", inserted, err)
	}
	api, err := New(st, Config{
		AccessToken: testAccessToken, ControlToken: testControlToken,
		RunControlEnabled: true, RunCreationEnabled: true, SessionMessageEnabled: true,
		ExecutionPermissionControlEnabled: true,
		ExecutionPermissionCapabilities: domain.ExecutionPermissionRuntimeCapabilities{
			OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
		},
		BrowserCDPPermissionControlEnabled: true,
		BrowserCDPPermissionCapabilities: domain.BrowserCDPPermissionRuntimeCapabilities{
			ControlEnabled: true, FullDebugEnabled: true,
		},
		SessionSteeringControlEnabled: true,
		AppVersion:                    "test-version",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &apiFixture{api: api, store: st, dbPath: dbPath, run: run, root: root,
		workItems: workItems, notes: notes,
		artifactID: reviewed.Result.Metadata["artifact_stdout_id"], secret: secret,
		leaseID: acquiredLease.Lease.LeaseID, checkpoint: turn.Checkpoint, attempt: attempt,
		externalSelection: externalSelection, workspace: workspace}
}

func prepareAPIExternalSkillProjection(t *testing.T, st *store.SQLiteStore, databasePath string,
	run domain.Run,
) skills.ExternalSelection {
	t.Helper()
	ctx := context.Background()
	const name = "api-projection-review"
	const version = "1.0.0"
	content := []byte("# API projection review\n")
	contentDigest := sha256.Sum256(content)
	archiveDigest := sha256.Sum256([]byte("archive:" + name + "@" + version))
	createdAt := time.Now().UTC().Add(-time.Minute)
	operationDigest := runmutation.Fingerprint("skill_package_install_operation.v1",
		"api-projection-install-operation")
	installation := skills.PackageInstallation{
		ID: idgen.New("skill-install"), ProtocolVersion: skills.PackageInstallationProtocolVersion,
		Name: name, Version: version, Surface: domain.ExecutionSurfaceCode,
		Manifest: skills.Manifest{
			Protocol: skills.ProtocolVersion, Name: name, Version: version,
			Description: "External API projection fixture.",
			Profiles:    []domain.Profile{domain.ProfileReview},
			ToolDependencies: []toolgateway.ToolName{
				toolgateway.ListWorkspaceTool, toolgateway.ReadFileTool,
			},
			ContentPath:   skills.PackageContentPath,
			ContentSHA256: hex.EncodeToString(contentDigest[:]),
			ContentBytes:  len(content), ContentTokenUpperBound: len(content),
		},
		ArchiveSHA256: hex.EncodeToString(archiveDigest[:]), ArchiveBytes: 512,
		UncompressedBytes: 256, EntryCount: skills.PackageEntryCount,
		PackageFingerprint: runmutation.Fingerprint("package", name, version,
			hex.EncodeToString(contentDigest[:])),
		TrustClass: skills.PackageTrustOperatorInstalledUntrusted,
		RiskCodes: []skills.PackageRiskCode{
			skills.PackageRiskUntrustedInstructions, skills.PackageRiskDeclaredToolsOnly,
		},
		OperatorConfirmed: true, OperationKeyDigest: operationDigest,
		InstalledBy: "api-fixture-operator", CreatedAt: createdAt,
	}
	installation.RequestFingerprint = skills.PackageInstallationIntentFingerprint(installation)
	installation.InstallationFingerprint = skills.PackageInstallationFingerprint(installation)
	objectKey, err := skills.PackageObjectKey(installation.ArchiveSHA256)
	if err != nil {
		t.Fatal(err)
	}
	result, err := skills.NewPackageInstallResult(installation, skills.PackageObjectReceipt{
		Descriptor: skills.DescriptorForInstallation(installation), ObjectKey: objectKey,
	}, createdAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	legacyskill.Seed(t, databasePath, installation)
	if _, _, err := st.CompletePackageInstallation(ctx, result); err != nil {
		t.Fatal(err)
	}
	selected, err := application.NewExternalSkillSelectionService(st).Select(ctx,
		application.SelectExternalSkillsRequest{
			RunID: run.ID, PackageRefs: []string{name + "@" + version},
			SpecialistRef: name + "@" + version, TokenBudget: 1024,
			OperationKey:            "api-projection-selection-operation",
			RequestedBy:             "private-api-projection-operator",
			ConfirmUntrustedContext: true,
		})
	if err != nil {
		t.Fatal(err)
	}
	return selected.Selection
}

func assertExternalSkillResponseIsMetadataOnly(t *testing.T, body string,
	selection skills.ExternalSelection,
) {
	t.Helper()
	assertExternalSkillPrivateValuesOmitted(t, body, selection)
	for _, forbiddenField := range []string{
		`"selection_id"`, `"mission_id"`, `"mode_snapshot_id"`,
		`"installation_id"`, `"requested_by"`, `"object_key"`, `"content_sha256"`,
		`"archive_sha256"`, `"package_fingerprint"`,
	} {
		if strings.Contains(body, forbiddenField) {
			t.Fatalf("external Skill HTTP projection exposed private field %s: %s",
				forbiddenField, body)
		}
	}
}

func assertExternalSkillPrivateValuesOmitted(t *testing.T, body string,
	selection skills.ExternalSelection,
) {
	t.Helper()
	for _, forbidden := range []string{
		selection.ID, selection.ModeSnapshotID,
		selection.Fingerprint, selection.RequestedBy,
		selection.Items[0].InstallationID, selection.Items[0].InstallationFingerprint,
		selection.Items[0].InstallResultFingerprint, selection.Items[0].ContentSHA256,
		selection.Items[0].ArchiveSHA256, selection.Items[0].PackageFingerprint,
		selection.Items[0].ObjectKey,
	} {
		if forbidden != "" && strings.Contains(body, forbidden) {
			t.Fatalf("external Skill HTTP projection leaked private value %q: %s",
				forbidden, body)
		}
	}
}

func TestReadAPIExposesDurableStateWithoutArtifactContentOrCheckpointInput(t *testing.T) {
	fixture := newAPIFixture(t)
	steeringContent := "private queued operator guidance"
	steering, err := fixture.store.EnqueueOperatorSteering(context.Background(),
		domain.EnqueueOperatorSteeringRequest{
			RunID: fixture.run.ID, SessionID: fixture.run.SessionID,
			Content: steeringContent, OperationKey: "http-api-steering-operation-0001",
			RequestedBy: "private_operator_identity",
		})
	if err != nil {
		t.Fatal(err)
	}

	health := fixture.get(t, "/api/v1/health")
	if health.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", health.Code, health.Body.String())
	}
	assertSecurityHeaders(t, health)
	var healthData struct {
		Status        string `json:"status"`
		SchemaVersion int    `json:"schema_version"`
	}
	decodeData(t, health, &healthData)
	if healthData.Status != "ok" || healthData.SchemaVersion != store.LatestSchemaVersion {
		t.Fatalf("unexpected health data: %#v", healthData)
	}

	runs := fixture.get(t, "/api/v1/runs?limit=100&status=running")
	var runViews []RunView
	decodeData(t, runs, &runViews)
	if len(runViews) != 1 || runViews[0].ID != fixture.run.ID {
		t.Fatalf("unexpected Run list: %#v", runViews)
	}
	runDetailResponse := fixture.get(t, "/api/v1/runs/"+fixture.run.ID)
	if strings.Contains(runDetailResponse.Body.String(), "pending input is deliberately private") ||
		strings.Contains(runDetailResponse.Body.String(), steeringContent) ||
		strings.Contains(runDetailResponse.Body.String(), "private_operator_identity") ||
		strings.Contains(runDetailResponse.Body.String(), steering.Message.ContentSHA256) {
		t.Fatal("Run detail exposed private Supervisor or operator steering data")
	}
	assertExternalSkillPrivateValuesOmitted(t, runDetailResponse.Body.String(),
		fixture.externalSelection)
	var runDetail RunDetailView
	decodeData(t, runDetailResponse, &runDetail)
	if runDetail.Run.ID != fixture.run.ID || runDetail.Mission.Goal == "" || runDetail.Checkpoint == nil ||
		runDetail.Mode.ProtocolVersion != domain.RunModeProtocolVersion ||
		runDetail.Mode.Surface != string(domain.ExecutionSurfaceCode) ||
		runDetail.Mode.Phase != string(domain.ExecutionPhaseDeliver) ||
		runDetail.Mode.Revision != 1 || runDetail.Mode.CapabilityGrant ||
		runDetail.Checkpoint.Phase != string(domain.SupervisorTurnStarted) || runDetail.ToolUsage.Consumed != 1 ||
		runDetail.Lease == nil || !runDetail.Lease.Active || runDetail.Lease.Generation != 1 ||
		runDetail.Lease.OwnerID != "http-api-test-worker" ||
		runDetail.Steering.Pending != 1 || runDetail.Steering.Prepared != 0 ||
		len(runDetail.Steering.Messages) != 1 ||
		runDetail.Steering.Messages[0].ID != steering.Message.ID ||
		runDetail.Steering.Messages[0].Sequence != 1 || runDetail.ExternalSkills == nil ||
		runDetail.ExternalSkills.ProtocolVersion != skills.ExternalSkillProjectionProtocolVersion ||
		runDetail.ExternalSkills.ItemCount != 1 || len(runDetail.ExternalSkills.Items) != 1 ||
		runDetail.ExternalSkills.Items[0].Name != "api-projection-review" ||
		runDetail.AgentCodeTools.ProtocolVersion != toolgateway.AgentCodeRegistryVersion ||
		runDetail.AgentCodeTools.Generation == "" || len(runDetail.AgentCodeTools.Tools) != 9 {
		t.Fatalf("unexpected Run detail: %#v", runDetail)
	}
	agentCodeAvailability := make(map[string]bool, len(runDetail.AgentCodeTools.Tools))
	for _, capability := range runDetail.AgentCodeTools.Tools {
		agentCodeAvailability[capability.Name] = capability.Available
	}
	if !agentCodeAvailability[string(toolgateway.WorkspaceReadTool)] ||
		!agentCodeAvailability[string(toolgateway.GitHubEvidenceListTool)] ||
		!agentCodeAvailability[string(toolgateway.GitHubEvidenceReadTool)] ||
		agentCodeAvailability[string(toolgateway.WorkspaceChangeTool)] {
		t.Fatalf("unexpected Review profile workspace tool matrix: %#v", runDetail.AgentCodeTools)
	}
	if strings.Contains(runDetailResponse.Body.String(), `"lease_id"`) {
		t.Fatal("Run detail exposed the execution fencing token")
	}
	externalSkillsResponse := fixture.get(t,
		"/api/v1/runs/"+fixture.run.ID+"/external-skills")
	assertExternalSkillResponseIsMetadataOnly(t, externalSkillsResponse.Body.String(),
		fixture.externalSelection)
	var externalSkills ExternalSkillProjectionView
	decodeData(t, externalSkillsResponse, &externalSkills)
	if externalSkills.RunID != fixture.run.ID || externalSkills.ItemCount != 1 ||
		len(externalSkills.Items) != 1 || externalSkills.Items[0].Name != "api-projection-review" ||
		externalSkills.Items[0].TrustClass != string(skills.PackageTrustOperatorInstalledUntrusted) ||
		externalSkills.ToolCapabilityGrant || !externalSkills.ContextDeliveryAuthorized {
		t.Fatalf("unexpected external Skill projection: %#v", externalSkills)
	}
	graphResponse := fixture.get(t, "/api/v1/runs/"+fixture.run.ID+"/agent-graph")
	var graph AgentGraphView
	decodeData(t, graphResponse, &graph)
	if graph.ProtocolVersion != domain.AgentGraphProtocolVersion || graph.RunID != fixture.run.ID ||
		graph.RootAgentID != fixture.root.ID || len(graph.Nodes) != 1 ||
		graph.Nodes[0].ID != fixture.root.ID || graph.Nodes[0].Role != string(domain.AgentRoleRoot) {
		t.Fatalf("unexpected Agent graph: %#v", graph)
	}
	for _, endpoint := range []string{"delegations", "fanout-plans", "reports"} {
		response := fixture.get(t, "/api/v1/runs/"+fixture.run.ID+"/"+endpoint+"?limit=10")
		var values []json.RawMessage
		decodeData(t, response, &values)
		if len(values) != 0 {
			t.Fatalf("empty projection %s returned %#v", endpoint, values)
		}
		var envelope apiTestEnvelope
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil ||
			envelope.Page == nil || envelope.Page.Limit != 10 {
			t.Fatalf("projection %s omitted pagination: body=%s err=%v", endpoint, response.Body.String(), err)
		}
	}

	sessions := fixture.get(t, "/api/v1/sessions")
	var sessionViews []SessionView
	decodeData(t, sessions, &sessionViews)
	if len(sessionViews) != 1 || sessionViews[0].ID != fixture.run.SessionID {
		t.Fatalf("unexpected Session list: %#v", sessionViews)
	}
	var sessionDetail SessionDetailView
	decodeData(t, fixture.get(t, "/api/v1/sessions/"+fixture.run.SessionID), &sessionDetail)
	if sessionDetail.Run == nil || sessionDetail.Run.ID != fixture.run.ID {
		t.Fatalf("Session was not projected onto its Run: %#v", sessionDetail)
	}
	var messages []MessageView
	decodeData(t, fixture.get(t, "/api/v1/sessions/"+fixture.run.SessionID+"/messages"), &messages)
	if len(messages) != 2 {
		t.Fatalf("default message view included compacted history: %#v", messages)
	}
	decodeData(t, fixture.get(t,
		"/api/v1/sessions/"+fixture.run.SessionID+"/messages?include_compacted=true"), &messages)
	if len(messages) != 3 {
		t.Fatalf("compacted message view is incomplete: %#v", messages)
	}
	for _, message := range messages {
		if message.ProvenanceVersion != session.ContextProvenanceVersion || message.SourceKind == "" ||
			len(message.ContentSHA256) != 64 {
			t.Fatalf("message view omitted context provenance: %#v", message)
		}
		if message.Role == "assistant" {
			if len(message.OutputItems) != 1 ||
				message.OutputItems[0].Type != llm.StreamItemMessage ||
				message.OutputItems[0].Status != llm.StreamItemCompleted ||
				!message.OutputItems[0].Durable || message.OutputItems[0].Provisional {
				t.Fatalf("old assistant history was not projected as a completed item: %#v", message)
			}
		} else if len(message.OutputItems) != 0 {
			t.Fatalf("non-assistant history gained model output items: %#v", message)
		}
	}

	workResponse := fixture.get(t, "/api/v1/runs/"+fixture.run.ID+"/work-items?owner=root")
	var workItems []WorkItemView
	decodeData(t, workResponse, &workItems)
	if len(workItems) != 3 || workItems[0].AcceptanceCriteria == nil || workItems[0].Dependencies == nil {
		t.Fatalf("unexpected WorkItem list: %#v", workItems)
	}
	var workItem WorkItemView
	decodeData(t, fixture.get(t, "/api/v1/work-items/"+fixture.workItems[0].ID), &workItem)
	if workItem.ID != fixture.workItems[0].ID {
		t.Fatalf("unexpected WorkItem detail: %#v", workItem)
	}
	agentWorkResponse := fixture.get(t, "/api/v1/runs/"+fixture.run.ID+
		"/work-items?owner_agent_id="+url.QueryEscape(fixture.root.ID))
	var agentWorkItems []WorkItemView
	decodeData(t, agentWorkResponse, &agentWorkItems)
	if len(agentWorkItems) != 3 || agentWorkItems[0].OwnerAgentID != fixture.root.ID ||
		workItem.OwnerAgentID != fixture.root.ID {
		t.Fatalf("WorkItem Agent ownership is missing from API: list=%#v detail=%#v", agentWorkItems, workItem)
	}

	noteResponse := fixture.get(t, "/api/v1/runs/"+fixture.run.ID+"/notes?tag=api")
	if strings.Contains(noteResponse.Body.String(), fixture.secret) || !strings.Contains(noteResponse.Body.String(), "[REDACTED:") {
		t.Fatalf("Note response was not redacted: %s", noteResponse.Body.String())
	}
	var notes []NoteView
	decodeData(t, noteResponse, &notes)
	if len(notes) != 3 || notes[0].Tags == nil || notes[0].SourceRefs == nil || notes[0].EvidenceIDs == nil {
		t.Fatalf("unexpected Note list: %#v", notes)
	}
	var note NoteView
	decodeData(t, fixture.get(t, "/api/v1/notes/"+fixture.notes[0].ID), &note)
	if note.ID != fixture.notes[0].ID {
		t.Fatalf("unexpected Note detail: %#v", note)
	}
	agentNoteResponse := fixture.get(t, "/api/v1/runs/"+fixture.run.ID+
		"/notes?owner_agent_id="+url.QueryEscape(fixture.root.ID))
	var agentNotes []NoteView
	decodeData(t, agentNoteResponse, &agentNotes)
	if len(agentNotes) != 3 || agentNotes[0].OwnerAgentID != fixture.root.ID ||
		note.OwnerAgentID != fixture.root.ID {
		t.Fatalf("Note Agent ownership is missing from API: list=%#v detail=%#v", agentNotes, note)
	}

	artifactResponse := fixture.get(t, "/api/v1/runs/"+fixture.run.ID+"/artifacts?stream=stdout")
	if strings.Contains(artifactResponse.Body.String(), "dry run: echo api evidence") ||
		strings.Contains(artifactResponse.Body.String(), `"content"`) {
		t.Fatalf("Artifact metadata endpoint exposed content: %s", artifactResponse.Body.String())
	}
	var artifacts []ArtifactView
	decodeData(t, artifactResponse, &artifacts)
	if len(artifacts) != 1 || artifacts[0].ID != fixture.artifactID || artifacts[0].SHA256 == "" {
		t.Fatalf("unexpected Artifact metadata: %#v", artifacts)
	}
	var artifact ArtifactView
	decodeData(t, fixture.get(t, "/api/v1/artifacts/"+fixture.artifactID), &artifact)
	if artifact.ID != fixture.artifactID {
		t.Fatalf("unexpected Artifact detail: %#v", artifact)
	}

	eventResponse := fixture.get(t, "/api/v1/runs/"+fixture.run.ID+"/events?limit=100")
	if strings.Contains(eventResponse.Body.String(), fixture.leaseID) ||
		strings.Contains(eventResponse.Body.String(), `"lease_id"`) {
		t.Fatal("Run timeline exposed the execution fencing token")
	}
	var eventViews []EventView
	decodeData(t, eventResponse, &eventViews)
	if len(eventViews) < 10 {
		t.Fatalf("Run timeline is incomplete: %#v", eventViews)
	}
	for _, event := range eventViews {
		if !json.Valid(event.Payload) {
			t.Fatalf("event %s has invalid payload: %s", event.EventID, event.Payload)
		}
	}
	var rounds []SupervisorToolRoundView
	decodeData(t, fixture.get(t, "/api/v1/runs/"+fixture.run.ID+"/tool-rounds"), &rounds)
	if len(rounds) != 0 || rounds == nil {
		t.Fatalf("unexpected empty tool round view: %#v", rounds)
	}
	missing := fixture.get(t, "/api/v1/runs/missing/events")
	assertAPIError(t, missing, http.StatusNotFound, "NOT_FOUND")
}

func TestExternalSkillProjectionIsAbsentWithoutRunSelection(t *testing.T) {
	fixture := newAPIFixture(t)
	_, unselectedRun, err := application.NewRunService(fixture.store).Create(
		context.Background(), application.CreateRunRequest{
			Goal: "Run without external Skill provenance", Profile: "review",
			Budget: domain.Budget{MaxTurns: 2, MaxTokens: 1024},
		})
	if err != nil {
		t.Fatal(err)
	}
	unselectedDetail := fixture.get(t, "/api/v1/runs/"+unselectedRun.ID)
	if unselectedDetail.Code != http.StatusOK ||
		strings.Contains(unselectedDetail.Body.String(), `"external_skills"`) {
		t.Fatalf("Run without an external selection fabricated projection data: %s",
			unselectedDetail.Body.String())
	}
	assertAPIError(t, fixture.get(t,
		"/api/v1/runs/"+unselectedRun.ID+"/external-skills"),
		http.StatusNotFound, "NOT_FOUND")
}

func TestRunActivitySeparatesPublicUpdatesFromHarnessEvidence(t *testing.T) {
	fixture := newAPIFixture(t)
	response := fixture.get(t, "/api/v1/runs/"+fixture.run.ID+"/activity?limit=100")
	var activity RunActivityView
	decodeData(t, response, &activity)
	if activity.Version != "run_activity.v1" || activity.RunID != fixture.run.ID ||
		activity.ThroughSequence <= 0 || activity.PrivateReasoningIncluded ||
		len(activity.Items) < 3 {
		t.Fatalf("unexpected Run activity: %#v", activity)
	}
	var modelUpdate, harnessEvent bool
	for _, item := range activity.Items {
		if item.Source == "model" && item.Kind == "model_update" &&
			item.Detail == "inspection started" && !item.Verifiable {
			modelUpdate = true
		}
		if item.Source == "harness" && item.Kind == "model_call" &&
			item.Title == "模型调用开始" && item.Verifiable {
			harnessEvent = true
		}
	}
	if !modelUpdate || !harnessEvent {
		t.Fatalf("activity provenance was not separated: %#v", activity.Items)
	}
	body := response.Body.String()
	for _, forbidden := range []string{fixture.secret, fixture.leaseID, `"payload"`,
		`"thinking"`, `"command"`, "echo api evidence"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("Run activity exposed private/raw data %q: %s", forbidden, body)
		}
	}
	for _, requestPath := range []string{
		"/api/v1/runs/" + fixture.run.ID + "/activity?limit=0",
		"/api/v1/runs/" + fixture.run.ID + "/activity?limit=101",
		"/api/v1/runs/" + fixture.run.ID + "/activity?unknown=true",
	} {
		assertAPIError(t, fixture.get(t, requestPath), http.StatusBadRequest,
			"INVALID_ARGUMENT")
	}
	assertAPIError(t, fixture.get(t, "/api/v1/runs/missing/activity"),
		http.StatusNotFound, "NOT_FOUND")
}

func TestThreadTranscriptUsesStableCrossRunCursorAndSafeProjection(t *testing.T) {
	fixture := newAPIFixture(t)
	ctx := context.Background()
	threadRecord, err := fixture.store.GetThreadByRun(ctx, fixture.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.SaveSessionMessage(ctx,
		session.NewMessage(fixture.run.SessionID, "assistant", "durable transcript tail")); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/threads/" + threadRecord.ID + "/transcript"
	firstResponse := fixture.get(t, path+"?limit=2")
	var first []ThreadTranscriptItemView
	firstEnvelope := decodeData(t, firstResponse, &first)
	if len(first) == 0 || firstEnvelope.Page == nil || firstEnvelope.Page.NextCursor == "" {
		t.Fatalf("newest transcript page is incomplete: items=%#v page=%#v", first, firstEnvelope.Page)
	}
	last := first[len(first)-1]
	if last.Version != "thread_transcript.v1" || last.Detail != "durable transcript tail" ||
		last.Source != "model" || last.ActivityType != "message" || !last.Durable || last.Provisional {
		t.Fatalf("durable transcript tail is wrong: %#v", last)
	}
	oldCursor := firstEnvelope.Page.NextCursor
	if _, err := fixture.store.SaveSessionMessage(ctx,
		session.NewMessage(fixture.run.SessionID, "assistant", "new append")); err != nil {
		t.Fatal(err)
	}
	var older []ThreadTranscriptItemView
	olderEnvelope := decodeData(t, fixture.get(t, path+"?limit=2&cursor="+
		url.QueryEscape(oldCursor)), &older)
	if olderEnvelope.Page == nil {
		t.Fatal("continued transcript omitted its page contract")
	}
	for _, item := range older {
		if item.Detail == "new append" || item.ID == last.ID {
			t.Fatalf("append shifted or duplicated the old cursor: %#v", older)
		}
	}
	var refreshed []ThreadTranscriptItemView
	decodeData(t, fixture.get(t, path+"?limit=2"), &refreshed)
	if len(refreshed) == 0 || refreshed[len(refreshed)-1].Detail != "new append" {
		t.Fatalf("fresh tail did not observe append: %#v", refreshed)
	}
	for _, forbidden := range []string{fixture.secret, fixture.leaseID, "echo api evidence", `"payload"`} {
		if strings.Contains(firstResponse.Body.String(), forbidden) {
			t.Fatalf("Thread transcript exposed private/raw data %q: %s", forbidden,
				firstResponse.Body.String())
		}
	}
	assertAPIError(t, fixture.get(t, "/api/v1/threads/missing/transcript"),
		http.StatusNotFound, "NOT_FOUND")
	assertAPIError(t, fixture.get(t, path+"?cursor=not-a-cursor"),
		http.StatusBadRequest, "INVALID_ARGUMENT")
	assertAPIError(t, fixture.get(t, path+"-different?cursor="+url.QueryEscape(oldCursor)),
		http.StatusNotFound, "NOT_FOUND")
}

func TestThreadTranscriptKeepsCommittedInputBeforeToolsAcrossPages(t *testing.T) {
	ctx := t.Context()
	st, err := store.Open(filepath.Join(t.TempDir(), "transcript-input-order.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	runs := application.NewRunService(st)
	_, run, err := runs.Create(ctx, application.CreateRunRequest{Goal: "review the plan", Profile: "review", Phase: "plan",
		ModelRoute: "http-plan/model", Budget: domain.Budget{MaxTurns: 4, MaxTokens: 1000, MaxToolCalls: 4}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	thread, err := st.GetThreadByRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	input, err := st.EnqueueOperatorSteering(ctx, domain.EnqueueOperatorSteeringRequest{
		RunID: run.ID, SessionID: run.SessionID, Content: "prepare one reviewed plan", RequestedBy: "test-operator",
		OperationKey: "transcript-order-queued-input-1"})
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(st, Config{AccessToken: testAccessToken, AppVersion: "test-version"})
	if err != nil {
		t.Fatal(err)
	}
	readAll := func(limit int) []ThreadTranscriptItemView {
		t.Helper()
		var result []ThreadTranscriptItemView
		cursor := ""
		for count := 0; count < 250; count++ {
			path := fmt.Sprintf("/api/v1/threads/%s/transcript?limit=%d", thread.ID, limit)
			if cursor != "" {
				path += "&cursor=" + url.QueryEscape(cursor)
			}
			response := performRequest(t, api, http.MethodGet, path, testAccessToken,
				"127.0.0.1:8765", "127.0.0.1:45000", nil)
			var items []ThreadTranscriptItemView
			envelope := decodeData(t, response, &items)
			result = append(items, result...)
			if envelope.Page == nil || envelope.Page.NextCursor == "" {
				return result
			}
			if envelope.Page.NextCursor == cursor {
				t.Fatal("transcript cursor did not advance through an empty source page")
			}
			cursor = envelope.Page.NextCursor
		}
		t.Fatal("transcript exceeded its bounded fixture history")
		return nil
	}
	var queued ThreadTranscriptItemView
	for _, item := range readAll(1) {
		if item.SourceRef == input.Message.ID {
			queued = item
		}
	}
	if queued.ID == "" || queued.Status != "pending" {
		t.Fatalf("queue input missing before execution: %#v", queued)
	}
	provider := &httpPlanProvider{responses: []*llm.ChatResponse{
		{Provider: "http-plan", Model: "model", ToolCalls: []llm.ToolCall{{ID: "transcript-plan-call",
			Name: "plan_delivery_propose", Arguments: json.RawMessage(httpPlanDeliveryPayload)}}},
		{Text: httpRootWaitResponse(t), Provider: "http-plan", Model: "model"},
	}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	if _, err := application.NewAgentRunner(st, router, policy.NewDefaultChecker()).Step(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	// A later, independent legacy input can have identical text; it must not be
	// merged with the exact steering/Session pair merely because the words match.
	if _, err := st.SaveSessionMessage(ctx, session.NewMessage(run.SessionID, "user", input.Message.Content)); err != nil {
		t.Fatal(err)
	}
	before, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{1, 2, 100} {
		items := readAll(limit)
		users, tools := 0, 0
		seen := make(map[string]bool)
		for _, item := range items {
			if seen[item.ID] {
				t.Fatalf("page size %d duplicated %s", limit, item.ID)
			}
			seen[item.ID] = true
			if item.Kind == "operator_input" && item.Detail == input.Message.Content {
				users++
				if users == 1 && (item.ID != queued.ID || item.Sequence != queued.Sequence ||
					!item.CreatedAt.Equal(queued.CreatedAt) || item.Status != "committed") {
					t.Fatalf("page size %d moved the input to commit time: %#v", limit, item)
				}
			}
			if item.Kind == "tool_call" && item.ToolName == "plan_delivery_propose" {
				tools++
				if users != 1 || item.Sequence <= queued.Sequence {
					t.Fatalf("page size %d placed tools before their input: %#v", limit, item)
				}
			}
		}
		if users != 2 || tools == 0 {
			t.Fatalf("page size %d lost or duplicated input/tool facts: users=%d tools=%d", limit, users, tools)
		}
	}
	var matches []ThreadSearchResultView
	decodeData(t, performRequest(t, api, http.MethodGet, "/api/v1/threads/search?q="+url.QueryEscape(input.Message.Content),
		testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil), &matches)
	if len(matches) != 2 || matches[1].MessageID != queued.ID {
		t.Fatalf("search must retain the original queue anchor and the independent same-text message: %#v", matches)
	}
	after, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatal("read-only transcript projection changed durable event history")
	}
}

func TestReadAPIPaginationCursorIsOpaqueScopedAndBounded(t *testing.T) {
	fixture := newAPIFixture(t)
	first := fixture.get(t, "/api/v1/runs/"+fixture.run.ID+"/notes?limit=2")
	var firstNotes []NoteView
	firstEnvelope := decodeData(t, first, &firstNotes)
	if len(firstNotes) != 2 || firstEnvelope.Page == nil || firstEnvelope.Page.NextCursor == "" {
		t.Fatalf("first page is incomplete: notes=%#v page=%#v", firstNotes, firstEnvelope.Page)
	}
	second := fixture.get(t, "/api/v1/runs/"+fixture.run.ID+"/notes?limit=2&cursor="+
		url.QueryEscape(firstEnvelope.Page.NextCursor))
	var secondNotes []NoteView
	secondEnvelope := decodeData(t, second, &secondNotes)
	if len(secondNotes) != 1 || secondEnvelope.Page == nil || secondEnvelope.Page.NextCursor != "" ||
		secondNotes[0].ID == firstNotes[0].ID || secondNotes[0].ID == firstNotes[1].ID {
		t.Fatalf("second page is inconsistent: notes=%#v page=%#v", secondNotes, secondEnvelope.Page)
	}

	badCursorPaths := []string{
		"/api/v1/runs/" + fixture.run.ID + "/notes?limit=2&tag=api&cursor=" +
			url.QueryEscape(firstEnvelope.Page.NextCursor),
		"/api/v1/runs/" + fixture.run.ID + "/events?limit=2&cursor=" +
			url.QueryEscape(firstEnvelope.Page.NextCursor),
		"/api/v1/runs/" + fixture.run.ID + "/notes?cursor=not-a-cursor",
	}
	for _, requestPath := range badCursorPaths {
		assertAPIError(t, fixture.get(t, requestPath), http.StatusBadRequest, "INVALID_ARGUMENT")
	}

	invalidQueries := []string{
		"?limit=0", "?limit=101", "?limit=", "?limit=2&limit=3", "?unknown=true",
	}
	for _, query := range invalidQueries {
		assertAPIError(t, fixture.get(t, "/api/v1/sessions"+query), http.StatusBadRequest, "INVALID_ARGUMENT")
	}
}

func TestPageMarksTheBoundedCursorWindowAsTruncated(t *testing.T) {
	items, page := trimPage([]int{1, 2}, pageRequest{
		Limit: 1, Offset: maxStoreCursorOffset, Scope: "test-scope",
	})
	if len(items) != 1 || page == nil || page.NextCursor != "" || !page.Truncated {
		t.Fatalf("cursor window did not report its hard boundary: items=%#v page=%#v", items, page)
	}
}

func TestReadAPISecurityBoundaryAndInternalErrorRedaction(t *testing.T) {
	fixture := newAPIFixture(t)
	tests := []struct {
		name      string
		method    string
		token     string
		host      string
		remote    string
		body      io.Reader
		status    int
		errorCode string
	}{
		{name: "missing token", method: http.MethodGet, host: "127.0.0.1:8765", remote: "127.0.0.1:45000",
			status: http.StatusUnauthorized, errorCode: "POLICY_DENIED"},
		{name: "wrong token", method: http.MethodGet, token: strings.Repeat("x", 32), host: "127.0.0.1:8765",
			remote: "127.0.0.1:45000", status: http.StatusUnauthorized, errorCode: "POLICY_DENIED"},
		{name: "external Host", method: http.MethodGet, token: testAccessToken, host: "agent.example:8765",
			remote: "127.0.0.1:45000", status: http.StatusForbidden, errorCode: "POLICY_DENIED"},
		{name: "external client", method: http.MethodGet, token: testAccessToken, host: "127.0.0.1:8765",
			remote: "192.0.2.20:45000", status: http.StatusForbidden, errorCode: "POLICY_DENIED"},
		{name: "write method", method: http.MethodPost, token: testAccessToken, host: "127.0.0.1:8765",
			remote: "127.0.0.1:45000", status: http.StatusMethodNotAllowed, errorCode: "INVALID_ARGUMENT"},
		{name: "GET body", method: http.MethodGet, token: testAccessToken, host: "127.0.0.1:8765",
			remote: "127.0.0.1:45000", body: strings.NewReader("unexpected"),
			status: http.StatusBadRequest, errorCode: "INVALID_ARGUMENT"},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			response := fixture.request(t, current.method, "/api/v1/health", current.token,
				current.host, current.remote, current.body)
			assertAPIError(t, response, current.status, current.errorCode)
			assertSecurityHeaders(t, response)
			if current.status == http.StatusMethodNotAllowed && response.Header().Get("Allow") != http.MethodGet {
				t.Fatalf("missing Allow header: %#v", response.Header())
			}
		})
	}

	nonCanonical := fixture.request(t, http.MethodGet, "/api/v1//health", testAccessToken,
		"127.0.0.1:8765", "127.0.0.1:45000", nil)
	assertAPIError(t, nonCanonical, http.StatusBadRequest, "INVALID_ARGUMENT")
	oversizedRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/v1/health", nil)
	oversizedRequest.Host = "127.0.0.1:8765"
	oversizedRequest.RemoteAddr = "127.0.0.1:45000"
	oversizedRequest.Header.Set("Authorization", "Bearer "+testAccessToken)
	oversizedRequest.RequestURI = "/api/v1/health?x=" + strings.Repeat("a", MaxRequestTargetBytes)
	oversized := httptest.NewRecorder()
	fixture.api.ServeHTTP(oversized, oversizedRequest)
	assertAPIError(t, oversized, http.StatusRequestURITooLong, "RESOURCE_EXHAUSTED")

	closedStore, err := store.Open(filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	closedAPI, err := New(closedStore, Config{AccessToken: testAccessToken})
	if err != nil {
		t.Fatal(err)
	}
	if err := closedStore.Close(); err != nil {
		t.Fatal(err)
	}
	closedResponse := performRequest(t, closedAPI, http.MethodGet, "/api/v1/health", testAccessToken,
		"127.0.0.1:8765", "127.0.0.1:45000", nil)
	assertAPIError(t, closedResponse, http.StatusInternalServerError, "INTERNAL")
	if strings.Contains(strings.ToLower(closedResponse.Body.String()), "closed") ||
		strings.Contains(strings.ToLower(closedResponse.Body.String()), "sql") {
		t.Fatalf("internal Store error leaked through API: %s", closedResponse.Body.String())
	}
}

func TestReadAPIHandlesConcurrentSQLiteReaders(t *testing.T) {
	fixture := newAPIFixture(t)
	const readers = 32
	start := make(chan struct{})
	errorsFound := make(chan error, readers)
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(readers)
	done.Add(readers)
	for index := 0; index < readers; index++ {
		index := index
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			path := "/api/v1/health"
			if index%2 == 0 {
				path = "/api/v1/runs/" + fixture.run.ID + "/events?limit=5"
			}
			response := performRequest(t, fixture.api, http.MethodGet, path, testAccessToken,
				"127.0.0.1:8765", "127.0.0.1:45000", nil)
			if response.Code != http.StatusOK || !json.Valid(response.Body.Bytes()) {
				errorsFound <- fmt.Errorf("reader %d: status=%d body=%s", index, response.Code, response.Body.String())
			}
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
}

func TestHTTPServerLifecycleAndLoopbackValidation(t *testing.T) {
	fixture := newAPIFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	listener, err := ListenLoopback(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(fixture.api, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	request, err := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+"/api/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testAccessToken)
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || !json.Valid(body) {
		t.Fatalf("network API response is invalid: status=%d body=%s err=%v", response.StatusCode, body, readErr)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("graceful shutdown failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP API did not stop after context cancellation")
	}

	for _, address := range []string{"0.0.0.0:0", ":0", "agent.example:0", "127.0.0.1:-1", "127.0.0.1"} {
		if listener, err := ListenLoopback(context.Background(), address); err == nil {
			_ = listener.Close()
			t.Fatalf("unsafe or invalid listener address was accepted: %q", address)
		}
	}
	cancelled, cancelImmediately := context.WithCancel(context.Background())
	cancelImmediately()
	if listener, err := ListenLoopback(cancelled, "127.0.0.1:0"); listener != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled listener did not preserve context error: listener=%v err=%v", listener, err)
	}

	generated, err := GenerateAccessToken()
	if err != nil || len(generated) < MinAccessTokenBytes {
		t.Fatalf("generated token is invalid: len=%d err=%v", len(generated), err)
	}
	for _, token := range []string{"short", strings.Repeat("x", MaxAccessTokenBytes+1),
		" " + strings.Repeat("x", MinAccessTokenBytes), strings.Repeat("x", MinAccessTokenBytes-1) + "\n"} {
		if _, err := New(fixture.store, Config{AccessToken: token}); err == nil {
			t.Fatalf("invalid API token was accepted: %q", token)
		}
	}
}

func (f *apiFixture) get(t *testing.T, requestPath string) *httptest.ResponseRecorder {
	t.Helper()
	return f.request(t, http.MethodGet, requestPath, testAccessToken,
		"127.0.0.1:8765", "127.0.0.1:45000", nil)
}

func (f *apiFixture) request(t *testing.T, method string, requestPath string, token string,
	host string, remote string, body io.Reader,
) *httptest.ResponseRecorder {
	t.Helper()
	return performRequest(t, f.api, method, requestPath, token, host, remote, body)
}

func performRequest(t *testing.T, api *API, method string, requestPath string, token string,
	host string, remote string, body io.Reader,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "http://127.0.0.1"+requestPath, body)
	request.Host = host
	request.RemoteAddr = remote
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	return response
}

func decodeData[T any](t *testing.T, response *httptest.ResponseRecorder, target *T) apiTestEnvelope {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("API status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope apiTestEnvelope
	decoder := json.NewDecoder(bytes.NewReader(response.Body.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("decode API envelope: %v body=%s", err, response.Body.String())
	}
	if envelope.Version != Version || envelope.RequestID == "" || envelope.Error != nil {
		t.Fatalf("invalid success envelope: %#v", envelope)
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		t.Fatalf("decode API data: %v data=%s", err, envelope.Data)
	}
	return envelope
}

func assertAPIError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("API status=%d, want %d body=%s", response.Code, status, response.Body.String())
	}
	var envelope apiTestEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode API error: %v body=%s", err, response.Body.String())
	}
	if envelope.Version != Version || envelope.RequestID == "" || envelope.Error == nil ||
		envelope.Error.Code != code || envelope.Error.Message == "" {
		t.Fatalf("invalid error envelope: %#v", envelope)
	}
}

func assertSecurityHeaders(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	header := response.Header()
	if header.Get("Cache-Control") != "no-store" || header.Get("X-Content-Type-Options") != "nosniff" ||
		header.Get("X-Frame-Options") != "DENY" || header.Get("X-CyberAgent-API-Version") != Version ||
		header.Get("X-Request-ID") == "" || header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("HTTP security headers are incomplete: %#v", header)
	}
}
