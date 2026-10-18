package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

const (
	ScheduledJobsPath                                             = "/api/v1/scheduled-jobs"
	ScheduledJobPathTemplate                                      = "/api/v1/scheduled-jobs/{job_id}"
	RunScheduledJobsPathTemplate                                  = "/api/v1/runs/{run_id}/scheduled-jobs"
	ScheduledJobActionPathTemplate                                = "/api/v1/runs/{run_id}/scheduled-jobs/{job_id}/{action}"
	ScheduledJobObservationPathTemplate                           = "/api/v1/runs/{run_id}/scheduled-jobs/{job_id}/enable-observation"
	scheduledJobObservationAction       domain.ScheduledJobAction = "enable-observation"
	DoctorSnapshotPath                                            = "/api/v1/doctor"
	DebugQueryPath                                                = "/api/v1/debug"
	DiagnosticBundlePath                                          = "/api/v1/diagnostic-bundle"
)

type ScheduledJobController interface {
	Create(context.Context, application.CreateScheduledJobRequest) (
		application.ScheduledJobControlResult, error)
	Transition(context.Context, application.TransitionScheduledJobRequest) (
		application.ScheduledJobControlResult, error)
	Get(context.Context, string, int, int) (application.ScheduledJobSnapshot, error)
	List(context.Context, string, int) ([]domain.ScheduledJob, error)
}

type ScheduledJobScheduleRequestView struct {
	Kind            domain.ScheduledJobKind          `json:"kind"`
	Timezone        string                           `json:"timezone"`
	AnchorAt        time.Time                        `json:"anchor_at"`
	IntervalSeconds int64                            `json:"interval_seconds,omitempty"`
	MisfirePolicy   domain.ScheduledJobMisfirePolicy `json:"misfire_policy"`
}

type ScheduledJobCreateRequestView struct {
	Version                   string                              `json:"version"`
	Schedule                  ScheduledJobScheduleRequestView     `json:"schedule"`
	DeadlineAt                time.Time                           `json:"deadline_at"`
	StopOnTargetTerminal      bool                                `json:"stop_on_target_terminal"`
	MaxRounds                 int                                 `json:"max_rounds"`
	MaxModelCalls             int                                 `json:"max_model_calls"`
	MaxElapsedSeconds         int64                               `json:"max_elapsed_seconds"`
	Retry                     domain.ScheduledJobRetryPolicy      `json:"retry"`
	Notification              domain.ScheduledJobNotificationMode `json:"notification"`
	ExecutionMode             domain.ScheduledJobExecutionMode    `json:"execution_mode"`
	ConfirmRepair             bool                                `json:"confirm_repair"`
	ObservationConsentVersion int                                 `json:"observation_consent_version,omitempty"`
	ExecutionTarget           *domain.ScheduledJobExecutionTarget `json:"execution_target,omitempty"`
	ExecutionConsentVersion   int                                 `json:"execution_consent_version,omitempty"`
}

type ScheduledJobObservationController interface {
	EnableObservation(context.Context, application.EnableScheduledJobObservationRequest) (
		application.ScheduledJobControlResult, error)
}

type ScheduledJobObservationRequestView struct {
	Version                   string `json:"version"`
	ExpectedRevision          int64  `json:"expected_revision"`
	ObservationConsentVersion int    `json:"observation_consent_version"`
}

