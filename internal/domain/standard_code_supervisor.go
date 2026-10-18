package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const StandardCodeSupervisorProtocolVersion = "standard_code_supervisor.v1"

const (
	StandardCodeSupervisorMinimumReadRounds             = 2
	StandardCodeSupervisorMaximumToolRounds             = 4
	StandardCodeSupervisorMaximumCommands               = 12
	StandardCodeSupervisorMaximumJobs                   = 2
	StandardCodeSupervisorMaximumFixRounds              = 3
	StandardCodeSupervisorMaximumOutputBytes      int64 = 1024 * 1024
	StandardCodeSupervisorMaximumNoProgress             = 3
	StandardCodeSupervisorMaximumRepeatedFailures       = 3
	StandardCodeSupervisorMaximumLedgerEntries          = 512
	StandardCodeSupervisorMaximumSnapshotBytes          = 64 * 1024
)

type StandardCodeSupervisorState string

const (
	StandardCodeSupervisorInspect    StandardCodeSupervisorState = "inspect"
	StandardCodeSupervisorPlan       StandardCodeSupervisorState = "plan"
	StandardCodeSupervisorCheckpoint StandardCodeSupervisorState = "checkpoint"
	StandardCodeSupervisorEdit       StandardCodeSupervisorState = "edit"
	StandardCodeSupervisorExecute    StandardCodeSupervisorState = "execute"
	StandardCodeSupervisorObserve    StandardCodeSupervisorState = "observe"
	StandardCodeSupervisorDiagnose   StandardCodeSupervisorState = "diagnose"
	StandardCodeSupervisorDeliver    StandardCodeSupervisorState = "deliver"
	StandardCodeSupervisorStopped    StandardCodeSupervisorState = "stopped"
)

func (s StandardCodeSupervisorState) Valid() bool {
	switch s {
	case StandardCodeSupervisorInspect, StandardCodeSupervisorPlan,
		StandardCodeSupervisorCheckpoint, StandardCodeSupervisorEdit,
		StandardCodeSupervisorExecute, StandardCodeSupervisorObserve,
		StandardCodeSupervisorDiagnose, StandardCodeSupervisorDeliver,
		StandardCodeSupervisorStopped:
		return true
	default:
		return false
	}
}

type StandardCodeSupervisorLedgerKind string

const (
	StandardCodeSupervisorInitialized    StandardCodeSupervisorLedgerKind = "initialized"
	StandardCodeSupervisorTurnPrepared   StandardCodeSupervisorLedgerKind = "turn_prepared"
	StandardCodeSupervisorPhaseAdvanced  StandardCodeSupervisorLedgerKind = "phase_advanced"
	StandardCodeSupervisorCallAuthorized StandardCodeSupervisorLedgerKind = "call_authorized"
	StandardCodeSupervisorCallDenied     StandardCodeSupervisorLedgerKind = "call_denied"
	StandardCodeSupervisorCallReplayed   StandardCodeSupervisorLedgerKind = "call_replayed"
	StandardCodeSupervisorCallObserved   StandardCodeSupervisorLedgerKind = "call_observed"
	StandardCodeSupervisorRoundObserved  StandardCodeSupervisorLedgerKind = "round_observed"
	StandardCodeSupervisorActionRecorded StandardCodeSupervisorLedgerKind = "action_recorded"
	StandardCodeSupervisorStoppedRecord  StandardCodeSupervisorLedgerKind = "stopped"
)

func (k StandardCodeSupervisorLedgerKind) Valid() bool {
	switch k {
	case StandardCodeSupervisorInitialized, StandardCodeSupervisorTurnPrepared,
		StandardCodeSupervisorPhaseAdvanced, StandardCodeSupervisorCallAuthorized,
		StandardCodeSupervisorCallDenied, StandardCodeSupervisorCallReplayed,
		StandardCodeSupervisorCallObserved, StandardCodeSupervisorRoundObserved,
		StandardCodeSupervisorActionRecorded, StandardCodeSupervisorStoppedRecord:
		return true
	default:
		return false
	}
}

type StandardCodeSupervisorDecision string

