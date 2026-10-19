package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runactivity"
	"cyberagent-workbench/internal/threadtranscript"
)

const (
	ThreadCollectionPath                 = "/api/v1/threads"
	ThreadTurnControlPathTemplate        = "/api/v1/threads/{thread_id}/turns"
	ThreadExecutionPathTemplate          = "/api/v1/threads/{thread_id}/execution"
	ThreadInterruptPathTemplate          = "/api/v1/threads/{thread_id}/interrupt"
	ThreadRunRecoveryControlPathTemplate = "/api/v1/threads/{thread_id}/recovery"
	ThreadActivityDetailPathTemplate     = "/api/v1/threads/{thread_id}/activities/{activity_ref}"
	ThreadActivityArtifactPathTemplate   = "/api/v1/threads/{thread_id}/activities/{activity_ref}/artifacts/{artifact_id}"
)

type ThreadTurnController interface {
	Execute(context.Context, application.ExecuteThreadTurnRequest) (
		application.ExecuteThreadTurnResult, error)
}

type ThreadCreationControlRequestView struct {
	Version           string                     `json:"version"`
	Goal              string                     `json:"goal"`
	WorkspaceID       string                     `json:"workspace_id,omitempty"`
	Profile           string                     `json:"profile,omitempty"`
	Surface           string                     `json:"surface,omitempty"`
	Phase             string                     `json:"phase,omitempty"`
	NetworkMode       string                     `json:"network_mode,omitempty"`
	AllowedTargets    []string                   `json:"allowed_targets,omitempty"`
	WebAccessMode     domain.WebAccessMode       `json:"web_access_mode,omitempty"`
	WebAllowedTargets []string                   `json:"web_allowed_targets,omitempty"`
	Budget            *domain.TaskBudgetSettings `json:"budget,omitempty"`
	Provider          string                     `json:"provider,omitempty"`
	Model             string                     `json:"model,omitempty"`
}

type ThreadCreationControlView struct {
	Thread   ThreadView  `json:"thread"`
	Mission  MissionView `json:"mission"`
	Run      RunView     `json:"run"`
	Session  SessionView `json:"session"`
	Mode     RunModeView `json:"mode"`
	Replayed bool        `json:"replayed"`
}

type ThreadMessageControlRequestView struct {
	Version string `json:"version"`
	Content string `json:"content"`
}

type ThreadTurnControlRequestView struct {
	Version       string                           `json:"version"`
	Content       string                           `json:"content"`
	Files         []domain.WorkspaceFileReference  `json:"files,omitempty"`
	Images        []domain.ImageReference          `json:"images,omitempty"`
	Attachments   []domain.FileAttachmentReference `json:"attachments,omitempty"`
	MCPReferences []domain.MCPReference            `json:"mcp_references,omitempty"`
}

type ThreadMessageControlView struct {
	Version          string                      `json:"version"`
	Thread           ThreadView                  `json:"thread"`
	RunID            string                      `json:"run_id"`
	SessionID        string                      `json:"session_id"`
	PredecessorRunID string                      `json:"predecessor_run_id,omitempty"`
	SuccessorCreated bool                        `json:"successor_created"`
	Steering         OperatorSteeringMessageView `json:"steering"`
	Replayed         bool                        `json:"replayed"`
	ExecutionStarted bool                        `json:"execution_started"`
	ModelCalled      bool                        `json:"model_called"`
	ToolCalled       bool                        `json:"tool_called"`
	CapabilityGrant  bool                        `json:"capability_grant"`
}

type ThreadLifecycleControlRequestView struct {
	Version         string `json:"version"`
	ExpectedVersion int64  `json:"expected_version"`
}

type ThreadLifecycleControlView struct {
	Version         string     `json:"version"`
	Thread          ThreadView `json:"thread"`
	CapabilityGrant bool       `json:"capability_grant"`
}

type ThreadRunRecoveryControlRequestView struct {
	Version            string `json:"version"`
	RunID              string `json:"run_id"`
	HandoffOperationID string `json:"handoff_operation_id"`
}

type ThreadRunRecoveryControlView struct {
	Version           string     `json:"version"`
	Thread            ThreadView `json:"thread"`
	FailedRun         RunView    `json:"failed_run"`
	SuccessorRequired bool       `json:"successor_required"`
	Replayed          bool       `json:"replayed"`
}