type ScheduledJobTransitionRequestView struct {
	Version          string `json:"version"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type ScheduledJobControlView struct {
	ProtocolVersion  string              `json:"protocol_version"`
	Action           string              `json:"action"`
	Job              domain.ScheduledJob `json:"job"`
	Replayed         bool                `json:"replayed"`
	ExecutionStarted bool                `json:"execution_started"`
	AuthorityBypass  bool                `json:"authority_bypass"`
}

type ScheduledJobListView struct {
	ProtocolVersion string                `json:"protocol_version"`
	Items           []domain.ScheduledJob `json:"items"`
}

type ScheduledJobDetailView struct {
	ProtocolVersion string                           `json:"protocol_version"`
	Snapshot        application.ScheduledJobSnapshot `json:"snapshot"`
}

func matchScheduledJobMutationPath(requestPath string) (runID string,
	jobID string, action domain.ScheduledJobAction, matched bool,
) {
	const prefix = "/api/v1/runs/"
	if !strings.HasPrefix(requestPath, prefix) {
		return "", "", "", false
	}
	segments := strings.Split(strings.TrimPrefix(requestPath, prefix), "/")
	if len(segments) == 2 && segments[0] != "" && segments[1] == "scheduled-jobs" {
		return segments[0], "", domain.ScheduledJobCreate, true
	}
	if len(segments) != 4 || segments[0] == "" || segments[1] != "scheduled-jobs" ||
		segments[2] == "" {
		return "", "", "", false
	}
	action = domain.ScheduledJobAction(segments[3])
	if action != domain.ScheduledJobPause && action != domain.ScheduledJobResume &&
		action != domain.ScheduledJobCancel && action != scheduledJobObservationAction {
		return "", "", "", false
	}
	return segments[0], segments[2], action, true
}

func (a *API) serveScheduledJobControl(writer http.ResponseWriter,
	request *http.Request, requestID string, runID string, jobID string,
	action domain.ScheduledJobAction,
) {
	const label = "Scheduled job control"
	if !a.authorizeRunOperation(writer, request, requestID,
		a.scheduledJobControlEnabled, label) {
		return
	}
	if a.scheduledJobController == nil {
		a.writeError(writer, requestID, apperror.New(apperror.CodeUnavailable,
			"scheduled job controller is unavailable"), 0)
		return
	}
	if err := validatePathIdentity(runID); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if jobID != "" {
		if err := validatePathIdentity(jobID); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
	}
	if err := validateJSONContentType(request.Header); err != nil {
		a.writeError(writer, requestID, err, http.StatusUnsupportedMediaType)
		return
	}
	operationKey, body, err := a.readRunOperationRequest(request, label)
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	var result application.ScheduledJobControlResult
	if action == domain.ScheduledJobCreate {
		var view ScheduledJobCreateRequestView
		if err := decodeStrictRunOperation(body, &view, label); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		result, err = a.scheduledJobController.Create(request.Context(),
			application.CreateScheduledJobRequest{
				Version: view.Version, RunID: runID, TargetRunID: runID,
				Schedule: domain.ScheduledJobSchedule{Kind: view.Schedule.Kind,
					Timezone: view.Schedule.Timezone, AnchorAt: view.Schedule.AnchorAt,
					IntervalSeconds: view.Schedule.IntervalSeconds,
					MisfirePolicy:   view.Schedule.MisfirePolicy},
				DeadlineAt:           view.DeadlineAt,
				StopOnTargetTerminal: view.StopOnTargetTerminal,
				MaxRounds:            view.MaxRounds, MaxModelCalls: view.MaxModelCalls,
				MaxElapsedSeconds: view.MaxElapsedSeconds, Retry: view.Retry,
				Notification: view.Notification, ExecutionMode: view.ExecutionMode,
				ConfirmRepair: view.ConfirmRepair, OperationKey: operationKey,
				ObservationConsentVersion: view.ObservationConsentVersion,
				ExecutionTarget:           view.ExecutionTarget,
				ExecutionConsentVersion:   view.ExecutionConsentVersion,
				RequestedBy:               "http_control",
			})
	} else if action == scheduledJobObservationAction {
		var view ScheduledJobObservationRequestView
		if err := decodeStrictRunOperation(body, &view, label); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		if view.Version != domain.ScheduledJobControlProtocolVersion {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
				"scheduled observation version is invalid"), 0)
			return
		}
		controller, ok := a.scheduledJobController.(ScheduledJobObservationController)
		if !ok {
			a.writeError(writer, requestID, apperror.New(apperror.CodeUnavailable,
				"scheduled observation control is unavailable"), 0)
			return
		}
		result, err = controller.EnableObservation(request.Context(),
			application.EnableScheduledJobObservationRequest{
				RunID: runID, JobID: jobID, ExpectedRevision: view.ExpectedRevision,
				ObservationConsentVersion: view.ObservationConsentVersion,
				OperationKey:              operationKey, RequestedBy: "http_control",
			})
	} else {
		var view ScheduledJobTransitionRequestView
		if err := decodeStrictRunOperation(body, &view, label); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		result, err = a.scheduledJobController.Transition(request.Context(),
			application.TransitionScheduledJobRequest{
				Version: view.Version, RunID: runID, JobID: jobID, Action: action,
				ExpectedRevision: view.ExpectedRevision, OperationKey: operationKey,
				RequestedBy: "http_control",
			})
	}
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccessStatus(writer, requestID, ScheduledJobControlView{
		ProtocolVersion: domain.ScheduledJobControlProtocolVersion,
		Action:          string(action), Job: result.Job, Replayed: result.Replayed,
		ExecutionStarted: false, AuthorityBypass: false,
	}, nil, http.StatusAccepted)
}

func (a *API) scheduledJobs(request *http.Request,
	runIDOverride string,
) (any, *Page, error) {
	if a.scheduledJobController == nil {
		return nil, nil, apperror.New(apperror.CodeNotFound,
			"scheduled job projection is unavailable")
	}
	values := request.URL.Query()
	allowed := []string{"limit"}
	if runIDOverride == "" {
		allowed = append(allowed, "run_id")
	}
	if err := validateExactQuery(values, allowed...); err != nil {
		return nil, nil, err
	}
	runID := runIDOverride
	if runID == "" {
		runID, _ = singleQueryValue(values, "run_id")
	}
	limit := 50
	if raw, ok := singleQueryValue(values, "limit"); ok {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument,
				"scheduled job limit must be between 1 and 100")
		}
		limit = parsed
	}
	items, err := a.scheduledJobController.List(request.Context(), runID, limit)
	if err != nil {
		return nil, nil, err
	}
	return ScheduledJobListView{ProtocolVersion: domain.ScheduledJobProtocolVersion,
		Items: items}, nil, nil
}

func (a *API) scheduledJob(request *http.Request,
	jobID string,
) (any, *Page, error) {
	if a.scheduledJobController == nil {
		return nil, nil, apperror.New(apperror.CodeNotFound,
			"scheduled job projection is unavailable")
	}
	values := request.URL.Query()
	if err := validateExactQuery(values, "round_limit", "notification_limit"); err != nil {
		return nil, nil, err
	}
	roundLimit, notificationLimit := 20, 20
	for name, target := range map[string]*int{"round_limit": &roundLimit,
		"notification_limit": &notificationLimit} {
		if raw, ok := singleQueryValue(values, name); ok {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 100 {
				return nil, nil, apperror.New(apperror.CodeInvalidArgument,
					"scheduled job detail limits must be between 1 and 100")
			}
			*target = parsed
		}
	}
	snapshot, err := a.scheduledJobController.Get(request.Context(), jobID,
		roundLimit, notificationLimit)
	if err != nil {
		return nil, nil, err
	}
	return ScheduledJobDetailView{ProtocolVersion: domain.ScheduledJobProtocolVersion,
		Snapshot: snapshot}, nil, nil
}

func (a *API) doctorSnapshot(request *http.Request) (any, *Page, error) {
	values := request.URL.Query()
	if err := validateExactQuery(values, "run_id"); err != nil {
		return nil, nil, err
	}
	runID, _ := singleQueryValue(values, "run_id")
	value, err := a.diagnostics.Doctor(request.Context(), runID)
	return value, nil, err
}

func (a *API) debugQuery(request *http.Request) (any, *Page, error) {
	query, err := debugRequestFromQuery(request)
	if err != nil {
		return nil, nil, err
	}
	value, err := a.diagnostics.Debug(request.Context(), query)
	return value, nil, err
}

func (a *API) diagnosticBundle(request *http.Request) (any, *Page, error) {
	query, err := debugRequestFromQuery(request)
	if err != nil {
		return nil, nil, err
	}
	value, err := a.diagnostics.Bundle(request.Context(), query.RunID, query)
	return value, nil, err
}

func debugRequestFromQuery(request *http.Request) (application.DebugQueryRequest, error) {
	values := request.URL.Query()
	if err := validateExactQuery(values, "run_id", "after_sequence", "from", "to",
		"limit", "correlation_kind", "correlation_id", "type_prefix", "source_prefix"); err != nil {
		return application.DebugQueryRequest{}, err
	}
	result := application.DebugQueryRequest{Version: application.DebugQueryProtocolVersion,
		Limit: 100}
	result.RunID, _ = singleQueryValue(values, "run_id")
	result.CorrelationKind, _ = singleQueryValue(values, "correlation_kind")
	result.CorrelationID, _ = singleQueryValue(values, "correlation_id")
	result.TypePrefix, _ = singleQueryValue(values, "type_prefix")
	result.SourcePrefix, _ = singleQueryValue(values, "source_prefix")
	if raw, ok := singleQueryValue(values, "after_sequence"); ok {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return application.DebugQueryRequest{}, apperror.New(
				apperror.CodeInvalidArgument, "debug sequence cursor is invalid")
		}
		result.AfterSequence = parsed
	}
	if raw, ok := singleQueryValue(values, "limit"); ok {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return application.DebugQueryRequest{}, apperror.New(
				apperror.CodeInvalidArgument, "debug limit is invalid")
		}
		result.Limit = parsed
	}
	for name, target := range map[string]*time.Time{"from": &result.From, "to": &result.To} {
		if raw, ok := singleQueryValue(values, name); ok {
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return application.DebugQueryRequest{}, apperror.New(
					apperror.CodeInvalidArgument, "debug time bounds must use RFC3339")
			}
			*target = parsed.UTC()
		}
	}
	return result, nil
}

func validateExactQuery(values map[string][]string, allowed ...string) error {
	accepted := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		accepted[name] = struct{}{}
	}
	for name, items := range values {
		if _, ok := accepted[name]; !ok {
			return apperror.New(apperror.CodeInvalidArgument,
				"unknown query parameter "+strconv.Quote(name))
		}
		if len(items) != 1 || strings.TrimSpace(items[0]) == "" {
			return apperror.New(apperror.CodeInvalidArgument,
				"query parameters must appear exactly once with a value")
		}
	}
	return nil
}
