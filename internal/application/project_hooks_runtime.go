package application

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

type projectHookCallStore interface {
	GetProjectHookCall(context.Context, string, string) (domain.SupervisorToolCall, bool, error)
	RecordProjectHookCall(context.Context, domain.SupervisorCheckpoint, llm.ToolCall, domain.ProjectHookCallOrigin) error
	ListRunFileEditPreviewsPage(context.Context, string, int, int) ([]fileedit.Preview, error)
	RunFileWorkspaceStore
}

func (s *AgentRunner) projectHookSelection(ctx context.Context, runID string) (ProjectHookSelection, error) {
	if s.projectHooks == nil {
		return ProjectHookSelection{}, nil
	}
	return s.projectHooks.AuthorizeProjectHooks(ctx, runID)
}

func (s *AgentRunner) checkPendingProjectHook(ctx context.Context, call domain.SupervisorToolCall) (disabled bool, err error) {
	if call.ProjectHook == nil {
		return false, nil
	}
	if s.projectHooks == nil {
		return false, apperror.New(apperror.CodeFailedPrecondition, "project hook authorization is unavailable")
	}
	selection, err := s.projectHookSelection(ctx, call.RunID)
	if err != nil {
		return false, err
	}
	if selection.Fingerprint == "" && !selection.AfterEditFormat && !selection.BeforeFinishTest {
		reader, ok := s.store.(interface {
			GetSupervisorApprovalCall(context.Context, string, string) (domain.SupervisorToolCall, bool, error)
		})
		if !ok {
			return false, apperror.New(apperror.CodeFailedPrecondition, "project hook dispatch observation is unavailable")
		}
		_, started, err := reader.GetSupervisorApprovalCall(ctx, call.RunID, call.CallID)
		if err != nil {
			return false, err
		}
		if started {
			return false, apperror.New(apperror.CodeFailedPrecondition, "project hook was dispatched without a completed receipt; inspect its existing command job before continuing")
		}
		return true, nil
	}
	if selection.Fingerprint != call.ProjectHook.ConfigFingerprint ||
		(call.ProjectHook.Slot == "format" && !selection.AfterEditFormat) ||
		(call.ProjectHook.Slot == "test" && !selection.BeforeFinishTest) {
		return false, apperror.New(apperror.CodeFailedPrecondition, "queued project hook configuration is no longer trusted")
	}
	return false, nil
}

// A host hook uses the ordinary durable call, budget, approval and job path.
// This wrapper drains one new format intent at a time, without recursively
// treating command writes as FileEdits or asking the model to emit a tool call.
func (s *AgentRunner) resumeSupervisorTools(ctx context.Context, turn domain.SupervisorTurn,
	rounds []domain.SupervisorToolRound, standardCode ...*standardCodeSupervisorTurn,
) ([]domain.SupervisorToolRound, bool, error) {
	for {
		var waiting bool
		var err error
		rounds, waiting, err = s.resumeSupervisorToolsRaw(ctx, turn, rounds, standardCode...)
		if err != nil || waiting || len(rounds) == 0 {
			// A new turn has no accepted model boundary yet. Pre-existing manual
			// edits are checked at its next tool or finish boundary instead.
			return rounds, waiting, err
		}
		// A pin proposal ends tool execution until the user has answered.
		// Host hooks share this boundary with model-requested tools.
		if contextPinConfirmationBoundary(rounds) {
			return rounds, false, nil
		}
		added, err := s.prepareProjectFormat(ctx, turn, rounds, false)
		if err != nil || !added {
			return rounds, false, err
		}
		rounds, err = s.store.ListSupervisorToolRounds(ctx, turn.Checkpoint)
		if err != nil {
			return rounds, false, err
		}
	}
}

