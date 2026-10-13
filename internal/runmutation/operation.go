package runmutation

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/durableoperation"
)

const MaxIdentityRunes = 256

type TargetKind string

const (
	TargetWorkItem TargetKind = "work_item"
	TargetNote     TargetKind = "note"
)

func (k TargetKind) Valid() bool {
	return k == TargetWorkItem || k == TargetNote
}

type Operation struct {
	KeyDigest          string
	RequestFingerprint string
	InvocationID       string
	RunID              string
	SessionID          string
	WorkspaceID        string
	LeaseID            string
	LeaseGeneration    int64
	ToolName           string
	TargetKind         TargetKind
	TargetID           string
	RequestedBy        string
	CreatedAt          time.Time
}

func (o Operation) Validate() error {
	return o.validate(true)
}

// ValidateStored accepts records without an execution lease because leases are
// transient fencing credentials and are intentionally not persisted with intent.
func (o Operation) ValidateStored() error {
	return o.validate(false)
}

func (o Operation) validate(requireSupervisorLease bool) error {
	for label, value := range map[string]string{
		"invocation id": o.InvocationID, "run id": o.RunID, "session id": o.SessionID,
		"workspace id": o.WorkspaceID, "tool name": o.ToolName, "target id": o.TargetID,
		"lease id": o.LeaseID, "requester": o.RequestedBy,
	} {
		if !utf8.ValidString(value) || strings.TrimSpace(value) != value || len([]rune(value)) > MaxIdentityRunes {
			return fmt.Errorf("structured mutation %s must be normalized and bounded UTF-8", label)
		}
	}
	if o.InvocationID == "" || o.RunID == "" || o.SessionID == "" || o.ToolName == "" ||
		o.TargetID == "" || o.RequestedBy == "" {
		return errors.New("structured mutation invocation, Run, Session, tool, target, and requester are required")
	}
	if (o.LeaseID == "") != (o.LeaseGeneration == 0) || o.LeaseGeneration < 0 {
		return errors.New("structured mutation execution lease identity and generation are inconsistent")
	}
	if requireSupervisorLease && o.RequestedBy == "run_supervisor" && o.LeaseID == "" {
		return errors.New("supervisor structured mutation requires a Run execution lease")
	}
	if !validDigest(o.KeyDigest) || !validDigest(o.RequestFingerprint) {
		return errors.New("structured mutation key and request fingerprints must be lowercase SHA-256 digests")
	}
	if !o.TargetKind.Valid() {
		return fmt.Errorf("invalid structured mutation target kind %q", o.TargetKind)
	}
	if o.CreatedAt.IsZero() {
		return errors.New("structured mutation creation time is required")
	}
	return nil
}

func OperationKeyDigest(toolName string, runID string, operationKey string) string {
	return Fingerprint("structured_tool_operation.v1", toolName, runID, operationKey)
}

func RunCreationOperationDigest(operationKey string) string {
	return pilotFingerprint("run_creation_operation.v1", operationKey)
}

func RunCreationRequestFingerprint(goal string, workspaceID string, profile string,
	surface string, phase string, requestedBy string,
) string {
	return pilotFingerprint("run_creation_request.v1", goal, workspaceID, profile,
		surface, phase, requestedBy)
}

// RunCreationRequestFingerprintWithNetwork extends the controlled creation
// intent with its canonical network authority. The disabled form deliberately
// retains the v1 digest so idempotent replays created before network authority
// was exposed remain valid.
func RunCreationRequestFingerprintWithNetwork(goal string, workspaceID string, profile string,
	surface string, phase string, networkMode string, allowedTargets []string,
	requestedBy string,
) string {
	if networkMode == "disabled" && len(allowedTargets) == 0 {
		return RunCreationRequestFingerprint(goal, workspaceID, profile, surface, phase, requestedBy)
	}
	fields := []string{goal, workspaceID, profile, surface, phase, networkMode,
		strconv.Itoa(len(allowedTargets))}
	fields = append(fields, allowedTargets...)
	fields = append(fields, requestedBy)
	return pilotFingerprint("run_creation_request.v2", fields...)
}

// RunCreationRequestFingerprintWithNetworkAndModelRoute binds an explicit
// provider/model route into first-Run creation. The profile-default form keeps
// the historical digest for replay compatibility.
func RunCreationRequestFingerprintWithNetworkAndModelRoute(goal string, workspaceID string,
	profile string, surface string, phase string, networkMode string,
	allowedTargets []string, modelRoute string, requestedBy string,
) string {
	if modelRoute == "" || modelRoute == profile {
		return RunCreationRequestFingerprintWithNetwork(goal, workspaceID, profile,
			surface, phase, networkMode, allowedTargets, requestedBy)
	}
	fields := []string{goal, workspaceID, profile, surface, phase, networkMode,
		strconv.Itoa(len(allowedTargets))}
	fields = append(fields, allowedTargets...)
	fields = append(fields, modelRoute, requestedBy)
	return pilotFingerprint("run_creation_request.v3", fields...)
}