func (a *API) routeThreads(request *http.Request, segments []string) (any, *Page, error) {
	switch len(segments) {
	case 1:
		return a.threads(request)
	case 2:
		if segments[1] == "search" {
			return a.searchThreadMessages(request)
		}
		if segments[1] == "creation-request" {
			return a.threadRequestObservation(request, "")
		}
		return a.thread(request, segments[1])
	case 3:
		switch segments[2] {
		case "plan":
			return a.threadPlanObservation(request, segments[1])
		case "review":
			return a.threadReviewView(request, segments[1])
		case "changes":
			return a.threadChanges(request, segments[1])
		case "turn-request":
			return a.threadRequestObservation(request, segments[1])
		case "messages":
			return a.threadMessages(request, segments[1])
		case "queued-messages":
			return a.threadQueuedMessages(request, segments[1])
		case "runs":
			return a.threadRuns(request, segments[1])
		case "transcript":
			return a.threadTranscript(request, segments[1])
		case "export":
			return a.threadExport(request, segments[1])
		case "execution":
			controller, ok := a.threadTurnController.(ThreadExecutionController)
			if !ok || !a.runExecutionEnabled {
				return nil, nil, apperror.New(apperror.CodeNotFound, "Thread execution control is unavailable")
			}
			state, err := controller.ExecutionState(request.Context(), segments[1])
			return state, nil, err
		}
	case 4:
		if segments[2] == "messages" {
			return a.savedThreadMessage(request, segments[1], segments[3])
		}
		if segments[2] == "activities" {
			return a.threadActivityDetail(request, segments[1], segments[3])
		}
	case 6:
		if segments[2] == "activities" && segments[4] == "artifacts" {
			return a.threadActivityArtifact(request, segments[1], segments[3], segments[5])
		}
	}
	return nil, nil, apperror.New(apperror.CodeNotFound,
		"Thread HTTP API endpoint was not found")
}

func (a *API) threadActivityDetail(request *http.Request, threadID,
	activityRef string,
) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	detail, err := application.NewThreadActivityDetailService(a.store).
		WithCommandRuntimeSource(a.commandActivitySource).
		Get(request.Context(), threadID, activityRef)
	if err != nil {
		return nil, nil, err
	}
	return threadActivityDetailView(detail), nil, nil
}

func (a *API) threadActivityArtifact(request *http.Request, threadID,
	activityRef, artifactRef string,
) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	value, err := application.NewThreadActivityDetailService(a.store).
		WithCommandRuntimeSource(a.commandActivitySource).
		GetArtifact(request.Context(), threadID, activityRef, artifactRef)
	if err != nil {
		return nil, nil, err
	}
	return threadActivityArtifactView(value), nil, nil
}

func (a *API) threadTranscript(request *http.Request, threadID string) (any, *Page, error) {
	values := request.URL.Query()
	if err := validateSingleQueryValues(values, "limit", "cursor"); err != nil {
		return nil, nil, err
	}
	if _, err := a.store.GetThread(request.Context(), threadID); err != nil {
		return nil, nil, err
	}
	pageRequest, err := parseThreadTranscriptPage(values, request.URL.Path)
	if err != nil {
		return nil, nil, err
	}
	source, err := a.store.ListThreadTranscriptSourceBefore(request.Context(), threadID,
		pageRequest.BeforeOrdinal, pageRequest.BeforeSequence, pageRequest.Limit+1)
	if err != nil {
		return nil, nil, err
	}
	hasMore := len(source) > pageRequest.Limit
	if hasMore {
		source = source[:pageRequest.Limit]
	}
	buildSource := make([]threadtranscript.Source, 0, len(source))
	peerStore, _ := a.store.(interface {
		ThreadPeerMessageForSteering(context.Context, string) (domain.ThreadPeerMessage, bool, error)
		GetThreadPeerMessage(context.Context, string) (domain.ThreadPeerMessage, error)
	})
	for _, record := range source {
		if peerStore != nil && record.Event != nil && record.Event.Type == "operator.steering_queued" {
			_, found, lookupErr := peerStore.ThreadPeerMessageForSteering(request.Context(), record.Event.SubjectID)
			if lookupErr != nil {
				return nil, nil, lookupErr
			}
			if found {
				continue
			}
		}
		buildSource = append(buildSource, record)
	}
	items, err := threadtranscript.Build(threadID, buildSource)
	if err != nil {
		return nil, nil, apperror.Wrap(apperror.CodeFailedPrecondition,
			"durable Thread transcript could not be projected", err)
	}
	views := make([]ThreadTranscriptItemView, len(items))
	detailService := application.NewThreadActivityDetailService(a.store).
		WithCommandRuntimeSource(a.commandActivitySource)
	summaries := make(map[string]*ThreadActivitySummaryView)
	for index := range items {
		views[index] = threadTranscriptItemView(items[index])
		if items[index].Source == "peer" && peerStore != nil {
			peer, lookupErr := peerStore.GetThreadPeerMessage(request.Context(), items[index].SourceRef)
			if lookupErr != nil {
				return nil, nil, lookupErr
			}
			views[index].PeerMessage = &peer
			views[index].Detail = peer.Content
			views[index].Status = peer.Status
			switch peer.Status {
			case "delivered":
				views[index].Stage = "result"
			case "failed", "cancelled":
				views[index].Stage = "blocked"
			}
		}
		if items[index].Source == runactivity.SourceOperator && items[index].SourceRef != "" {
			if imageStore, ok := a.store.(workspaceImageStore); ok {
				images, imageErr := imageStore.ListOperatorMessageImages(request.Context(), items[index].RunID, items[index].SourceRef)
				if imageErr != nil {
					return nil, nil, imageErr
				}
				views[index].Images = images
			}
			if reader, ok := a.store.(interface {
				ListOperatorMessageAttachments(context.Context, string, string) ([]domain.WorkspaceFileAttachment, error)
			}); ok {
				attachments, err := reader.ListOperatorMessageAttachments(request.Context(), items[index].RunID, items[index].SourceRef)
				if err != nil {
					return nil, nil, err
				}
				views[index].Attachments = attachments
			}
		}
		activityRef := views[index].ActivityDetailRef
		if activityRef == "" {
			continue
		}
		if views[index].ToolName != "command_runtime" {
			continue
		}
		if summary, found := summaries[activityRef]; found {
			views[index].ActivitySummary = summary
			continue
		}
		summary, summaryErr := detailService.Summary(request.Context(), threadID, activityRef)
		if summaryErr != nil {
			// Transcript availability must not depend on optional presentation
			// detail. The Thread-scoped detail endpoint remains authoritative.
			summaries[activityRef] = nil
			continue
		}
		views[index].ActivitySummary = threadActivitySummaryView(summary)
		summaries[activityRef] = views[index].ActivitySummary
	}
	return views, threadTranscriptPage(pageRequest, source, hasMore), nil
}