func (s *AgentRunner) prepareProjectFormat(ctx context.Context, turn domain.SupervisorTurn,
	rounds []domain.SupervisorToolRound, finishing bool,
) (bool, error) {
	selection, err := s.projectHookSelection(ctx, turn.Run.ID)
	if err != nil || !selection.AfterEditFormat {
		return false, err
	}
	store, ok := s.store.(projectHookCallStore)
	if !ok {
		return false, apperror.New(apperror.CodeFailedPrecondition, "project hook execution store is unavailable")
	}
	seen := map[string]bool{}
	for offset := 0; ; offset += 100 {
		edits, err := store.ListRunFileEditPreviewsPage(ctx, turn.Run.ID, offset, 100)
		if err != nil {
			return false, err
		}
		for _, edit := range edits {
			if edit.Status != fileedit.StatusApplied {
				continue
			}
			target := edit.Path
			if edit.Operation == fileedit.OperationMove {
				target = edit.DestinationPath
				// A move supersedes both the old source and destination histories.
				seen[edit.Path] = true
			}
			if seen[target] {
				continue
			}
			seen[target] = true
			if edit.Operation == fileedit.OperationDelete || filepath.Ext(target) != ".go" {
				continue
			}
			key := runmutation.Fingerprint("project-hook-format.v1", selection.Fingerprint, edit.ID, target)
			previous, found, err := store.GetProjectHookCall(ctx, turn.Run.ID, key)
			if err != nil {
				return false, err
			}
			if found {
				if finishing && !projectHookCommandPassed(previous) {
					return false, apperror.New(apperror.CodeFailedPrecondition, "project formatting has not passed; repair the file before finishing")
				}
				continue
			}
			version := runmutation.Fingerprint("project-hook-file-version.v1", edit.ID, edit.ProposedHash, edit.DestinationProposedHash)
			return true, s.recordProjectHook(ctx, turn, rounds, domain.ProjectHookCallOrigin{TriggerKey: key, Slot: "format", Target: target, ConfigFingerprint: selection.Fingerprint, WorkspaceVersion: version})
		}
		if len(edits) < 100 {
			return false, nil
		}
	}
}

func (s *AgentRunner) recordProjectHook(ctx context.Context, turn domain.SupervisorTurn,
	rounds []domain.SupervisorToolRound, origin domain.ProjectHookCallOrigin,
	wait ...toolgateway.RunTestsInput,
) error {
	if len(rounds) >= domain.MaxSupervisorExecutionRounds {
		return apperror.New(apperror.CodeResourceExhausted, "project hook requires a remaining tool round before completion")
	}
	permission, err := s.store.GetRunExecutionPermission(ctx, turn.Run.ID)
	if err != nil {
		return err
	}
	runtime, err := s.supervisorCommandRuntimeTools(ctx, turn.Run.ID, permission.Mode)
	if err != nil {
		return err
	}
	if !runtime.Adapter.Executable() || len(runtime.Authority) == 0 {
		return apperror.New(apperror.CodeFailedPrecondition, "project hook requires the active command runtime and execution permission")
	}
	store, ok := s.store.(projectHookCallStore)
	if !ok {
		return apperror.New(apperror.CodeFailedPrecondition, "project hook execution store is unavailable")
	}
	name := toolgateway.RunTestsTool
	var raw []byte
	if origin.Slot == "test" {
		input := toolgateway.RunTestsInput{Version: toolgateway.RunTestsVersion, Action: toolgateway.CommandRuntimeActionStart,
			Target: ".", TimeoutMilliseconds: runner.MaxCommandRuntimeTimeout.Milliseconds()}
		if len(wait) > 0 {
			input = wait[0]
		}
		raw, err = json.Marshal(input)
	} else {
		files, e := ResolveRunFileWorkspace(ctx, store, turn.Run, turn.Mission, s.drydocks)
		if e != nil {
			return e
		}
		spec, e := PrepareProjectCommandForAdapter(turn.Run, "format", origin.Target, runtime.Adapter, files.Workspace.RootPath)
		if e != nil {
			return e
		}
		spec.TimeoutMilliseconds = toolgateway.MaxCommandRuntimeForegroundMillis
		spec.Output.InlineBytes = runner.MinCommandRuntimeInlineBytes
		maxBytes := toolgateway.MaxCommandRuntimePageBytes
		name = toolgateway.CommandRuntimeTool
		raw, err = json.Marshal(toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion, Action: toolgateway.CommandRuntimeActionRun, Commands: []runner.CommandRuntimeSpec{spec}, FailurePolicy: toolgateway.CommandRuntimeFailFast, MaxBytes: &maxBytes})
	}
	if err != nil {
		return err
	}
	raw, err = toolgateway.NormalizeSupervisorToolPayload(name, raw)
	if err != nil {
		return err
	}
	key := runmutation.SupervisorToolOperationKey(turn.Run.ID, turn.Checkpoint.NextTurn, string(name), string(raw))
	callID, err := runmutation.SupervisorToolCallID(key, len(rounds)+1)
	if err != nil {
		return err
	}
	calls, err := s.bindCommandRuntimeCalls(ctx, []llm.ToolCall{{ID: callID, Name: string(name), Arguments: raw, Authority: runtime.Authority}})
	if err != nil {
		return err
	}
	return store.RecordProjectHookCall(ctx, turn.Checkpoint, calls[0], origin)
}