const (
	StandardCodeSupervisorRecorded StandardCodeSupervisorDecision = "recorded"
	StandardCodeSupervisorAllowed  StandardCodeSupervisorDecision = "allowed"
	StandardCodeSupervisorDenied   StandardCodeSupervisorDecision = "denied"
	StandardCodeSupervisorReplayed StandardCodeSupervisorDecision = "replayed"
	StandardCodeSupervisorObserved StandardCodeSupervisorDecision = "observed"
)

func (d StandardCodeSupervisorDecision) Valid() bool {
	switch d {
	case StandardCodeSupervisorRecorded, StandardCodeSupervisorAllowed,
		StandardCodeSupervisorDenied, StandardCodeSupervisorReplayed,
		StandardCodeSupervisorObserved:
		return true
	default:
		return false
	}
}

type StandardCodeSupervisorToolKind string

const (
	StandardCodeToolWorkspaceRead     StandardCodeSupervisorToolKind = "workspace_read"
	StandardCodeToolCodeIntelRead     StandardCodeSupervisorToolKind = "code_intel_read"
	StandardCodeToolPlanProposal      StandardCodeSupervisorToolKind = "plan_proposal"
	StandardCodeToolWorkspaceProposal StandardCodeSupervisorToolKind = "workspace_proposal"
	StandardCodeToolWorkspaceMutation StandardCodeSupervisorToolKind = "workspace_mutation"
	StandardCodeToolCommandRun        StandardCodeSupervisorToolKind = "command_run"
	StandardCodeToolCommandStart      StandardCodeSupervisorToolKind = "command_start"
	StandardCodeToolCommandList       StandardCodeSupervisorToolKind = "command_list"
	StandardCodeToolCommandRead       StandardCodeSupervisorToolKind = "command_read"
	StandardCodeToolCommandWait       StandardCodeSupervisorToolKind = "command_wait"
	StandardCodeToolCommandWrite      StandardCodeSupervisorToolKind = "command_write_stdin"
	StandardCodeToolCommandCancel     StandardCodeSupervisorToolKind = "command_cancel"
	StandardCodeToolCommandKill       StandardCodeSupervisorToolKind = "command_kill"
	StandardCodeToolOther             StandardCodeSupervisorToolKind = "other"
)

func (k StandardCodeSupervisorToolKind) Valid() bool {
	switch k {
	case StandardCodeToolWorkspaceRead, StandardCodeToolCodeIntelRead,
		StandardCodeToolPlanProposal, StandardCodeToolWorkspaceProposal,
		StandardCodeToolWorkspaceMutation, StandardCodeToolCommandRun,
		StandardCodeToolCommandStart, StandardCodeToolCommandList,
		StandardCodeToolCommandRead, StandardCodeToolCommandWait,
		StandardCodeToolCommandWrite, StandardCodeToolCommandCancel,
		StandardCodeToolCommandKill, StandardCodeToolOther:
		return true
	default:
		return false
	}
}

func (k StandardCodeSupervisorToolKind) ReadOnly() bool {
	return k == StandardCodeToolWorkspaceRead || k == StandardCodeToolCodeIntelRead ||
		k == StandardCodeToolCommandList || k == StandardCodeToolCommandRead ||
		k == StandardCodeToolCommandWait
}

func (k StandardCodeSupervisorToolKind) SideEffecting() bool {
	switch k {
	case StandardCodeToolWorkspaceProposal, StandardCodeToolWorkspaceMutation,
		StandardCodeToolCommandRun, StandardCodeToolCommandStart,
		StandardCodeToolCommandWrite, StandardCodeToolCommandCancel,
		StandardCodeToolCommandKill:
		return true
	default:
		return false
	}
}

type StandardCodeSupervisorLimits struct {
	MinimumReadRounds       int   `json:"minimum_read_rounds"`
	MaximumToolRounds       int   `json:"maximum_tool_rounds_per_turn"`
	MaximumCommands         int   `json:"maximum_commands"`
	MaximumJobs             int   `json:"maximum_jobs"`
	MaximumFixRounds        int   `json:"maximum_fix_rounds"`
	MaximumOutputBytes      int64 `json:"maximum_output_bytes"`
	MaximumNoProgress       int   `json:"maximum_no_progress"`
	MaximumRepeatedFailures int   `json:"maximum_repeated_failures"`
}

