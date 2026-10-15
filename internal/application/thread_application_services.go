package application

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"

	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/session"
)

const (
	ThreadApplicationServicesProtocolVersion = "thread_application_services.v1"
	DefaultThreadApplicationServicesLimit    = 20
	MaxThreadApplicationServicesLimit        = 50
	MaxThreadApplicationServiceURLs          = runner.MaxCommandServiceURLs
)

type ThreadApplicationServiceStore interface {
	GetThread(context.Context, string) (domain.Thread, error)
	ListThreadCommandRuntimeServiceMetadata(context.Context, string, int) ([]runner.CommandRuntimeServiceMetadata, error)
	GetThreadCommandRuntimeJob(context.Context, string, string) (runner.CommandRuntimeJob, error)
	FindThreadCommandRuntimeStartCall(context.Context, string, runner.CommandRuntimeJobIdentity) (domain.SupervisorToolCall, bool, error)
	SupervisorOperatorMessageID(context.Context, string, string, int) (string, error)
	GetWorkspaceInfo(context.Context, string) (session.WorkspaceInfo, error)
}

// This bridge can only observe already bound Job metadata and reap its existing
// owner. It has no start, stdin, permission, browser or network operation.
type ThreadApplicationServiceCommandRuntime interface {
	ThreadActivityCommandRuntimeSource
	ReadOwnedCommandRuntimeServicePrefix(context.Context, runner.CommandRuntimeJobIdentity, int) (string, string, bool, error)
	ReadOwnedCommandRuntimeServiceState(context.Context, runner.CommandRuntimeJobIdentity) (runner.CommandRuntimeJobSnapshot, bool, error)
	StopThreadApplicationServiceJob(context.Context, runner.CommandRuntimeJob) (runner.CommandRuntimeJobSnapshot, bool, error)
}

