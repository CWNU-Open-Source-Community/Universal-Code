package toolbudget

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxIdentityRunes       = 256
	RequesterRunSupervisor = "run_supervisor"
)

type ChargeRequest struct {
	AgentID         string
	AgentAttemptID  string
	ToolCallID      string
	RunID           string
	SessionID       string
	WorkspaceID     string
	ToolName        string
	ActionClass     string
	LeaseID         string
	LeaseGeneration int64
	RequestedBy     string
}

func (r ChargeRequest) Normalize() (ChargeRequest, error) {
	r.RunID = strings.TrimSpace(r.RunID)
	r.AgentID = strings.TrimSpace(r.AgentID)
	r.AgentAttemptID = strings.TrimSpace(r.AgentAttemptID)
	r.ToolCallID = strings.TrimSpace(r.ToolCallID)
	r.SessionID = strings.TrimSpace(r.SessionID)
	r.WorkspaceID = strings.TrimSpace(r.WorkspaceID)
	r.ToolName = strings.TrimSpace(r.ToolName)
	r.ActionClass = strings.TrimSpace(r.ActionClass)
	r.LeaseID = strings.TrimSpace(r.LeaseID)
	r.RequestedBy = strings.TrimSpace(r.RequestedBy)
	for label, value := range map[string]string{
		"run id": r.RunID, "session id": r.SessionID, "workspace id": r.WorkspaceID,
		"tool name": r.ToolName, "action class": r.ActionClass, "lease id": r.LeaseID,
		"requester": r.RequestedBy,
		"agent id":  r.AgentID, "agent attempt id": r.AgentAttemptID, "tool call id": r.ToolCallID,
	} {
		if !utf8.ValidString(value) || len([]rune(value)) > MaxIdentityRunes {
			return ChargeRequest{}, fmt.Errorf("tool budget %s must be bounded UTF-8", label)
		}
	}
	if r.ToolName == "" || r.ActionClass == "" {
		return ChargeRequest{}, errors.New("tool budget tool name and action class are required")
	}
	if (r.LeaseID == "") != (r.LeaseGeneration == 0) || r.LeaseGeneration < 0 {
		return ChargeRequest{}, errors.New("tool budget execution lease identity and generation are inconsistent")
	}
	if r.RequestedBy == RequesterRunSupervisor && (r.RunID == "" || r.LeaseID == "") {
		return ChargeRequest{}, errors.New("run supervisor tool budget charge requires a Run execution lease")
	}
	if r.RequestedBy == "specialist_runner" && (r.RunID == "" || r.LeaseID == "" || r.AgentID == "" || r.AgentAttemptID == "" || r.ToolCallID == "") {
		return ChargeRequest{}, errors.New("Specialist budget charge requires its fenced recorded child call")
	}
	return r, nil
}

type Usage struct {
	RunID       string
	Consumed    int64
	Limit       int64
	Remaining   int64
	LastCharge  string
	LastUpdated time.Time
	ExhaustedAt *time.Time
	Tracked     bool
}

type Store interface {
	ChargeToolCall(ctx context.Context, request ChargeRequest) (Usage, error)
	GetToolCallUsage(ctx context.Context, runID string) (Usage, error)
}
