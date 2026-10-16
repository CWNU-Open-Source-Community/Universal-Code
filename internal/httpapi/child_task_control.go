package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

const (
	ChildTaskProposalsPathTemplate        = "/api/v1/runs/{run_id}/child-task-proposals"
	ChildTaskProposalReviewPathTemplate   = "/api/v1/runs/{run_id}/child-task-proposals/{proposal_id}/review"
	ChildTaskProposalAdmitPathTemplate    = "/api/v1/runs/{run_id}/child-task-proposals/{proposal_id}/admit"
	ChildTaskProposalsListProtocolVersion = "child_task_proposals.v1"
)

// ChildTaskControlController is the bounded operator review/admission
// surface for model-proposed child task proposals.
type ChildTaskControlController interface {
	ListChildTaskProposals(context.Context, string, int) ([]domain.ChildTaskProposal, error)
	ListChildTaskAssignments(context.Context, string) ([]domain.ChildTaskAssignment, error)
	ReviewChildTaskProposal(context.Context, string, domain.ChildTaskReview, string) (domain.ChildTaskProposal, bool, error)
	AdmitChildTaskProposal(context.Context, string, string, string) (domain.ChildTaskProposal, []domain.ChildTaskAssignment, error)
}

type ChildTaskReviewRequestView struct {
	Version       string `json:"version"`
	Action        string `json:"action"`
	Reviewer      string `json:"reviewer"`
	FanoutTier    string `json:"fanout_tier"`
	ConfirmReview bool   `json:"confirm_review"`
}

type ChildTaskAdmitRequestView struct {
	Version      string `json:"version"`
	ConfirmAdmit bool   `json:"confirm_admit"`
}

type ChildTaskTaskView struct {
	AllowedReadPaths   []string `json:"allowed_read_paths,omitempty"`
	ToolCallLimit      int64    `json:"tool_call_limit,omitempty"`
	CostLimitMicros    int64    `json:"cost_limit_micros,omitempty"`
	Ordinal            int      `json:"ordinal"`
	Title              string   `json:"title"`
	Goal               string   `json:"goal"`
	Skills             []string `json:"skills"`
	InputRefs          []string `json:"input_refs"`
	DependencyOrdinals []int    `json:"dependency_ordinals"`
	SurfaceHint        string   `json:"surface_hint"`
	TurnLimit          int64    `json:"turn_limit"`
	TokenLimit         int64    `json:"token_limit"`
	TimeoutMillis      int64    `json:"timeout_millis"`
}

type ChildTaskAssignmentView struct {
	Ordinal         int    `json:"ordinal"`
	Surface         string `json:"surface"`
	FanoutTier      string `json:"fanout_tier"`
	Status          string `json:"status"`
	TurnLimit       int64  `json:"turn_limit"`
	TokenLimit      int64  `json:"token_limit"`
	TimeoutMillis   int64  `json:"timeout_millis"`
	AdmittedAgentID string `json:"admitted_agent_id,omitempty"`
	FanoutPlanID    string `json:"fanout_plan_id,omitempty"`
}

type ChildTaskProposalView struct {
	ID          string                    `json:"id"`
	RunID       string                    `json:"run_id"`
	RootAgentID string                    `json:"root_agent_id"`
	Status      string                    `json:"status"`
	Surface     string                    `json:"surface"`
	FanoutTier  string                    `json:"fanout_tier"`
	RequestedBy string                    `json:"requested_by"`
	CreatedAt   time.Time                 `json:"created_at"`
	Tasks       []ChildTaskTaskView       `json:"tasks"`
	Assignments []ChildTaskAssignmentView `json:"assignments"`
}

type ChildTaskProposalsListView struct {
	ProtocolVersion string                  `json:"protocol_version"`
	Items           []ChildTaskProposalView `json:"items"`
}