func (a *API) threads(request *http.Request) (any, *Page, error) {
	values := request.URL.Query()
	if err := validateSingleQueryValues(values, "limit", "cursor", "status",
		"include_deleted", "q"); err != nil {
		return nil, nil, err
	}
	if query, exists := values["q"]; exists && len(query) != 1 {
		return nil, nil, apperror.New(apperror.CodeInvalidArgument,
			"query parameter \"q\" must appear exactly once")
	}
	rawQuery, _ := singleQueryValue(values, "q")
	titleQuery, err := domain.NormalizeThreadTitleQuery(rawQuery)
	if err != nil {
		return nil, nil, apperror.New(apperror.CodeInvalidArgument, err.Error())
	}
	if titleQuery == "" {
		values.Del("q")
	} else {
		values.Set("q", titleQuery)
	}
	pageRequest, err := parseStableListPage(values, request.URL.Path)
	if err != nil {
		return nil, nil, err
	}
	filter := domain.ThreadFilter{Limit: stableListStoreLimit(pageRequest), TitleQuery: titleQuery}
	if raw, ok := singleQueryValue(values, "status"); ok {
		filter.Status = domain.ThreadStatus(strings.ToLower(raw))
		if !domain.ValidThreadStatus(filter.Status) {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument,
				"invalid Thread status filter")
		}
	}
	if raw, ok := singleQueryValue(values, "include_deleted"); ok {
		filter.IncludeDeleted, err = strconv.ParseBool(raw)
		if err != nil {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument,
				"include_deleted must be true or false")
		}
	}
	items, err := a.store.ListThreadsByCreationPage(request.Context(), filter,
		pageRequest.Anchor.BeforeCreatedAt, pageRequest.Anchor.BeforeID)
	if err != nil {
		return nil, nil, err
	}
	items, page := trimStableListPage(items, pageRequest, func(item domain.Thread) (time.Time, string) {
		return item.CreatedAt, item.ID
	})
	views := a.threadListViews(request.Context(), items)
	return views, page, nil
}

func (a *API) threadListViews(ctx context.Context, items []domain.Thread) []ThreadView {
	ids := make([]string, len(items))
	for index := range items {
		ids[index] = items[index].ID
	}
	facts, factsErr := a.store.GetThreadExecutionFacts(ctx, ids)
	var live map[string]application.ThreadExecutionState
	if observer, ok := a.threadTurnController.(ThreadExecutionSnapshotController); ok && a.runExecutionEnabled {
		states, err := observer.ExecutionStates(ctx, ids)
		if err == nil {
			live = states
		}
	}
	views := make([]ThreadView, len(items))
	for index, item := range items {
		fact, found := facts[item.ID]
		expectedRunID := item.ActiveRunID
		if expectedRunID == "" {
			expectedRunID = item.LastRunID
		}
		validFact := factsErr == nil && found && fact.RunID == expectedRunID && domain.ValidRunStatus(fact.RunStatus)
		if validFact {
			views[index] = threadView(item, fact.RunStatus)
			if fact.PendingApproval && item.Status == domain.ThreadActive && item.ActiveRunID != "" {
				views[index].ComposerState = "waiting_approval"
			}
		} else {
			views[index] = threadView(item)
			views[index].ComposerState = "unavailable"
		}
		state, observed := live[item.ID]
		views[index].ExecutionState = threadListExecutionState(state, observed &&
			state.ThreadID == item.ID && state.Version == application.ThreadExecutionProtocolVersion, fact, validFact)
	}
	return views
}

