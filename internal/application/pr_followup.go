package application

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/githubreview"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/scheduler"
)

const PRFollowupVersion = "pr-followup.v1"

type PRFollowupStore interface {
	CreatePRFollowup(context.Context, domain.PRFollowupState, string) (domain.PRFollowupState, bool, error)
	GetPRFollowup(context.Context, string) (domain.PRFollowupState, error)
	ListPRFollowups(context.Context, string, int) ([]domain.PRFollowup, error)
	ClaimDuePRFollowup(context.Context, string, time.Time) (domain.PRFollowupState, bool, error)
	SavePRFollowup(context.Context, domain.PRFollowupState, int64) (domain.PRFollowupState, error)
	RenewPRFollowupLease(context.Context, string, string, string, time.Time) (bool, error)
	ReleasePRFollowupLease(context.Context, string, string, string) error
}

type prFollowupTurnExecutor interface {
	ExecuteAutomatic(context.Context, ExecuteThreadTurnRequest) (ExecuteThreadTurnResult, error)
	CheckAutomaticTurn(context.Context, string) error
	ExecutionState(context.Context, string) (ThreadExecutionState, error)
	ObserveAutomaticTurn(context.Context, string, string, string) (AutomaticThreadTurnObservation, error)
}

type PRFollowupService struct {
	store        PRFollowupStore
	turns        prFollowupTurnExecutor
	review       *GitHubReviewService
	capture      func(context.Context, string) (ThreadGitContext, error)
	read         func(context.Context, string, int64) (githubreview.FollowupSnapshot, error)
	logs         func(context.Context, string, githubreview.FollowupSnapshot) (string, error)
	worker       *scheduler.Worker
	now          func() time.Time
	activeMu     sync.Mutex
	activeID     string
	activeCancel context.CancelFunc
}

type PRFollowupCreateRequest struct {
	Version                 string    `json:"version"`
	ConnectionID            string    `json:"connection_id"`
	PullRequest             int64     `json:"pull_request"`
	IntervalSeconds         int64     `json:"interval_seconds"`
	DeadlineAt              time.Time `json:"deadline_at"`
	MaxTurns                int       `json:"max_turns"`
	IncludeExistingComments bool      `json:"include_existing_comments"`
}

