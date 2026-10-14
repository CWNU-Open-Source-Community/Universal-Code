package operatoraction

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"cyberagent-workbench/internal/domain"
)

const (
	ProtocolVersion = "operator_action_center.v1"
	MaxItems        = 100
)

type Kind string

const (
	KindSteeringPending Kind = "steering_pending"
	KindApprovalPending Kind = "approval_pending"
	KindFileEditReview  Kind = "file_edit_review"
	KindFileEditApply   Kind = "file_edit_apply"
	KindWakeDue         Kind = "wake_due"
	KindChildTaskReview Kind = "child_task_review"
)

func (k Kind) Valid() bool {
	switch k {
	case KindSteeringPending, KindApprovalPending, KindFileEditReview,
		KindFileEditApply, KindWakeDue, KindChildTaskReview:
		return true
	default:
		return false
	}
}

type Destination string

const (
	DestinationQueue      Destination = "queue"
	DestinationApprovals  Destination = "approvals"
	DestinationDiffs      Destination = "diffs"
	DestinationWake       Destination = "wake"
	DestinationChildTasks Destination = "child-tasks"
)

func DestinationFor(kind Kind) (Destination, bool) {
	switch kind {
	case KindSteeringPending:
		return DestinationQueue, true
	case KindApprovalPending:
		return DestinationApprovals, true
	case KindFileEditReview, KindFileEditApply:
		return DestinationDiffs, true
	case KindWakeDue:
		return DestinationWake, true
	case KindChildTaskReview:
		return DestinationChildTasks, true
	default:
		return "", false
	}
}

// Record is the private persistence projection consumed by the application
// service. SourceID and binding metadata are never returned to browser clients.
type Record struct {
	SourceID    string
	Kind        Kind
	State       string
	RunID       string
	SessionID   string
	WorkspaceID string
	AvailableAt time.Time
	DueAt       *time.Time
}

func (r Record) Validate() error {
	if !domain.ValidAgentID(r.SourceID) || !domain.ValidAgentID(r.RunID) ||
		(r.SessionID != "" && !domain.ValidAgentID(r.SessionID)) ||
		(r.WorkspaceID != "" && !domain.ValidAgentID(r.WorkspaceID)) {
		return errors.New("operator action record identities are invalid")
	}
	if !r.Kind.Valid() || r.AvailableAt.IsZero() {
		return errors.New("operator action record kind and availability time are required")
	}
	expectedState := map[Kind]string{
		KindSteeringPending: "pending",
		KindApprovalPending: "pending",
		KindFileEditReview:  "proposed",
		KindFileEditApply:   "approved",
		KindWakeDue:         "queued",
		KindChildTaskReview: "proposed",
	}[r.Kind]
	if r.State != expectedState {
		return fmt.Errorf("operator action %s has invalid state %q", r.Kind, r.State)
	}
	if r.Kind == KindWakeDue {
		if r.DueAt == nil || r.DueAt.IsZero() {
			return errors.New("due wake action requires a due time")
		}
	} else if r.DueAt != nil {
		return errors.New("non-wake operator action cannot contain a due time")
	}
	return nil
}

type Item struct {
	ID          string
	Kind        Kind
	State       string
	Destination Destination
	AvailableAt time.Time
	DueAt       *time.Time
}

func (i Item) Validate() error {
	if !domain.ValidAgentID(i.ID) || !i.Kind.Valid() || strings.TrimSpace(i.State) != i.State ||
		i.State == "" || i.AvailableAt.IsZero() {
		return errors.New("operator action item is invalid")
	}
	destination, ok := DestinationFor(i.Kind)
	if !ok || destination != i.Destination {
		return errors.New("operator action destination does not match its kind")
	}
	record := Record{SourceID: i.ID, RunID: i.ID, Kind: i.Kind, State: i.State,
		AvailableAt: i.AvailableAt, DueAt: i.DueAt}
	return record.Validate()
}

type Center struct {
	ProtocolVersion string
	RunID           string
	GeneratedAt     time.Time
	Items           []Item
	Truncated       bool
}

func (c Center) Validate() error {
	if c.ProtocolVersion != ProtocolVersion || !domain.ValidAgentID(c.RunID) ||
		c.GeneratedAt.IsZero() || len(c.Items) > MaxItems {
		return errors.New("operator action center envelope is invalid")
	}
	seen := make(map[string]struct{}, len(c.Items))
	for _, item := range c.Items {
		if err := item.Validate(); err != nil {
			return err
		}
		if _, exists := seen[item.ID]; exists {
			return errors.New("operator action center contains a duplicate item")
		}
		seen[item.ID] = struct{}{}
	}
	return nil
}