func threadListExecutionState(live application.ThreadExecutionState, observed bool,
	fact domain.ThreadExecutionFacts, validFact bool,
) string {
	if !observed {
		return "unknown"
	}
	switch live.State {
	case "running", "stopping", "stop_failed":
		return live.State
	case "idle":
		if !validFact || fact.Unsettled {
			return "unknown"
		}
		switch fact.RunStatus {
		case domain.RunCompleted, domain.RunFailed, domain.RunCancelled:
			return string(fact.RunStatus)
		}
		if fact.PendingApproval {
			return "waiting_approval"
		}
		switch fact.RunStatus {
		case domain.RunWaitingApproval, domain.RunPaused:
			return string(fact.RunStatus)
		case domain.RunCreated, domain.RunPreparing, domain.RunRunning:
			return "idle"
		}
	}
	return "unknown"
}

func (a *API) thread(request *http.Request, threadID string) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	threadRecord, err := a.store.GetThread(request.Context(), threadID)
	if err != nil {
		return nil, nil, err
	}
	mission, err := a.store.GetMission(request.Context(), threadRecord.MissionID)
	if err != nil {
		return nil, nil, err
	}
	bindings, err := a.store.ListThreadRuns(request.Context(), threadRecord.ID)
	if err != nil {
		return nil, nil, err
	}
	runs := make([]ThreadRunView, len(bindings))
	var active *RunView
	var activeStatus domain.RunStatus
	var last RunView
	for index, binding := range bindings {
		run, err := a.store.GetRun(request.Context(), binding.RunID)
		if err != nil {
			return nil, nil, err
		}
		view := runView(run)
		if run.ID == threadRecord.ActiveRunID || run.ID == threadRecord.LastRunID {
			if err := a.projectStandardCodePreset(request.Context(), &view); err != nil {
				return nil, nil, err
			}
		}
		runs[index] = ThreadRunView{Run: view, Ordinal: binding.Ordinal,
			PredecessorRunID: binding.PredecessorRunID, CreatedAt: binding.CreatedAt}
		if run.ID == threadRecord.ActiveRunID {
			copy := view
			active = &copy
			activeStatus = run.Status
		}
		if run.ID == threadRecord.LastRunID {
			last = view
		}
	}
	view := threadView(threadRecord, activeStatus)
	var recoveryView *ThreadRunRecoveryView
	if recovery, found, recoveryErr := application.NewThreadRunRecoveryService(a.store).
		Get(request.Context(), threadRecord.ID); recoveryErr != nil {
		return nil, nil, recoveryErr
	} else if found {
		projected := threadRunRecoveryView(recovery)
		recoveryView = &projected
	}
	detail := ThreadDetailView{Thread: view, Mission: missionView(mission),
		ActiveRun: active, LastRun: last, Runs: runs, Recovery: recoveryView}
	if reader, ok := a.store.(interface {
		GetThreadStatistics(context.Context, string) (domain.ThreadStatistics, error)
	}); ok {
		statistics, err := reader.GetThreadStatistics(request.Context(), threadRecord.ID)
		if err != nil {
			return nil, nil, err
		}
		detail.Statistics = &statistics
	}
	return detail, nil, nil
}

func (a *API) threadRuns(request *http.Request, threadID string) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	if _, err := a.store.GetThread(request.Context(), threadID); err != nil {
		return nil, nil, err
	}
	bindings, err := a.store.ListThreadRuns(request.Context(), threadID)
	if err != nil {
		return nil, nil, err
	}
	views := make([]ThreadRunView, len(bindings))
	for index, binding := range bindings {
		run, err := a.store.GetRun(request.Context(), binding.RunID)
		if err != nil {
			return nil, nil, err
		}
		views[index] = ThreadRunView{Run: runView(run), Ordinal: binding.Ordinal,
			PredecessorRunID: binding.PredecessorRunID, CreatedAt: binding.CreatedAt}
	}
	return views, nil, nil
}