// Returns added when a real model finish was held for hook evidence. The caller
// rebuilds its request and lets the model inspect that evidence before replying.
func (s *AgentRunner) beforeProjectHookFinish(ctx context.Context, turn domain.SupervisorTurn,
	rounds []domain.SupervisorToolRound, standardCode *standardCodeSupervisorTurn,
	waitAfterStart ...bool,
) ([]domain.SupervisorToolRound, bool, bool, error) {
	startedVerification := false
	added, err := s.prepareProjectFormat(ctx, turn, rounds, true)
	if err != nil {
		return rounds, false, false, err
	}
	if !added {
		selection, err := s.projectHookSelection(ctx, turn.Run.ID)
		if err != nil || !selection.BeforeFinishTest {
			return rounds, false, false, err
		}
		version, err := s.projectHookWorkspaceVersion(ctx, turn)
		if err != nil {
			return rounds, false, false, err
		}
		store, ok := s.store.(projectHookCallStore)
		if !ok {
			return rounds, false, false, apperror.New(apperror.CodeFailedPrecondition, "project hook execution store is unavailable")
		}
		permission, err := s.store.GetRunExecutionPermission(ctx, turn.Run.ID)
		if err != nil {
			return rounds, false, false, err
		}
		runtime, err := s.supervisorCommandRuntimeTools(ctx, turn.Run.ID, permission.Mode)
		if err != nil {
			return rounds, false, false, err
		}
		a, err := commandruntimeadapter.DecodeAuthority(runtime.Authority)
		if err != nil {
			return rounds, false, false, err
		}
		key := runmutation.Fingerprint("project-hook-test.v2", selection.Fingerprint, version, string(runtime.Authority))
		reader, ok := s.commandRuntime.(interface {
			LatestProjectVerificationReceipt(context.Context, commandruntimeadapter.Authority, string) (toolgateway.ProjectVerificationReceipt, bool, error)
		})
		if !ok {
			return rounds, false, false, apperror.New(apperror.CodeFailedPrecondition, "project verification receipt reader is unavailable")
		}
		receipt, found, err := reader.LatestProjectVerificationReceipt(ctx, a, ".")
		if err != nil {
			return rounds, false, false, err
		}
		origin := domain.ProjectHookCallOrigin{TriggerKey: key, Slot: "test", Target: ".", ConfigFingerprint: selection.Fingerprint, WorkspaceVersion: version}
		var observation []toolgateway.RunTestsInput
		if found {
			if receipt.Reusable && receipt.State == "passed" {
				return rounds, false, false, standardCode.reuseProjectVerificationReceipt(ctx, receipt)
			}
			terminalNeedsObservation := receipt.State == "failed" && receipt.JobRecordVersion > 0 && receipt.CompletedSourceVersion == ""
			if receipt.State != "pending" && !terminalNeedsObservation {
				return rounds, false, false, apperror.New(apperror.CodeFailedPrecondition, "project verification has not passed for this source version; repair it before finishing")
			}
			cursor, maxBytes, waitMillis := receipt.Cursor, toolgateway.MaxCommandRuntimePageBytes, int(runner.MaxCommandRuntimeWait.Milliseconds())
			origin.TriggerKey = runmutation.Fingerprint("project-hook-test-wait.v1", key, receipt.JobID, turn.Checkpoint.AttemptID, fmt.Sprint(turn.Checkpoint.NextTurn), fmt.Sprint(len(rounds)))
			observation = []toolgateway.RunTestsInput{{Version: toolgateway.RunTestsVersion, Action: toolgateway.CommandRuntimeActionWait, JobID: receipt.JobID, Cursor: &cursor, MaxBytes: &maxBytes, WaitMilliseconds: &waitMillis}}
		} else if _, found, err := store.GetProjectHookCall(ctx, turn.Run.ID, key); err != nil || found {
			if err != nil {
				return rounds, false, false, err
			}
			return rounds, false, false, apperror.New(apperror.CodeFailedPrecondition, "project verification has an unresolved dispatch; inspect the original command Job before continuing")
		}
		startedVerification = len(observation) == 0
		err = s.recordProjectHook(ctx, turn, rounds, origin, observation...)
		if err != nil {
			return rounds, false, false, err
		}
	}
	rounds, err = s.store.ListSupervisorToolRounds(ctx, turn.Checkpoint)
	if err != nil {
		return rounds, false, false, err
	}
	rounds, waiting, err := s.resumeSupervisorTools(ctx, turn, rounds, standardCode)
	// A short first observation avoids another model request for ordinary fast
	// tests. Longer tests retain their original Job and continue through wait.
	if err == nil && !waiting && startedVerification && (len(waitAfterStart) == 0 || waitAfterStart[0]) {
		observed, _, waiting, err := s.beforeProjectHookFinish(ctx, turn, rounds, standardCode, false)
		return observed, true, waiting, err
	}
	return rounds, true, waiting, err
}

