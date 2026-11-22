package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runworktree"
)

const drydockCLIUsage = "usage: cyberagent drydock status|create|use|checkpoint|rewind|undo|fork|deliver|cleanup|reconcile|gc [--run <run-id>] [--generation <n>] [--operation-key <key>] [explicit confirmation flags]"

func drydockOperationUsage(action string) string {
	switch action {
	case "status":
		return "usage: cyberagent drydock status --run <run-id> [--limit <n>] [--json]"
	case "create":
		return "usage: cyberagent drydock create --run <run-id> --operation-key <key> [--confirm-workspace-trust --expected-trust-digest <digest>] [--json]"
	case "use":
		return "usage: cyberagent drydock use --run <run-id> --generation <n> --operation-key <key> [--json]"
	case "checkpoint":
		return "usage: cyberagent drydock checkpoint --run <run-id> --generation <n> --operation-key <key> [--title <text>] [--confirm-observed-changes] [--json]"
	case "rewind":
		return "usage: cyberagent drydock rewind --run <run-id> --generation <n> --operation-key <key> --target-checkpoint <id> [--confirm] [--confirm-observed-changes] [--json]"
	case "undo":
		return "usage: cyberagent drydock undo --run <run-id> --generation <n> --operation-key <key> [--confirm] [--confirm-observed-changes] [--json]"
	case "fork":
		return "usage: cyberagent drydock fork --run <run-id> --generation <n> --operation-key <key> --confirm --enable-permission-control --target-checkpoint <id> --expected-current-checkpoint <id> --workspace-name <name> --workspace-root <root> --branch <branch> [--goal <text>] [--json]"
	case "deliver":
		return "usage: cyberagent drydock deliver --run <run-id> --generation <n> --operation-key <key> --confirm [--json]"
	case "cleanup":
		return "usage: cyberagent drydock cleanup --run <run-id> --generation <n> --operation-key <key> --confirm [--json]"
	case "reconcile":
		return "usage: cyberagent drydock reconcile [--json]"
	case "gc":
		return "usage: cyberagent drydock gc --confirm [--limit <n>] [--json]"
	}
	return drydockCLIUsage
}