func DefaultStandardCodeSupervisorLimits() StandardCodeSupervisorLimits {
	return StandardCodeSupervisorLimits{
		MinimumReadRounds:       StandardCodeSupervisorMinimumReadRounds,
		MaximumToolRounds:       StandardCodeSupervisorMaximumToolRounds,
		MaximumCommands:         StandardCodeSupervisorMaximumCommands,
		MaximumJobs:             StandardCodeSupervisorMaximumJobs,
		MaximumFixRounds:        StandardCodeSupervisorMaximumFixRounds,
		MaximumOutputBytes:      StandardCodeSupervisorMaximumOutputBytes,
		MaximumNoProgress:       StandardCodeSupervisorMaximumNoProgress,
		MaximumRepeatedFailures: StandardCodeSupervisorMaximumRepeatedFailures,
	}
}

func (l StandardCodeSupervisorLimits) Validate() error {
	if l != DefaultStandardCodeSupervisorLimits() {
		return errors.New("Standard Code Supervisor limits cannot be widened or weakened")
	}
	return nil
}

type StandardCodeSupervisorJob struct {
	ProjectTest        bool   `json:"project_test,omitempty"`
	JobID              string `json:"job_id"`
	PermissionRevision int64  `json:"permission_revision"`
	MutationEpoch      int    `json:"mutation_epoch"`
	LastCursor         uint64 `json:"last_cursor"`
	State              string `json:"state"`
}

func (j StandardCodeSupervisorJob) Validate() error {
	if !ValidAgentID(j.JobID) || j.PermissionRevision <= 0 || j.MutationEpoch < 0 || (j.MutationEpoch == 0 && !j.ProjectTest) ||
		!validStandardCodeSupervisorJobState(j.State) {
		return errors.New("Standard Code Supervisor Job ownership is invalid")
	}
	return nil
}

func validStandardCodeSupervisorJobState(state string) bool {
	switch state {
	case "prepared", "running", "stopping", "completed", "failed", "timed_out",
		"cancelled", "killed", "interrupted":
		return true
	default:
		return false
	}
}

type StandardCodeSupervisorSnapshot struct {
	ProtocolVersion              string                       `json:"protocol_version"`
	RunID                        string                       `json:"run_id"`
	MissionID                    string                       `json:"mission_id"`
	WorkspaceID                  string                       `json:"workspace_id"`
	RootAgentID                  string                       `json:"root_agent_id"`
	PresetOperationKeyDigest     string                       `json:"preset_operation_key_digest"`
	State                        StandardCodeSupervisorState  `json:"state"`
	ModeSnapshotID               string                       `json:"mode_snapshot_id"`
	ModeRevision                 int64                        `json:"mode_revision"`
	ProfileSnapshotID            string                       `json:"profile_snapshot_id"`
	ProfileRevision              int64                        `json:"profile_revision"`
	InteractionSnapshotID        string                       `json:"interaction_snapshot_id"`
	InteractionRevision          int64                        `json:"interaction_revision"`
	PermissionSnapshotID         string                       `json:"permission_snapshot_id"`
	PermissionRevision           int64                        `json:"permission_revision"`
	BrowserCDPSnapshotID         string                       `json:"browser_cdp_snapshot_id"`
	BrowserCDPRevision           int64                        `json:"browser_cdp_revision"`
	WorkspaceRootFingerprint     string                       `json:"workspace_root_fingerprint"`
	CapabilityGeneration         string                       `json:"capability_generation"`
	ExpectedCapabilityGeneration string                       `json:"expected_capability_generation,omitempty"`
	PlanSelectionID              string                       `json:"plan_selection_id,omitempty"`
	Continuation                 *StandardCodeContinuation    `json:"continuation,omitempty"`
	Turn                         int                          `json:"turn"`
	AttemptID                    string                       `json:"attempt_id"`
	TurnToolRounds               int                          `json:"turn_tool_rounds"`
	TotalToolRounds              int                          `json:"total_tool_rounds"`
	ConsecutiveReadRounds        int                          `json:"consecutive_read_rounds"`
	InspectionComplete           bool                         `json:"inspection_complete"`
	CommandsUsed                 int                          `json:"commands_used"`
	JobsStarted                  int                          `json:"jobs_started"`
	Jobs                         []StandardCodeSupervisorJob  `json:"jobs"`
	FixRounds                    int                          `json:"fix_rounds"`
	MutationEpoch                int                          `json:"mutation_epoch"`
	VerifiedMutationEpoch        int                          `json:"verified_mutation_epoch"`
	OutputBytes                  int64                        `json:"output_bytes"`
	NoProgressCount              int                          `json:"no_progress_count"`
	RepeatedFailureCount         int                          `json:"repeated_failure_count"`
	LastIntentFingerprint        string                       `json:"last_intent_fingerprint,omitempty"`
	LastEvidenceFingerprint      string                       `json:"last_evidence_fingerprint,omitempty"`
	LastFailureFingerprint       string                       `json:"last_failure_fingerprint,omitempty"`
	VerificationJobIDs           []string                     `json:"verification_job_ids,omitempty"`
	DeliveryID                   string                       `json:"delivery_id,omitempty"`
	DeliveryReceiptSHA256        string                       `json:"delivery_receipt_sha256,omitempty"`
	DeliveryCheckpointID         string                       `json:"delivery_checkpoint_id,omitempty"`
	DeliveryRevisionSHA256       string                       `json:"delivery_revision_sha256,omitempty"`
	StopReason                   string                       `json:"stop_reason,omitempty"`
	RunTokenLimit                int64                        `json:"run_token_limit"`
	RunTimeoutMillis             int64                        `json:"run_timeout_millis"`
	RunToolCallLimit             int64                        `json:"run_tool_call_limit"`
	Limits                       StandardCodeSupervisorLimits `json:"limits"`
	Version                      int64                        `json:"version"`
	CreatedAt                    time.Time                    `json:"created_at"`
	UpdatedAt                    time.Time                    `json:"updated_at"`
}

