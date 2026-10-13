package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
)

func TestChildTaskProposalReviewAndAdmitOverHTTP(t *testing.T) {
	fixture := newAPIFixture(t)
	fixture.api.childTaskControlController = application.NewChildTaskControlService(fixture.store)
	fixture.api.modelControlEnabled = true
	ctx := context.Background()
	_, childRun, err := application.NewRunService(fixture.store).Create(ctx,
		application.CreateRunRequest{Goal: "child task review test", Profile: "code",
			Budget: domain.Budget{MaxTurns: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunService(fixture.store).Start(ctx, childRun.ID); err != nil {
		t.Fatal(err)
	}
	root, _, err := fixture.store.RegisterRootAgent(ctx, childRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := domain.NormalizeChildTaskProposalSpec(domain.ChildTaskProposalSpec{
		Version: domain.ChildTaskProposalVersion,
		Tasks: []domain.ChildTask{
			{Title: "record", Goal: "record one finding", Skills: []string{"model.chat", "note_create"},
				TurnLimit: 2, TokenLimit: 256, TimeoutMillis: 60000},
			{Title: "record2", Goal: "record another finding", Skills: []string{"model.chat", "note_create"},
				DependencyOrdinals: []int{1}, TurnLimit: 2, TokenLimit: 256, TimeoutMillis: 60000},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	surface, tier, err := domain.ResolveChildTaskSurface(spec)
	if err != nil || surface != domain.ChildTaskSurfaceCore || tier != "" {
		t.Fatalf("surface: %q %q %v", surface, tier, err)
	}
	now := time.Now().UTC()
	proposal := domain.ChildTaskProposal{ID: idgen.New("childtask"), RunID: childRun.ID,
		RootAgentID: root.ID, SessionID: childRun.SessionID,
		WorkspaceID: fixture.workspace.ID, Status: domain.ChildTaskProposalProposed, Spec: spec,
		Surface: surface, RequestedBy: "run_supervisor", Version: 1, CreatedAt: now}
	operation := domain.ChildTaskOperation{
		KeyDigest:          runmutation.OperationKeyDigest("child_task_propose", childRun.ID, "http-child-task-0001-abcdefgh"),
		RequestFingerprint: runmutation.Fingerprint("child_task_request.v1", childRun.ID, spec.SpecJSONFingerprint()),
		ProposalID:         proposal.ID, RunID: childRun.ID, SessionID: childRun.SessionID,
		WorkspaceID: fixture.workspace.ID, RootAgentID: root.ID,
		LeaseID: idgen.New("lease"), LeaseGeneration: 1, RequestedBy: "run_supervisor", CreatedAt: now}
	if _, _, err := fixture.store.CreateChildTaskProposal(ctx, operation, proposal,
		eventsEvent(t, childRun), eventsEvent(t, childRun), eventsEvent(t, childRun)); err != nil {
		t.Fatal(err)
	}
	listResponse := fixture.get(t, "/api/v1/runs/"+childRun.ID+"/child-task-proposals")
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), proposal.ID) {
		t.Fatalf("list failed: %d %s", listResponse.Code, listResponse.Body.String())
	}
	reviewBody := `{"version":"child_task_review.v1","action":"approve","reviewer":"http_test","fanout_tier":"2","confirm_review":true}`
	for _, wrongRun := range []string{fixture.run.ID, "missing-run"} {
		for _, action := range []string{"approve", "deny"} {
			response := performControlPathRequest(t, fixture.api,
				"/api/v1/runs/"+wrongRun+"/child-task-proposals/"+proposal.ID+"/review",
				"child-task-wrong-review-0001", strings.NewReader(strings.Replace(reviewBody, "approve", action, 1)))
			if response.Code != http.StatusNotFound {
				t.Fatalf("foreign review: run=%s action=%s status=%d body=%s", wrongRun, action, response.Code, response.Body.String())
			}
		}
	}
	saved, found, err := fixture.store.GetChildTaskProposal(ctx, proposal.ID)
	if err != nil || !found || saved.Status != domain.ChildTaskProposalProposed {
		t.Fatalf("foreign review changed proposal: found=%v proposal=%+v err=%v", found, saved, err)
	}
	reviewResponse := performControlPathRequest(t, fixture.api,
		"/api/v1/runs/"+childRun.ID+"/child-task-proposals/"+proposal.ID+"/review",
		"child-task-review-0001-abcdefgh", strings.NewReader(reviewBody))
	if reviewResponse.Code != http.StatusOK || !strings.Contains(reviewResponse.Body.String(), "approved") {
		t.Fatalf("review failed: %d %s", reviewResponse.Code, reviewResponse.Body.String())
	}
	eventsBefore, err := fixture.store.ListRunEvents(ctx, childRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	admitBody := `{"version":"child_task_admit.v1","confirm_admit":true}`
	for _, wrongRun := range []string{fixture.run.ID, "missing-run"} {
		response := performControlPathRequest(t, fixture.api,
			"/api/v1/runs/"+wrongRun+"/child-task-proposals/"+proposal.ID+"/admit",
			"child-task-wrong-admit-0001", strings.NewReader(admitBody))
		if response.Code != http.StatusNotFound {
			t.Fatalf("foreign admission: run=%s status=%d body=%s", wrongRun, response.Code, response.Body.String())
		}
	}
	assignments, err := fixture.store.ListChildTaskAssignments(ctx, proposal.ID)
	if err != nil || len(assignments) != 2 {
		t.Fatalf("foreign admission changed assignments: %+v err=%v", assignments, err)
	}
	for _, assignment := range assignments {
		if assignment.Status != domain.ChildTaskAssignmentProposed || assignment.AdmittedAgentID != "" || assignment.FanoutPlanID != "" {
			t.Fatalf("foreign admission assigned a child: %+v", assignment)
		}
	}
	edges, err := fixture.store.ListDependencyEdges(ctx, childRun.ID, 8)
	if err != nil || len(edges) != 0 {
		t.Fatalf("foreign admission created dependencies: %+v err=%v", edges, err)
	}
	nodes, err := fixture.store.ListAgentNodes(ctx, childRun.ID)
	if err != nil || len(nodes) != 1 || nodes[0].ID != root.ID {
		t.Fatalf("foreign admission created a child: %+v err=%v", nodes, err)
	}
	eventsAfter, err := fixture.store.ListRunEvents(ctx, childRun.ID)
	if err != nil || len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("foreign admission wrote events: before=%d after=%d err=%v", len(eventsBefore), len(eventsAfter), err)
	}
	admitResponse := performControlPathRequest(t, fixture.api,
		"/api/v1/runs/"+childRun.ID+"/child-task-proposals/"+proposal.ID+"/admit",
		"child-task-admit-0001-abcdefgh", strings.NewReader(admitBody))
	if admitResponse.Code != http.StatusOK {
		t.Fatalf("admit failed: %d %s", admitResponse.Code, admitResponse.Body.String())
	}
	edges, err = fixture.store.ListDependencyEdges(ctx, childRun.ID, 8)
	if err != nil || len(edges) != 1 {
		t.Fatalf("admission did not bind the dependency: %#v err=%v", edges, err)
	}
	// A completed operation cannot be replayed through a different Run either.
	for _, action := range []struct{ path, key, body string }{
		{"review", "child-task-review-0001-abcdefgh", reviewBody},
		{"admit", "child-task-admit-0001-abcdefgh", admitBody},
	} {
		response := performControlPathRequest(t, fixture.api,
			"/api/v1/runs/"+fixture.run.ID+"/child-task-proposals/"+proposal.ID+"/"+action.path,
			action.key, strings.NewReader(action.body))
		if response.Code != http.StatusNotFound {
			t.Fatalf("foreign %s replay: status=%d body=%s", action.path, response.Code, response.Body.String())
		}
	}
}

func eventsEvent(t *testing.T, childRun domain.Run) events.Event {
	t.Helper()
	event, err := events.New(childRun.ID, childRun.MissionID, events.ToolCompletedEvent,
		"test", idgen.New("evt"), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	return event
}