func (a *API) threadMessages(request *http.Request, threadID string) (any, *Page, error) {
	values := request.URL.Query()
	if err := validateSingleQueryValues(values, "limit", "cursor", "include_compacted"); err != nil {
		return nil, nil, err
	}
	if _, err := a.store.GetThread(request.Context(), threadID); err != nil {
		return nil, nil, err
	}
	pageRequest, err := parsePage(values, request.URL.Path)
	if err != nil {
		return nil, nil, err
	}
	includeCompacted := false
	if raw, ok := singleQueryValue(values, "include_compacted"); ok {
		includeCompacted, err = strconv.ParseBool(raw)
		if err != nil {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument,
				"include_compacted must be true or false")
		}
	}
	messages, err := a.store.ListThreadMessagesPage(request.Context(), threadID,
		includeCompacted, pageRequest.Offset, pageRequest.Limit+1)
	if err != nil {
		return nil, nil, err
	}
	views := make([]ThreadMessageView, len(messages))
	for index := range messages {
		views[index] = threadMessageView(messages[index])
	}
	views, page := trimPage(views, pageRequest)
	return views, page, nil
}

func (a *API) threadExport(request *http.Request, threadID string) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	value, err := a.store.ExportThread(request.Context(), threadID)
	if err != nil {
		return nil, nil, err
	}
	bindingsByRun := make(map[string]domain.ThreadRun, len(value.Bindings))
	for _, binding := range value.Bindings {
		bindingsByRun[binding.RunID] = binding
	}
	runs := make([]ThreadRunView, len(value.Runs))
	for index, run := range value.Runs {
		binding := bindingsByRun[run.ID]
		runs[index] = ThreadRunView{Run: runView(run), Ordinal: binding.Ordinal,
			PredecessorRunID: binding.PredecessorRunID, CreatedAt: binding.CreatedAt}
	}
	sessions := make([]SessionView, len(value.Sessions))
	for index, linkedSession := range value.Sessions {
		sessions[index] = SessionView{ID: linkedSession.ID,
			WorkspaceID: linkedSession.WorkspaceID, Title: linkedSession.Title,
			Route: linkedSession.Route, Status: linkedSession.Status,
			CreatedAt: linkedSession.CreatedAt, UpdatedAt: linkedSession.UpdatedAt}
	}
	messages := make([]ThreadMessageView, len(value.Messages))
	for index := range value.Messages {
		messages[index] = threadMessageView(value.Messages[index])
	}
	eventViews := make([]ThreadEventView, len(value.Events))
	for index, event := range value.Events {
		eventViews[index] = ThreadEventView{ID: event.ID, ThreadID: event.ThreadID,
			RunID: event.RunID, Type: event.Type, Source: event.Source,
			Payload: json.RawMessage(event.PayloadJSON), CreatedAt: event.CreatedAt}
	}
	auditViews := make([]ThreadRunAuditEventView, len(value.AuditEvents))
	for index, event := range value.AuditEvents {
		auditViews[index] = ThreadRunAuditEventView{EventID: event.EventID,
			Version: event.Version, RunID: event.RunID, MissionID: event.MissionID,
			Sequence: event.Sequence, Type: event.Type, Source: event.Source,
			SubjectID: event.SubjectID, Payload: json.RawMessage(event.PayloadJSON),
			CreatedAt: event.CreatedAt}
	}
	activeStatus := domain.RunStatus("")
	for _, run := range value.Runs {
		if run.ID == value.Thread.ActiveRunID {
			activeStatus = run.Status
			break
		}
	}
	missions := make([]MissionView, len(value.Missions))
	for i, mission := range value.Missions {
		missions[i] = missionView(mission)
	}
	return ThreadExportView{Missions: missions, ProtocolVersion: value.ProtocolVersion,
		ExportedAt: value.ExportedAt, Thread: threadView(value.Thread, activeStatus),
		Mission: missionView(value.Mission), Runs: runs, Sessions: sessions, Messages: messages,
		Events: eventViews, AuditEvents: auditViews}, nil, nil
}

func matchThreadMutationPath(requestPath string) (string, string, bool) {
	const prefix = ThreadCollectionPath + "/"
	if !strings.HasPrefix(requestPath, prefix) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(requestPath, prefix), "/")
	if len(parts) != 2 || parts[0] == "" {
		return "", "", false
	}
	switch parts[1] {
	case "project", "messages", "turns", "branches", "interrupt", "recovery", "archive", "restore", "delete":
		return parts[0], parts[1], true
	default:
		return "", "", false
	}
}

