package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/sandbox"
)

// Historical Sandbox fixtures use the frozen v177 donor for the current
// lifecycle writers, but seed the quiescent candidate contract that existed
// before v131. Current databases keep the unmodified service and Store.
func newSandboxFixtureService(t testing.TB, state *SQLiteStore) *application.SandboxManifestService {
	t.Helper()
	version, err := state.SchemaVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if version == 177 {
		return application.NewSandboxManifestService(legacySandboxCandidateSeedStore{state}, policy.NewDefaultChecker())
	}
	return application.NewSandboxManifestService(state, policy.NewDefaultChecker())
}

type legacySandboxCandidateSeedStore struct{ *SQLiteStore }

func (s legacySandboxCandidateSeedStore) CreateSandboxExecutionCandidate(ctx context.Context,
	candidate sandbox.ExecutionCandidate, operation sandbox.CandidateOperation,
) (sandbox.ValidatedExecutionCandidate, bool, error) {
	if !candidate.LeaseQuiescent || candidate.RunLeaseID != "" ||
		candidate.RunLeaseGeneration != 0 || candidate.RunLeaseOwnerID != "" {
		return sandbox.ValidatedExecutionCandidate{}, false,
			errors.New("historical Sandbox candidate cannot discard an active Run lease")
	}
	candidate.ProtocolVersion = sandbox.ExecutionCandidateLegacyProtocolVersion
	operation.RequestFingerprint = sandbox.CandidateOperationRequestFingerprint(candidate)
	// Use the real writer so its immutable rows, operation binding and event
	// payload all describe the same v1 candidate. No constraints are relaxed.
	return s.SQLiteStore.CreateSandboxExecutionCandidate(ctx, candidate, operation)
}

func TestHistoricalSandboxCandidateSeedPreservesV1BindingsAcrossUpgrade(t *testing.T) {
	ctx := t.Context()
	donor, run, _ := openSandboxManifestStoreAt(t, ctx, filepath.Join(t.TempDir(), "seed.db"), 177)
	restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, donor)
	lifecycle := createSandboxLifecycleStoreFixture(t, ctx, donor, run.ID)
	var key string
	if err := donor.db.QueryRowContext(ctx, `SELECT operation_key_digest
		FROM sandbox_execution_candidate_operations WHERE candidate_id = ?`,
		lifecycle.Execution.CandidateID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	assertV1 := func(state *SQLiteStore) {
		t.Helper()
		stored, err := state.GetSandboxExecutionCandidate(ctx, lifecycle.Execution.CandidateID)
		if err != nil || stored.Candidate.ProtocolVersion != sandbox.ExecutionCandidateLegacyProtocolVersion {
			t.Fatalf("historical candidate=%#v err=%v", stored, err)
		}
		operation, found, err := state.GetSandboxExecutionCandidateOperation(ctx, key)
		if err != nil || !found || operation.RequestFingerprint != sandbox.CandidateRequestFingerprint(
			stored.Candidate.PreparationID, stored.Candidate.ManifestFingerprint,
			stored.Candidate.ApprovalID, stored.Candidate.RequestedBy) {
			t.Fatalf("historical candidate operation=%#v found=%t err=%v", operation, found, err)
		}
		if err := validateStoredSandboxExecutionCandidateBinding(stored.Candidate, operation); err != nil {
			t.Fatal(err)
		}
		if _, replayed, err := state.CreateSandboxExecutionCandidate(ctx, stored.Candidate, operation); err != nil || !replayed {
			t.Fatalf("historical candidate replay=%t err=%v", replayed, err)
		}
		timeline, err := state.ListRunEvents(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range timeline {
			if event.Type != events.SandboxExecutionCandidateValidatedEvent || event.SubjectID != stored.Candidate.ID {
				continue
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
				t.Fatal(err)
			}
			if payload["protocol"] != sandbox.ExecutionCandidateLegacyProtocolVersion ||
				payload["lease_quiescent"] != true || payload["run_lease_bound"] != false {
				t.Fatalf("candidate event disagrees with v1 binding: %#v", payload)
			}
			return
		}
		t.Fatal("historical candidate event is missing")
	}
	assertV1(donor)
	path := filepath.Join(t.TempDir(), "candidate-v50.db")
	restoreLegacyContext()
	historical := historicalTestDatabaseFromSeed(t, donor, path, 50)
	if err := historical.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	assertV1(upgraded)
}
