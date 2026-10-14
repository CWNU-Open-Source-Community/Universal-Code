package application

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/operatoraction"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runworktree"
)

func TestCodeHandoffDrydockFileEdits(t *testing.T) {
	fixture, owned := newFileEditDrydockFixture(t)
	ctx := t.Context()
	proposals := NewFileEditProposalService(fixture.state, policy.NewDefaultChecker()).WithRunWorktree(fixture.service)
	source, err := proposals.IssueSource(ctx, fixture.run.ID, "tracked.txt")
	if err != nil {
		t.Fatal(err)
	}
	created, err := proposals.Propose(ctx, CreateFileEditProposalRequest{
		Version: FileEditProposalProtocolVersion, RunID: fixture.run.ID,
		SourceHandle: source.Handle, ProposedText: "handoff owned edit\n"})
	if err != nil || created.Edit.WorkspaceID != owned.WorkspaceID {
		t.Fatalf("Drydock proposal=%+v err=%v", created, err)
	}
	assertActions := func(want map[operatoraction.Kind]int) {
		t.Helper()
		center, err := NewOperatorActionCenterService(fixture.state).List(ctx, fixture.run.ID)
		if err != nil {
			t.Fatal(err)
		}
		got := make(map[operatoraction.Kind]int)
		for _, item := range center.Items {
			got[item.Kind]++
		}
		if len(got) != len(want) {
			t.Fatalf("actions=%v want=%v", got, want)
		}
		for kind, count := range want {
			if got[kind] != count {
				t.Fatalf("actions=%v want=%v", got, want)
			}
		}
	}
	assertActions(map[operatoraction.Kind]int{operatoraction.KindApprovalPending: 1, operatoraction.KindFileEditReview: 1})
	pending, err := NewCodeHandoffService(fixture.state).Build(ctx, fixture.run.ID)
	if err != nil || pending.ChangeSet.Proposed != 1 || pending.ChangeSet.ReturnedCount != 1 {
		t.Fatalf("pending handoff=%+v err=%v", pending.ChangeSet, err)
	}
	if _, err := NewFileEditReviewService(fixture.state).WithRunWorktree(fixture.service).Review(ctx,
		ReviewFileEditRequest{Version: FileEditReviewProtocolVersion, RunID: fixture.run.ID,
			EditID: created.Edit.ID, Action: FileEditApproveIntent}); err != nil {
		t.Fatal(err)
	}
	assertActions(map[operatoraction.Kind]int{operatoraction.KindFileEditApply: 1})
	previews, err := fixture.state.ListFileEditPreviewsPage(ctx,
		fileedit.ListFilter{SessionID: fixture.run.SessionID, WorkspaceID: owned.WorkspaceID}, 0, 10)
	if err != nil || len(previews) != 1 {
		t.Fatalf("owned previews=%+v err=%v", previews, err)
	}
	handoff, err := NewCodeHandoffService(fixture.state).Build(ctx, fixture.run.ID)
	if err != nil || handoff.WorkspaceID != fixture.workspace.ID ||
		handoff.ChangeSet.Approved != 1 || handoff.ChangeSet.ReturnedCount != 1 ||
		handoff.ChangeSet.TotalDiffBytes != len(previews[0].Diff) || handoff.ChangeSet.TotalDiffBytes == 0 {
		t.Fatalf("approved handoff=%+v err=%v", handoff, err)
	}
	for _, format := range []string{CodeHandoffExportFormatJSON, CodeHandoffExportFormatMarkdown} {
		exported, err := NewCodeHandoffExportService(fixture.state).Build(ctx, fixture.run.ID, format)
		if err != nil {
			t.Fatal(err)
		}
		if format == CodeHandoffExportFormatJSON {
			var decoded CodeHandoff
			if err := json.Unmarshal([]byte(exported.Content), &decoded); err != nil || decoded.ChangeSet != handoff.ChangeSet {
				t.Fatalf("exported change set=%+v err=%v", decoded.ChangeSet, err)
			}
		} else if !strings.Contains(exported.Content, "Change set: 0 proposed, 1 approved, 0 applied") {
			t.Fatalf("Markdown omitted approved Drydock edit: %s", exported.Content)
		}
	}
}

type drydockProjectionTestStore struct {
	operatorActionCenterTestStore
	owned   runworktree.Workspace
	binding domain.ThreadDrydockBinding
}

func (s *drydockProjectionTestStore) GetRunFileDrydock(context.Context, string) (runworktree.Workspace, bool, error) {
	return s.owned, true, nil
}

func (s *drydockProjectionTestStore) GetThreadDrydockBinding(context.Context, string) (domain.ThreadDrydockBinding, bool, error) {
	return s.binding, s.binding.RunID != "", nil
}

func TestDrydockActionProjectionPreservesBindings(t *testing.T) {
	state := &drydockProjectionTestStore{operatorActionCenterTestStore: operatorActionCenterTestStore{
		run:     domain.Run{ID: "run-actions", MissionID: "mission-actions", SessionID: "session-actions"},
		mission: domain.Mission{ID: "mission-actions", WorkspaceID: "source-actions"},
	}, owned: runworktree.Workspace{ID: "drydock-actions", RunID: "run-actions", MissionID: "mission-actions",
		SessionID: "session-actions", SourceWorkspaceID: "source-actions", WorkspaceID: "owned-actions"}}
	record := operatoraction.Record{SourceID: "approval-actions", Kind: operatoraction.KindApprovalPending,
		State: "pending", RunID: state.run.ID, SessionID: state.run.SessionID,
		WorkspaceID: state.owned.WorkspaceID, AvailableAt: time.Now().UTC()}
	for _, test := range []struct {
		name         string
		change       func(*operatoraction.Record)
		wantConflict bool
	}{
		{"owned approval", func(*operatoraction.Record) {}, false},
		{"source approval", func(r *operatoraction.Record) { r.WorkspaceID = state.mission.WorkspaceID }, false},
		{"another Run", func(r *operatoraction.Record) { r.RunID = "other-run" }, true},
		{"another Session", func(r *operatoraction.Record) { r.SessionID = "other-session" }, true},
		{"another workspace", func(r *operatoraction.Record) { r.WorkspaceID = "other-workspace" }, true},
		{"old source edit", func(r *operatoraction.Record) {
			r.Kind, r.State, r.WorkspaceID = operatoraction.KindFileEditApply, "approved", state.mission.WorkspaceID
		}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := record
			test.change(&value)
			state.records = []operatoraction.Record{value}
			_, err := NewOperatorActionCenterService(state).List(t.Context(), state.run.ID)
			if (test.wantConflict && apperror.CodeOf(err) != apperror.CodeConflict) || (!test.wantConflict && err != nil) {
				t.Fatalf("projection error=%v wantConflict=%t", err, test.wantConflict)
			}
		})
	}
	state.records = []operatoraction.Record{record}
	state.owned.RunID, state.owned.SessionID = "predecessor-run", "predecessor-session"
	if _, err := NewOperatorActionCenterService(state).List(t.Context(), state.run.ID); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("foreign Drydock accepted without continuation: %v", err)
	}
	state.binding = domain.ThreadDrydockBinding{RunID: state.run.ID, ThreadID: "thread-actions", DrydockID: state.owned.ID}
	if _, err := NewOperatorActionCenterService(state).List(t.Context(), state.run.ID); err != nil {
		t.Fatalf("explicit continuation rejected: %v", err)
	}
	state.owned.SourceWorkspaceID = "other-source"
	if _, err := NewOperatorActionCenterService(state).List(t.Context(), state.run.ID); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("foreign source binding accepted: %v", err)
	}
}