func (a *API) serveThreadCreationControl(writer http.ResponseWriter, request *http.Request,
	requestID string,
) {
	if !a.runCreationEnabled || !a.sessionMessageEnabled {
		a.writeError(writer, requestID, apperror.New(apperror.CodeNotFound,
			"HTTP API endpoint was not found"), http.StatusNotFound)
		return
	}
	if !a.authorizeThreadControl(writer, request, requestID) {
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"Thread creation endpoint only supports POST"), http.StatusMethodNotAllowed)
		return
	}
	operationKey, err := runCreationIdempotencyKey(request.Header)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	var view ThreadCreationControlRequestView
	if !a.decodeThreadControlBody(writer, request, requestID, "Thread creation", &view) {
		return
	}
	if view.Version != domain.ThreadCreationProtocolVersion {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"unsupported Thread creation version"), 0)
		return
	}
	modelRoute := ""
	customModelProvider := false
	var expectedProviderDefinitionRevision uint64
	if (view.Provider == "") != (view.Model == "") {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"Thread creation Provider and model must be supplied together"), 0)
		return
	}
	if view.Provider != "" {
		if a.threadModelRouteController == nil {
			a.writeError(writer, requestID, apperror.New(apperror.CodeFailedPrecondition,
				"Thread model route catalog is unavailable"), 0)
			return
		}
		catalog, catalogErr := a.threadModelRouteController.Catalog(request.Context())
		if catalogErr != nil {
			a.writeError(writer, requestID, catalogErr, 0)
			return
		}
		eligible := false
		for _, route := range catalog.Routes {
			if route.ProviderID == view.Provider && route.Model == view.Model && route.Selectable {
				eligible = true
				customModelProvider = route.Custom
				expectedProviderDefinitionRevision = route.DefinitionRevision
				break
			}
		}
		if !eligible {
			a.writeError(writer, requestID, apperror.New(apperror.CodeFailedPrecondition,
				"Thread creation Provider model route is not currently eligible"), 0)
			return
		}
		modelRoute = view.Provider + "/" + view.Model
	} else if a.threadModelRouteController != nil {
		// Product Thread creation must not persist a dead named route. Resolve
		// the profile default through the live catalog and, when that default is
		// stale, fall back deterministically to a selectable configured model.
		catalog, catalogErr := a.threadModelRouteController.Catalog(request.Context())
		if catalogErr != nil {
			a.writeError(writer, requestID, catalogErr, 0)
			return
		}
		profile := strings.TrimSpace(view.Profile)
		if profile == "" {
			profile = string(domain.ProfileCode)
		}
		selected := -1
		defaultFound := false
		for index, route := range catalog.Routes {
			if !route.Selectable {
				continue
			}
			if selected < 0 {
				selected = index
			}
			for _, namedRoute := range route.DefaultForRoutes {
				if namedRoute == profile {
					selected = index
					defaultFound = true
					break
				}
			}
			if defaultFound {
				break
			}
		}
		if selected < 0 {
			a.writeError(writer, requestID, apperror.New(apperror.CodeFailedPrecondition,
				"No configured model is ready; select or configure a model before creating a Thread"), 0)
			return
		}
		route := catalog.Routes[selected]
		modelRoute = route.ProviderID + "/" + route.Model
		customModelProvider = route.Custom
		expectedProviderDefinitionRevision = route.DefinitionRevision
	}
	if view.WorkspaceID == "" {
		manager, enabled := a.projectlessManager()
		if !enabled {
			a.writeError(writer, requestID, apperror.New(apperror.CodeFailedPrecondition, "local chat directories are unavailable"), 0)
			return
		}
		record, _, err := manager.Allocate(request.Context(), operationKey)
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		view.WorkspaceID = record.ID
	}
	result, err := application.NewControlledRunCreationService(a.store).
		WithLifecycleHooks(a.lifecycleHooks).Create(request.Context(),
		application.ControlledRunCreationRequest{Version: domain.RunCreationProtocolVersion,
			Goal: view.Goal, WorkspaceID: view.WorkspaceID, Profile: view.Profile,
			Surface: view.Surface, Phase: view.Phase, NetworkMode: view.NetworkMode, Budget: view.Budget,
			ModelRoute:                         modelRoute,
			CustomModelProvider:                customModelProvider,
			ExpectedProviderDefinitionRevision: expectedProviderDefinitionRevision,
			AllowedTargets:                     append([]string(nil), view.AllowedTargets...), OperationKey: operationKey,
			WebAccessMode: string(view.WebAccessMode), WebAllowedTargets: view.WebAllowedTargets,
			RequestedBy: "http_thread_operator"})
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	threadRecord, err := a.store.GetThreadByRun(request.Context(), result.Run.ID)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccessStatus(writer, requestID, ThreadCreationControlView{
		Thread: threadView(threadRecord, result.Run.Status), Mission: missionView(result.Mission),
		Run: runView(result.Run), Session: sessionView(result.Session),
		Mode: runModeView(result.Mode), Replayed: result.Replayed,
	}, nil, http.StatusAccepted)
}