type PRFollowupTransitionRequest struct {
	Version          string `json:"version"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type PRFollowupList struct {
	Version     string              `json:"version"`
	ThreadID    string              `json:"thread_id"`
	WorkerState string              `json:"worker_state"`
	Items       []domain.PRFollowup `json:"items"`
}

func NewPRFollowupService(store PRFollowupStore, review *GitHubReviewService, git *ThreadGitService, turns *ThreadTurnService) (*PRFollowupService, error) {
	if store == nil || review == nil || git == nil || turns == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "PR follow-up requires GitHub, task Git and task execution")
	}
	s := &PRFollowupService{store: store, turns: turns, review: review, capture: git.CaptureThreadGitContext, now: func() time.Time { return time.Now().UTC() }}
	s.read = s.readGitHub
	s.logs = s.readFailureLogs
	worker, err := scheduler.NewWorker(s, scheduler.WorkerConfig{})
	if err != nil {
		return nil, err
	}
	s.worker = worker
	return s, nil
}

func (s *PRFollowupService) Run(ctx context.Context) error { return s.worker.Run(ctx) }

func (s *PRFollowupService) List(ctx context.Context, threadID string) (PRFollowupList, error) {
	items, err := s.store.ListPRFollowups(ctx, threadID, 20)
	if items == nil {
		items = []domain.PRFollowup{}
	}
	return PRFollowupList{Version: PRFollowupVersion, ThreadID: threadID, WorkerState: string(s.worker.Health().State), Items: items}, err
}

func (s *PRFollowupService) Create(ctx context.Context, threadID, key string, request PRFollowupCreateRequest) (domain.PRFollowup, error) {
	now := s.now()
	if request.Version != PRFollowupVersion || !validControlIdentity(threadID) || !validControlIdentity(key) || !validControlIdentity(request.ConnectionID) || request.PullRequest <= 0 || request.IntervalSeconds < 60 || request.IntervalSeconds > 3600 || request.MaxTurns < 1 || request.MaxTurns > 20 {
		return domain.PRFollowup{}, apperror.New(apperror.CodeInvalidArgument, "PR follow-up requires a 1–60 minute interval, 1–20 repair turns and a deadline within seven days")
	}
	request.DeadlineAt = request.DeadlineAt.UTC()
	id := "pr-followup-" + githubreview.Fingerprint(threadID, key)[:32]
	fingerprint := githubreview.Fingerprint(PRFollowupVersion, threadID, request.ConnectionID, fmt.Sprint(request.PullRequest), fmt.Sprint(request.IntervalSeconds), request.DeadlineAt.Format(time.RFC3339Nano), fmt.Sprint(request.MaxTurns), fmt.Sprint(request.IncludeExistingComments))
	// A repeated create confirms the original intent even after its workspace or
	// PR has changed; the store still checks the immutable request fingerprint.
	if existing, err := s.store.GetPRFollowup(ctx, id); err == nil {
		value, _, createErr := s.store.CreatePRFollowup(ctx, existing, fingerprint)
		return value.Record, createErr
	} else if apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeNotFound {
		return domain.PRFollowup{}, err
	}
	if !request.DeadlineAt.After(now) || request.DeadlineAt.After(now.Add(7*24*time.Hour)) {
		return domain.PRFollowup{}, apperror.New(apperror.CodeInvalidArgument, "PR follow-up deadline must be within the next seven days")
	}
	bound, err := s.capture(ctx, threadID)
	if err != nil {
		return domain.PRFollowup{}, err
	}
	snapshot, err := s.read(ctx, request.ConnectionID, request.PullRequest)
	if err != nil {
		return domain.PRFollowup{}, err
	}
	if snapshot.State != githubreview.EvidenceVerified || snapshot.PullRequest.State != "open" {
		return domain.PRFollowup{}, apperror.New(apperror.CodeFailedPrecondition, "PR must be open and its CI/comment snapshot complete before enabling follow-up")
	}
	if err = requireThreadPRRepository(bound, snapshot.PullRequest.Repository); err != nil {
		return domain.PRFollowup{}, err
	}
	if bound.Branch != snapshot.PullRequest.HeadBranch || bound.HeadSHA != snapshot.PullRequest.HeadSHA {
		return domain.PRFollowup{}, apperror.New(apperror.CodeFailedPrecondition, "select the task workspace at this PR's current source branch and commit before enabling follow-up")
	}
	state := domain.PRFollowupState{Record: domain.PRFollowup{ID: id, ThreadID: threadID, ConnectionID: request.ConnectionID, Repository: snapshot.PullRequest.Repository.FullName,
		WorkspaceID: bound.WorkspaceID, RootPathSHA256: githubreview.Fingerprint(bound.RootPath), Branch: bound.Branch, PullRequest: request.PullRequest,
		IntervalSeconds: request.IntervalSeconds, DeadlineAt: request.DeadlineAt, MaxTurns: request.MaxTurns, IncludeExistingComments: request.IncludeExistingComments,
		Status: domain.PRFollowupActive, Revision: 1, NextCheckAt: now, CreatedAt: now, UpdatedAt: now, LastHeadSHA: snapshot.PullRequest.HeadSHA, LastResult: "已启用，等待首次检查"}, Observed: map[string]string{}}
	// Establish the comment baseline now, not at the first background tick, so a
	// comment arriving between confirmation and polling is not silently skipped.
	if !request.IncludeExistingComments {
		baseline := observePRFollowup(snapshot, nil, false)
		for k, v := range baseline.seen {
			if strings.HasPrefix(k, "comment:") || strings.HasPrefix(k, "inline_comment:") || strings.HasPrefix(k, "review:") {
				state.Observed[k] = v
			}
		}
	}
	state.Initialized = true
	created, _, err := s.store.CreatePRFollowup(ctx, state, fingerprint)
	return created.Record, err
}

func (s *PRFollowupService) Transition(ctx context.Context, threadID, id, action string, request PRFollowupTransitionRequest) (domain.PRFollowup, error) {
	state, err := s.store.GetPRFollowup(ctx, id)
	if err != nil {
		return domain.PRFollowup{}, err
	}
	if state.Record.ThreadID != threadID {
		return domain.PRFollowup{}, apperror.New(apperror.CodeNotFound, "PR follow-up was not found in this task")
	}
	if request.Version != PRFollowupVersion || request.ExpectedRevision != state.Record.Revision {
		return domain.PRFollowup{}, apperror.New(apperror.CodeConflict, "PR follow-up changed; refresh before controlling it")
	}
	if state.Record.Status == domain.PRFollowupCompleted || state.Record.Status == domain.PRFollowupCancelled || state.Record.Status == domain.PRFollowupExhausted {
		return domain.PRFollowup{}, apperror.New(apperror.CodeFailedPrecondition, "this PR follow-up has ended")
	}
	switch action {
	case "pause":
		state.Record.Status = domain.PRFollowupPaused
		state.Record.LastResult = "已暂停持续跟进"
	case "cancel":
		state.Record.Status = domain.PRFollowupCancelled
		state.Record.LastResult = "已停止持续跟进"
	case "resume":
		if state.Record.Status == domain.PRFollowupActive {
			return state.Record, nil
		}
		if !s.now().Before(state.Record.DeadlineAt) {
			return domain.PRFollowup{}, apperror.New(apperror.CodeFailedPrecondition, "the follow-up deadline has passed")
		}
		if state.Record.Status == domain.PRFollowupReady && state.Pending != nil {
			state.ReviewedTurnID = state.Pending.ID
		}
		state.Record.Status = domain.PRFollowupActive
		state.Record.NextCheckAt = s.now()
		state.Record.LastError = ""
		state.Record.LastResult = "继续检查 PR"
	default:
		return domain.PRFollowup{}, apperror.New(apperror.CodeInvalidArgument, "unknown PR follow-up action")
	}
	state.LeaseOwner, state.LeaseToken = "", ""
	state.Record.UpdatedAt = s.now()
	updated, err := s.store.SavePRFollowup(ctx, state, request.ExpectedRevision)
	if err == nil && action != "resume" {
		s.activeMu.Lock()
		if s.activeID == id && s.activeCancel != nil {
			s.activeCancel()
		}
		s.activeMu.Unlock()
	}
	return updated.Record, err
}

func (s *PRFollowupService) readGitHub(ctx context.Context, connectionID string, number int64) (githubreview.FollowupSnapshot, error) {
	connection, _, client, err := s.review.loadClient(ctx, connectionID, true)
	if err != nil {
		return githubreview.FollowupSnapshot{}, err
	}
	remote, ok := client.(interface {
		ReadFollowupSnapshot(context.Context, githubreview.FollowupSnapshotRequest) (githubreview.FollowupSnapshot, error)
	})
	if !ok {
		return githubreview.FollowupSnapshot{}, apperror.New(apperror.CodeUnavailable, "GitHub PR follow-up transport is unavailable")
	}
	snapshot, err := remote.ReadFollowupSnapshot(ctx, githubreview.FollowupSnapshotRequest{Repository: connection.Repository, Number: number, Credential: connection.Credential})
	if err != nil {
		return snapshot, githubReviewApplicationError(err)
	}
	return snapshot, nil
}

func (s *PRFollowupService) readFailureLogs(ctx context.Context, connectionID string, snapshot githubreview.FollowupSnapshot) (string, error) {
	result, err := s.review.Fetch(ctx, GitHubReviewFetchRequest{ProtocolVersion: GitHubReviewAPIProtocolVersion, ConnectionID: connectionID, PullRequest: snapshot.PullRequest.Number})
	if err != nil {
		return "", err
	}
	if result.Snapshot.Identity.HeadSHA != snapshot.PullRequest.HeadSHA || result.Snapshot.State == githubreview.EvidenceStale {
		return "", apperror.New(apperror.CodeConflict, "PR changed while reading CI logs")
	}
	current := make(map[int64]bool)
	for _, run := range latestPRFollowupWorkflows(snapshot.WorkflowRuns) {
		current[run.ID] = true
	}
	var logs strings.Builder
	for _, job := range result.Snapshot.Jobs {
		if !current[job.RunID] || (job.Conclusion != "failure" && job.Conclusion != "timed_out") {
			continue
		}
		text := job.FailedLog.Text
		if len(text) > 2000 {
			text = strings.ToValidUTF8(text[:2000], "") + "\n[excerpt truncated]"
		}
		if logs.Len()+len(text) > 4000 {
			logs.WriteString("\nAdditional job logs omitted; inspect them before claiming a complete diagnosis.\n")
			break
		}
		fmt.Fprintf(&logs, "\nJob %d %s (%s): %s\n%s\n", job.ID, job.Name, job.LogState, job.LogReason, text)
	}
	if logs.Len() == 0 {
		return "CI failure logs are unavailable in this snapshot. Reproduce the failing check locally or report the missing diagnostic evidence.", nil
	}
	return redact.String(logs.String()), nil
}
