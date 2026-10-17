package httpapi

import (
	"net/http"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

func (a *API) runAgentGraph(request *http.Request, runID string) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	if _, err := a.store.GetRun(request.Context(), runID); err != nil {
		return nil, nil, err
	}
	nodes, err := a.store.ListAgentNodes(request.Context(), runID)
	if err != nil {
		return nil, nil, err
	}
	view := AgentGraphView{ProtocolVersion: domain.AgentGraphProtocolVersion,
		RunID: runID, Nodes: make([]AgentNodeView, 0, len(nodes))}
	for _, node := range nodes {
		if node.Role == domain.AgentRoleRoot {
			view.RootAgentID = node.ID
		}
		completion, found, err := a.store.GetAgentCompletion(request.Context(), node.ID)
		if err != nil {
			return nil, nil, err
		}
		if found {
			view.Nodes = append(view.Nodes, agentNodeView(node, &completion))
		} else {
			view.Nodes = append(view.Nodes, agentNodeView(node, nil))
		}
	}
	return view, nil, nil
}

func (a *API) runExternalSkills(request *http.Request, runID string) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	if _, err := a.store.GetRun(request.Context(), runID); err != nil {
		return nil, nil, err
	}
	projection, found, err := a.store.GetExternalSkillProjectionByRun(
		request.Context(), runID)
	if err != nil {
		return nil, nil, err
	}
	if !found {
		return nil, nil, apperror.New(apperror.CodeNotFound,
			"external Skill projection was not found in Run")
	}
	return externalSkillProjectionView(projection), nil, nil
}

func (a *API) runDelegations(request *http.Request, runID string) (any, *Page, error) {
	pageRequest, err := a.projectionPage(request, runID)
	if err != nil {
		return nil, nil, err
	}
	proposals, err := a.store.ListSpecialistDelegationProposalsPage(request.Context(),
		runID, pageRequest.Offset, pageRequest.Limit+1)
	if err != nil {
		return nil, nil, err
	}
	views := make([]DelegationView, 0, len(proposals))
	for _, proposal := range proposals {
		view, err := a.delegationView(request, proposal)
		if err != nil {
			return nil, nil, err
		}
		views = append(views, view)
	}
	views, page := trimPage(views, pageRequest)
	return views, page, nil
}

func (a *API) delegationView(request *http.Request,
	proposal domain.SpecialistDelegationProposal,
) (DelegationView, error) {
	view := DelegationView{ID: proposal.ID, RunID: proposal.RunID,
		RootAgentID: proposal.RootAgentID, Status: string(proposal.Status),
		RequestedBy: proposal.RequestedBy, CreatedAt: proposal.CreatedAt,
		Assignments: make([]DelegationAssignmentView, len(proposal.Spec.Assignments))}
	for index, assignment := range proposal.Spec.Assignments {
		view.Assignments[index] = DelegationAssignmentView{Worker: assignment.Worker, Ordinal: assignment.Ordinal,
			Title: assignment.Title, Goal: assignment.Goal,
			Skills: append([]string(nil), assignment.Skills...), TurnLimit: assignment.TurnLimit,
			TokenLimit: assignment.TokenLimit, AllowedReadPaths: assignment.AllowedReadPaths, ToolCallLimit: assignment.ToolCallLimit, CostLimitMicros: assignment.CostLimitMicros}
	}
	review, found, err := a.store.GetSpecialistDelegationReviewByProposal(request.Context(), proposal.ID)
	if err != nil {
		return DelegationView{}, err
	}
	if found {
		view.Review = &DelegationReviewView{ID: review.ID, Decision: string(review.Decision),
			ReviewedBy: review.ReviewedBy, CreatedAt: review.CreatedAt}
	}
	application, found, err := a.store.GetSpecialistDelegationApplicationByProposal(
		request.Context(), proposal.ID)
	if err != nil {
		return DelegationView{}, err
	}
	if !found {
		return view, nil
	}
	view.Application = &DelegationApplicationView{ID: application.ID,
		Status: string(application.Status), AssignmentCount: application.AssignmentCount,
		MaxChildren: application.MaxChildren, MaxTurnsPerChild: application.MaxTurnsPerChild,
		MaxTokensPerChild: application.MaxTokensPerChild, RequestedBy: application.RequestedBy,
		StopCode: application.StopCode, CreatedAt: application.CreatedAt,
		UpdatedAt: application.UpdatedAt, CompletedAt: application.CompletedAt}
	for index, assignment := range application.Assignments {
		if index >= len(view.Assignments) {
			break
		}
		view.Assignments[index].ApplicationStatus = string(assignment.Status)
		view.Assignments[index].AgentID = assignment.AgentID
	}
	scheduleRequest, found, err := a.store.GetLatestSpecialistOperatorScheduleRequestByApplication(
		request.Context(), application.ID)
	if err != nil || !found {
		return view, err
	}
	view.Schedule = &DelegationScheduleView{RequestID: scheduleRequest.ID,
		AgentIDs: append([]string(nil), scheduleRequest.AgentIDs...), MaxRounds: scheduleRequest.MaxRounds,
		RequestedBy: scheduleRequest.RequestedBy, RequestedAt: scheduleRequest.CreatedAt}
	schedule, attempt, found, err := a.store.GetLatestSpecialistOperatorScheduleAttempt(
		request.Context(), scheduleRequest.ID)
	if err != nil || !found {
		return view, err
	}
	view.Schedule.AttemptOrdinal = attempt.Ordinal
	view.Schedule.ScheduleID = schedule.ID
	view.Schedule.Status = string(schedule.Status)
	view.Schedule.RoundsCompleted = schedule.RoundsCompleted
	view.Schedule.TurnsStarted = schedule.TurnsStarted
	view.Schedule.RecoveredAttempts = schedule.RecoveredAttempts
	view.Schedule.StartedAt = &schedule.StartedAt
	view.Schedule.FinishedAt = schedule.FinishedAt
	return view, nil
}