func projectHookCommandPassed(call domain.SupervisorToolCall) bool {
	if call.Status != domain.SupervisorToolCompleted {
		return false
	}
	var envelope supervisorToolResultEnvelope
	var projection standardCodeCommandProjection
	if json.Unmarshal([]byte(call.ResultJSON), &envelope) != nil || json.Unmarshal([]byte(envelope.Stdout), &projection) != nil || len(projection.Jobs) != 1 {
		return false
	}
	if projection.VerificationReceipt != nil && (!projection.VerificationReceipt.Reusable || projection.VerificationReceipt.State != "passed") {
		return false
	}
	// Host isolation limitations are retained as evidence; they do not change
	// a completed process exit status. Other incomplete receipts fail closed.
	for _, reason := range projection.IncompleteReasons {
		if reason != "host_unsandboxed cannot prove host credentials are unavailable to the child process" && reason != "host_unsandboxed cannot enforce the command network=disabled intent" {
			return false
		}
	}
	job := projection.Jobs[0]
	return job.State == runner.CommandRuntimeJobCompleted && job.ExitCode != nil && *job.ExitCode == 0 && job.TreeReaped
}

func (s *AgentRunner) projectHookWorkspaceVersion(ctx context.Context, turn domain.SupervisorTurn) (string, error) {
	store, ok := s.store.(RunFileWorkspaceStore)
	if !ok {
		return "", apperror.New(apperror.CodeFailedPrecondition, "project hook workspace is unavailable")
	}
	files, err := ResolveRunFileWorkspace(ctx, store, turn.Run, turn.Mission, s.drydocks)
	if err != nil {
		return "", err
	}
	return projectVerificationWorkspaceVersion(ctx, turn.Run, turn.Mission, files.Workspace.ID, files.Workspace.RootPath)
}

func projectHookSnapshotVersion(snapshot workspacecheckpoint.Snapshot) (string, error) {
	for _, reason := range snapshot.Checkpoint.IncompleteReasons {
		switch reason {
		case "workspace manifest exceeds the entry limit", "case-colliding Workspace paths cannot be restored safely", "Workspace path casing differs from the Git index":
			return "", apperror.New(apperror.CodeFailedPrecondition, "project verification cannot identify the complete workspace: "+reason)
		}
	}
	parts := []string{"project-hook-workspace.v1", snapshot.Checkpoint.RootFingerprint, snapshot.Checkpoint.BaseCommit}
	for _, entry := range snapshot.Entries {
		if !entry.Tracked && (entry.State == workspacecheckpoint.StateIgnored || entry.State == workspacecheckpoint.StateGenerated) {
			continue
		}
		if entry.StoragePolicy == workspacecheckpoint.StorageUnreadable || entry.StoragePolicy == workspacecheckpoint.StorageExcludedLink || entry.StoragePolicy == workspacecheckpoint.StorageExcludedSpecial || entry.WorktreeSHA256 == "" || (entry.WorktreeSHA256 == "missing" && entry.State != workspacecheckpoint.StateMissing) {
			return "", apperror.New(apperror.CodeFailedPrecondition, "project verification cannot identify file version: "+entry.Path)
		}
		parts = append(parts, entry.Path, entry.WorktreeSHA256, fmt.Sprint(entry.Mode))
	}
	return runmutation.Fingerprint(parts...), nil
}

func supervisorNativeToolRounds(rounds []domain.SupervisorToolRound) int {
	count := 0
	for _, round := range rounds {
		if !round.ProjectHook() {
			count++
		}
	}
	return count
}
