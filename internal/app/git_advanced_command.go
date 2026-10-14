package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/gitadvanced"
	"cyberagent-workbench/internal/repository"
)

const gitAdvancedCLIUsage = "usage: cyberagent git-advanced status|discover-hunks|preview|run [operation] --run <run-id> --enable-git-advanced --enable-permission-control [typed operation flags]"

func (a *App) gitAdvancedCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New(gitAdvancedCLIUsage)
	}
	action := strings.TrimSpace(args[0])
	var operation gitadvanced.Operation
	flagArgs := args[1:]
	if action != "status" {
		if len(args) < 2 {
			return errors.New(gitAdvancedCLIUsage)
		}
		operation = gitadvanced.Operation(strings.TrimSpace(args[1]))
		flagArgs = args[2:]
		if !operation.Valid() {
			return apperror.New(apperror.CodeInvalidArgument,
				"Git advanced operation is not in the closed git-advanced.v1 schema")
		}
	}
	if action != "status" && action != "discover-hunks" && action != "preview" && action != "run" {
		return errors.New(gitAdvancedCLIUsage)
	}

	fs := newFlagSet("git-advanced "+action, a.errOut)
	runID := fs.String("run", "", "exact active Run identity")
	operationKey := fs.String("operation-key", "", "stable mutation idempotency key")
	managedRoot := fs.String("managed-root", "", "product-managed worktree root")
	enable := fs.Bool("enable-git-advanced", false, "enable this process-local advanced Git capability")
	permissionControl := fs.Bool("enable-permission-control", false,
		"enable operator approval permission checks")
	dangerFullAccess := fs.Bool("enable-danger-full-access", false,
		"allow an existing full-access Run permission")
	confirmFull := fs.Bool("confirm-full", false,
		"activate the current Full preference only for this invocation")
	confirm := fs.Bool("confirm", false, "approve and execute the exact printed preview")
	jsonOutput := fs.Bool("json", false, "print the bounded JSON contract")
	message := fs.String("message", "", "stash audit message")
	includeUntracked := fs.Bool("include-untracked", false, "include untracked, never ignored, files")
	keepIndex := fs.Bool("keep-index", false, "retain staged changes after stash create")
	restoreIndex := fs.Bool("restore-index", false, "restore the stash index parent")
	stashOID := fs.String("stash", "", "exact stash object identity")
	sequenceID := fs.String("sequence", "", "durable sequence identity")
	upstreamOID := fs.String("upstream", "", "exact local rebase upstream commit")
	ontoOID := fs.String("onto", "", "exact local rebase target commit")
	goodCommit := fs.String("good", "", "exact known-good bisect commit")
	badCommit := fs.String("bad", "", "exact known-bad bisect commit")
	expectedCurrent := fs.String("expected-current", "", "exact reviewed bisect HEAD")
	recipe := fs.String("recipe", "", "go_test or npm_test")
	maxSteps := fs.Int("max-steps", 16, "bounded bisect recipe steps")
	timeoutSeconds := fs.Int("timeout-seconds", 300, "per-step bisect timeout")
	worktreeID := fs.String("worktree-id", "", "durable managed worktree identity")
	worktreeName := fs.String("worktree-name", "", "safe product-managed worktree name")
	branch := fs.String("branch", "", "new local worktree branch")
	lockReason := fs.String("lock-reason", "", "bounded worktree lock reason")
	limit := fs.Int("limit", 100, "maximum status records")
	var paths, hunkIDs, commits multiStringFlag
	fs.Var(&paths, "path", "Workspace-relative path (repeatable)")
	fs.Var(&hunkIDs, "hunk", "stable hunk SHA-256 identity (repeatable)")
	fs.Var(&commits, "commit", "exact commit identity (repeatable)")
	flagValues := map[string]bool{
		"run": true, "operation-key": true, "managed-root": true,
		"enable-git-advanced": false, "enable-permission-control": false,
		"enable-danger-full-access": false, "confirm": false, "confirm-full": false, "json": false, "message": true,
		"include-untracked": false, "keep-index": false, "restore-index": false,
		"stash": true, "sequence": true, "upstream": true, "onto": true,
		"good": true, "bad": true, "expected-current": true, "recipe": true,
		"max-steps": true, "timeout-seconds": true, "worktree-id": true,
		"worktree-name": true, "branch": true, "lock-reason": true,
		"limit": true, "path": true, "hunk": true, "commit": true,
	}
	if err := fs.Parse(reorderFlags(flagArgs, flagValues)); err != nil {
		return err
	}
	if fs.NArg() != 0 || strings.TrimSpace(*runID) == "" || !*enable ||
		!*permissionControl || action == "run" && strings.TrimSpace(*operationKey) == "" {
		return errors.New(gitAdvancedCLIUsage)
	}
	if action != "run" && *confirm || *confirmFull && (action != "run" || !*confirm) {
		return apperror.New(apperror.CodeInvalidArgument,
			"--confirm requires git-advanced run; --confirm-full also requires --confirm")
	}

	if err := a.ensureStore(); err != nil {
		return err
	}
	root := strings.TrimSpace(*managedRoot)
	if root == "" {
		root = filepath.Join(a.home, "worktrees")
	}
	executor, err := repository.NewAdvancedExecutor(root, true)
	if err != nil {
		return err
	}
	capabilities := cliExecutionPermissionCapabilities(*permissionControl,
		*dangerFullAccess)
	checkpoints, err := application.NewWorkspaceCheckpointService(a.store, capabilities)
	if err != nil {
		return err
	}
	service, err := application.NewGitAdvancedService(a.store, executor, capabilities,
		checkpoints)
	if err != nil {
		return err
	}
	if action == "status" {
		projection, err := service.Projection(ctx, *runID, *limit)
		if err != nil {
			return err
		}
		return printGitAdvancedValue(a.out, projection, true)
	}

	spec := gitadvanced.Spec{ProtocolVersion: gitadvanced.ProtocolVersion,
		Operation: operation, Paths: paths.values, HunkIDs: hunkIDs.values,
		Message: *message, IncludeUntracked: *includeUntracked, KeepIndex: *keepIndex,
		RestoreIndex: *restoreIndex, StashOID: strings.TrimSpace(*stashOID),
		SequenceID: strings.TrimSpace(*sequenceID), UpstreamOID: strings.TrimSpace(*upstreamOID),
		OntoOID: strings.TrimSpace(*ontoOID), Commits: commits.values,
		GoodCommit: strings.TrimSpace(*goodCommit), BadCommit: strings.TrimSpace(*badCommit),
		ExpectedCurrent: strings.TrimSpace(*expectedCurrent), WorktreeID: strings.TrimSpace(*worktreeID),
		WorktreeName: strings.TrimSpace(*worktreeName), Branch: strings.TrimSpace(*branch),
		LockReason: *lockReason}
	if operation == gitadvanced.WorktreeCreate {
		if len(spec.Commits) == 1 {
			spec.Commit, spec.Commits = spec.Commits[0], nil
		}
	}
	if strings.TrimSpace(*recipe) != "" {
		spec.Recipe = &gitadvanced.BisectRecipe{Name: gitadvanced.RecipeName(*recipe),
			MaxSteps: *maxSteps, TimeoutSeconds: *timeoutSeconds}
	}
	if err := spec.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"Git advanced typed operation is invalid", err)
	}
	if action == "run" && *confirm {
		release, activationErr := a.activateCLIInvocationFull(ctx, *runID, capabilities, *confirmFull)
		if activationErr != nil {
			return activationErr
		}
		defer release()
	}
	var inspected application.GitAdvancedReviewResult
	if action == "discover-hunks" {
		inspected, err = service.DiscoverHunks(ctx, *runID, spec)
	} else {
		inspected, err = service.Preview(ctx, *runID, spec)
	}
	if err != nil {
		return err
	}
	if action != "run" || !*confirm {
		if *jsonOutput {
			return printGitAdvancedValue(a.out, inspected, true)
		}
		printGitAdvancedPreview(a.out, inspected)
		if action == "run" {
			fmt.Fprintln(a.out, "review_only: true (re-run with --confirm to create a one-time Approval and execute)")
		}
		return nil
	}
	if !inspected.Preview.Executable() {
		printGitAdvancedPreview(a.out, inspected)
		return apperror.New(apperror.CodeFailedPrecondition,
			"Git advanced preview is blocked")
	}
	lease, found, err := a.store.GetRunExecutionLease(ctx, strings.TrimSpace(*runID))
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeFailedPrecondition,
				"Git advanced run requires an active Workspace lease")
		}
		return err
	}
	scope := application.GitAdvancedScope{
		CapabilityGeneration: executor.Capability().Generation,
		LeaseID:              lease.LeaseID, LeaseGeneration: lease.Generation}
	reviewed, err := service.Review(ctx, application.GitAdvancedReviewRequest{
		ProtocolVersion: application.GitAdvancedAPIProtocolVersion,
		RunID:           *runID, OperationKey: *operationKey, RequestedBy: "cli_operator",
		Scope: scope, Spec: spec})
	if err != nil {
		return err
	}
	if reviewed.Preview.ID != inspected.Preview.ID || reviewed.Operation == nil ||
		reviewed.Approval == nil {
		return apperror.New(apperror.CodeConflict,
			"Git advanced repository changed after the displayed preview")
	}
	decision, err := a.store.DecideApproval(ctx, approval.DecisionRequest{
		ProposalID:     reviewed.Operation.ID,
		IdempotencyKey: strings.TrimSpace(*operationKey) + "-cli-approval",
		Action:         approval.ActionApprove, ReviewedBy: "cli_operator",
	})
	if err != nil {
		return err
	}
	executed, err := service.Execute(ctx, application.GitAdvancedExecuteRequest{
		ProtocolVersion: application.GitAdvancedAPIProtocolVersion,
		RunID:           *runID, OperationID: reviewed.Operation.ID,
		ApprovalID: decision.Approval.ID, RequestedBy: "cli_operator", Scope: scope})
	if *jsonOutput {
		value := struct {
			Preview application.GitAdvancedReviewResult  `json:"preview"`
			Result  application.GitAdvancedExecuteResult `json:"result"`
		}{Preview: reviewed, Result: executed}
		if printErr := printGitAdvancedValue(a.out, value, true); printErr != nil && err == nil {
			err = printErr
		}
	} else {
		printGitAdvancedPreview(a.out, reviewed)
		fmt.Fprintf(a.out, "operation_id: %s\napproval_id: %s\nreceipt_id: %s\nstatus: %s\ncheckpoint_id: %s\nsequence_id: %s\nworktree_id: %s\nreplayed: %t\n",
			executed.Operation.ID, decision.Approval.ID, executed.Receipt.ID,
			executed.Receipt.Status, executed.Receipt.CheckpointID,
			executed.Receipt.SequenceID, executed.Receipt.WorktreeID, executed.Replayed)
	}
	return err
}

