package operationreceipt

import "fmt"

const ProtocolVersion = "operation_receipt.v1"

const HistoryProtocolVersion = "operation_receipt_history.v1"

type Kind string

const (
	KindFileEditApply       Kind = "file_edit_apply"
	KindRunWakeConsume      Kind = "run_wake_consume"
	KindSkillPackageInstall Kind = "skill_package_install"
	KindSkillPackageRemove  Kind = "skill_package_remove"
)

type RetryStrategy string

const (
	RetrySameOperationKey   RetryStrategy = "same_operation_key"
	RetrySameWakeGeneration RetryStrategy = "same_wake_generation"
)

type RecoveryAction string

const (
	RecoveryNone            RecoveryAction = "none"
	RecoveryRetryAfterGrace RecoveryAction = "retry_after_cleanup_grace"
)

type CleanupState string

const (
	CleanupNotApplicable CleanupState = "not_applicable"
	CleanupComplete      CleanupState = "complete"
	CleanupPendingReview CleanupState = "pending_review"
)

// Receipt is a content-free projection of a durable operation result. It never
// carries operation keys, digests, local paths, file bodies, or lease identity.
type Receipt struct {
	ProtocolVersion string         `json:"protocol_version"`
	Kind            Kind           `json:"kind"`
	Outcome         string         `json:"outcome"`
	Durable         bool           `json:"durable"`
	Replayed        bool           `json:"replayed"`
	RetrySafe       bool           `json:"retry_safe"`
	RetryStrategy   RetryStrategy  `json:"retry_strategy"`
	RecoveryAction  RecoveryAction `json:"recovery_action"`
	CleanupState    CleanupState   `json:"cleanup_state"`
}

func Settled(kind Kind, replayed bool, cleanupPending bool) Receipt {
	receipt := Receipt{
		ProtocolVersion: ProtocolVersion,
		Kind:            kind,
		Durable:         true,
		Replayed:        replayed,
		RetrySafe:       true,
		RecoveryAction:  RecoveryNone,
		CleanupState:    CleanupNotApplicable,
	}
	switch kind {
	case KindFileEditApply:
		receipt.Outcome = "applied"
		receipt.RetryStrategy = RetrySameOperationKey
		receipt.CleanupState = CleanupComplete
		if cleanupPending {
			receipt.RecoveryAction = RecoveryRetryAfterGrace
			receipt.CleanupState = CleanupPendingReview
		}
	case KindRunWakeConsume:
		receipt.Outcome = "completed"
		receipt.RetryStrategy = RetrySameWakeGeneration
	case KindSkillPackageInstall:
		receipt.Outcome = "installed"
		receipt.RetryStrategy = RetrySameOperationKey
	case KindSkillPackageRemove:
		receipt.Outcome = "removed"
		receipt.RetryStrategy = RetrySameOperationKey
	}
	return receipt
}

func FileEditApply(outcome string, replayed bool, cleanupPending bool) Receipt {
	receipt := Settled(KindFileEditApply, replayed, cleanupPending)
	receipt.Outcome = outcome
	return receipt
}

func RunWakeConsume(outcome string, replayed bool) Receipt {
	receipt := Settled(KindRunWakeConsume, replayed, false)
	receipt.Outcome = outcome
	return receipt
}

func (r Receipt) Validate() error {
	if r.ProtocolVersion != ProtocolVersion || !r.Durable || !r.RetrySafe {
		return fmt.Errorf("operation receipt protocol or durable retry state is invalid")
	}
	if r.Kind != KindFileEditApply && r.Kind != KindRunWakeConsume &&
		r.Kind != KindSkillPackageInstall && r.Kind != KindSkillPackageRemove {
		return fmt.Errorf("operation receipt kind is invalid")
	}
	if r.Kind == KindFileEditApply && r.Outcome != "applied" && r.Outcome != "failed" {
		return fmt.Errorf("file edit operation receipt outcome is invalid")
	}
	if r.Kind == KindRunWakeConsume && r.Outcome != "completed" && r.Outcome != "failed" {
		return fmt.Errorf("run wake operation receipt outcome is invalid")
	}
	want := Settled(r.Kind, r.Replayed, r.CleanupState == CleanupPendingReview)
	if r.Kind == KindFileEditApply {
		want = FileEditApply(r.Outcome, r.Replayed,
			r.CleanupState == CleanupPendingReview)
	}
	if r.Kind == KindRunWakeConsume {
		want = RunWakeConsume(r.Outcome, r.Replayed)
	}
	if r != want {
		return fmt.Errorf("operation receipt fields are inconsistent")
	}
	return nil
}
