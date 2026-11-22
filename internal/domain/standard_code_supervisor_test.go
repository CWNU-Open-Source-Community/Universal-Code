package domain

import (
	"strings"
	"testing"
	"time"
)

func TestStandardCodeSupervisorSnapshotRequiresInspectionAndCurrentVerification(t *testing.T) {
	now := time.Now().UTC()
	snapshot := StandardCodeSupervisorSnapshot{
		ProtocolVersion: StandardCodeSupervisorProtocolVersion,
		RunID:           "run-standard-code", MissionID: "mission-standard-code",
		WorkspaceID: "workspace-standard-code", RootAgentID: "root-standard-code",
		PresetOperationKeyDigest: strings.Repeat("a", 64),
		State:                    StandardCodeSupervisorInspect,
		ModeSnapshotID:           "mode-standard-code", ModeRevision: 1,
		ProfileSnapshotID: "profile-standard-code", ProfileRevision: 1,
		InteractionSnapshotID: "interaction-standard-code", InteractionRevision: 1,
		PermissionSnapshotID: "permission-standard-code", PermissionRevision: 1,
		BrowserCDPSnapshotID: "browser-cdp-standard-code", BrowserCDPRevision: 1,
		WorkspaceRootFingerprint: strings.Repeat("f", 64),
		CapabilityGeneration:     strings.Repeat("b", 64),
		Turn:                     1, AttemptID: "attempt-standard-code",
		Limits: DefaultStandardCodeSupervisorLimits(), Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	snapshot.InspectionComplete = true
	if err := snapshot.Validate(); err == nil {
		t.Fatal("inspection completion without consecutive read proof was accepted")
	}
	snapshot.ConsecutiveReadRounds = StandardCodeSupervisorMinimumReadRounds
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	snapshot.State = StandardCodeSupervisorDeliver
	if err := snapshot.Validate(); err == nil || snapshot.CanDeliver() {
		t.Fatal("Deliver without an applied and verified mutation was accepted")
	}
	snapshot.MutationEpoch = 2
	snapshot.VerifiedMutationEpoch = 1
	if err := snapshot.Validate(); err == nil || snapshot.CanDeliver() {
		t.Fatal("stale verification was accepted for a newer mutation")
	}
	snapshot.VerifiedMutationEpoch = 2
	if err := snapshot.Validate(); err == nil || snapshot.CanDeliver() {
		t.Fatal("current mutation epoch without verification Job evidence was accepted")
	}
	snapshot.VerificationJobIDs = []string{"verification-job-standard-code"}
	if err := snapshot.Validate(); err != nil || !snapshot.CanDeliver() {
		t.Fatalf("current structural verification was rejected: %v", err)
	}
}

func TestStandardCodeSupervisorLimitsCannotBeWidened(t *testing.T) {
	limits := DefaultStandardCodeSupervisorLimits()
	if limits.MaximumToolRounds != MaxSupervisorExecutionRounds {
		t.Fatalf("expected MaximumToolRounds=%d, got %d", MaxSupervisorExecutionRounds, limits.MaximumToolRounds)
	}
	if err := limits.Validate(); err != nil {
		t.Fatal(err)
	}
	legacy := limits
	legacy.MaximumToolRounds = 4
	if err := legacy.Validate(); err != nil {
		t.Fatalf("legacy 4-round Standard Code ledger limit was rejected: %v", err)
	}
	for _, invalidRounds := range []int{3, 5, MaxSupervisorExecutionRounds + 1} {
		bad := limits
		bad.MaximumToolRounds = invalidRounds
		if err := bad.Validate(); err == nil {
			t.Fatalf("invalid MaximumToolRounds=%d was accepted", invalidRounds)
		}
	}
	limits.MaximumCommands++
	if err := limits.Validate(); err == nil {
		t.Fatal("widened Standard Code command budget was accepted")
	}
}

func TestStandardCodeSupervisorJobOwnershipIsExactAndBounded(t *testing.T) {
	job := StandardCodeSupervisorJob{JobID: "command-job-standard-code",
		PermissionRevision: 3, MutationEpoch: 2, LastCursor: 19, State: "running"}
	if err := job.Validate(); err != nil {
		t.Fatal(err)
	}
	job.PermissionRevision = 0
	if err := job.Validate(); err == nil {
		t.Fatal("background Job without a permission revision was accepted")
	}
	job.PermissionRevision = 3
	job.State = "model_claimed_success"
	if err := job.Validate(); err == nil {
		t.Fatal("background Job with a non-runtime state was accepted")
	}
}

func TestStandardCodeSupervisorCodeIntelCallIsDurableAndAuthorityBound(t *testing.T) {
	call := SupervisorToolCall{RunID: "run-code-intel", Turn: 1,
		AttemptID: "attempt-code-intel", Round: 1, Position: 1, ModelAttempt: 1,
		CallID: "call-code-intel", ToolName: "code_workspace_symbols",
		PayloadJSON: `{"version":"code-intel-lsp.v1"}`, AuthorityJSON: `{}`,
		Status: SupervisorToolPending, CreatedAt: time.Now().UTC()}
	if err := call.Validate(); err != nil {
		t.Fatalf("Code Intel read could not enter the durable Supervisor ledger: %v", err)
	}
	call.AuthorityJSON = ""
	if err := call.Validate(); err == nil {
		t.Fatal("Code Intel read without Go-issued authority was accepted")
	}
}