func (s StandardCodeSupervisorSnapshot) Validate() error {
	if s.Continuation != nil {
		if err := s.Continuation.Validate(); err != nil {
			return err
		}
		if s.Continuation.PredecessorRunID == s.RunID || s.MutationEpoch < s.Continuation.MutationEpoch {
			return errors.New("Standard Code continuation cannot replace its source epoch")
		}
	}
	if s.ProtocolVersion != StandardCodeSupervisorProtocolVersion || !s.State.Valid() ||
		s.Limits.Validate() != nil {
		return errors.New("Standard Code Supervisor snapshot protocol, state, or limits are invalid")
	}
	for label, value := range map[string]string{
		"Run id": s.RunID, "Mission id": s.MissionID, "Workspace id": s.WorkspaceID,
		"root Agent id": s.RootAgentID, "mode snapshot id": s.ModeSnapshotID,
		"profile snapshot id":     s.ProfileSnapshotID,
		"interaction snapshot id": s.InteractionSnapshotID,
		"permission snapshot id":  s.PermissionSnapshotID,
		"browser CDP snapshot id": s.BrowserCDPSnapshotID, "attempt id": s.AttemptID,
	} {
		if !ValidAgentID(value) {
			return fmt.Errorf("Standard Code Supervisor %s is invalid", label)
		}
	}
	if s.PlanSelectionID != "" && !ValidAgentID(s.PlanSelectionID) {
		return errors.New("Standard Code Supervisor Plan selection id is invalid")
	}
	for _, digest := range []string{s.PresetOperationKeyDigest,
		s.WorkspaceRootFingerprint, s.CapabilityGeneration} {
		if !validLowerHexDigest(digest) {
			return errors.New("Standard Code Supervisor authority digest is invalid")
		}
	}
	if s.ExpectedCapabilityGeneration != "" &&
		!validLowerHexDigest(s.ExpectedCapabilityGeneration) {
		return errors.New("Standard Code Supervisor expected authority digest is invalid")
	}
	for _, digest := range []string{s.LastIntentFingerprint, s.LastEvidenceFingerprint,
		s.LastFailureFingerprint, s.DeliveryReceiptSHA256, s.DeliveryRevisionSHA256} {
		if digest != "" && !validLowerHexDigest(digest) {
			return errors.New("Standard Code Supervisor evidence digest is invalid")
		}
	}
	if s.ModeRevision <= 0 || s.ProfileRevision <= 0 || s.InteractionRevision <= 0 ||
		s.PermissionRevision <= 0 || s.BrowserCDPRevision <= 0 || s.Turn <= 0 ||
		s.TurnToolRounds < 0 || s.TurnToolRounds > s.Limits.MaximumToolRounds ||
		s.TotalToolRounds < s.TurnToolRounds || s.ConsecutiveReadRounds < 0 ||
		s.CommandsUsed < 0 || s.CommandsUsed > s.Limits.MaximumCommands ||
		s.JobsStarted < 0 || s.JobsStarted > s.Limits.MaximumJobs ||
		len(s.Jobs) > s.Limits.MaximumJobs || s.FixRounds < 0 ||
		s.FixRounds > s.Limits.MaximumFixRounds || s.MutationEpoch < 0 ||
		s.VerifiedMutationEpoch < 0 || s.VerifiedMutationEpoch > s.MutationEpoch ||
		s.OutputBytes < 0 || s.OutputBytes > s.Limits.MaximumOutputBytes ||
		s.NoProgressCount < 0 || s.NoProgressCount > s.Limits.MaximumNoProgress ||
		s.RepeatedFailureCount < 0 ||
		s.RepeatedFailureCount > s.Limits.MaximumRepeatedFailures ||
		s.RunTokenLimit < 0 || s.RunTimeoutMillis < 0 || s.RunToolCallLimit < 0 ||
		s.Version <= 0 || s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() ||
		s.UpdatedAt.Before(s.CreatedAt) {
		return errors.New("Standard Code Supervisor counters, budgets, or timestamps are invalid")
	}
	if s.InspectionComplete != (s.ConsecutiveReadRounds >= s.Limits.MinimumReadRounds) {
		return errors.New("Standard Code Supervisor inspection proof is inconsistent")
	}
	if s.State == StandardCodeSupervisorDeliver &&
		(s.MutationEpoch <= 0 || s.VerifiedMutationEpoch != s.MutationEpoch ||
			len(s.VerificationJobIDs) == 0) {
		return errors.New("Standard Code Supervisor cannot deliver without current verification")
	}
	deliveryBound := s.DeliveryID != "" || s.DeliveryReceiptSHA256 != "" ||
		s.DeliveryCheckpointID != "" || s.DeliveryRevisionSHA256 != ""
	if deliveryBound && (!ValidAgentID(s.DeliveryID) ||
		!ValidAgentID(s.DeliveryCheckpointID) ||
		!validLowerHexDigest(s.DeliveryReceiptSHA256) ||
		!validLowerHexDigest(s.DeliveryRevisionSHA256) ||
		s.State != StandardCodeSupervisorDeliver) {
		return errors.New("Standard Code Supervisor delivery receipt binding is invalid")
	}
	if s.ExpectedCapabilityGeneration != "" && s.MutationEpoch <= 0 {
		return errors.New("Standard Code Supervisor capability refresh requires a mutation")
	}
	if (s.State == StandardCodeSupervisorStopped) != (s.StopReason != "") ||
		!validStandardCodeSupervisorText(s.StopReason, 256, true) {
		return errors.New("Standard Code Supervisor stop state is inconsistent")
	}
	seenJobs := make(map[string]struct{}, len(s.Jobs))
	for _, job := range s.Jobs {
		if err := job.Validate(); err != nil {
			return err
		}
		if _, exists := seenJobs[job.JobID]; exists {
			return errors.New("Standard Code Supervisor Job ownership is duplicated")
		}
		seenJobs[job.JobID] = struct{}{}
	}
	seenVerificationJobs := make(map[string]struct{}, len(s.VerificationJobIDs))
	for _, id := range s.VerificationJobIDs {
		if !ValidAgentID(id) {
			return errors.New("Standard Code Supervisor verification Job id is invalid")
		}
		if _, exists := seenVerificationJobs[id]; exists {
			return errors.New("Standard Code Supervisor verification Job id is duplicated")
		}
		seenVerificationJobs[id] = struct{}{}
	}
	return nil
}