func (a *API) serveThreadMutationControl(writer http.ResponseWriter, request *http.Request,
	requestID, threadID, action string,
) {
	turnAction := action == "turns" || action == "interrupt"
	recoveryAction := action == "recovery"
	if (!turnAction && !recoveryAction && (!a.runCreationEnabled || !a.sessionMessageEnabled)) ||
		(turnAction && (!a.runCreationEnabled || !a.sessionMessageEnabled ||
			!a.runLifecycleEnabled || !a.runExecutionEnabled ||
			a.threadTurnController == nil)) ||
		(recoveryAction && (!a.runCreationEnabled || !a.sessionMessageEnabled ||
			!a.runLifecycleEnabled)) {
		a.writeError(writer, requestID, apperror.New(apperror.CodeNotFound,
			"HTTP API endpoint was not found"), http.StatusNotFound)
		return
	}
	if !a.authorizeThreadControl(writer, request, requestID) {
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"Thread control endpoints only support POST"), http.StatusMethodNotAllowed)
		return
	}
	if err := validatePathIdentity(threadID); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	operationKey, err := sessionControlIdempotencyKey(request.Header, "Thread "+action)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if action == "project" {
		a.serveThreadProjectMove(writer, request, requestID, threadID, operationKey)
		return
	}
	if action == "branches" {
		a.serveThreadBranch(writer, request, requestID, threadID, operationKey)
		return
	}
	if action == "interrupt" {
		controller, ok := a.threadTurnController.(ThreadExecutionController)
		if !ok {
			a.writeError(writer, requestID, apperror.New(apperror.CodeNotFound,
				"Thread execution control is unavailable"), 0)
			return
		}
		var view ThreadInterruptRequestView
		if !a.decodeThreadControlBody(writer, request, requestID, "Thread interrupt", &view) {
			return
		}
		if view.Version != application.ThreadExecutionProtocolVersion {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
				"Unsupported Thread execution version"), 0)
			return
		}
		state, err := controller.Interrupt(request.Context(), threadID, view.ExecutionID)
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		a.writeSuccessStatus(writer, requestID, state, nil, http.StatusAccepted)
		return
	}
	if action == "messages" {
		var view ThreadMessageControlRequestView
		if !a.decodeThreadControlBody(writer, request, requestID, "Thread message", &view) {
			return
		}
		result, err := application.NewThreadServiceWithExecutionCapabilities(
			a.store, a.executionPermissionCapabilities).WithModelRouteRegistry(
			a.modelRegistry).Submit(request.Context(),
			application.SubmitThreadMessageRequest{Version: view.Version, ThreadID: threadID,
				Content: view.Content, OperationKey: operationKey,
				RequestedBy: "http_thread_operator"})
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		a.writeSuccessStatus(writer, requestID, ThreadMessageControlView{
			Version: domain.ThreadMessageProtocolVersion,
			Thread:  threadView(result.Thread, result.Run.Status),
			RunID:   result.Run.ID, SessionID: result.Session.ID,
			PredecessorRunID: result.PredecessorRunID,
			SuccessorCreated: result.SuccessorCreated,
			Steering:         operatorSteeringMessageView(result.Message), Replayed: result.Replayed,
		}, nil, http.StatusAccepted)
		return
	}
	if action == "turns" {
		var view ThreadTurnControlRequestView
		if !a.decodeThreadControlBody(writer, request, requestID, "Thread turn", &view) {
			return
		}
		if len(view.Files) > 0 && !a.evidenceAttachmentEnabled {
			a.writeError(writer, requestID, apperror.New(apperror.CodeFailedPrecondition,
				"Thread file references are not enabled on this server"), 0)
			return
		}
		// A turn can outlive the ordinary 30-second HTTP write timeout. Its
		// existing request context and Go execution budgets still own the work.
		// Restore a bounded write deadline when the final response is ready.
		responseControl := http.NewResponseController(writer)
		if err := responseControl.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
			a.writeError(writer, requestID, apperror.Wrap(apperror.CodeUnavailable,
				"Thread response deadline could not be configured", err), 0)
			return
		}
		result, err := a.threadTurnController.Execute(request.Context(),
			application.ExecuteThreadTurnRequest{Version: view.Version, ThreadID: threadID,
				Content: view.Content, OperationKey: operationKey,
				Files: view.Files, Images: view.Images, Attachments: view.Attachments, MCPReferences: view.MCPReferences,
				RequestedBy: "http_thread_operator"})
		_ = responseControl.SetWriteDeadline(time.Now().Add(30 * time.Second))
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		submission := result.Submission
		a.writeSuccessStatus(writer, requestID, ThreadMessageControlView{
			Version: domain.ThreadMessageProtocolVersion,
			Thread:  threadView(submission.Thread, submission.Run.Status),
			RunID:   submission.Run.ID, SessionID: submission.Session.ID,
			PredecessorRunID: submission.PredecessorRunID,
			SuccessorCreated: submission.SuccessorCreated,
			Steering:         operatorSteeringMessageView(submission.Message),
			Replayed:         result.Replayed,
			ExecutionStarted: result.ExecutionStarted,
			ModelCalled:      result.ModelCalled,
			ToolCalled:       result.ToolCalled,
		}, nil, http.StatusAccepted)
		return
	}
	if action == "recovery" {
		var view ThreadRunRecoveryControlRequestView
		if !a.decodeThreadControlBody(writer, request, requestID, "Thread recovery", &view) {
			return
		}
		result, err := application.NewThreadRunRecoveryService(a.store).
			WithLifecycleHooks(a.lifecycleHooks).
			WithExecutionPermissionRuntimeAuthority(
				a.executionPermissionCapabilities.RuntimeAuthority).
			Recover(request.Context(), application.RecoverThreadRunRequest{
				Version: view.Version, ThreadID: threadID, RunID: view.RunID,
				HandoffOperationID: view.HandoffOperationID,
				OperationKey:       operationKey,
				RequestedBy:        "http_thread_operator",
			})
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		a.writeSuccessStatus(writer, requestID, ThreadRunRecoveryControlView{
			Version: domain.ThreadRunRecoveryProtocolVersion,
			Thread:  threadView(result.Thread), FailedRun: runView(result.FailedRun),
			SuccessorRequired: result.Thread.ActiveRunID == "", Replayed: result.Replayed,
		}, nil, http.StatusOK)
		return
	}
	var view ThreadLifecycleControlRequestView
	if !a.decodeThreadControlBody(writer, request, requestID, "Thread lifecycle", &view) {
		return
	}
	if view.Version != domain.ThreadLifecycleProtocolVersion {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"unsupported Thread lifecycle version"), 0)
		return
	}
	threadRecord, err := application.NewThreadServiceWithExecutionCapabilities(
		a.store, a.executionPermissionCapabilities).Transition(request.Context(),
		threadID, domain.ThreadLifecycleAction(action), view.ExpectedVersion,
		"http_thread_operator", operationKey)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	activeStatus := domain.RunStatus("")
	if threadRecord.ActiveRunID != "" {
		activeRun, loadErr := a.store.GetRun(request.Context(), threadRecord.ActiveRunID)
		if loadErr != nil {
			a.writeError(writer, requestID, loadErr, 0)
			return
		}
		activeStatus = activeRun.Status
	}
	a.writeSuccessStatus(writer, requestID, ThreadLifecycleControlView{
		Version: domain.ThreadLifecycleProtocolVersion,
		Thread:  threadView(threadRecord, activeStatus),
	}, nil, http.StatusOK)
}