func RunNetworkAuthorityOperationDigest(runID string, operationKey string) string {
	return Fingerprint("run_network_authority_operation.v1", runID, operationKey)
}

func RunNetworkAuthorityRequestFingerprint(runID string, expectedModeRevision int64,
	addedTargets []string, requestedBy string, reason string,
) string {
	fields := []string{"run_network_authority_request.v1", runID,
		strconv.FormatInt(expectedModeRevision, 10),
		strconv.Itoa(len(addedTargets))}
	fields = append(fields, addedTargets...)
	fields = append(fields, requestedBy, reason)
	return Fingerprint(fields...)
}

func RunLifecycleOperationDigest(runID string, operationKey string) string {
	return Fingerprint("run_lifecycle_operation.v1", runID, operationKey)
}

func RunLifecycleRequestFingerprint(runID string, action string, expectedStatus string,
	requestedBy string,
) string {
	return Fingerprint("run_lifecycle_request.v1", runID, action, expectedStatus,
		requestedBy)
}

func RunExecutionHandoffOperationDigest(runID string, operationKey string) string {
	return Fingerprint("run_execution_handoff_operation.v1", runID, operationKey)
}

func RunExecutionHandoffRequestFingerprint(runID string, requestedBy string,
	maxSteps int,
) string {
	return Fingerprint("run_execution_handoff_request.v1", runID, requestedBy,
		strconv.Itoa(maxSteps))
}

func RunWakeOperationDigest(runID string, operationKey string) string {
	return Fingerprint("run_wake_operation.v1", runID, operationKey)
}

func RunWakeScheduleRequestFingerprint(runID string, requestedBy string,
	maxAttempts int, initialDelaySeconds int, baseBackoffSeconds int,
	maxBackoffSeconds int, maxElapsedSeconds int,
) string {
	return Fingerprint("run_wake_schedule_request.v1", runID, requestedBy,
		strconv.Itoa(maxAttempts), strconv.Itoa(initialDelaySeconds),
		strconv.Itoa(baseBackoffSeconds), strconv.Itoa(maxBackoffSeconds),
		strconv.Itoa(maxElapsedSeconds))
}

func RunWakeCancelRequestFingerprint(runID string, requestedBy string) string {
	return Fingerprint("run_wake_cancel_request.v1", runID, requestedBy)
}

func ScheduledJobOperationDigest(runID string, operationKey string) string {
	return pilotFingerprint("scheduled_job_operation.v1", runID, operationKey)
}

func ScheduledJobCreateRequestFingerprint(runID string, specJSON string,
	requestedBy string, repairConfirmed bool,
) string {
	return pilotFingerprint("scheduled_job_create_request.v1", runID, specJSON,
		requestedBy, strconv.FormatBool(repairConfirmed))
}

func ScheduledJobTransitionRequestFingerprint(runID string, jobID string,
	action string, expectedRevision int64, requestedBy string,
) string {
	return pilotFingerprint("scheduled_job_transition_request.v1", runID, jobID,
		action, strconv.FormatInt(expectedRevision, 10), requestedBy)
}

// pilotFingerprint preserves the established digest API while making an
// invalid hard-coded domain fail closed as an empty digest. Domain operation
// validation rejects that value before persistence.
func pilotFingerprint(domainSeparator string, fields ...string) string {
	value, err := durableoperation.Fingerprint(domainSeparator, fields...)
	if err != nil {
		return ""
	}
	return value
}

func RunWakeConsumptionOperationKey(intentID string, generation int) string {
	return "wake-consume-" + Fingerprint("run_wake_consumption_handoff.v1",
		intentID, strconv.Itoa(generation))
}

func FileEditApplyOperationDigest(runID string, editID string,
	operationKey string,
) string {
	return Fingerprint("file_edit_apply_operation.v1", runID, editID, operationKey)
}

func FileEditApplyRequestFingerprint(runID string, editID string,
	appliedBy string,
) string {
	return Fingerprint("file_edit_apply_request.v1", runID, editID, appliedBy)
}

func EvidenceAttachmentOperationDigest(runID string, operationKey string) string {
	return Fingerprint("session_evidence_attachment_operation.v1", runID, operationKey)
}

func EvidenceAttachmentRequestFingerprint(runID string, workspaceID string,
	sourceKind string, sourceRef string, contentSHA256 string, attachedBy string,
) string {
	return Fingerprint("session_evidence_attachment_request.v1", runID, workspaceID,
		sourceKind, sourceRef, contentSHA256, attachedBy)
}