func (a *App) drydockCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New(drydockCLIUsage)
	}
	action := strings.TrimSpace(args[0])
	switch action {
	case "status", "create", "use", "checkpoint", "rewind", "undo", "fork", "deliver", "cleanup", "reconcile", "gc":
	default:
		return errors.New(drydockOperationUsage(action))
	}
	fs := newFlagSet("drydock "+action, a.errOut)
	runID := fs.String("run", "", "exact Standard Code Run identity")
	generation := fs.Int64("generation", 0, "expected Drydock ownership generation")
	operationKey := fs.String("operation-key", "", "stable operation idempotency key")
	requestedBy := fs.String("requested-by", "cli_operator", "bounded operator attribution")
	confirmTrust := fs.Bool("confirm-workspace-trust", false,
		"confirm the exact displayed source Workspace Trust digest")
	expectedTrust := fs.String("expected-trust-digest", "",
		"exact SHA-256 from a prior create review")
	confirmChanges := fs.Bool("confirm-observed-changes", false,
		"attribute the currently observed Drydock changes to this checkpoint")
	confirmFull := fs.Bool("confirm-full", false,
		"activate the current Full preference only for this invocation")
	confirm := fs.Bool("confirm", false, "confirm the exact requested lifecycle operation")
	title := fs.String("title", "", "checkpoint title")
	targetCheckpoint := fs.String("target-checkpoint", "", "exact Drydock checkpoint identity")
	expectedCurrentCheckpoint := fs.String("expected-current-checkpoint", "",
		"exact current Drydock checkpoint identity")
	forkWorkspaceName := fs.String("workspace-name", "", "new registered Workspace name")
	forkWorkspaceRoot := fs.String("workspace-root", "", "new absolute fork Worktree root")
	forkBranch := fs.String("branch", "", "new local fork branch")
	forkGoal := fs.String("goal", "", "new authority-reset Run goal")
	permissionControl := fs.Bool("enable-permission-control", false,
		"enable the existing restore/fork permission checks")
	dangerFullAccess := fs.Bool("enable-danger-full-access", false,
		"allow an existing full-access Run permission")
	limit := fs.Int("limit", 100, "bounded receipt or GC limit")
	jsonOutput := fs.Bool("json", false, "print the bounded JSON contract")
	if err := fs.Parse(reorderFlags(args[1:], map[string]bool{
		"run": true, "generation": true, "operation-key": true,
		"requested-by":            true,
		"confirm-workspace-trust": false, "expected-trust-digest": true,
		"confirm-observed-changes": false, "confirm": false, "confirm-full": false, "title": true,
		"target-checkpoint": true, "expected-current-checkpoint": true,
		"workspace-name": true, "workspace-root": true, "branch": true, "goal": true,
		"enable-permission-control": false, "enable-danger-full-access": false,
		"limit": true, "json": false,
	})); err != nil {
		return err
	}
	if fs.NArg() != 0 || *confirmFull && (!*confirm ||
		(action != "rewind" && action != "undo" && action != "fork")) {
		return errors.New(drydockOperationUsage(action))
	}
	if err := a.ensureStore(); err != nil {
		return err
	}
	root := filepath.Join(a.home, "drydocks")
	executor, err := repository.NewRunWorktreeExecutor(root)
	if err != nil {
		return err
	}
	service, err := application.NewRunWorktreeService(a.store, executor)
	if err != nil {
		return err
	}
	if action == "rewind" || action == "undo" || action == "fork" {
		capabilities := cliExecutionPermissionCapabilities(*permissionControl, *dangerFullAccess)
		release, activationErr := a.activateCLIInvocationFull(ctx, *runID, capabilities, *confirmFull)
		if activationErr != nil {
			return activationErr
		}
		defer release()
		checkpoints, checkpointErr := application.NewWorkspaceCheckpointService(a.store, capabilities)
		if checkpointErr != nil {
			return checkpointErr
		}
		service.WithCheckpointService(checkpoints)
	}
	actor := strings.TrimSpace(*requestedBy)
	run := strings.TrimSpace(*runID)
	key := strings.TrimSpace(*operationKey)

	var value any
	switch action {
	case "status":
		if run == "" || *limit < 1 {
			return errors.New(drydockOperationUsage(action))
		}
		value, err = service.Projection(ctx, run, *limit)
	case "create":
		if run == "" || key == "" || actor == "" ||
			*confirmTrust && strings.TrimSpace(*expectedTrust) == "" {
			return errors.New(drydockOperationUsage(action))
		}
		value, err = service.Create(ctx, application.DrydockCreateRequest{
			RunID: run, OperationKey: key, RequestedBy: actor,
			ConfirmWorkspaceTrust: *confirmTrust,
			ExpectedTrustDigest:   strings.TrimSpace(*expectedTrust),
		})
	case "use":
		if run == "" || key == "" || actor == "" || *generation < 1 {
			return errors.New(drydockOperationUsage(action))
		}
		value, err = service.Use(ctx, application.DrydockUseRequest{RunID: run,
			ExpectedGeneration: *generation, OperationKey: key, RequestedBy: actor})
	case "checkpoint":
		if run == "" || key == "" || actor == "" || *generation < 1 {
			return errors.New(drydockOperationUsage(action))
		}
		value, err = service.Checkpoint(ctx, application.DrydockCheckpointRequest{
			RunID: run, ExpectedGeneration: *generation, OperationKey: key,
			RequestedBy: actor, Title: strings.TrimSpace(*title),
			ConfirmObservedChanges: *confirmChanges})
	case "rewind":
		if run == "" || key == "" || actor == "" || *generation < 1 ||
			strings.TrimSpace(*targetCheckpoint) == "" {
			return errors.New(drydockOperationUsage(action))
		}
		value, err = service.Rewind(ctx, application.DrydockRewindRequest{
			RunID: run, TargetCheckpointID: strings.TrimSpace(*targetCheckpoint),
			ExpectedGeneration: *generation, OperationKey: key, RequestedBy: actor,
			Confirm: *confirm, ConfirmObservedChanges: *confirmChanges})
	case "undo":
		if run == "" || key == "" || actor == "" || *generation < 1 {
			return errors.New(drydockOperationUsage(action))
		}
		value, err = service.Undo(ctx, application.DrydockUndoRequest{
			RunID: run, ExpectedGeneration: *generation, OperationKey: key,
			RequestedBy: actor, Confirm: *confirm,
			ConfirmObservedChanges: *confirmChanges})
	case "fork":
		if run == "" || key == "" || actor == "" || *generation < 1 || !*confirm ||
			!*permissionControl || strings.TrimSpace(*targetCheckpoint) == "" ||
			strings.TrimSpace(*expectedCurrentCheckpoint) == "" ||
			strings.TrimSpace(*forkWorkspaceName) == "" ||
			strings.TrimSpace(*forkWorkspaceRoot) == "" ||
			strings.TrimSpace(*forkBranch) == "" {
			return errors.New(drydockOperationUsage(action))
		}
		value, err = service.Fork(ctx, application.DrydockForkRequest{RunID: run,
			TargetCheckpointID:          strings.TrimSpace(*targetCheckpoint),
			ExpectedCurrentCheckpointID: strings.TrimSpace(*expectedCurrentCheckpoint),
			ExpectedGeneration:          *generation, OperationKey: key, RequestedBy: actor,
			WorkspaceName: strings.TrimSpace(*forkWorkspaceName),
			WorkspaceRoot: strings.TrimSpace(*forkWorkspaceRoot),
			Branch:        strings.TrimSpace(*forkBranch), Goal: strings.TrimSpace(*forkGoal),
			Confirm: true})
	case "deliver":
		if run == "" || key == "" || actor == "" || *generation < 1 || !*confirm {
			return errors.New(drydockOperationUsage(action))
		}
		value, err = service.Deliver(ctx, application.DrydockDeliveryRequest{
			RunID: run, ExpectedGeneration: *generation, OperationKey: key,
			RequestedBy: actor, Confirm: true})
	case "cleanup":
		if run == "" || key == "" || actor == "" || *generation < 1 || !*confirm {
			return errors.New(drydockOperationUsage(action))
		}
		value, err = service.Cleanup(ctx, application.DrydockCleanupRequest{
			RunID: run, ExpectedGeneration: *generation, OperationKey: key,
			RequestedBy: actor, Confirm: true})
	case "reconcile":
		value, err = service.Reconcile(ctx)
	case "gc":
		if !*confirm || *limit < 1 {
			return errors.New(drydockOperationUsage(action))
		}
		value, err = service.GarbageCollect(ctx, *limit)
	}
	if err != nil {
		return err
	}
	return printDrydockCLIValue(a.out, value, *jsonOutput)
}