func (a *API) decodeThreadControlBody(writer http.ResponseWriter, request *http.Request,
	requestID, label string, target any,
) bool {
	if err := rejectQuery(request.URL.Query()); err != nil {
		a.writeError(writer, requestID, err, 0)
		return false
	}
	if err := validateJSONContentType(request.Header); err != nil {
		a.writeError(writer, requestID, err, http.StatusUnsupportedMediaType)
		return false
	}
	body, err := readBoundedRequestBody(request, MaxSessionMessageRequestBodyBytes)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return false
	}
	if !utf8.Valid(body) {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			label+" body must be valid UTF-8 JSON"), 0)
		return false
	}
	if err := rejectDuplicateJSONObjectFields(body, label); err != nil {
		a.writeError(writer, requestID, err, 0)
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		a.writeError(writer, requestID, apperror.Wrap(apperror.CodeInvalidArgument,
			fmt.Sprintf("%s body must be one JSON object", label), err), 0)
		return false
	}
	if err := ensureJSONEOF(decoder); err != nil {
		a.writeError(writer, requestID, err, 0)
		return false
	}
	return true
}

func (a *API) authorizeThreadControl(writer http.ResponseWriter, request *http.Request,
	requestID string,
) bool {
	if a.authorized(request, a.controlTokenHash) {
		return true
	}
	writer.Header().Set("WWW-Authenticate", `Bearer realm="CyberAgent Control API"`)
	a.writeError(writer, requestID, apperror.New(apperror.CodePolicyDenied,
		"valid control bearer authorization is required"), http.StatusUnauthorized)
	return false
}