func (a *API) listChildTaskProposals(request *http.Request, runID string) (any, *Page, error) {
	if err := validateSingleQueryValues(request.URL.Query(), "limit"); err != nil {
		return nil, nil, err
	}
	if _, err := a.store.GetRun(request.Context(), runID); err != nil {
		return nil, nil, err
	}
	if a.childTaskControlController == nil {
		return nil, nil, apperror.New(apperror.CodeNotFound, "HTTP API endpoint was not found")
	}
	proposals, err := a.childTaskControlController.ListChildTaskProposals(
		request.Context(), runID, 50)
	if err != nil {
		return nil, nil, err
	}
	view := ChildTaskProposalsListView{ProtocolVersion: ChildTaskProposalsListProtocolVersion,
		Items: make([]ChildTaskProposalView, 0, len(proposals))}
	for _, proposal := range proposals {
		item := ChildTaskProposalView{ID: proposal.ID, RunID: proposal.RunID,
			RootAgentID: proposal.RootAgentID, Status: proposal.Status,
			Surface: string(proposal.Surface), FanoutTier: string(proposal.FanoutTier),
			RequestedBy: proposal.RequestedBy, CreatedAt: proposal.CreatedAt,
			Tasks:       make([]ChildTaskTaskView, 0, len(proposal.Spec.Tasks)),
			Assignments: make([]ChildTaskAssignmentView, 0, len(proposal.Spec.Tasks))}
		for _, task := range proposal.Spec.Tasks {
			item.Tasks = append(item.Tasks, ChildTaskTaskView{Ordinal: task.Ordinal,
				Title: task.Title, Goal: task.Goal, Skills: task.Skills,
				InputRefs: task.InputRefs, DependencyOrdinals: task.DependencyOrdinals,
				SurfaceHint: string(task.SurfaceHint), TurnLimit: task.TurnLimit,
				TokenLimit: task.TokenLimit, TimeoutMillis: task.TimeoutMillis, AllowedReadPaths: task.AllowedReadPaths, ToolCallLimit: task.ToolCallLimit, CostLimitMicros: task.CostLimitMicros})
		}
		if assignments, listErr := a.childTaskControlController.ListChildTaskAssignments(
			request.Context(), proposal.ID); listErr == nil {
			for _, assignment := range assignments {
				item.Assignments = append(item.Assignments, ChildTaskAssignmentView{
					Ordinal: assignment.Ordinal, Surface: string(assignment.Surface),
					FanoutTier: string(assignment.FanoutTier), Status: assignment.Status,
					TurnLimit: assignment.TurnLimit, TokenLimit: assignment.TokenLimit,
					TimeoutMillis:   assignment.TimeoutMillis,
					AdmittedAgentID: assignment.AdmittedAgentID, FanoutPlanID: assignment.FanoutPlanID})
			}
		}
		view.Items = append(view.Items, item)
	}
	return view, nil, nil
}

func matchChildTaskProposalReviewPath(requestPath string) (string, string, bool) {
	return matchChildTaskProposalActionPath(requestPath, "review")
}

func matchChildTaskProposalAdmitPath(requestPath string) (string, string, bool) {
	return matchChildTaskProposalActionPath(requestPath, "admit")
}

func matchChildTaskProposalActionPath(requestPath, action string) (string, string, bool) {
	const prefix = "/api/v1/runs/"
	if !strings.HasPrefix(requestPath, prefix) {
		return "", "", false
	}
	segments := strings.Split(strings.TrimPrefix(requestPath, prefix), "/")
	if len(segments) != 4 || segments[0] == "" || segments[1] != "child-task-proposals" ||
		segments[2] == "" || segments[3] != action {
		return "", "", false
	}
	return segments[0], segments[2], true
}

