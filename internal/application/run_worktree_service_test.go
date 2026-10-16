package application

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/gitadvanced"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runworktree"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

func TestDrydockLifecycleCoversDirtySourceCheckpointDeliveryAndReceipts(t *testing.T) {
	fixture := newDrydockApplicationFixture(t, "中文 source repo")
	writeDrydockTestFile(t, filepath.Join(fixture.sourceRoot, "tracked.txt"), "source dirty\n")
	writeDrydockTestFile(t, filepath.Join(fixture.sourceRoot, "source untracked.txt"), "excluded\n")
	writeDrydockTestFile(t, filepath.Join(fixture.sourceRoot, ".gitignore"), "ignored-build/\n")
	writeDrydockTestFile(t, filepath.Join(fixture.sourceRoot, "ignored-build", "output.bin"),
		"ignored\n")

	preview, err := fixture.service.Create(t.Context(), DrydockCreateRequest{
		RunID: fixture.run.ID, OperationKey: "create-preview-0001", RequestedBy: "operator"})
	if err != nil || !preview.TrustRequired || preview.Workspace != nil ||
		!preview.SourceState.DirtyTracked || !preview.SourceState.DirtyUntracked ||
		!preview.SourceState.DirtyIgnored {
		t.Fatalf("trust preview=%+v err=%v", preview, err)
	}
	if _, err := fixture.service.Create(t.Context(), DrydockCreateRequest{
		RunID: fixture.run.ID, OperationKey: "create-wrong-trust-0001",
		RequestedBy: "operator", ConfirmWorkspaceTrust: true,
		ExpectedTrustDigest: strings.Repeat("0", 64)}); err == nil ||
		!strings.Contains(err.Error(), "exact reviewed source state") {
		t.Fatalf("mismatched Workspace Trust confirmation error=%v", err)
	}
	createRequest := DrydockCreateRequest{
		RunID: fixture.run.ID, OperationKey: "create-confirm-0001", RequestedBy: "operator",
		ConfirmWorkspaceTrust: true, ExpectedTrustDigest: preview.TrustDigest}
	created, err := fixture.service.Create(t.Context(), createRequest)
	if err != nil || created.Workspace == nil || created.Trust == nil ||
		created.Receipt == nil || created.Checkpoint == nil ||
		created.Workspace.State != runworktree.StateReady ||
		created.Trust.GrantsProcessAuthority || created.Receipt.GrantsProcessAuthority {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	workspace := *created.Workspace
	registered, err := fixture.state.ListWorkspaces(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range registered {
		if candidate.ID == workspace.WorkspaceID {
			t.Fatal("Drydock synthetic Workspace leaked into ordinary Workspace selection")
		}
	}
	if _, err := fixture.state.GetWorkspaceByID(t.Context(), workspace.WorkspaceID); err == nil {
		t.Fatal("Drydock synthetic Workspace was addressable through ordinary Workspace lookup")
	}
	if got := readDrydockTestFile(t, filepath.Join(workspace.Path, "tracked.txt")); got != "base\n" {
		t.Fatalf("dirty source content leaked into Drydock: %q", got)
	}
	if _, err := os.Stat(filepath.Join(workspace.Path, "source untracked.txt")); !os.IsNotExist(err) {
		t.Fatalf("source untracked file leaked into Drydock: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace.Path, "ignored-build", "output.bin")); !os.IsNotExist(err) {
		t.Fatalf("source ignored file leaked into Drydock: %v", err)
	}
	createReplay, err := fixture.service.Create(t.Context(), createRequest)
	if err != nil || !createReplay.Replayed || createReplay.Workspace == nil ||
		createReplay.Receipt == nil || createReplay.Checkpoint == nil ||
		createReplay.Receipt.ID != created.Receipt.ID ||
		createReplay.Checkpoint.ID != created.Checkpoint.ID {
		t.Fatalf("create replay=%+v err=%v", createReplay, err)
	}
	wrongCreateReplay := createRequest
	wrongCreateReplay.RequestedBy = "different_operator"
	if _, err := fixture.service.Create(t.Context(), wrongCreateReplay); err == nil ||
		!strings.Contains(err.Error(), "different intent") {
		t.Fatalf("create intent reuse error=%v", err)
	}
	duplicate, err := fixture.service.Create(t.Context(), DrydockCreateRequest{
		RunID: fixture.run.ID, OperationKey: "create-duplicate-0002", RequestedBy: "operator"})
	if err != nil || !duplicate.Replayed || duplicate.Workspace == nil ||
		duplicate.Workspace.ID != workspace.ID {
		t.Fatalf("duplicate=%+v err=%v", duplicate, err)
	}

	useRequest := DrydockUseRequest{RunID: fixture.run.ID,
		ExpectedGeneration: workspace.Generation, OperationKey: "use-0001",
		RequestedBy: "runtime_adapter"}
	used, err := fixture.service.Use(t.Context(), useRequest)
	if err != nil || used.GrantsProcessAuthority || used.Workspace.Generation != workspace.Generation+1 {
		t.Fatalf("use=%+v err=%v", used, err)
	}
	useReplay, err := fixture.service.Use(t.Context(), useRequest)
	if err != nil || !useReplay.Replayed || useReplay.Receipt.ID != used.Receipt.ID {
		t.Fatalf("use replay=%+v err=%v", useReplay, err)
	}
	wrongUse := useRequest
	wrongUse.RequestedBy = "different_adapter"
	if _, err := fixture.service.Use(t.Context(), wrongUse); err == nil ||
		!strings.Contains(err.Error(), "different intent") {
		t.Fatalf("use intent reuse error=%v", err)
	}
	workspace = used.Workspace
	writeDrydockTestFile(t, filepath.Join(workspace.Path, "tracked.txt"), "staged\n")
	runDrydockTestGit(t, workspace.Path, "add", "--", "tracked.txt")
	writeDrydockTestFile(t, filepath.Join(workspace.Path, "未跟踪 file.txt"), "untracked\n")

	checkpointed, err := fixture.service.Checkpoint(t.Context(), DrydockCheckpointRequest{
		RunID: fixture.run.ID, ExpectedGeneration: workspace.Generation,
		OperationKey: "checkpoint-0001", RequestedBy: "runtime_adapter",
		Title: "tracked untracked index", ConfirmObservedChanges: true})
	if err != nil || checkpointed.Checkpoint.RecoveryLevel != "complete" ||
		checkpointed.Workspace.LastCheckpointID != checkpointed.Checkpoint.ID {
		t.Fatalf("checkpoint=%+v err=%v", checkpointed, err)
	}
	snapshot, err := fixture.state.GetWorkspaceCheckpointSnapshot(t.Context(),
		checkpointed.Checkpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]struct {
		tracked bool
		staged  bool
	}{}
	for _, entry := range snapshot.Entries {
		entries[entry.Path] = struct {
			tracked bool
			staged  bool
		}{entry.Tracked, entry.Staged}
	}
	if !entries["tracked.txt"].tracked || !entries["tracked.txt"].staged ||
		entries["未跟踪 file.txt"].tracked || entries["未跟踪 file.txt"].staged ||
		checkpointed.Checkpoint.IndexBlobSHA256 == "" {
		t.Fatalf("checkpoint did not preserve tracked/untracked/index state: %+v checkpoint=%+v",
			entries, checkpointed.Checkpoint)
	}
	checkpointReplay, err := fixture.service.Checkpoint(t.Context(),
		DrydockCheckpointRequest{RunID: fixture.run.ID,
			ExpectedGeneration: used.Workspace.Generation,
			OperationKey:       "checkpoint-0001", RequestedBy: "runtime_adapter",
			Title: "tracked untracked index", ConfirmObservedChanges: true})
	if err != nil || !checkpointReplay.Replayed ||
		checkpointReplay.Checkpoint.ID != checkpointed.Checkpoint.ID {
		t.Fatalf("checkpoint replay=%+v err=%v", checkpointReplay, err)
	}
	workspace = checkpointed.Workspace
	deliveryRequest := DrydockDeliveryRequest{
		RunID: fixture.run.ID, ExpectedGeneration: workspace.Generation,
		OperationKey: "deliver-0001", RequestedBy: "operator", Confirm: true}
	delivered, err := fixture.service.Deliver(t.Context(), deliveryRequest)
	if err != nil || delivered.Workspace.State != runworktree.StateDelivered ||
		delivered.Review.Proposal.AutomaticMerge || delivered.Review.Proposal.PushAuthorized ||
		delivered.Review.Proposal.ForceAuthorized ||
		delivered.Review.Proposal.SourceOverwriteAllowed ||
		!strings.Contains(delivered.Review.Patch, "staged") ||
		!containsDrydockTestString(delivered.Review.Proposal.ChangedPaths, "未跟踪 file.txt") {
		t.Fatalf("delivery=%+v err=%v", delivered, err)
	}
	deliveryReplay, err := fixture.service.Deliver(t.Context(), deliveryRequest)
	if err != nil || !deliveryReplay.Replayed ||
		deliveryReplay.Review.Proposal.ID != delivered.Review.Proposal.ID {
		t.Fatalf("delivery replay=%+v err=%v", deliveryReplay, err)
	}
	rewindRequest := DrydockRewindRequest{RunID: fixture.run.ID,
		TargetCheckpointID: created.Checkpoint.ID,
		ExpectedGeneration: delivered.Workspace.Generation,
		OperationKey:       "rewind-0001", RequestedBy: "desktop_operator"}
	rewindPreview, err := fixture.service.Rewind(t.Context(), rewindRequest)
	if err != nil || rewindPreview.Confirmed || len(rewindPreview.Preview.Changes) == 0 {
		t.Fatalf("rewind preview=%+v err=%v", rewindPreview, err)
	}
	rewindRequest.Confirm = true
	prepareDrydockRestoreForTest(t, fixture)
	rewound, err := fixture.service.Rewind(t.Context(), rewindRequest)
	if err != nil || !rewound.Confirmed || rewound.After == nil || rewound.Receipt == nil ||
		rewound.Receipt.Operation != runworktree.OperationRewind ||
		rewound.After.ParentCheckpointID == "" {
		t.Fatalf("rewound=%+v err=%v", rewound, err)
	}
	if got := readDrydockTestFile(t, filepath.Join(rewound.Workspace.Path, "tracked.txt")); got != "base\n" {
		t.Fatalf("rewind tracked content=%q", got)
	}
	if _, err := os.Stat(filepath.Join(rewound.Workspace.Path, "未跟踪 file.txt")); !os.IsNotExist(err) {
		t.Fatalf("rewind retained target-absent untracked file: %v", err)
	}
	if rewound.After.IndexSHA256 != created.Checkpoint.IndexSHA256 {
		t.Fatalf("rewind did not restore the baseline index: got=%s want=%s",
			rewound.After.IndexSHA256, created.Checkpoint.IndexSHA256)
	}
	undoRequest := DrydockUndoRequest{RunID: fixture.run.ID,
		ExpectedGeneration: rewound.Workspace.Generation,
		OperationKey:       "undo-0001", RequestedBy: "desktop_operator"}
	undoPreview, err := fixture.service.Undo(t.Context(), undoRequest)
	if err != nil || undoPreview.Confirmed || len(undoPreview.Preview.Changes) == 0 {
		t.Fatalf("undo preview=%+v err=%v", undoPreview, err)
	}
	undoRequest.Confirm = true
	undone, err := fixture.service.Undo(t.Context(), undoRequest)
	if err != nil || !undone.Confirmed || undone.After == nil || undone.Receipt == nil ||
		undone.Receipt.Operation != runworktree.OperationUndo {
		t.Fatalf("undone=%+v err=%v", undone, err)
	}
	if got := readDrydockTestFile(t, filepath.Join(undone.Workspace.Path, "tracked.txt")); got != "staged\n" {
		t.Fatalf("undo tracked content=%q", got)
	}
	if got := readDrydockTestFile(t, filepath.Join(undone.Workspace.Path,
		"未跟踪 file.txt")); got != "untracked\n" {
		t.Fatalf("undo untracked content=%q", got)
	}
	if staged := strings.TrimSpace(runDrydockTestGitOutput(t, undone.Workspace.Path,
		"diff", "--cached", "--name-only")); staged != "tracked.txt" {
		t.Fatalf("undo did not restore the staged index: %q", staged)
	}
	undoReplay, err := fixture.service.Undo(t.Context(), undoRequest)
	if err != nil || !undoReplay.Replayed || undoReplay.Receipt == nil ||
		undoReplay.Receipt.ID != undone.Receipt.ID || undoReplay.After == nil ||
		undoReplay.After.ID != undone.After.ID || undoReplay.Target.ID != rewound.After.ParentCheckpointID {
		t.Fatalf("undo replay=%+v err=%v", undoReplay, err)
	}

	eventCounts := map[string]int{}
	eventItems, err := fixture.state.ListRunEvents(t.Context(), fixture.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range eventItems {
		eventCounts[event.Type]++
	}
	for _, eventType := range []string{events.DrydockTrustConfirmedEvent,
		events.DrydockCreatePreparedEvent, events.DrydockCreatedEvent,
		events.DrydockUseAttestedEvent, events.DrydockCheckpointRecordedEvent,
		events.DrydockDeliveryProposedEvent, events.DrydockRewindCompletedEvent,
		events.DrydockUndoCompletedEvent} {
		if eventCounts[eventType] != 1 {
			t.Fatalf("event %s count=%d all=%+v", eventType, eventCounts[eventType], eventCounts)
		}
	}
	receipts, err := fixture.state.ListDrydockReceipts(t.Context(), workspace.ID, 100)
	if err != nil || len(receipts) != 6 {
		t.Fatalf("receipts=%+v err=%v", receipts, err)
	}
	for _, receipt := range receipts {
		if receipt.GrantsProcessAuthority {
			t.Fatalf("receipt granted process authority: %+v", receipt)
		}
	}
}

func TestResolveDrydockExecutionBindingDistinguishesPrestartDriftFromOwnedOutput(t *testing.T) {
	fixture := newDrydockApplicationFixture(t, "standard code execution binding")
	preview, err := fixture.service.Create(t.Context(), DrydockCreateRequest{
		RunID: fixture.run.ID, OperationKey: "standard-code-binding-preview",
		RequestedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := fixture.service.Create(t.Context(), DrydockCreateRequest{
		RunID: fixture.run.ID, OperationKey: "standard-code-binding-create",
		RequestedBy: "operator", ConfirmWorkspaceTrust: true,
		ExpectedTrustDigest: preview.TrustDigest})
	if err != nil || created.Workspace == nil {
		t.Fatalf("create Drydock: %#v err=%v", created, err)
	}
	workspace := *created.Workspace
	binding := sandbox.DockerStandardCodeRunnerBinding{
		RunID: workspace.RunID, MissionID: workspace.MissionID,
		SessionID: workspace.SessionID, WorkspaceID: workspace.SourceWorkspaceID,
		DrydockID: workspace.ID, DrydockWorkspaceID: workspace.WorkspaceID,
		DrydockGeneration:    workspace.Generation,
		CheckpointID:         workspace.LastCheckpointID,
		DrydockBindingSHA256: workspace.ExpectedBindingFingerprint,
		ProfileSnapshotID:    "standard-code-profile-1", ProfileRevision: 1,
		PermissionSnapshotID: "standard-code-permission-1", PermissionRevision: 1,
		CapabilityGeneration: strings.Repeat("c", 64),
		CommandSHA256:        strings.Repeat("d", 64),
		StdinPolicy:          sandbox.DockerStandardCodeStdinClosed,
		Toolchain:            sandbox.DockerStandardCodeToolchainGo,
		WorkingDirectory:     ".", Arguments: []string{"test", "./..."},
		TimeoutSeconds: 60}
	resolved, err := fixture.service.ResolveDrydockExecutionBinding(t.Context(),
		binding, true)
	if err != nil || resolved.ID != workspace.SourceWorkspaceID ||
		resolved.RootPath != workspace.Path {
		t.Fatalf("resolve exact Drydock = %#v err=%v", resolved, err)
	}
	afterResolve, found, err := fixture.state.GetDrydockByRun(t.Context(), fixture.run.ID)
	if err != nil || !found || afterResolve.Generation != workspace.Generation {
		t.Fatalf("read-only resolve advanced ownership: %#v found=%t err=%v",
			afterResolve, found, err)
	}
	writeDrydockTestFile(t, filepath.Join(workspace.Path, "container-output.txt"),
		"owned container output\n")
	if _, err := fixture.service.ResolveDrydockExecutionBinding(t.Context(), binding,
		true); err == nil {
		t.Fatal("pre-start Drydock content drift was accepted")
	}
	if resolved, err = fixture.service.ResolveDrydockExecutionBinding(t.Context(),
		binding, false); err != nil || resolved.RootPath != workspace.Path {
		t.Fatalf("owned post-start output blocked cleanup: %#v err=%v", resolved, err)
	}
}

func TestResolveDrydockExecutionBindingUsesSuccessorIdentityAndRetainsPhysicalOwner(t *testing.T) {
	f := newDrydockApplicationFixture(t, "continued Docker dependency preparation")
	owned := mustCreateDrydock(t, f)
	ctx := t.Context()
	thread, err := f.state.GetThreadByRun(ctx, f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	runs := NewRunService(f.state)
	if _, err := runs.Start(ctx, f.run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Fail(ctx, f.run.ID, "retain working directory for continuation"); err != nil {
		t.Fatal(err)
	}
	threads := NewThreadServiceWithExecutionCapabilities(f.state, standardCodeThreadTestRuntime().ExecutionPermissionCapabilities).WithRunWorktree(f.service)
	next, err := threads.Submit(ctx, SubmitThreadMessageRequest{Version: domain.ThreadMessageProtocolVersion,
		ThreadID: thread.ID, Content: "Continue the prepared project", OperationKey: "docker-binding-successor", RequestedBy: "operator"})
	if err != nil || !next.SuccessorCreated {
		t.Fatalf("successor=%+v err=%v", next, err)
	}
	binding := sandbox.DockerStandardCodeRunnerBinding{
		RunID: next.Run.ID, MissionID: owned.MissionID, SessionID: next.Run.SessionID,
		WorkspaceID: owned.SourceWorkspaceID, DrydockID: owned.ID, DrydockWorkspaceID: owned.WorkspaceID,
		DrydockGeneration: owned.Generation, CheckpointID: owned.LastCheckpointID, DrydockBindingSHA256: owned.ExpectedBindingFingerprint,
		ProfileSnapshotID: "continued-profile", ProfileRevision: 1, PermissionSnapshotID: "continued-permission", PermissionRevision: 1,
		CapabilityGeneration: strings.Repeat("c", 64), CommandSHA256: strings.Repeat("d", 64),
		StdinPolicy: sandbox.DockerStandardCodeStdinClosed, Toolchain: sandbox.DockerStandardCodeToolchainGo,
		WorkingDirectory: ".", Arguments: []string{"test", "./..."}, TimeoutSeconds: 60,
	}
	resolved, err := f.service.ResolveDrydockExecutionBinding(ctx, binding, true)
	if err != nil || resolved.RootPath != owned.Path {
		t.Fatalf("continued Docker binding=%+v err=%v", resolved, err)
	}
	for _, change := range []func(*sandbox.DockerStandardCodeRunnerBinding){
		func(b *sandbox.DockerStandardCodeRunnerBinding) { b.SessionID = owned.SessionID },
		func(b *sandbox.DockerStandardCodeRunnerBinding) { b.MissionID = "unrelated-mission" },
		func(b *sandbox.DockerStandardCodeRunnerBinding) { b.DrydockID = "unrelated-drydock" },
	} {
		other := binding
		change(&other)
		if _, err := f.service.ResolveDrydockExecutionBinding(ctx, other, true); err == nil {
			t.Fatal("continued Docker binding accepted changed authority")
		}
	}
	if _, err := runs.Start(ctx, next.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Fail(ctx, next.Run.ID, "continue again"); err != nil {
		t.Fatal(err)
	}
	if _, err := threads.Submit(ctx, SubmitThreadMessageRequest{Version: domain.ThreadMessageProtocolVersion,
		ThreadID: thread.ID, Content: "Continue again", OperationKey: "docker-binding-next-successor", RequestedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ResolveDrydockExecutionBinding(ctx, binding, true); err == nil {
		t.Fatal("previous successor retained launch authority after the next epoch")
	}
	if _, err := f.service.ResolveDrydockExecutionBinding(ctx, binding, false); err != nil {
		t.Fatalf("historical exact binding blocked owned cleanup: %v", err)
	}
	physical, found, err := f.state.GetDrydockByRun(ctx, f.run.ID)
	if err != nil || !found || physical.RunID != owned.RunID || physical.SessionID != owned.SessionID || physical.Generation != owned.Generation {
		t.Fatalf("read-only continuation changed physical ownership: %+v found=%t err=%v", physical, found, err)
	}
}

func TestDrydockRunBindingAndRootBranchBaseDriftFailClosed(t *testing.T) {
	t.Run("another Run cannot claim the Drydock", func(t *testing.T) {
		fixture := newDrydockApplicationFixture(t, "shared repo")
		created := mustCreateDrydock(t, fixture)
		if _, _, err := NewRunService(fixture.state).Create(t.Context(), CreateRunRequest{
			Goal: "claim synthetic Drydock Workspace", Profile: "code",
			WorkspaceID: created.WorkspaceID,
			Budget:      domain.Budget{MaxTurns: 3, MaxToolCalls: 8}}); err == nil {
			t.Fatal("another Run bound the Drydock synthetic Workspace")
		}
		_, otherRun, err := NewRunService(fixture.state).Create(t.Context(), CreateRunRequest{
			Goal: "other Run", Profile: "code", WorkspaceID: fixture.workspace.ID,
			Budget: domain.Budget{MaxTurns: 3, MaxToolCalls: 8}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.service.Use(t.Context(), DrydockUseRequest{RunID: otherRun.ID,
			ExpectedGeneration: created.Generation, OperationKey: "foreign-use-0001",
			RequestedBy: "other"}); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("foreign Run use error=%v", err)
		}
	})

	t.Run("source base drift", func(t *testing.T) {
		fixture := newDrydockApplicationFixture(t, "base drift")
		created := mustCreateDrydock(t, fixture)
		writeDrydockTestFile(t, filepath.Join(fixture.sourceRoot, "base-drift.txt"), "drift\n")
		runDrydockTestGit(t, fixture.sourceRoot, "add", ".")
		runDrydockTestGit(t, fixture.sourceRoot, "commit", "-m", "source drift")
		if _, err := fixture.service.Use(t.Context(), DrydockUseRequest{RunID: fixture.run.ID,
			ExpectedGeneration: created.Generation, OperationKey: "base-drift-use-0001",
			RequestedBy: "operator"}); err == nil || !strings.Contains(err.Error(), "drifted") {
			t.Fatalf("base drift error=%v", err)
		}
	})

	t.Run("Drydock branch drift", func(t *testing.T) {
		fixture := newDrydockApplicationFixture(t, "branch drift")
		created := mustCreateDrydock(t, fixture)
		runDrydockTestGit(t, created.Path, "branch", "-m", "codex/drifted-branch")
		if _, err := fixture.service.Use(t.Context(), DrydockUseRequest{RunID: fixture.run.ID,
			ExpectedGeneration: created.Generation, OperationKey: "branch-drift-use-0001",
			RequestedBy: "operator"}); err == nil {
			t.Fatal("branch drift was accepted")
		}
	})

	t.Run("registered root drift", func(t *testing.T) {
		fixture := newDrydockApplicationFixture(t, "root drift")
		created := mustCreateDrydock(t, fixture)
		otherRoot := newDrydockTestRepository(t, "replacement root")
		if err := fixture.state.SaveWorkspace(t.Context(), store.WorkspaceRecord{
			ID: fixture.workspace.ID, Name: fixture.workspace.Name, RootPath: otherRoot}); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.service.Use(t.Context(), DrydockUseRequest{RunID: fixture.run.ID,
			ExpectedGeneration: created.Generation, OperationKey: "root-drift-use-0001",
			RequestedBy: "operator"}); err == nil || !strings.Contains(err.Error(), "drifted") {
			t.Fatalf("root drift error=%v", err)
		}
	})
}

func TestDrydockExplicitlyRejectsSymlinkAndSubmoduleEntries(t *testing.T) {
	fixture := newDrydockApplicationFixture(t, "special entries")
	linkPayload := filepath.Join(fixture.sourceRoot, "link-payload.txt")
	writeDrydockTestFile(t, linkPayload, "tracked.txt\n")
	linkOID := strings.TrimSpace(runDrydockTestGitOutput(t, fixture.sourceRoot,
		"hash-object", "-w", "--", "link-payload.txt"))
	if err := os.Remove(linkPayload); err != nil {
		t.Fatal(err)
	}
	runDrydockTestGit(t, fixture.sourceRoot, "update-index", "--add", "--cacheinfo",
		"120000,"+linkOID+",linked-entry")
	head := strings.TrimSpace(runDrydockTestGitOutput(t, fixture.sourceRoot,
		"rev-parse", "HEAD"))
	runDrydockTestGit(t, fixture.sourceRoot, "update-index", "--add", "--cacheinfo",
		"160000,"+head+",nested-module")

	result, err := fixture.service.Create(t.Context(), DrydockCreateRequest{
		RunID: fixture.run.ID, OperationKey: "special-entry-preview-0001",
		RequestedBy: "operator"})
	if err == nil || !strings.Contains(err.Error(), "rejects source symlink/reparse") ||
		result.SourceState.SymlinkEntries != 1 || result.SourceState.SubmoduleEntries != 1 ||
		result.Workspace != nil {
		t.Fatalf("special entry result=%+v err=%v", result, err)
	}
	if _, found, getErr := fixture.state.GetDrydockByRun(t.Context(), fixture.run.ID); getErr != nil || found {
		t.Fatalf("rejected source created durable Drydock: found=%t err=%v", found, getErr)
	}
}

func TestDrydockStoredTrustCannotSilentlyAdoptSourceStateDrift(t *testing.T) {
	fixture := newDrydockApplicationFixture(t, "trust state drift")
	source, err := fixture.executor.InspectSource(t.Context(), fixture.workspace.ID,
		fixture.sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	trust := runworktree.Trust{ID: drydockTrustID(fixture.run.ID, source.Identity),
		ProtocolVersion: runworktree.TrustProtocolVersion, RunID: fixture.run.ID,
		WorkspaceID: fixture.workspace.ID, Source: source.Identity,
		SourceState: source.State, ConfirmedBy: "operator", ConfirmedAt: now}
	if _, _, err := fixture.state.CreateDrydockTrust(t.Context(), trust); err != nil {
		t.Fatal(err)
	}
	writeDrydockTestFile(t, filepath.Join(fixture.sourceRoot, "tracked.txt"),
		"changed after trust\n")

	result, err := fixture.service.Create(t.Context(), DrydockCreateRequest{
		RunID: fixture.run.ID, OperationKey: "trust-state-drift-create-0001",
		RequestedBy: "operator", ConfirmWorkspaceTrust: true,
		ExpectedTrustDigest: runworktree.TrustConfirmationDigest(source.Identity, source.State)})
	if err == nil || !strings.Contains(err.Error(), "current source identity and state") ||
		result.Workspace != nil {
		t.Fatalf("source state drift result=%+v err=%v", result, err)
	}
}

func TestDrydockCrashRecoveryAndGCPreservePostCrashUserFile(t *testing.T) {
	t.Run("interrupted exact create is recovered", func(t *testing.T) {
		fixture := newDrydockApplicationFixture(t, "crash create")
		source, err := fixture.executor.InspectSource(t.Context(), fixture.workspace.ID,
			fixture.sourceRoot)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		trust := runworktree.Trust{ID: drydockTrustID(fixture.run.ID, source.Identity),
			ProtocolVersion: runworktree.TrustProtocolVersion, RunID: fixture.run.ID,
			WorkspaceID: fixture.workspace.ID, Source: source.Identity,
			SourceState: source.State, ConfirmedBy: "operator", ConfirmedAt: now}
		trust, _, err = fixture.state.CreateDrydockTrust(t.Context(), trust)
		if err != nil {
			t.Fatal(err)
		}
		identityDigest := runworktree.Fingerprint("drydock", fixture.run.ID,
			source.Identity.Fingerprint())
		name := "drydock-" + identityDigest[:24]
		branch := "codex/drydock/" + identityDigest[:24]
		plan, err := fixture.executor.PlanCreate(t.Context(), fixture.sourceRoot, name,
			branch, source.Identity.BaseCommit)
		if err != nil {
			t.Fatal(err)
		}
		workspace := runworktree.Workspace{ID: "drydock-" + identityDigest[:32],
			ProtocolVersion: runworktree.WorkspaceProtocolVersion, RunID: fixture.run.ID,
			MissionID: fixture.run.MissionID, SessionID: fixture.run.SessionID,
			SourceWorkspaceID: fixture.workspace.ID,
			WorkspaceID:       "drydock-ws-" + identityDigest[:32], TrustID: trust.ID,
			Source: source.Identity, Name: name, Path: filepath.Clean(plan.Path),
			PathSHA256: runworktree.FingerprintBytes([]byte(filepath.ToSlash(filepath.Clean(plan.Path)))),
			Branch:     branch, BaseCommit: source.Identity.BaseCommit,
			CreatePreviewID: plan.Preview.ID, State: runworktree.StatePreparing,
			Generation: 1, ExpiresAt: now.Add(runworktree.DefaultLifetime),
			CreatedAt: now, UpdatedAt: now}
		workspace, _, err = fixture.state.PrepareDrydock(t.Context(), workspace)
		if err != nil {
			t.Fatal(err)
		}
		gitReceipt, err := fixture.executor.ExecuteCreate(t.Context(), fixture.sourceRoot, plan)
		if err != nil || gitReceipt.Status != gitadvanced.ReceiptSucceeded {
			t.Fatalf("Git create receipt=%+v err=%v", gitReceipt, err)
		}
		reconciled, err := fixture.service.Reconcile(t.Context())
		if err != nil || reconciled.Recovered != 1 {
			t.Fatalf("reconciled=%+v err=%v", reconciled, err)
		}
		stored, found, err := fixture.state.GetDrydockByRun(t.Context(), fixture.run.ID)
		if err != nil || !found || stored.State != runworktree.StateReady ||
			stored.LastCheckpointID == "" {
			t.Fatalf("stored=%+v found=%t err=%v", stored, found, err)
		}
	})

	t.Run("GC preserves a post-crash user file", func(t *testing.T) {
		fixture := newDrydockApplicationFixture(t, "crash user file")
		created := mustCreateDrydock(t, fixture)
		userPath := filepath.Join(created.Path, "post-crash-user.txt")
		writeDrydockTestFile(t, userPath, "keep me\n")
		fixture.service.now = func() time.Time { return created.ExpiresAt.Add(time.Hour) }
		gc, err := fixture.service.GarbageCollect(t.Context(), 10)
		if err != nil || gc.Cleaned != 0 || gc.Preserved != 1 {
			t.Fatalf("gc=%+v err=%v", gc, err)
		}
		if got := readDrydockTestFile(t, userPath); got != "keep me\n" {
			t.Fatalf("post-crash file was changed or removed: %q", got)
		}
		stored, found, err := fixture.state.GetDrydockByRun(t.Context(), fixture.run.ID)
		if err != nil || !found || stored.State != runworktree.StateReady || stored.Generation != created.Generation {
			t.Fatalf("stored=%+v found=%t err=%v", stored, found, err)
		}
		if stored.ExpectedBindingFingerprint != created.ExpectedBindingFingerprint {
			t.Fatal("GC changed the retained Thread directory binding")
		}
		unconfirmed, err := fixture.service.Checkpoint(t.Context(),
			DrydockCheckpointRequest{RunID: fixture.run.ID,
				ExpectedGeneration: stored.Generation,
				OperationKey:       "post-crash-unconfirmed-recovery-0001",
				RequestedBy:        "operator"})
		if err == nil || unconfirmed.Workspace.State != runworktree.StateRecoveryRequired ||
			unconfirmed.Receipt.Outcome != runworktree.OutcomePreserved {
			t.Fatalf("unconfirmed recovery=%+v err=%v", unconfirmed, err)
		}
		confirmed, err := fixture.service.Checkpoint(t.Context(),
			DrydockCheckpointRequest{RunID: fixture.run.ID,
				ExpectedGeneration: unconfirmed.Workspace.Generation,
				OperationKey:       "post-crash-confirmed-recovery-0001",
				RequestedBy:        "operator", ConfirmObservedChanges: true})
		if err != nil || confirmed.Workspace.State != runworktree.StateReady ||
			confirmed.Receipt.Operation != runworktree.OperationRecover {
			t.Fatalf("confirmed recovery=%+v err=%v", confirmed, err)
		}
		replayed, err := fixture.service.Checkpoint(t.Context(),
			DrydockCheckpointRequest{RunID: fixture.run.ID,
				ExpectedGeneration: unconfirmed.Workspace.Generation,
				OperationKey:       "post-crash-confirmed-recovery-0001",
				RequestedBy:        "operator", ConfirmObservedChanges: true})
		if err != nil || !replayed.Replayed || replayed.Checkpoint.ID != confirmed.Checkpoint.ID {
			t.Fatalf("recovered checkpoint replay=%+v err=%v", replayed, err)
		}
		if got := readDrydockTestFile(t, userPath); got != "keep me\n" {
			t.Fatalf("confirmed checkpoint changed post-crash file: %q", got)
		}
	})
}

func TestDrydockRewindSupportsCheckpointsAtACommittedDescendant(t *testing.T) {
	fixture := newDrydockApplicationFixture(t, "descendant rewind")
	created := mustCreateDrydock(t, fixture)
	writeDrydockTestFile(t, filepath.Join(created.Path, "tracked.txt"), "committed descendant\n")
	runDrydockTestGit(t, created.Path, "add", "tracked.txt")
	runDrydockTestGit(t, created.Path, "commit", "-m", "Drydock descendant")
	committedHead := strings.TrimSpace(runDrydockTestGitOutput(t, created.Path,
		"rev-parse", "HEAD"))

	committed, err := fixture.service.Checkpoint(t.Context(), DrydockCheckpointRequest{
		RunID: fixture.run.ID, ExpectedGeneration: created.Generation,
		OperationKey: "descendant-checkpoint-0001", RequestedBy: "operator",
		ConfirmObservedChanges: true})
	if err != nil || committed.Checkpoint.BaseCommit != committedHead {
		t.Fatalf("committed checkpoint=%+v err=%v", committed, err)
	}
	writeDrydockTestFile(t, filepath.Join(created.Path, "tracked.txt"), "later worktree change\n")
	later, err := fixture.service.Checkpoint(t.Context(), DrydockCheckpointRequest{
		RunID: fixture.run.ID, ExpectedGeneration: committed.Workspace.Generation,
		OperationKey: "descendant-checkpoint-0002", RequestedBy: "operator",
		ConfirmObservedChanges: true})
	if err != nil || later.Checkpoint.BaseCommit != committedHead {
		t.Fatalf("later checkpoint=%+v err=%v", later, err)
	}
	request := DrydockRewindRequest{RunID: fixture.run.ID,
		TargetCheckpointID: committed.Checkpoint.ID,
		ExpectedGeneration: later.Workspace.Generation,
		OperationKey:       "descendant-rewind-0001", RequestedBy: "desktop_operator"}
	preview, err := fixture.service.Rewind(t.Context(), request)
	if err != nil || len(preview.Preview.Changes) == 0 {
		t.Fatalf("descendant rewind preview=%+v err=%v", preview, err)
	}
	request.Confirm = true
	prepareDrydockRestoreForTest(t, fixture)
	rewound, err := fixture.service.Rewind(t.Context(), request)
	if err != nil || rewound.After == nil || rewound.After.BaseCommit != committedHead {
		t.Fatalf("descendant rewind=%+v err=%v", rewound, err)
	}
	if got := readDrydockTestFile(t, filepath.Join(created.Path, "tracked.txt")); got != "committed descendant\n" {
		t.Fatalf("descendant rewind content=%q", got)
	}
	if head := strings.TrimSpace(runDrydockTestGitOutput(t, created.Path,
		"rev-parse", "HEAD")); head != committedHead {
		t.Fatalf("rewind rewrote commit history: head=%s want=%s", head, committedHead)
	}
}

func TestDrydockCleanupUsesExactNonForceGitReceiptAndRetainsBranch(t *testing.T) {
	fixture := newDrydockApplicationFixture(t, "clean cleanup")
	created := mustCreateDrydock(t, fixture)
	branch := created.Branch
	result, err := fixture.service.Cleanup(t.Context(), DrydockCleanupRequest{
		RunID: fixture.run.ID, ExpectedGeneration: created.Generation,
		OperationKey: "cleanup-0001", RequestedBy: "operator", Confirm: true})
	if err != nil || result.Workspace.State != runworktree.StateCleaned || result.Preserved ||
		result.Receipt.GitReceiptID == "" {
		t.Fatalf("cleanup=%+v err=%v", result, err)
	}
	if _, err := os.Stat(created.Path); !os.IsNotExist(err) {
		t.Fatalf("cleaned worktree path still exists: %v", err)
	}
	output := runDrydockTestGitOutput(t, fixture.sourceRoot, "show-ref", "--verify",
		"refs/heads/"+branch)
	if strings.TrimSpace(output) == "" {
		t.Fatal("cleanup deleted the reviewable local branch")
	}
	eventsList, err := fixture.state.ListRunEvents(t.Context(), fixture.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundCleanup := false
	for _, event := range eventsList {
		foundCleanup = foundCleanup || event.Type == events.DrydockCleanupCompletedEvent
	}
	if !foundCleanup {
		t.Fatal("cleanup event was not recorded")
	}
	replayed, err := fixture.service.Cleanup(t.Context(), DrydockCleanupRequest{
		RunID: fixture.run.ID, ExpectedGeneration: created.Generation,
		OperationKey: "cleanup-0001", RequestedBy: "operator", Confirm: true})
	if err != nil || !replayed.Replayed || replayed.Receipt.ID != result.Receipt.ID {
		t.Fatalf("cleanup replay=%+v err=%v", replayed, err)
	}
}

func TestDrydockCleanupClosesExactlyAbsentPostCrashRegistrationWithoutDeleting(t *testing.T) {
	fixture := newDrydockApplicationFixture(t, "cleanup crash")
	created := mustCreateDrydock(t, fixture)
	unrelatedPath := filepath.Join(filepath.Dir(created.Path), "unrelated user directory",
		"keep.txt")
	writeDrydockTestFile(t, unrelatedPath, "keep\n")

	preview, err := fixture.executor.PlanRemove(t.Context(), fixture.sourceRoot,
		created.Name, created.ManagedWorktreeID)
	if err != nil || !preview.Executable() {
		t.Fatalf("cleanup preflight=%+v err=%v", preview, err)
	}
	gitReceipt, err := fixture.executor.ExecuteRemove(t.Context(), fixture.sourceRoot, preview)
	if err != nil || gitReceipt.Validate() != nil ||
		gitReceipt.Status != gitadvanced.ReceiptSucceeded {
		t.Fatalf("simulated interrupted cleanup receipt=%+v err=%v", gitReceipt, err)
	}
	if _, err := os.Lstat(created.Path); !os.IsNotExist(err) {
		t.Fatalf("simulated Git cleanup did not remove exact path: %v", err)
	}

	reconciled, err := fixture.service.Reconcile(t.Context())
	if err != nil || reconciled.RecoveryRequired != 1 {
		t.Fatalf("post-crash reconcile=%+v err=%v", reconciled, err)
	}
	stored, found, err := fixture.state.GetDrydockByRun(t.Context(), fixture.run.ID)
	if err != nil || !found || stored.State != runworktree.StateRecoveryRequired {
		t.Fatalf("post-crash stored=%+v found=%t err=%v", stored, found, err)
	}
	closed, err := fixture.service.Cleanup(t.Context(), DrydockCleanupRequest{
		RunID: fixture.run.ID, ExpectedGeneration: stored.Generation,
		OperationKey: "cleanup-absent-recovery-0001", RequestedBy: "operator", Confirm: true})
	if err != nil || closed.Workspace.State != runworktree.StateCleaned || closed.Preserved ||
		closed.Receipt.GitReceiptID != "" ||
		!strings.Contains(closed.Receipt.Summary, "no filesystem entry was deleted") {
		t.Fatalf("absent cleanup=%+v err=%v", closed, err)
	}
	if got := readDrydockTestFile(t, unrelatedPath); got != "keep\n" {
		t.Fatalf("absent cleanup changed unrelated file: %q", got)
	}
}

func TestDrydockForkUsesCheckpointAndCreatesAuthorityResetRun(t *testing.T) {
	fixture := newDrydockApplicationFixture(t, "checkpoint fork")
	capabilities := domain.ExecutionPermissionRuntimeCapabilities{
		OperatorApprovalEnabled: true}
	checkpoints, err := NewWorkspaceCheckpointService(fixture.state, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	sourceCheckpoint, _, err := checkpoints.Capture(t.Context(),
		WorkspaceCheckpointCaptureRequest{RunID: fixture.run.ID,
			OperationKey: "source-checkpoint-before-drydock-0001",
			RequestedBy:  "desktop_operator", Title: "source cursor remains independent"})
	if err != nil {
		t.Fatal(err)
	}
	created := mustCreateDrydock(t, fixture)
	sourceState, found, err := fixture.state.GetWorkspaceCheckpointRunState(
		t.Context(), fixture.run.ID)
	if err != nil || !found || sourceState.WorkspaceID != fixture.workspace.ID ||
		sourceState.CurrentCheckpointID != sourceCheckpoint.ID {
		t.Fatalf("source cursor changed by Drydock: state=%+v found=%t err=%v",
			sourceState, found, err)
	}
	writeDrydockTestFile(t, filepath.Join(created.Path, "descendant.txt"),
		"committed before fork\n")
	runDrydockTestGit(t, created.Path, "add", "descendant.txt")
	runDrydockTestGit(t, created.Path, "commit", "-m", "descendant before fork")
	descendant, err := fixture.service.Checkpoint(t.Context(), DrydockCheckpointRequest{
		RunID: fixture.run.ID, ExpectedGeneration: created.Generation,
		OperationKey: "drydock-fork-descendant-checkpoint-0001",
		RequestedBy:  "desktop_operator", ConfirmObservedChanges: true})
	if err != nil || descendant.Checkpoint.BaseCommit == created.BaseCommit {
		t.Fatalf("descendant fork checkpoint=%+v err=%v", descendant, err)
	}
	created = descendant.Workspace
	prepareDrydockRestoreForTest(t, fixture)
	fixture.service.WithCheckpointService(checkpoints)
	sourceTimeline, err := checkpoints.Timeline(t.Context(), fixture.run.ID, 100)
	if err != nil || sourceTimeline.WorkspaceID != fixture.workspace.ID ||
		sourceTimeline.Current == nil ||
		sourceTimeline.Current.CurrentCheckpointID != sourceCheckpoint.ID ||
		len(sourceTimeline.Checkpoints) != 1 ||
		sourceTimeline.Checkpoints[0].WorkspaceID != fixture.workspace.ID {
		t.Fatalf("Drydock resolver changed source checkpoint service: timeline=%+v err=%v",
			sourceTimeline, err)
	}
	destination := filepath.Join(t.TempDir(), "独立 fork workspace")
	request := DrydockForkRequest{RunID: fixture.run.ID,
		TargetCheckpointID:          descendant.Checkpoint.ID,
		ExpectedCurrentCheckpointID: created.LastCheckpointID,
		ExpectedGeneration:          created.Generation,
		OperationKey:                "drydock-fork-0001", RequestedBy: "desktop_operator",
		WorkspaceName: "drydock-checkpoint-fork", WorkspaceRoot: destination,
		Branch: "checkpoint/drydock-fork", Goal: "continue from a reviewed Drydock checkpoint",
		Confirm: true}
	forked, err := fixture.service.Fork(t.Context(), request)
	if err != nil || forked.Fork.Run.ID == fixture.run.ID ||
		forked.Fork.Workspace.ID == created.WorkspaceID ||
		forked.Fork.Checkpoint.RunID != forked.Fork.Run.ID ||
		forked.Receipt.Operation != runworktree.OperationFork ||
		forked.Workspace.Generation != created.Generation+1 {
		t.Fatalf("forked=%+v err=%v", forked, err)
	}
	defer func() {
		if err := repository.RemoveCreatedWorktree(context.Background(), created.Path,
			forked.Fork.Workspace.RootPath, request.Branch); err != nil {
			t.Errorf("cleanup exact test fork worktree: %v", err)
		}
	}()
	if got := readDrydockTestFile(t, filepath.Join(forked.Fork.Workspace.RootPath,
		"tracked.txt")); got != "base\n" {
		t.Fatalf("forked checkpoint content=%q", got)
	}
	if got := readDrydockTestFile(t, filepath.Join(forked.Fork.Workspace.RootPath,
		"descendant.txt")); got != "committed before fork\n" {
		t.Fatalf("forked descendant checkpoint content=%q", got)
	}
	for _, boundary := range []string{"approvals", "capability grants", "credentials",
		"execution leases", "network authorization", "processes"} {
		if !containsDrydockTestString(forked.Fork.NotInherited, boundary) {
			t.Fatalf("fork did not declare %q as non-inherited: %+v", boundary,
				forked.Fork.NotInherited)
		}
	}
	replayed, err := fixture.service.Fork(t.Context(), request)
	if err != nil || !replayed.Replayed || replayed.Fork.Run.ID != forked.Fork.Run.ID ||
		replayed.Receipt.ID != forked.Receipt.ID {
		t.Fatalf("fork replay=%+v err=%v", replayed, err)
	}
	sourceTimeline, err = checkpoints.Timeline(t.Context(), fixture.run.ID, 100)
	if err != nil || len(sourceTimeline.Checkpoints) != 1 ||
		len(sourceTimeline.Transactions) != 0 || sourceTimeline.Current == nil ||
		sourceTimeline.Current.CurrentCheckpointID != sourceCheckpoint.ID {
		t.Fatalf("Drydock fork leaked into source timeline: timeline=%+v err=%v",
			sourceTimeline, err)
	}
}

func TestDrydockReconcilesInterruptedCheckpointWithOwnedWorkspaceBinding(t *testing.T) {
	fixture := newDrydockApplicationFixture(t, "checkpoint startup recovery")
	checkpoints, err := NewWorkspaceCheckpointService(fixture.state,
		domain.ExecutionPermissionRuntimeCapabilities{WorkspaceSandboxEnabled: true,
			OperatorApprovalEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	fixture.service.WithCheckpointService(checkpoints)
	workspace := mustCreateDrydock(t, fixture)
	boundary, err := fixture.service.checkpoints.BeginBoundary(t.Context(),
		WorkspaceMutationBoundaryRequest{RunID: fixture.run.ID,
			Kind:             workspacecheckpoint.TransactionCommandBatch,
			OperationKey:     "drydock-interrupted-command-0001",
			TriggerReceiptID: "command-runtime-job-1"})
	if err != nil {
		t.Fatal(err)
	}
	writeDrydockTestFile(t, filepath.Join(workspace.Path, "interrupted.txt"),
		"preserved after restart\n")

	reconciled, err := fixture.service.ReconcileWorkspaceCheckpoints(t.Context())
	if err != nil || reconciled != 1 {
		t.Fatalf("Drydock checkpoint reconciliation count=%d err=%v", reconciled, err)
	}
	transaction, found, err := fixture.state.GetWorkspaceCheckpointTransaction(t.Context(),
		boundary.Transaction.ID)
	if err != nil || !found || transaction.Status != workspacecheckpoint.TransactionInterrupted ||
		transaction.AfterCheckpointID == "" {
		t.Fatalf("reconciled transaction=%+v found=%t err=%v", transaction, found, err)
	}
	after, err := fixture.state.GetWorkspaceCheckpointSnapshot(t.Context(),
		transaction.AfterCheckpointID)
	if err != nil || after.Checkpoint.WorkspaceID != workspace.WorkspaceID ||
		after.Checkpoint.ParentCheckpointID != boundary.Before.ID {
		t.Fatalf("reconciled Drydock checkpoint=%+v err=%v", after.Checkpoint, err)
	}
	foundPreserved := false
	for _, entry := range after.Entries {
		foundPreserved = foundPreserved || entry.Path == "interrupted.txt"
	}
	if !foundPreserved {
		t.Fatal("reconciled Drydock checkpoint omitted the interrupted file")
	}
}

type drydockApplicationFixture struct {
	state        *store.SQLiteStore
	databasePath string
	service      *RunWorktreeService
	executor     *repository.RunWorktreeExecutor
	run          domain.Run
	workspace    store.WorkspaceRecord
	sourceRoot   string
}

func newDrydockApplicationFixture(t *testing.T, rootName string) drydockApplicationFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	sourceRoot := newDrydockTestRepository(t, rootName)
	databasePath := filepath.Join(t.TempDir(), "drydock.db")
	state, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	workspace := store.WorkspaceRecord{ID: "workspace-" + runworktree.Fingerprint("workspace", rootName)[:16],
		Name:     "workspace-" + runworktree.Fingerprint("workspace-name", rootName)[:16],
		RootPath: sourceRoot}
	if err := state.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	_, run, err := NewRunService(state).Create(context.Background(), CreateRunRequest{
		Goal: "test Drydock lifecycle", Profile: "code", Surface: "code", Phase: "deliver",
		WorkspaceID: workspace.ID,
		Budget:      domain.Budget{MaxTurns: 8, MaxTokens: 2_000, MaxToolCalls: 32}})
	if err != nil {
		t.Fatal(err)
	}
	managedRoot := filepath.Join(t.TempDir(), "产品 Drydock 根")
	executor, err := repository.NewRunWorktreeExecutor(managedRoot)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewRunWorktreeService(state, executor)
	if err != nil {
		t.Fatal(err)
	}
	return drydockApplicationFixture{state: state, service: service, executor: executor,
		run: run, workspace: workspace, sourceRoot: sourceRoot, databasePath: databasePath}
}

func mustCreateDrydock(t *testing.T, fixture drydockApplicationFixture) runworktree.Workspace {
	t.Helper()
	preview, err := fixture.service.Create(t.Context(), DrydockCreateRequest{
		RunID: fixture.run.ID, OperationKey: "create-preview-0000", RequestedBy: "operator"})
	if err != nil || !preview.TrustRequired || preview.TrustDigest == "" {
		t.Fatalf("trust preview=%+v err=%v", preview, err)
	}
	result, err := fixture.service.Create(t.Context(), DrydockCreateRequest{
		RunID: fixture.run.ID, OperationKey: "create-0001", RequestedBy: "operator",
		ConfirmWorkspaceTrust: true, ExpectedTrustDigest: preview.TrustDigest})
	if err != nil || result.Workspace == nil {
		t.Fatalf("create=%+v err=%v", result, err)
	}
	return *result.Workspace
}

func newDrydockTestRepository(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name+" 空格")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	runDrydockTestGit(t, root, "init", "-q", "-b", "main")
	runDrydockTestGit(t, root, "config", "user.email", "drydock@example.invalid")
	runDrydockTestGit(t, root, "config", "user.name", "Drydock Test")
	writeDrydockTestFile(t, filepath.Join(root, "tracked.txt"), "base\n")
	runDrydockTestGit(t, root, "add", ".")
	runDrydockTestGit(t, root, "commit", "-m", "baseline")
	return root
}

func runDrydockTestGit(t *testing.T, root string, args ...string) {
	t.Helper()
	_ = runDrydockTestGitOutput(t, root, args...)
}

func runDrydockTestGitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func writeDrydockTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readDrydockTestFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func containsDrydockTestString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