type ThreadApplicationServiceView struct {
	ThreadID        string     `json:"thread_id"`
	RunID           string     `json:"run_id"`
	JobID           string     `json:"job_id"`
	State           string     `json:"state"`
	ExitCode        *int       `json:"exit_code,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	SourceCallID    string     `json:"source_call_id,omitempty"`
	SourceMessageID string     `json:"source_message_id,omitempty"`
	SourceTurn      int        `json:"source_turn,omitempty"`
	CanStop         bool       `json:"can_stop"`
}

type ThreadApplicationServicesView struct {
	Version  string                         `json:"version"`
	ThreadID string                         `json:"thread_id"`
	Services []ThreadApplicationServiceView `json:"services"`
	HasMore  bool                           `json:"has_more"`
}

type ThreadApplicationServiceOutputView struct {
	Stdout           string `json:"stdout"`
	Stderr           string `json:"stderr"`
	BaseCursor       uint64 `json:"base_cursor"`
	NextCursor       uint64 `json:"next_cursor"`
	EndCursor        uint64 `json:"end_cursor"`
	Dropped          bool   `json:"dropped"`
	TruncationReason string `json:"truncation_reason,omitempty"`
	Available        bool   `json:"available"`
}

type ThreadApplicationServiceURLView struct {
	URL      string `json:"url"`
	Source   string `json:"source"`
	Verified bool   `json:"verified"`
}

type ThreadApplicationServiceDetailView struct {
	Version       string                             `json:"version"`
	Service       ThreadApplicationServiceView       `json:"service"`
	Output        ThreadApplicationServiceOutputView `json:"output"`
	CandidateURLs []ThreadApplicationServiceURLView  `json:"candidate_urls"`
}

type ThreadApplicationServiceStopRequest struct {
	Version, ExpectedRunID, OperationKey string
}

type ThreadApplicationServiceStopView struct {
	Version  string                       `json:"version"`
	Service  ThreadApplicationServiceView `json:"service"`
	Replayed bool                         `json:"replayed"`
}

type ThreadApplicationService struct {
	store    ThreadApplicationServiceStore
	commands ThreadApplicationServiceCommandRuntime
}

func NewThreadApplicationService(store ThreadApplicationServiceStore) *ThreadApplicationService {
	return &ThreadApplicationService{store: store}
}

func (s *ThreadApplicationService) WithCommandRuntime(runtime ThreadApplicationServiceCommandRuntime) *ThreadApplicationService {
	if s != nil {
		s.commands = runtime
	}
	return s
}

func (s *ThreadApplicationService) checkThread(ctx context.Context, threadID string) error {
	if s == nil || s.store == nil || ctx == nil || ctx.Err() != nil {
		return apperror.New(apperror.CodeFailedPrecondition, "Thread application services are unavailable")
	}
	if !domain.ValidAgentID(threadID) {
		return apperror.New(apperror.CodeInvalidArgument, "Thread service identity is invalid")
	}
	_, err := s.store.GetThread(ctx, threadID)
	return apperror.Normalize(err)
}

func (s *ThreadApplicationService) List(ctx context.Context, threadID string, limit int) (ThreadApplicationServicesView, error) {
	value := ThreadApplicationServicesView{Version: ThreadApplicationServicesProtocolVersion, ThreadID: threadID, Services: []ThreadApplicationServiceView{}}
	if err := s.checkThread(ctx, threadID); err != nil {
		return value, err
	}
	if limit == 0 {
		limit = DefaultThreadApplicationServicesLimit
	}
	if limit < 1 || limit > MaxThreadApplicationServicesLimit {
		return value, apperror.New(apperror.CodeInvalidArgument, "Thread service limit is invalid")
	}
	jobs, err := s.store.ListThreadCommandRuntimeServiceMetadata(ctx, threadID, limit+1)
	if err != nil {
		return value, apperror.Normalize(err)
	}
	value.HasMore = len(jobs) > limit
	if value.HasMore {
		jobs = jobs[:limit]
	}
	for _, job := range jobs {
		view, err := s.project(ctx, threadID, job)
		if err != nil {
			return value, err
		}
		value.Services = append(value.Services, view)
	}
	return value, nil
}

func (s *ThreadApplicationService) project(ctx context.Context, threadID string, job runner.CommandRuntimeServiceMetadata) (ThreadApplicationServiceView, error) {
	id := job.Identity
	view := ThreadApplicationServiceView{ThreadID: threadID, RunID: id.RunID, JobID: id.ID, State: string(job.State),
		ExitCode: cloneActivityInt(job.ExitCode), CreatedAt: job.CreatedAt, StartedAt: cloneActivityTime(job.StartedAt), CompletedAt: cloneActivityTime(job.CompletedAt)}
	if s.commands != nil {
		snapshot, owned, err := s.commands.ReadOwnedCommandRuntimeServiceState(ctx, id)
		if err != nil {
			return view, apperror.Wrap(apperror.CodeFailedPrecondition, "Thread service owner could not be observed", err)
		}
		if owned {
			view.State, view.ExitCode, view.StartedAt, view.CompletedAt = string(snapshot.State), cloneActivityInt(snapshot.ExitCode), cloneActivityTime(snapshot.StartedAt), cloneActivityTime(snapshot.CompletedAt)
			view.CanStop = !snapshot.State.Terminal() && snapshot.State != runner.CommandRuntimeJobStopping
		}
	}
	call, found, err := s.store.FindThreadCommandRuntimeStartCall(ctx, threadID, id)
	if err != nil {
		return view, apperror.Normalize(err)
	}
	if found {
		view.SourceCallID, view.SourceTurn = call.CallID, call.Turn
		view.SourceMessageID, err = s.store.SupervisorOperatorMessageID(ctx, call.RunID, call.AttemptID, call.Turn)
		if err != nil {
			return view, apperror.Normalize(err)
		}
	}
	return view, nil
}

func serviceMetadata(job runner.CommandRuntimeJob) runner.CommandRuntimeServiceMetadata {
	return runner.CommandRuntimeServiceMetadata{Identity: runner.CommandRuntimeIdentity(job), State: job.State, ExitCode: job.ExitCode, CreatedAt: job.CreatedAt, StartedAt: job.StartedAt, CompletedAt: job.CompletedAt}
}

func (s *ThreadApplicationService) Get(ctx context.Context, threadID, jobID string) (ThreadApplicationServiceDetailView, error) {
	value := ThreadApplicationServiceDetailView{Version: ThreadApplicationServicesProtocolVersion,
		CandidateURLs: []ThreadApplicationServiceURLView{}}
	if err := s.checkThread(ctx, threadID); err != nil {
		return value, err
	}
	if !domain.ValidAgentID(jobID) {
		return value, apperror.New(apperror.CodeInvalidArgument, "Thread service Job identity is invalid")
	}
	job, err := s.store.GetThreadCommandRuntimeJob(ctx, threadID, jobID)
	if err != nil {
		return value, apperror.Normalize(err)
	}
	if job.Validate() != nil {
		return value, apperror.New(apperror.CodeFailedPrecondition, "Thread service Job is invalid")
	}
	value.Service, err = s.project(ctx, threadID, serviceMetadata(job))
	if err != nil {
		return value, err
	}
	// Interactive stdin is not eligible for public output: a later private
	// input may be echoed, and its plaintext is deliberately not persisted.
	if job.StdinPolicy != runner.CommandRuntimeStdinClosed || job.StdinWriteCount > 0 {
		return value, nil
	}
	var page runner.CommandRuntimeOutputPage
	var snapshot runner.CommandRuntimeJobSnapshot
	found := false
	if s.commands != nil {
		snapshot, page, found, err = s.commands.ReadCommandRuntimeActivityTail(ctx, job, runner.MaxCommandRuntimeOutputRead)
		if err != nil {
			return value, apperror.Wrap(apperror.CodeFailedPrecondition, "Thread service output could not be observed", err)
		}
	}
	if !found {
		snapshot, page, found, err = runner.ProjectCommandRuntimeStoredActivityTail(job, runner.MaxCommandRuntimeOutputRead)
		if err != nil {
			return value, apperror.Wrap(apperror.CodeFailedPrecondition, "Thread service saved output is invalid", err)
		}
	}
	if !found {
		return value, nil
	}
	if snapshot.ID != job.ID || page.JobID != job.ID {
		return value, apperror.New(apperror.CodeFailedPrecondition, "Thread service output binding is invalid")
	}
	value.Service.State, value.Service.ExitCode = string(snapshot.State), cloneActivityInt(snapshot.ExitCode)
	if snapshot.State.Terminal() || snapshot.State == runner.CommandRuntimeJobStopping {
		value.Service.CanStop = false
	}
	workspace, err := s.store.GetWorkspaceInfo(ctx, job.WorkspaceID)
	if err != nil {
		return value, apperror.Normalize(err)
	}
	value.Output = projectThreadServiceOutput(page, workspace.RootPath, activityCommandSecrets(safeThreadCommandSpecFromJob(job)))
	secrets := activityCommandSecrets(safeThreadCommandSpecFromJob(job))
	prefixStdout, prefixStderr := job.Stdout, job.Stderr
	if s.commands != nil {
		stdout, stderr, owned, prefixErr := s.commands.ReadOwnedCommandRuntimeServicePrefix(ctx, runner.CommandRuntimeIdentity(job), runner.MaxCommandRuntimeOutputRead/2)
		if prefixErr != nil {
			return value, apperror.Wrap(apperror.CodeFailedPrecondition, "Thread service saved prefix could not be observed", prefixErr)
		}
		if owned {
			prefixStdout, prefixStderr = stdout, stderr
		}
	}
	prefixStdout = scrubThreadServiceText(threadServicePrefix(prefixStdout), workspace.RootPath, secrets)
	prefixStderr = scrubThreadServiceText(threadServicePrefix(prefixStderr), workspace.RootPath, secrets)
	value.CandidateURLs = threadServiceCandidateURLs([]runner.CommandRuntimeFrame{{Text: prefixStdout}, {Text: prefixStderr}, {Text: value.Output.Stdout}, {Text: value.Output.Stderr}})
	return value, nil
}

// Cursor fields describe the original sanitized ring. Each stream is coalesced
// before the existing public scrubber removes exact env input and host paths,
// including values split across collection chunks. No model paraphrase is used.
func projectThreadServiceOutput(page runner.CommandRuntimeOutputPage, root string, secrets []string) ThreadApplicationServiceOutputView {
	value := ThreadApplicationServiceOutputView{BaseCursor: page.BaseCursor, NextCursor: page.NextCursor, EndCursor: page.EndCursor, Dropped: page.Dropped, TruncationReason: page.TruncationReason, Available: true}
	for _, stream := range []runner.CommandRuntimeStream{runner.CommandRuntimeStdout, runner.CommandRuntimeStderr} {
		var combined strings.Builder
		for _, item := range page.Frames {
			if item.Stream != stream {
				continue
			}
			combined.WriteString(item.Text)
		}
		if combined.Len() == 0 {
			continue
		}
		text := scrubThreadServiceText(combined.String(), root, secrets)
		if len(text) > runner.MaxCommandRuntimeOutputRead/2 {
			text = strings.ToValidUTF8(text[len(text)-runner.MaxCommandRuntimeOutputRead/2:], "")
			value.Dropped = true
			if value.TruncationReason == "" {
				value.TruncationReason = "public_output_limit"
			}
		}
		if stream == runner.CommandRuntimeStdout {
			value.Stdout = text
		} else {
			value.Stderr = text
		}
	}
	return value
}

func threadServicePrefix(value string) string {
	if len(value) <= runner.MaxCommandRuntimeOutputRead/2 {
		return value
	}
	return strings.ToValidUTF8(value[:runner.MaxCommandRuntimeOutputRead/2], "")
}

var threadServiceURLPattern = regexp.MustCompile(runner.CommandServiceURLPattern)

// The existing host-path scrubber recognizes the trailing p:/ in http:// as a
// Windows drive. Preserve only already-safe loopback URLs, after checking all
// exact private inputs and workspace-root spellings. Every other byte still
// goes through that same public scrubber, including unsafe or secret URLs.
func scrubThreadServiceText(value, root string, secrets []string) string {
	type preservedURL struct{ marker, value string }
	var preserved []preservedURL
	protected := threadServiceURLPattern.ReplaceAllStringFunc(value, func(raw string) string {
		candidate := strings.TrimRight(raw, ".,;)")
		if _, ok := canonicalThreadServiceURL(candidate); !ok {
			return raw
		}
		for _, secret := range secrets {
			if secret != "" && strings.Contains(candidate, secret) {
				return raw
			}
		}
		for _, path := range []string{root, strings.ReplaceAll(root, "\\", "/")} {
			if path != "" && strings.Contains(strings.ToLower(candidate), strings.ToLower(path)) {
				return raw
			}
		}
		parsed, _ := url.Parse(candidate)
		for _, secret := range secrets {
			if secret != "" && strings.Contains(parsed.Path, secret) {
				return raw
			}
		}
		for _, path := range []string{root, strings.ReplaceAll(root, "\\", "/")} {
			if path != "" && strings.Contains(strings.ToLower(parsed.Path), strings.ToLower(path)) {
				return raw
			}
		}
		if windowsAbsolutePath.MatchString(parsed.Path) || fileURIAbsolutePath.MatchString(parsed.Path) {
			return raw
		}
		marker := "APPLICATIONURL" + strconv.Itoa(len(preserved)) + "PLACEHOLDER"
		for strings.Contains(value, marker) {
			marker += "X"
		}
		preserved = append(preserved, preservedURL{marker: marker, value: candidate})
		return marker + strings.TrimPrefix(raw, candidate)
	})
	protected = scrubThreadActivityText(protected, root, secrets...)
	for _, item := range preserved {
		protected = strings.ReplaceAll(protected, item.marker, item.value)
	}
	return protected
}

func canonicalThreadServiceURL(candidate string) (string, bool) {
	return runner.CanonicalCommandServiceURL(candidate)
}

func threadServiceCandidateURLs(frames []runner.CommandRuntimeFrame) []ThreadApplicationServiceURLView {
	candidates := runner.CommandServiceURLCandidates(frames)
	values := make([]ThreadApplicationServiceURLView, 0, len(candidates))
	for _, candidate := range candidates {
		values = append(values, ThreadApplicationServiceURLView(candidate))
	}
	return values
}
func (s *ThreadApplicationService) Stop(ctx context.Context, threadID, jobID string, request ThreadApplicationServiceStopRequest) (ThreadApplicationServiceStopView, error) {
	value := ThreadApplicationServiceStopView{Version: ThreadApplicationServicesProtocolVersion}
	if err := s.checkThread(ctx, threadID); err != nil {
		return value, err
	}
	if request.Version != ThreadApplicationServicesProtocolVersion || !domain.ValidAgentID(request.ExpectedRunID) ||
		!domain.ValidAgentID(jobID) || request.OperationKey != "application-stop-"+jobID {
		return value, apperror.New(apperror.CodeInvalidArgument, "Thread service stop binding is invalid")
	}
	job, err := s.store.GetThreadCommandRuntimeJob(ctx, threadID, jobID)
	if err != nil {
		return value, apperror.Normalize(err)
	}
	if job.RunID != request.ExpectedRunID {
		return value, apperror.New(apperror.CodeConflict, "Thread service belongs to another Run")
	}
	if job.Validate() != nil {
		return value, apperror.New(apperror.CodeFailedPrecondition, "Thread service Job is invalid")
	}
	if job.State.Terminal() {
		if !job.TreeReaped {
			return value, apperror.New(apperror.CodeConflict, "Thread service process tree is not reaped")
		}
		value.Service, err = s.project(ctx, threadID, serviceMetadata(job))
		value.Replayed = true
		return value, err
	}
	if s.commands == nil {
		return value, apperror.New(apperror.CodeFailedPrecondition, "Thread service live owner is unavailable")
	}
	snapshot, replayed, err := s.commands.StopThreadApplicationServiceJob(ctx, job)
	if err != nil {
		return value, apperror.Wrap(apperror.CodeConflict, "Thread service live ownership is unavailable", err)
	}
	value.Service, err = s.project(ctx, threadID, serviceMetadata(job))
	if err != nil {
		return value, err
	}
	value.Service.State, value.Service.ExitCode, value.Service.CompletedAt, value.Service.CanStop = string(snapshot.State), cloneActivityInt(snapshot.ExitCode), cloneActivityTime(snapshot.CompletedAt), false
	value.Replayed = replayed
	return value, nil
}

func (s *CommandRuntimeService) ReadOwnedCommandRuntimeServiceState(ctx context.Context, id runner.CommandRuntimeJobIdentity) (runner.CommandRuntimeJobSnapshot, bool, error) {
	if s == nil || s.manager == nil || !id.Adapter.SameBackend(s.adapter) {
		return runner.CommandRuntimeJobSnapshot{}, false, nil
	}
	return s.manager.ReadOwnedCommandRuntimeServiceState(ctx, id)
}

func (s *CommandRuntimeService) ReadOwnedCommandRuntimeServicePrefix(ctx context.Context, id runner.CommandRuntimeJobIdentity, maxBytes int) (string, string, bool, error) {
	if s == nil || s.manager == nil || !id.Adapter.SameBackend(s.adapter) {
		return "", "", false, nil
	}
	return s.manager.ReadOwnedCommandRuntimeServicePrefix(ctx, id, maxBytes)
}

func (s *CommandRuntimeService) StopThreadApplicationServiceJob(ctx context.Context, job runner.CommandRuntimeJob) (runner.CommandRuntimeJobSnapshot, bool, error) {
	if s == nil || s.manager == nil || !job.Adapter.SameBackend(s.adapter) {
		return runner.CommandRuntimeJobSnapshot{}, false, runner.ErrCommandRuntimeUncertain
	}
	snapshot, replayed, err := s.manager.CancelOwnedCommandRuntimeServiceJob(ctx, job)
	if err == nil && snapshot.State.Terminal() {
		record, readErr := s.store.GetCommandRuntimeJob(ctx, job.ID)
		if readErr == nil {
			readErr = s.completeCommandRuntimeJobBoundary(ctx, record)
		}
		err = errors.Join(err, readErr)
	}
	return snapshot, replayed, err
}

func (m *CommandRuntimeMultiplexer) ReadOwnedCommandRuntimeServiceState(ctx context.Context, id runner.CommandRuntimeJobIdentity) (runner.CommandRuntimeJobSnapshot, bool, error) {
	if m != nil {
		for _, service := range m.adapters {
			if id.Adapter.SameBackend(service.adapter) {
				return service.ReadOwnedCommandRuntimeServiceState(ctx, id)
			}
		}
	}
	return runner.CommandRuntimeJobSnapshot{}, false, nil
}

func (m *CommandRuntimeMultiplexer) ReadOwnedCommandRuntimeServicePrefix(ctx context.Context, id runner.CommandRuntimeJobIdentity, maxBytes int) (string, string, bool, error) {
	if m != nil {
		for _, service := range m.adapters {
			if id.Adapter.SameBackend(service.adapter) {
				return service.ReadOwnedCommandRuntimeServicePrefix(ctx, id, maxBytes)
			}
		}
	}
	return "", "", false, nil
}

func (m *CommandRuntimeMultiplexer) StopThreadApplicationServiceJob(ctx context.Context, job runner.CommandRuntimeJob) (runner.CommandRuntimeJobSnapshot, bool, error) {
	if m != nil {
		for _, service := range m.adapters {
			if job.Adapter.SameBackend(service.adapter) {
				return service.StopThreadApplicationServiceJob(ctx, job)
			}
		}
	}
	return runner.CommandRuntimeJobSnapshot{}, false, runner.ErrCommandRuntimeUncertain
}