func VerificationEvidenceOperationDigest(runID string, operationKey string) string {
	return Fingerprint("operator_verification_evidence_operation.v1", runID, operationKey)
}

func VerificationEvidenceRequestFingerprint(runID string, sessionID string,
	workspaceID string, outcome string, title string, summary string,
	recordedBy string,
) string {
	return Fingerprint("operator_verification_evidence_request.v1", runID,
		sessionID, workspaceID, outcome, title, summary, recordedBy)
}

func VerificationPlanOperationDigest(runID string, operationKey string) string {
	return Fingerprint("operator_verification_plan_operation.v1", runID, operationKey)
}

func VerificationPlanRequestFingerprint(runID string, sessionID string,
	workspaceID string, planSHA256 string, authoredBy string,
) string {
	return Fingerprint("operator_verification_plan_request.v1", runID,
		sessionID, workspaceID, planSHA256, authoredBy)
}

func VerificationPlanEvidenceAssociationOperationDigest(runID string,
	operationKey string,
) string {
	return Fingerprint("operator_verification_plan_evidence_association_operation.v1",
		runID, operationKey)
}

func VerificationPlanEvidenceAssociationRequestFingerprint(runID string,
	sessionID string, workspaceID string, planID string, planItemOrdinal int,
	planItemSHA256 string, evidenceID string, evidenceOutcome string,
	evidenceEventSequence int64, associatedBy string,
) string {
	return Fingerprint("operator_verification_plan_evidence_association_request.v1",
		runID, sessionID, workspaceID, planID, strconv.Itoa(planItemOrdinal),
		planItemSHA256, evidenceID, evidenceOutcome,
		strconv.FormatInt(evidenceEventSequence, 10), associatedBy)
}

func VerificationSnapshotReceiptOperationDigest(runID string, operationKey string) string {
	return Fingerprint("operator_verification_plan_item_snapshot_receipt_operation.v1",
		runID, operationKey)
}

func VerificationSnapshotReceiptRequestFingerprint(runID string, sessionID string,
	workspaceID string, planID string, planItemOrdinal int, format string,
	highWater int64, contentSHA256 string, recordedBy string,
) string {
	return Fingerprint("operator_verification_plan_item_snapshot_receipt_request.v1",
		runID, sessionID, workspaceID, planID, strconv.Itoa(planItemOrdinal), format,
		strconv.FormatInt(highWater, 10), contentSHA256, recordedBy)
}

func VerificationSnapshotReceiptReviewOperationDigest(runID string,
	operationKey string,
) string {
	return Fingerprint("operator_verification_plan_item_snapshot_receipt_review_operation.v1",
		runID, operationKey)
}

func VerificationSnapshotReceiptReviewRequestFingerprint(runID string, sessionID string,
	workspaceID string, receiptID string, receiptContentSHA256 string,
	receiptEventSequence int64, decision string, reviewedBy string,
) string {
	return Fingerprint("operator_verification_plan_item_snapshot_receipt_review_request.v1",
		runID, sessionID, workspaceID, receiptID, receiptContentSHA256,
		strconv.FormatInt(receiptEventSequence, 10), decision, reviewedBy)
}

func SupervisorToolOperationKey(runID string, turn int, toolName string, payloadJSON string) string {
	return Fingerprint("supervisor_structured_tool.v1", strings.TrimSpace(runID), strconv.Itoa(turn),
		strings.TrimSpace(toolName), strings.TrimSpace(payloadJSON))
}

// SupervisorToolExecutionKey binds side effects to a durable logical call.
// The same payload in a later round has a different call ID, while resuming
// the original call keeps its command, checkpoint, and budget identity.
func SupervisorToolExecutionKey(runID string, turn int, toolName, payloadJSON, callID string) string {
	return Fingerprint("supervisor_tool_execution.v1",
		SupervisorToolOperationKey(runID, turn, toolName, payloadJSON), strings.TrimSpace(callID))
}

func SupervisorToolCallID(operationKey string, round int) (string, error) {
	operationKey = strings.TrimSpace(operationKey)
	if !validDigest(operationKey) {
		return "", errors.New("supervisor tool operation key must be a SHA-256 digest")
	}
	if round <= 0 {
		return "", errors.New("supervisor tool call round must be positive")
	}
	identity := Fingerprint("supervisor_tool_call_id.v1", operationKey, strconv.Itoa(round))
	return "toolu_" + identity[:24], nil
}

func Fingerprint(parts ...string) string {
	hash := sha256.New()
	var size [8]byte
	for _, part := range parts {
		binary.BigEndian.PutUint64(size[:], uint64(len([]byte(part))))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(part))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
