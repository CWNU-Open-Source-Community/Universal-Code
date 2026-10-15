package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/gitadvanced"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/runworktree"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/workspace"
	"cyberagent-workbench/internal/workspaceidentity"
)

const gitBisectDockerActor = "native-git-bisect"
const gitBisectDockerRequesterPrefix = gitBisectDockerActor + "/v1/"

func gitBisectExecutionWorkspace(ctx context.Context, owned runworktree.Workspace, scope runner.CommandRuntimeScope, spec runner.CommandRuntimeResolvedSpec) (runworktree.Workspace, error) {
	proof := gitBisectRuntimeFromContext(ctx)
	if proof == nil {
		return owned, nil
	}
	if batchValidationRuntimeFromContext(ctx) != nil || proof.runID != scope.RunID || proof.root != spec.WorkspaceRoot || proof.operationID == "" || proof.specFingerprint != runner.CommandRuntimeSpecFingerprint(spec) || proof.check == nil || proof.check(ctx) != nil {
		return runworktree.Workspace{}, runner.ErrCommandRuntimeBoundary
	}
	owned.Path, owned.RootFingerprint = proof.root, proof.rootIdentity
	return owned, nil
}

func gitBisectDockerWorkspace(ctx context.Context, binding sandbox.DockerStandardCodeRunnerBinding, owned sandbox.WorkspaceBinding) (sandbox.WorkspaceBinding, error) {
	proof := gitBisectRuntimeFromContext(ctx)
	if proof == nil {
		return owned, nil
	}
	if batchValidationRuntimeFromContext(ctx) != nil || proof.runID != binding.RunID || proof.operationID == "" || proof.commandSHA256 == "" || proof.commandSHA256 != binding.CommandSHA256 || proof.check == nil || proof.check(ctx) != nil {
		return sandbox.WorkspaceBinding{}, runner.ErrCommandRuntimeBoundary
	}
	owned.RootPath = proof.root
	return owned, nil
}

func gitBisectDockerRequester(proof *gitBisectRuntimeProof) string {
	return gitBisectDockerRequesterPrefix + proof.operationID + "/" + proof.rootIdentity
}

func isGitBisectDockerRequester(value string) bool {
	return value == gitBisectDockerActor || strings.HasPrefix(value, gitBisectDockerActor+"/")
}

func isNativeCommandDockerRequester(value string) bool {
	return isBatchValidationDockerRequester(value) || isGitBisectDockerRequester(value)
}

func parseGitBisectDockerRequester(value string) (string, string, error) {
	invalid := errors.New("Git bisect Docker cleanup reference is invalid")
	if !validDockerSandboxIdentity(value) || !strings.HasPrefix(value, gitBisectDockerRequesterPrefix) {
		return "", "", invalid
	}
	parts := strings.Split(strings.TrimPrefix(value, gitBisectDockerRequesterPrefix), "/")
	if len(parts) != 2 || len(parts[0]) == 0 || len(parts[0]) > 96 || !gitadvanced.ValidDigest(parts[1]) {
		return "", "", invalid
	}
	for _, r := range parts[0] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return "", "", invalid
		}
	}
	return parts[0], parts[1], nil
}

