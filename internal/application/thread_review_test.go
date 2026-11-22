package application_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/store"
)

func TestThreadReviewAcrossRunsRetainsEditsAndObservesDrift(t *testing.T) {
	st, first, registry := newThreadModelSettingsFixture(t)
	workspace, err := st.GetWorkspaceInfo(t.Context(), "workspace-model-settings")
	if err != nil {
		t.Fatal(err)
	}
	manager := fileedit.NewManager(st)
	initial, err := manager.Propose(t.Context(), fileedit.Proposal{SessionID: first.SessionID, WorkspaceID: workspace.ID,
		WorkspaceRoot: workspace.RootPath, Path: "app.txt", Operation: "create", ExpectedOriginalHash: "missing", ProposedText: "first revision\n"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = application.NewFileEditReviewService(st).Review(t.Context(), application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion, RunID: first.ID, EditID: initial.ID, Action: application.FileEditApproveIntent}); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Approve(t.Context(), initial.ID, workspace.RootPath); err != nil {
		t.Fatal(err)
	}
	second := executeThreadModelSettingsSwitch(t, st, first, registry).Submission.Run
	latest, err := manager.Propose(t.Context(), fileedit.Proposal{SessionID: second.SessionID, WorkspaceID: workspace.ID,
		WorkspaceRoot: workspace.RootPath, Path: "app.txt", ProposedText: "second revision\n"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = application.NewFileEditReviewService(st).Review(t.Context(), application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion, RunID: second.ID, EditID: latest.ID, Action: application.FileEditApproveIntent}); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Approve(t.Context(), latest.ID, workspace.RootPath); err != nil {
		t.Fatal(err)
	}
	pending, err := manager.Propose(t.Context(), fileedit.Proposal{SessionID: second.SessionID, WorkspaceID: workspace.ID,
		WorkspaceRoot: workspace.RootPath, Path: "pending.txt", Operation: "create", ExpectedOriginalHash: "missing", ProposedText: "not approved\n"})
	if err != nil {
		t.Fatal(err)
	}
	threadID := domain.InitialThreadID(first.ID)
	before, err := st.ExportThread(t.Context(), threadID)
	if err != nil {
		t.Fatal(err)
	}
	service := application.NewThreadReviewService(st).WithCodeHandoff(application.NewCodeHandoffService(st))
	review, err := service.Review(t.Context(), threadID)
	if err != nil {
		t.Fatal(err)
	}
	if review.TotalRuns != 2 || len(review.Runs) != 2 || review.CurrentRunID != second.ID || review.ChangeScope != "recorded_file_edits" ||
		review.Target.WorkspaceID != workspace.ID || review.Target.RootPath != workspace.RootPath || review.Target.Kind != "source" ||
		review.Revision.RepositoryKind != "none" || review.Revision.Head != "" || review.Revision.Dirty != nil || review.Revision.RevisionSHA256 == "" {
		t.Fatalf("review lost task or non-Git scope: %+v", review)
	}
	if len(review.AppliedChanges) != 2 || len(review.UnappliedChanges) != 1 || review.UnappliedChanges[0].EditID != pending.ID || review.UnappliedChanges[0].Status != "proposed" {
		t.Fatalf("applied and proposed records were conflated: %+v", review)
	}
	byID := map[string]application.ThreadReviewChange{}
	for _, item := range review.AppliedChanges {
		byID[item.EditID] = item
	}
	if byID[initial.ID].RunID != first.ID || byID[initial.ID].CurrentMatch != "changed" || byID[latest.ID].RunID != second.ID || byID[latest.ID].CurrentMatch != "matches" {
		t.Fatalf("cross-Run version provenance changed: %+v", byID)
	}
	if _, err := os.Stat(filepath.Join(workspace.RootPath, "pending.txt")); !os.IsNotExist(err) {
		t.Fatal("review wrote the unapproved file")
	}
	after, err := st.ExportThread(t.Context(), threadID)
	if err != nil {
		t.Fatal(err)
	}
	before.ExportedAt, after.ExportedAt = time.Time{}, time.Time{}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("read-only review changed persisted task history")
	}
	if err := os.WriteFile(filepath.Join(workspace.RootPath, "app.txt"), []byte("external edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	drifted, err := service.Review(t.Context(), threadID)
	if err != nil {
		t.Fatal(err)
	}
	if drifted.Revision.RevisionSHA256 == review.Revision.RevisionSHA256 || drifted.AppliedChanges[0].CurrentMatch != "changed" {
		t.Fatalf("external edit left an obsolete version marked current: %+v", drifted)
	}
}

func threadReviewFixture(t *testing.T, root string) (*store.SQLiteStore, domain.Run) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "review.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err = st.SaveWorkspace(t.Context(), store.WorkspaceRecord{ID: "review-workspace", Name: "Review fixture", RootPath: root, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{Goal: "Review this task", Profile: "code", Phase: "deliver", WorkspaceID: "review-workspace", ModelRoute: "fixture/model", Interactive: true})
	if err != nil {
		t.Fatal(err)
	}
	return st, run
}

type threadReviewPagedStore struct {
	*store.SQLiteStore
	previews []fileedit.Preview
	offsets  []int
}

func (s *threadReviewPagedStore) ListFileEditPreviewsPage(_ context.Context, filter fileedit.ListFilter, offset, limit int) ([]fileedit.Preview, error) {
	s.offsets = append(s.offsets, offset)
	if filter.SessionID == "" || filter.WorkspaceID != "" || limit != 100 {
		return nil, fmt.Errorf("unexpected unscoped/broken page request")
	}
	end := min(len(s.previews), offset+limit)
	if offset >= end {
		return []fileedit.Preview{}, nil
	}
	return s.previews[offset:end], nil
}

func TestThreadReviewPaginatesAndReportsBoundedOmissions(t *testing.T) {
	st, run := threadReviewFixture(t, t.TempDir())
	wrapped := &threadReviewPagedStore{SQLiteStore: st}
	for index := 0; index < 601; index++ {
		wrapped.previews = append(wrapped.previews, fileedit.Preview{ID: fmt.Sprintf("edit-%d", index), SessionID: run.SessionID, WorkspaceID: "review-workspace",
			Path: fmt.Sprintf("file-%d.txt", index), Operation: "create", Status: "proposed", OriginalHash: "missing", ProposedHash: fileedit.HashText("proposal"),
			Diff: strings.Repeat("字", 500), UpdatedAt: time.Now().UTC()})
	}
	review, err := application.NewThreadReviewService(wrapped).Review(t.Context(), domain.InitialThreadID(run.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(review.UnappliedChanges) != application.MaxThreadReviewChanges || !review.Partial || !strings.Contains(strings.Join(review.Reasons, ","), "changes_omitted") || !strings.Contains(strings.Join(review.Reasons, ","), "diffs_truncated") || len(wrapped.offsets) != 6 || wrapped.offsets[5] != 500 {
		t.Fatalf("review silently omitted history or failed to page: offsets=%v review=%+v", wrapped.offsets, review.Reasons)
	}
	bytes := 0
	for _, item := range review.UnappliedChanges {
		bytes += len(item.Diff)
	}
	if bytes > application.MaxThreadReviewDiffBytes {
		t.Fatal("diff response exceeded its bound")
	}
	wrapped.previews = []fileedit.Preview{{ID: "foreign", SessionID: run.SessionID, WorkspaceID: "another-workspace", Path: "leak.txt", Status: "applied"}}
	if _, err := application.NewThreadReviewService(wrapped).Review(t.Context(), domain.InitialThreadID(run.ID)); err == nil {
		t.Fatal("foreign Workspace record was accepted")
	}
}

func TestThreadReviewLinkedWorktreeAndUnbornGitAreReadOnly(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is unavailable")
	}
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-c", "user.name=Review fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.autocrlf=false"}, args...)...)
		hideSubprocessWindow(command)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git %v: %v %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	root := t.TempDir()
	git("-C", root, "init", "--quiet")
	st, run := threadReviewFixture(t, root)
	service := application.NewThreadReviewService(st)
	unborn, err := service.Review(t.Context(), domain.InitialThreadID(run.ID))
	if err != nil || unborn.Revision.Head != "unborn" || unborn.Revision.Dirty == nil || *unborn.Revision.Dirty {
		t.Fatalf("unborn Git review=%+v %v", unborn.Revision, err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("-C", root, "add", "tracked.txt")
	git("-C", root, "commit", "--quiet", "-m", "initial")
	linked := filepath.Join(t.TempDir(), "linked")
	git("-C", root, "worktree", "add", "--quiet", "-b", "review-linked", linked)
	linkedStore, linkedRun := threadReviewFixture(t, linked)
	linkedService := application.NewThreadReviewService(linkedStore)
	index := git("-C", linked, "rev-parse", "--path-format=absolute", "--git-path", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	clean, err := linkedService.Review(t.Context(), domain.InitialThreadID(linkedRun.ID))
	if err != nil || clean.Revision.RepositoryKind != "git" || clean.Revision.Branch != "review-linked" || len(clean.Revision.Head) != 40 || clean.Revision.Dirty == nil || *clean.Revision.Dirty {
		t.Fatalf("linked worktree review=%+v %v", clean.Revision, err)
	}
	after, err := os.ReadFile(index)
	if err != nil || string(before) != string(after) {
		t.Fatal("read-only review changed the Git index")
	}
	if err := os.WriteFile(filepath.Join(linked, "tracked.txt"), []byte("modified\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dirty, err := linkedService.Review(t.Context(), domain.InitialThreadID(linkedRun.ID))
	if err != nil || dirty.Revision.Dirty == nil || !*dirty.Revision.Dirty || dirty.Revision.Head != clean.Revision.Head || dirty.Revision.RevisionSHA256 == clean.Revision.RevisionSHA256 {
		t.Fatalf("dirty tree was compared using HEAD only: %+v %v", dirty.Revision, err)
	}
}

type threadReviewMutatingHandoff struct {
	st    *store.SQLiteStore
	root  string
	calls int
}

func (h *threadReviewMutatingHandoff) Build(ctx context.Context, runID string) (application.CodeHandoff, error) {
	h.calls++
	if err := os.WriteFile(filepath.Join(h.root, "moving.txt"), []byte(fmt.Sprintf("generation %d", h.calls)), 0600); err != nil {
		return application.CodeHandoff{}, err
	}
	run, err := h.st.GetRun(ctx, runID)
	if err != nil {
		return application.CodeHandoff{}, err
	}
	seq, err := h.st.LatestRunEventSequence(ctx, runID)
	return application.CodeHandoff{RunID: runID, MissionID: run.MissionID, SessionID: run.SessionID, WorkspaceID: "review-workspace", SourceEventSequence: seq}, err
}

func TestThreadReviewRejectsAContinuouslyChangingWorkspace(t *testing.T) {
	root := t.TempDir()
	st, run := threadReviewFixture(t, root)
	handoff := &threadReviewMutatingHandoff{st: st, root: root}
	_, err := application.NewThreadReviewService(st).WithCodeHandoff(handoff).Review(t.Context(), domain.InitialThreadID(run.ID))
	if err == nil || handoff.calls != 3 {
		t.Fatalf("torn revision returned success or retried without a bound: calls=%d err=%v", handoff.calls, err)
	}
}