func printDrydockCLIValue(out interface{ Write([]byte) (int, error) }, value any,
	jsonOutput bool,
) error {
	if jsonOutput {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	}
	switch result := value.(type) {
	case application.DrydockCreateResult:
		fmt.Fprintf(out, "protocol: %s\nsource_workspace: %s\nsource_root: %s\nsource_root_sha256: %s\nsource_root_fingerprint: %s\nrepository: %s\ncommon_dir: %s\nsource_branch: %s\nbase_commit: %s\nsource_dirty_tracked: %t\nsource_dirty_untracked: %t\nsource_dirty_ignored: %t\ntrust_required: %t\ntrust_digest: %s\ngrants_process_authority: false\n",
			result.ProtocolVersion, result.Source.WorkspaceID, result.SourceRoot,
			result.Source.RootPathSHA256, result.Source.RootFingerprint,
			result.Source.RepositorySHA256, result.Source.CommonDirSHA256,
			result.Source.Branch, result.Source.BaseCommit,
			result.SourceState.DirtyTracked, result.SourceState.DirtyUntracked,
			result.SourceState.DirtyIgnored, result.TrustRequired, result.TrustDigest)
		if result.Workspace != nil {
			printDrydockWorkspace(out, *result.Workspace, result.Workspace.Path)
		}
		if result.Receipt != nil {
			fmt.Fprintf(out, "receipt_id: %s\n", result.Receipt.ID)
		}
		return nil
	case application.DrydockUseResult:
		printDrydockWorkspace(out, result.Workspace, result.RootPath)
		fmt.Fprintf(out, "binding_fingerprint: %s\nreceipt_id: %s\ngrants_process_authority: false\nreplayed: %t\n",
			result.BindingFingerprint, result.Receipt.ID, result.Replayed)
		return nil
	case application.DrydockCheckpointResult:
		printDrydockWorkspace(out, result.Workspace, "")
		fmt.Fprintf(out, "checkpoint_id: %s\nrecovery_level: %s\nentry_count: %d\nindex_sha256: %s\nreceipt_id: %s\nreplayed: %t\n",
			result.Checkpoint.ID, result.Checkpoint.RecoveryLevel,
			result.Checkpoint.EntryCount, result.Checkpoint.IndexSHA256,
			result.Receipt.ID, result.Replayed)
		return nil
	case application.DrydockRewindResult:
		printDrydockWorkspace(out, result.Workspace, "")
		fmt.Fprintf(out, "target_checkpoint_id: %s\nbefore_checkpoint_id: %s\nrecovery_level: %s\nindex_changed: %t\nchange_count: %d\nconflict_count: %d\nconfirmed: %t\nreplayed: %t\n",
			result.Target.ID, result.Before.ID, result.Preview.RecoveryLevel,
			result.Preview.IndexChanged, len(result.Preview.Changes),
			len(result.Preview.Conflicts), result.Confirmed, result.Replayed)
		if result.After != nil {
			fmt.Fprintf(out, "after_checkpoint_id: %s\n", result.After.ID)
		}
		if result.Receipt != nil {
			fmt.Fprintf(out, "receipt_id: %s\n", result.Receipt.ID)
		}
		return nil
	case application.DrydockForkResult:
		printDrydockWorkspace(out, result.Workspace, "")
		fmt.Fprintf(out, "fork_run_id: %s\nfork_workspace_id: %s\nfork_workspace_root: %s\nfork_branch: %s\nfork_checkpoint_id: %s\nreceipt_id: %s\nreplayed: %t\n",
			result.Fork.Run.ID, result.Fork.Workspace.ID, result.Fork.Workspace.RootPath,
			result.Fork.Checkpoint.Branch, result.Fork.Checkpoint.ID,
			result.Receipt.ID, result.Replayed)
		return nil
	case application.DrydockDeliveryResult:
		printDrydockWorkspace(out, result.Workspace, "")
		fmt.Fprintf(out, "delivery_id: %s\ndiff_sha256: %s\ndiff_bytes: %d\ndiff_stat: %s\nautomatic_merge: false\npush_authorized: false\nforce_authorized: false\nsource_overwrite_allowed: false\nreceipt_id: %s\npatch:\n%s",
			result.Review.Proposal.ID, result.Review.Proposal.DiffSHA256,
			result.Review.Proposal.DiffBytes, result.Review.Proposal.DiffStat,
			result.Receipt.ID, result.Review.Patch)
		return nil
	case application.DrydockCleanupResult:
		printDrydockWorkspace(out, result.Workspace, "")
		fmt.Fprintf(out, "receipt_id: %s\npreserved: %t\nreplayed: %t\n",
			result.Receipt.ID, result.Preserved, result.Replayed)
		return nil
	default:
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	}
}

func printDrydockWorkspace(out interface{ Write([]byte) (int, error) },
	workspace runworktree.Workspace, root string,
) {
	fmt.Fprintf(out, "drydock_id: %s\nworkspace_id: %s\nrun_id: %s\nstate: %s\ngeneration: %d\nbranch: %s\nbase_commit: %s\nroot_sha256: %s\nroot_fingerprint: %s\nexpected_head: %s\nexpected_binding_fingerprint: %s\nlast_checkpoint_id: %s\nlast_delivery_id: %s\nrecovery_reason: %s\nexpires_at: %s\n",
		workspace.ID, workspace.WorkspaceID, workspace.RunID, workspace.State,
		workspace.Generation, workspace.Branch, workspace.BaseCommit,
		workspace.PathSHA256, workspace.RootFingerprint, workspace.ExpectedHead,
		workspace.ExpectedBindingFingerprint, workspace.LastCheckpointID,
		workspace.LastDeliveryID, workspace.RecoveryReason,
		workspace.ExpiresAt.UTC().Format("2006-01-02T15:04:05.999999999Z"))
	if root != "" {
		fmt.Fprintf(out, "root: %s\n", root)
	}
}