func printGitAdvancedPreview(out interface{ Write([]byte) (int, error) },
	value application.GitAdvancedReviewResult,
) {
	p := value.Preview
	fmt.Fprintf(out, "run: %s\nworkspace: %s\nprotocol: %s\noperation: %s\npreview_id: %s\ncapability_generation: %s\nrepository: %s\nhead: %s\nbranch: %s\nindex_sha256: %s\nworktree_sha256: %s\nupstream_ref: %s\nupstream_oid: %s\nsummary: %s\ncheckpoint_required: %t\nrecovery_action: %s\n",
		value.RunID, value.WorkspaceID, p.ProtocolVersion, p.Operation, p.ID,
		p.Capability.Generation, p.Binding.RepositorySHA256, p.Binding.Head,
		p.Binding.Branch, p.Binding.IndexSHA256, p.Binding.WorktreeSHA256,
		p.Binding.UpstreamRef, p.Binding.UpstreamOID, p.Summary,
		p.Recovery.Required, p.Recovery.RestoreAction)
	for _, reason := range p.BlockedReasons {
		fmt.Fprintf(out, "blocked: %s\n", reason)
	}
	for _, file := range p.Files {
		fmt.Fprintf(out, "file: %s\t%s\t%s -> %s\tdestructive=%t\n", file.Path,
			file.Change, file.BeforeSHA256, file.AfterSHA256, file.Destructive)
	}
	for _, hunk := range p.Hunks {
		fmt.Fprintf(out, "hunk: %s\t%s\t-%d,%d +%d,%d\n%s", hunk.ID, hunk.Path,
			hunk.OldStart, hunk.OldLines, hunk.NewStart, hunk.NewLines, hunk.Patch)
	}
	for _, file := range p.Conflict.Files {
		fmt.Fprintf(out, "conflict: %s\tbase=%s\tours=%s\ttheirs=%s\n",
			file.Path, file.BaseOID, file.OursOID, file.TheirsOID)
	}
}

func printGitAdvancedValue(out interface{ Write([]byte) (int, error) }, value any,
	jsonOutput bool,
) error {
	if !jsonOutput {
		return errors.New("internal Git advanced output mode is invalid")
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
