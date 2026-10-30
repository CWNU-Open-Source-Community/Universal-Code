package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/waitgraph"
)

const (
	DependencyEdgeProtocolVersion = "agent_dependency.v1"

	MaxDependencyReasonRunes   = 512
	MaxDependencyEdgesPerRun   = 4096
	MaxDependencyDepth         = 64
	MaxDependencyWakesPerAgent = 1024
	DependencyPollingLivelockLimit = 64
)

// ValidAgentDependencyStateForEdge reports whether a durable dependency edge
// may carry the given state. The closed set matches AgentDependencyState.
func ValidAgentDependencyStateForEdge(state AgentDependencyState) bool {
	switch state {
	case AgentDependencyWait, AgentDependencySatisfied, AgentDependencyFailed,
		AgentDependencyCancelled, AgentDependencyExpired:
		return true
	default:
		return false
	}
}

// DependencyFailurePolicy declares how a target failure propagates to the
// waiting source. Fail marks the edge failed and wakes the source with the
// failure; Notify marks the edge failed but wakes the source with a
// non-fatal notification so it can continue.
type DependencyFailurePolicy string

const (
	DependencyPolicyFail   DependencyFailurePolicy = "fail"
	DependencyPolicyNotify DependencyFailurePolicy = "notify"
)

func ValidDependencyFailurePolicy(policy DependencyFailurePolicy) bool {
	return policy == DependencyPolicyFail || policy == DependencyPolicyNotify
}

// DependencyEdge is the versioned, durable wait contract between one source
// and one target. It is operator/Go-owned configuration plus model-proposed
// intent: only the Go control plane validates and persists it.
type DependencyEdge struct {
	ID            string
	RunID         string
	SourceKind    waitgraph.Kind
	SourceID      string
	TargetKind    waitgraph.Kind
	TargetID      string
	Reason        string
	State         AgentDependencyState
	FailurePolicy DependencyFailurePolicy
	Generation    int64
	Deadline      time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ResolvedAt    *time.Time
}

// Normalize trims identity fields so a single edge has one canonical shape.
func (e DependencyEdge) Normalize() DependencyEdge {
	e.ID = strings.TrimSpace(e.ID)
	e.RunID = strings.TrimSpace(e.RunID)
	e.SourceID = strings.TrimSpace(e.SourceID)
	e.TargetID = strings.TrimSpace(e.TargetID)
	e.Reason = strings.TrimSpace(e.Reason)
	return e
}

// Validate checks the edge identity, bounds, and state invariants without
// touching the persisted graph.
func (e DependencyEdge) Validate() error {
	e = e.Normalize()
	if e.ID == "" || len([]byte(e.ID)) > 256 || strings.ContainsRune(e.ID, 0) {
		return errors.New("dependency edge id is invalid")
	}
	if e.RunID == "" || len([]byte(e.RunID)) > 256 || strings.ContainsRune(e.RunID, 0) {
		return errors.New("dependency edge run id is invalid")
	}
	source := waitgraph.Node{Kind: e.SourceKind, ID: e.SourceID}
	if err := source.Validate(); err != nil {
		return fmt.Errorf("dependency edge source is invalid: %w", err)
	}
	target := waitgraph.Node{Kind: e.TargetKind, ID: e.TargetID}
	if err := target.Validate(); err != nil {
		return fmt.Errorf("dependency edge target is invalid: %w", err)
	}
	if source == target {
		return errors.New("dependency edge source and target must differ")
	}
	if !utf8.ValidString(e.Reason) || strings.ContainsRune(e.Reason, 0) ||
		len([]rune(e.Reason)) > MaxDependencyReasonRunes {
		return fmt.Errorf("dependency edge reason must be valid UTF-8 within %d characters",
			MaxDependencyReasonRunes)
	}
	if !ValidAgentDependencyStateForEdge(e.State) {
		return errors.New("dependency edge state is invalid")
	}
	if !ValidDependencyFailurePolicy(e.FailurePolicy) {
		return errors.New("dependency failure policy is invalid")
	}
	if e.Generation <= 0 {
		return errors.New("dependency edge generation must be positive")
	}
	if e.Deadline.IsZero() || e.CreatedAt.IsZero() || e.UpdatedAt.IsZero() {
		return errors.New("dependency edge timestamps are required")
	}
	if !e.Deadline.After(e.CreatedAt) {
		return errors.New("dependency edge deadline must be after its creation")
	}
	if (e.State == AgentDependencyWait) != (e.ResolvedAt == nil) {
		return errors.New("dependency edge resolution timestamp is inconsistent with its state")
	}
	if e.ResolvedAt != nil && (e.ResolvedAt.Before(e.CreatedAt) || e.ResolvedAt.After(e.UpdatedAt)) {
		return errors.New("dependency edge resolution timestamp is out of range")
	}
	return nil
}

// SameTarget reports whether two edges wait on the same node.
func (e DependencyEdge) SameTarget(other DependencyEdge) bool {
	e, other = e.Normalize(), other.Normalize()
	return e.RunID == other.RunID && e.TargetKind == other.TargetKind &&
		e.TargetID == other.TargetID
}

// SameSource reports whether two edges originate from the same node.
func (e DependencyEdge) SameSource(other DependencyEdge) bool {
	e, other = e.Normalize(), other.Normalize()
	return e.RunID == other.RunID && e.SourceKind == other.SourceKind &&
		e.SourceID == other.SourceID
}

// DependencyWake is the unique, durable wake receipt. At most one wake may
// exist per edge, so replays and recovery can never wake a source twice.
type DependencyWake struct {
	ID        string
	RunID     string
	EdgeID    string
	Outcome   AgentDependencyState
	Reason    string
	CreatedAt time.Time
}

func (w DependencyWake) Validate() error {
	if strings.TrimSpace(w.ID) == "" || len([]byte(w.ID)) > 256 || strings.ContainsRune(w.ID, 0) {
		return errors.New("dependency wake id is invalid")
	}
	if strings.TrimSpace(w.RunID) == "" || strings.TrimSpace(w.EdgeID) == "" {
		return errors.New("dependency wake run and edge are required")
	}
	if !ValidAgentDependencyStateForEdge(w.Outcome) || w.Outcome == AgentDependencyWait {
		return errors.New("dependency wake outcome must be terminal")
	}
	if strings.ContainsRune(w.Reason, 0) || len([]rune(w.Reason)) > MaxDependencyReasonRunes {
		return errors.New("dependency wake reason is invalid")
	}
	if w.CreatedAt.IsZero() {
		return errors.New("dependency wake timestamp is required")
	}
	return nil
}

// DependencyStallDiagnosis projects the stable deadlock/livelock diagnosis for
// one Run without mutating any ledger row.
type DependencyStallDiagnosis struct {
	RunID              string
	DetectedAt         time.Time
	DeadlockedEdgeIDs  []string
	LivelockedSourceIDs []string
}

func (d DependencyStallDiagnosis) Validate() error {
	if strings.TrimSpace(d.RunID) == "" || d.DetectedAt.IsZero() {
		return errors.New("dependency stall diagnosis requires a run and timestamp")
	}
	for _, id := range append(append([]string{}, d.DeadlockedEdgeIDs...), d.LivelockedSourceIDs...) {
		if strings.TrimSpace(id) == "" || len([]byte(id)) > 256 || strings.ContainsRune(id, 0) {
			return errors.New("dependency stall diagnosis identity is invalid")
		}
	}
	return nil
}