func (a *API) runFanoutPlans(request *http.Request, runID string) (any, *Page, error) {
	pageRequest, err := a.projectionPage(request, runID)
	if err != nil {
		return nil, nil, err
	}
	plans, err := a.store.ListReadOnlyFanoutPlanSummariesPage(request.Context(), runID,
		pageRequest.Offset, pageRequest.Limit+1)
	if err != nil {
		return nil, nil, err
	}
	views := make([]FanoutPlanView, 0, len(plans))
	for _, plan := range plans {
		view := FanoutPlanView{ID: plan.ID, RunID: plan.RunID, WorkspaceID: plan.WorkspaceID,
			ScopePath: plan.ScopePath, Goal: plan.Goal, ProtocolVersion: plan.ProtocolVersion,
			RequestedTier: string(plan.RequestedTier), EffectiveParallelism: plan.EffectiveParallelism,
			Status: string(plan.Status), FileCount: plan.FileCount, TotalBytes: plan.TotalBytes,
			ExcludedCount: plan.ExcludedCount, ShardCount: plan.ShardCount,
			RequestedBy: plan.RequestedBy, CreatedAt: plan.CreatedAt}
		execution, found, err := a.store.GetLatestReadOnlyFanoutExecutionSummary(
			request.Context(), plan.ID)
		if err != nil {
			return nil, nil, err
		}
		if found {
			latest := fanoutExecutionView(execution)
			view.LatestExecution = &latest
		}
		views = append(views, view)
	}
	views, page := trimPage(views, pageRequest)
	return views, page, nil
}

func (a *API) projectionPage(request *http.Request, runID string) (pageRequest, error) {
	if err := validateSingleQueryValues(request.URL.Query(), "limit", "cursor"); err != nil {
		return pageRequest{}, err
	}
	if _, err := a.store.GetRun(request.Context(), runID); err != nil {
		return pageRequest{}, err
	}
	return parsePage(request.URL.Query(), request.URL.Path)
}

func fanoutExecutionView(value domain.ReadOnlyFanoutExecutionSummary) FanoutExecutionView {
	view := FanoutExecutionView{ID: value.ID, Status: string(value.Status),
		Parallelism: value.Parallelism, MaxOutputTokensPerShard: value.MaxOutputTokensPerShard,
		RequestedBy: value.RequestedBy, StopCode: value.StopCode, StartedAt: value.StartedAt,
		UpdatedAt: value.UpdatedAt, FinishedAt: value.FinishedAt,
		Shards: make([]FanoutExecutionShardView, len(value.Shards))}
	for index, shard := range value.Shards {
		view.Shards[index] = FanoutExecutionShardView{Ordinal: shard.Ordinal,
			Status: string(shard.Status), AttemptCount: shard.AttemptCount,
			CurrentAttempt: shard.CurrentAttempt, Provider: shard.Provider, Model: shard.Model,
			InputTokens: shard.InputTokens, OutputTokens: shard.OutputTokens,
			TotalTokens: shard.TotalTokens, ElapsedMillis: shard.ElapsedMillis,
			FindingCount: shard.FindingCount, ErrorCode: shard.ErrorCode,
			StartedAt: shard.StartedAt, FinishedAt: shard.FinishedAt}
	}
	return view
}