// A persisted requester only reconstructs the original mount for cleanup.
// Execution always requires the independent, live native Git proof.
func (s *DockerSandboxService) gitBisectCleanupWorkspace(ctx context.Context, admission domain.DockerSandboxAdmission, owned sandbox.WorkspaceBinding) (sandbox.WorkspaceBinding, error) {
	operationID, identity, err := parseGitBisectDockerRequester(admission.RequestedBy)
	if err != nil {
		return sandbox.WorkspaceBinding{}, err
	}
	store, ok := s.store.(interface {
		GetGitAdvancedOperation(context.Context, string) (gitadvanced.OperationRecord, bool, error)
		GetGitAdvancedSequence(context.Context, string) (gitadvanced.Sequence, bool, error)
	})
	if !ok {
		return sandbox.WorkspaceBinding{}, errors.New("Git bisect Docker cleanup ledger is unavailable")
	}
	record, found, err := store.GetGitAdvancedOperation(ctx, operationID)
	if err != nil || !found || record.RunID != admission.RunID || record.WorkspaceID != admission.WorkspaceID || record.Operation != gitadvanced.BisectRun {
		return sandbox.WorkspaceBinding{}, errors.New("Git bisect Docker cleanup operation changed")
	}
	var spec gitadvanced.Spec
	if err := json.Unmarshal([]byte(record.SpecJSON), &spec); err != nil || spec.Operation != gitadvanced.BisectRun || spec.SequenceID == "" {
		return sandbox.WorkspaceBinding{}, errors.New("Git bisect Docker cleanup specification changed")
	}
	sequence, found, err := store.GetGitAdvancedSequence(ctx, spec.SequenceID)
	if err != nil || !found || sequence.RunID != admission.RunID || sequence.WorkspaceID != admission.WorkspaceID || sequence.Kind != gitadvanced.SequenceBisect || sequence.RepositorySHA256 != record.RepositorySHA256 {
		return sandbox.WorkspaceBinding{}, errors.New("Git bisect Docker cleanup sequence changed")
	}
	source, err := s.store.GetSandboxWorkspace(ctx, record.WorkspaceID)
	if err != nil {
		return sandbox.WorkspaceBinding{}, err
	}
	actual, err := workspace.AgentCodeRootFingerprint(source.RootPath)
	if err != nil || actual != identity {
		return sandbox.WorkspaceBinding{}, errors.New("Git bisect Docker cleanup source directory changed")
	}
	owned.RootPath = source.RootPath
	return owned, nil
}

// This context is only installed after the immutable Docker cleanup ledger has
// resolved a source. It is never accepted by a dispatch-time binding request.
type gitBisectCleanupRootKey struct{}

func (s *RunWorktreeService) loadGitBisectDrydock(ctx context.Context, runID string, generation int64, root string) (runworktree.Workspace, repository.DrydockObservation, error) {
	binding, err := s.loadRunBinding(ctx, runID, true)
	if err != nil {
		return runworktree.Workspace{}, repository.DrydockObservation{}, err
	}
	owned, found, err := readRunFileDrydock(ctx, s.store, runID)
	if err != nil || !found || owned.Generation != generation || binding.workspace.RootPath != root || owned.Source.RootPath != root || owned.Source.WorkspaceID != binding.workspace.ID {
		return runworktree.Workspace{}, repository.DrydockObservation{}, runner.ErrCommandRuntimeBoundary
	}
	if err := requireCurrentRunFileDrydock(ctx, s.store, runID, owned); err != nil {
		return owned, repository.DrydockObservation{}, err
	}
	identity, err := workspaceidentity.Fingerprint(root)
	if err != nil || identity != owned.Source.RootFingerprint {
		return owned, repository.DrydockObservation{}, runner.ErrCommandRuntimeBoundary
	}
	source, err := s.executor.CaptureSourceRepositoryBinding(ctx, root)
	if err != nil || source.RepositorySHA256 != owned.Source.RepositorySHA256 || source.CommonDirSHA256 != owned.Source.CommonDirSHA256 || source.ObjectFormat != owned.Source.ObjectFormat {
		return owned, repository.DrydockObservation{}, runner.ErrCommandRuntimeBoundary
	}
	observed, err := s.executor.Inspect(ctx, root, gitadvanced.RepositoryBinding{CommonDirSHA256: owned.Source.CommonDirSHA256}, owned.Name)
	if err != nil {
		return owned, observed, err
	}
	if err := s.validateObservation(ctx, owned, observed); err != nil {
		return owned, observed, err
	}
	return owned, observed, nil
}

// Only a reaped natural process exit can classify a Git revision. Exit 125
// from a transport, permission, cancellation, or cleanup failure is uncertain.
func gitBisectDockerProcessExit(receipt *domain.DockerSandboxReceipt, executionErr error) bool {
	return executionErr == nil && receipt != nil && receipt.CleanupComplete &&
		receipt.Outcome == domain.DockerSandboxOutcomeFailed && receipt.ReasonCode == domain.DockerSandboxReasonProcessFailed &&
		receipt.ExitCode != nil && *receipt.ExitCode > 0 && *receipt.ExitCode < 128
}
