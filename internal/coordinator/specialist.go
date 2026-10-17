package coordinator

import (
	"context"
	"errors"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
)

type SpecialistAdmissionPolicy struct {
	MaxChildren       int
	MaxTurnsPerChild  int64
	MaxTokensPerChild int64
}

func (p SpecialistAdmissionPolicy) Validate() error {
	if p.MaxChildren <= 0 || p.MaxChildren > domain.MaxAgentChildren {
		return errors.New("specialist admission capacity must be between one and two")
	}
	if p.MaxTurnsPerChild <= 0 || p.MaxTokensPerChild <= 0 {
		return errors.New("specialist admission per-child budgets must be positive")
	}
	if p.MaxTokensPerChild > domain.MaxAgentTokenReservation {
		return errors.New("specialist admission token policy exceeds the safe aggregate range")
	}
	return nil
}

type AdmitSpecialistRequest struct {
	Worker           *domain.WorkerProfile `json:"worker,omitempty"`
	AllowedReadPaths []string              `json:"allowed_read_paths,omitempty"`
	ToolCallLimit    int64                 `json:"tool_call_limit,omitempty"`
	CostLimitMicros  int64                 `json:"cost_limit_micros,omitempty"`
	RunID            string
	ParentAgentID    string
	Title            string
	Skills           []string
	TurnLimit        int64
	TokenLimit       int64
	IdempotencyKey   string
}

type AdmitSpecialistResult struct {
	Agent    domain.AgentNode
	Replayed bool
}

func NewWithSpecialistAdmission(store Store,
	policy SpecialistAdmissionPolicy,
) (*Coordinator, error) {
	if err := policy.Validate(); err != nil {
		return nil, apperror.Wrap(apperror.CodeInvalidArgument,
			"specialist admission policy is invalid", err)
	}
	coordinator := New(store)
	coordinator.specialistPolicy = &policy
	return coordinator, nil
}

func (c *Coordinator) AdmitSpecialist(ctx context.Context,
	req AdmitSpecialistRequest,
) (AdmitSpecialistResult, error) {
	if c == nil || c.store == nil {
		return AdmitSpecialistResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"agent coordinator store is required")
	}
	if c.specialistPolicy == nil {
		return AdmitSpecialistResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"specialist admission is disabled")
	}
	operationKey, err := domain.NormalizeAgentOperationKey(req.IdempotencyKey)
	if err != nil {
		return AdmitSpecialistResult{}, apperror.Wrap(apperror.CodeInvalidArgument,
			"specialist admission idempotency key is invalid", err)
	}
	skills, err := domain.NormalizeAgentSkills(req.Skills)
	if err != nil || len(skills) == 0 {
		return AdmitSpecialistResult{}, apperror.Wrap(apperror.CodeInvalidArgument,
			"specialist admission skills are invalid", err)
	}
	for _, skill := range skills {
		if !domain.DelegableAgentSkill(skill) {
			return AdmitSpecialistResult{}, apperror.New(apperror.CodeInvalidArgument,
				"specialist admission includes a non-delegable control capability")
		}
	}
	if req.TurnLimit <= 0 || req.TurnLimit > c.specialistPolicy.MaxTurnsPerChild ||
		req.TokenLimit <= 0 || req.TokenLimit > c.specialistPolicy.MaxTokensPerChild {
		return AdmitSpecialistResult{}, apperror.New(apperror.CodeInvalidArgument,
			"specialist admission exceeds its per-child budget policy")
	}
	worker, err := domain.NormalizeWorkerProfile(req.Worker)
	if err != nil {
		return AdmitSpecialistResult{}, apperror.Wrap(apperror.CodeInvalidArgument, "specialist worker profile is invalid", err)
	}
	admission := domain.SpecialistAdmission{
		Worker:  worker,
		AgentID: idgen.New("agent"), SessionID: idgen.New("sess"),
		RunID: strings.TrimSpace(req.RunID), ParentAgentID: strings.TrimSpace(req.ParentAgentID),
		Title: strings.TrimSpace(req.Title), Skills: skills, TurnLimit: req.TurnLimit,
		AllowedReadPaths: req.AllowedReadPaths, ToolCallLimit: req.ToolCallLimit, CostLimitMicros: req.CostLimitMicros,
		TokenLimit: req.TokenLimit, MaxChildren: c.specialistPolicy.MaxChildren,
		CreatedAt: time.Now().UTC(),
	}
	agent, replayed, err := c.store.AdmitSpecialist(ctx, admission, operationKey)
	return AdmitSpecialistResult{Agent: agent, Replayed: replayed}, apperror.Normalize(err)
}