func (a *API) runFindingReports(request *http.Request, runID string) (any, *Page, error) {
	pageRequest, err := a.projectionPage(request, runID)
	if err != nil {
		return nil, nil, err
	}
	reports, err := a.store.ListFindingReportSummariesPage(request.Context(), runID,
		pageRequest.Offset, pageRequest.Limit+1)
	if err != nil {
		return nil, nil, err
	}
	views := make([]FindingReportSummaryView, len(reports))
	for index, report := range reports {
		views[index] = findingReportSummaryView(report)
	}
	views, page := trimPage(views, pageRequest)
	return views, page, nil
}

func (a *API) runFindingReport(request *http.Request,
	runID string, reportID string,
) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	if _, err := a.store.GetRun(request.Context(), runID); err != nil {
		return nil, nil, err
	}
	report, err := a.store.GetFindingReport(request.Context(), reportID)
	if err != nil {
		return nil, nil, err
	}
	if report.RunID != runID {
		return nil, nil, apperror.New(apperror.CodeNotFound, "Finding report was not found in Run")
	}
	return findingReportView(report), nil, nil
}

func findingReportView(report domain.FindingReport) FindingReportView {
	view := FindingReportView{Report: findingReportSummaryView(report.Summary()),
		Findings: make([]FindingView, len(report.Findings))}
	for index, finding := range report.Findings {
		current := FindingView{ID: finding.ID, Ordinal: finding.Ordinal,
			Status: string(finding.EffectiveStatus()), Severity: string(finding.Severity),
			Category: finding.Category, Title: finding.Title, Detail: finding.Detail,
			RelativePath: finding.RelativePath, LineStart: finding.LineStart,
			LineEnd: finding.LineEnd, Confidence: finding.Confidence,
			Evidence:            make([]FindingEvidenceView, len(finding.Evidence)),
			ArtifactEvidence:    artifactEvidenceViews(finding.ArtifactEvidence),
			RemediationEvidence: artifactEvidenceViews(finding.RemediationEvidence),
			Lifecycle: FindingLifecycleView{Status: string(finding.EffectiveStatus()),
				ValidationEvidenceCount:  len(finding.ArtifactEvidence),
				RemediationEvidenceCount: len(finding.RemediationEvidence)}}
		for evidenceIndex, evidence := range finding.Evidence {
			current.Evidence[evidenceIndex] = FindingEvidenceView{ID: evidence.ID,
				SourceID: evidence.SourceID, SourceShard: evidence.SourceShard,
				SourceOrdinal: evidence.SourceOrdinal, RelativePath: evidence.RelativePath,
				LineStart: evidence.LineStart, LineEnd: evidence.LineEnd,
				Confidence: evidence.Confidence}
		}
		if finding.Validation != nil {
			created := finding.Validation.CreatedAt
			current.Lifecycle.ValidationDecidedAt = &created
		}
		if finding.Acceptance != nil {
			created := finding.Acceptance.CreatedAt
			current.Lifecycle.AcceptedAt = &created
		}
		if finding.Fix != nil {
			created := finding.Fix.CreatedAt
			current.Lifecycle.FixedAt = &created
		}
		view.Findings[index] = current
	}
	return view
}

func artifactEvidenceViews(values []domain.FindingArtifactEvidence) []FindingArtifactEvidenceView {
	views := make([]FindingArtifactEvidenceView, len(values))
	for index, value := range values {
		views[index] = FindingArtifactEvidenceView{ID: value.ID, ArtifactID: value.ArtifactID,
			ArtifactSize: value.ArtifactSize, ArtifactMIME: value.ArtifactMIME,
			ArtifactStream: value.ArtifactStream, ArtifactRedacted: value.ArtifactRedacted,
			CreatedAt: value.CreatedAt}
	}
	return views
}