func (s StandardCodeSupervisorSnapshot) CanDeliver() bool {
	return s.State == StandardCodeSupervisorDeliver && s.MutationEpoch > 0 &&
		s.VerifiedMutationEpoch == s.MutationEpoch && len(s.VerificationJobIDs) > 0 &&
		s.StopReason == ""
}

type StandardCodeSupervisorLedgerEntry struct {
	ID                  string                           `json:"id"`
	OperationKeyDigest  string                           `json:"operation_key_digest"`
	RequestFingerprint  string                           `json:"request_fingerprint"`
	Kind                StandardCodeSupervisorLedgerKind `json:"kind"`
	Decision            StandardCodeSupervisorDecision   `json:"decision"`
	ToolCallID          string                           `json:"tool_call_id,omitempty"`
	ToolName            string                           `json:"tool_name,omitempty"`
	ToolAction          string                           `json:"tool_action,omitempty"`
	ToolKind            StandardCodeSupervisorToolKind   `json:"tool_kind"`
	IntentFingerprint   string                           `json:"intent_fingerprint,omitempty"`
	EvidenceFingerprint string                           `json:"evidence_fingerprint,omitempty"`
	ResultStatus        SupervisorToolCallStatus         `json:"result_status,omitempty"`
	ErrorCode           string                           `json:"error_code,omitempty"`
	FromState           StandardCodeSupervisorState      `json:"from_state,omitempty"`
	ToState             StandardCodeSupervisorState      `json:"to_state"`
	ReasonCode          string                           `json:"reason_code,omitempty"`
	Snapshot            StandardCodeSupervisorSnapshot   `json:"snapshot"`
	EventSequence       int64                            `json:"event_sequence"`
	CreatedAt           time.Time                        `json:"created_at"`
	LeaseID             string                           `json:"-"`
	LeaseGeneration     int64                            `json:"-"`
}