func (a *API) serveChildTaskProposalReview(writer http.ResponseWriter, request *http.Request,
	requestID, runID, proposalID string,
) {
	if a.childTaskControlController == nil {
		a.writeError(writer, requestID,
			apperror.New(apperror.CodeNotFound, "HTTP API endpoint was not found"),
			http.StatusNotFound)
		return
	}
	if !a.authorizeRunOperation(writer, request, requestID, a.modelControlEnabled,
		"child task proposal review") {
		return
	}
	var body ChildTaskReviewRequestView
	strictBody, err := readStrictControlBody(request, "child task proposal review")
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	if err := decodeStrictRunOperation(strictBody, &body, "child task proposal review"); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if body.Version != "child_task_review.v1" || !body.ConfirmReview ||
		(body.Action != "approve" && body.Action != "deny") ||
		strings.TrimSpace(body.Reviewer) == "" {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"child task review requires explicit confirmation, an action, and a reviewer"),
			http.StatusBadRequest)
		return
	}
	tier := domain.ReadOnlyFanoutTwo
	if parsed, parseErr := domain.ParseReadOnlyFanoutTier(body.FanoutTier); parseErr == nil &&
		parsed != domain.ReadOnlyFanoutAuto && parsed != "" {
		tier = parsed
	}
	proposal, _, err := a.childTaskControlController.ReviewChildTaskProposal(request.Context(), runID,
		domain.ChildTaskReview{ProposalID: proposalID, Action: body.Action,
			Reviewer: body.Reviewer, FanoutTier: tier}, requestID)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	view, _, err := a.childTaskProposalView(request.Context(), proposal)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccessStatus(writer, requestID, view, nil, http.StatusOK)
}

func (a *API) serveChildTaskProposalAdmit(writer http.ResponseWriter, request *http.Request,
	requestID, runID, proposalID string,
) {
	if a.childTaskControlController == nil {
		a.writeError(writer, requestID,
			apperror.New(apperror.CodeNotFound, "HTTP API endpoint was not found"),
			http.StatusNotFound)
		return
	}
	if !a.authorizeRunOperation(writer, request, requestID, a.modelControlEnabled,
		"child task proposal admission") {
		return
	}
	var body ChildTaskAdmitRequestView
	strictBody, err := readStrictControlBody(request, "child task proposal admission")
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	if err := decodeStrictRunOperation(strictBody, &body, "child task proposal admission"); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if body.Version != "child_task_admit.v1" || !body.ConfirmAdmit {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"child task admission requires explicit confirmation"), http.StatusBadRequest)
		return
	}
	proposal, assignments, err := a.childTaskControlController.AdmitChildTaskProposal(
		request.Context(), runID, proposalID, requestID)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	view, _, err := a.childTaskProposalView(request.Context(), proposal)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	_ = assignments
	a.writeSuccessStatus(writer, requestID, view, nil, http.StatusOK)
}

func (a *API) childTaskProposalView(ctx context.Context, proposal domain.ChildTaskProposal,
) (ChildTaskProposalView, bool, error) {
	view := ChildTaskProposalView{ID: proposal.ID, RunID: proposal.RunID,
		RootAgentID: proposal.RootAgentID, Status: proposal.Status,
		Surface: string(proposal.Surface), FanoutTier: string(proposal.FanoutTier),
		RequestedBy: proposal.RequestedBy, CreatedAt: proposal.CreatedAt,
		Tasks: make([]ChildTaskTaskView, 0, len(proposal.Spec.Tasks))}
	for _, task := range proposal.Spec.Tasks {
		view.Tasks = append(view.Tasks, ChildTaskTaskView{Ordinal: task.Ordinal,
			Title: task.Title, Goal: task.Goal, Skills: task.Skills,
			InputRefs: task.InputRefs, DependencyOrdinals: task.DependencyOrdinals,
			SurfaceHint: string(task.SurfaceHint), TurnLimit: task.TurnLimit,
			TokenLimit: task.TokenLimit, TimeoutMillis: task.TimeoutMillis, AllowedReadPaths: task.AllowedReadPaths, ToolCallLimit: task.ToolCallLimit, CostLimitMicros: task.CostLimitMicros})
	}
	return view, true, nil
}