func (e StandardCodeSupervisorLedgerEntry) Validate() error {
	if !ValidAgentID(e.ID) || !validLowerHexDigest(e.OperationKeyDigest) ||
		!validLowerHexDigest(e.RequestFingerprint) || !e.Kind.Valid() ||
		!e.Decision.Valid() || !e.ToolKind.Valid() || !e.ToState.Valid() ||
		e.Snapshot.State != e.ToState || e.EventSequence <= 0 || e.CreatedAt.IsZero() ||
		!e.CreatedAt.Equal(e.Snapshot.UpdatedAt) {
		return errors.New("Standard Code Supervisor ledger identity or transition is invalid")
	}
	if err := e.Snapshot.Validate(); err != nil {
		return err
	}
	if e.FromState != "" && !e.FromState.Valid() {
		return errors.New("Standard Code Supervisor prior state is invalid")
	}
	for _, digest := range []string{e.IntentFingerprint, e.EvidenceFingerprint} {
		if digest != "" && !validLowerHexDigest(digest) {
			return errors.New("Standard Code Supervisor ledger digest is invalid")
		}
	}
	for _, value := range []string{e.ToolCallID, e.ToolName, e.ToolAction,
		e.ErrorCode, e.ReasonCode} {
		if !validStandardCodeSupervisorText(value, MaxSupervisorToolIdentityRunes, true) {
			return errors.New("Standard Code Supervisor ledger text is invalid")
		}
	}
	callKind := e.Kind == StandardCodeSupervisorCallAuthorized ||
		e.Kind == StandardCodeSupervisorCallDenied ||
		e.Kind == StandardCodeSupervisorCallReplayed ||
		e.Kind == StandardCodeSupervisorCallObserved
	if callKind != (e.ToolCallID != "" && e.ToolName != "") {
		return errors.New("Standard Code Supervisor call ledger binding is inconsistent")
	}
	if e.ResultStatus != "" && !e.ResultStatus.Terminal() {
		return errors.New("Standard Code Supervisor result status is invalid")
	}
	if e.Kind == StandardCodeSupervisorCallObserved && e.ResultStatus == "" {
		return errors.New("Standard Code Supervisor observation requires a terminal result")
	}
	if e.Kind != StandardCodeSupervisorCallObserved && e.ResultStatus != "" {
		return errors.New("only Standard Code Supervisor observations may carry result status")
	}
	if (strings.TrimSpace(e.LeaseID) == "") != (e.LeaseGeneration == 0) ||
		e.LeaseGeneration < 0 {
		return errors.New("Standard Code Supervisor lease fence is inconsistent")
	}
	return nil
}

func validStandardCodeSupervisorText(value string, maxRunes int, allowEmpty bool) bool {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) ||
		strings.TrimSpace(value) != value || len([]rune(value)) > maxRunes {
		return false
	}
	return allowEmpty || value != ""
}
