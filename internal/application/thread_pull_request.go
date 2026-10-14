package application

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/githubreview"
	"cyberagent-workbench/internal/gitmutation"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolcontract"
)

type ThreadPullRequestStore interface {
	RunExecutionLeaseStore
	GetThread(context.Context, string) (domain.Thread, error)
	GetThreadByRun(context.Context, string) (domain.Thread, error)
	CreateRemoteOperation(context.Context, gitmutation.RemoteRecord) (gitmutation.RemoteRecord, bool, error)
	GetRemoteOperation(context.Context, string) (gitmutation.RemoteRecord, bool, error)
	StartRemoteOperation(context.Context, string, string, time.Time) (gitmutation.RemoteRecord, bool, error)
	CompleteRemoteOperation(context.Context, string, gitmutation.RemoteRecord, time.Time) (gitmutation.RemoteRecord, bool, error)
	EnsureApproval(context.Context, approval.Proposal) (approval.Record, error)
	GetApproval(context.Context, string) (approval.Record, error)
	GetApprovalByProposal(context.Context, string) (approval.Record, error)
	CheckThreadGitIdle(context.Context, string, string, *domain.RunExecutionLease) error
}

// The additional interface leaves the existing review/comment provider contract
// unchanged. Both services share the same authenticated github.com client.
type threadPullRequestRemote interface {
	DefaultBranch(context.Context, githubreview.RepositoryIdentity, githubreview.CredentialReference) (string, error)
	BranchSHA(context.Context, githubreview.RepositoryIdentity, string, githubreview.CredentialReference) (string, error)
	ListPullRequests(context.Context, githubreview.RepositoryIdentity, string, string, githubreview.CredentialReference) ([]githubreview.PullRequest, error)
	GetPullRequest(context.Context, githubreview.RepositoryIdentity, int64, githubreview.CredentialReference) (githubreview.PullRequest, error)
	ObserveDraft(context.Context, githubreview.PullRequestDraft) (githubreview.PullRequest, bool, error)
	CreateDraft(context.Context, githubreview.PullRequestDraft, ...toolcontract.DispatchGuard) (githubreview.PullRequest, error)
}

type ThreadPullRequestService struct {
	store  ThreadPullRequestStore
	review *GitHubReviewService
	git    ThreadPullRequestGitReader
	now    func() time.Time
}

func NewThreadPullRequestService(store ThreadPullRequestStore, review *GitHubReviewService, git ThreadPullRequestGitReader) *ThreadPullRequestService {
	return &ThreadPullRequestService{store: store, review: review, git: git, now: func() time.Time { return time.Now().UTC() }}
}

func (s *ThreadPullRequestService) client(ctx context.Context, connectionID string) (githubreview.Connection, githubReviewRemote, threadPullRequestRemote, error) {
	if s == nil || s.store == nil || s.git == nil || s.review == nil {
		return githubreview.Connection{}, nil, nil, apperror.New(apperror.CodeFailedPrecondition, "task pull requests are unavailable")
	}
	connection, _, client, err := s.review.loadClient(ctx, connectionID, true)
	if err != nil {
		return connection, nil, nil, err
	}
	remote, ok := client.(threadPullRequestRemote)
	if !ok {
		return connection, nil, nil, apperror.New(apperror.CodeFailedPrecondition, "GitHub draft pull request transport is unavailable")
	}
	return connection, client, remote, nil
}

func requireThreadPRRepository(bound ThreadGitContext, repo githubreview.RepositoryIdentity) error {
	for _, remote := range bound.RemoteURLs {
		parsed, err := url.Parse(remote.URL)
		if err == nil && parsed.Scheme == "https" && strings.EqualFold(parsed.Host, "github.com") && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && strings.EqualFold(strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git"), repo.FullName) {
			return nil
		}
	}
	return apperror.New(apperror.CodeFailedPrecondition, "the selected GitHub connection does not match this task's HTTPS Git remote")
}

func (s *ThreadPullRequestService) authority(ctx context.Context, bound ThreadGitContext, lease *domain.RunExecutionLease) error {
	authority, err := s.review.loadRunBinding(ctx, bound.RunID, false)
	if err != nil {
		return err
	}
	if authority.run.SessionID != bound.SessionID || authority.mission.WorkspaceID != bound.SourceWorkspaceID || authority.mode.Phase != domain.ExecutionPhaseDeliver {
		return apperror.New(apperror.CodeFailedPrecondition, "draft creation requires the current Code/Deliver task binding")
	}
	permission, err := s.review.store.GetRunExecutionPermission(ctx, bound.RunID)
	if err != nil {
		return err
	}
	if permission.Mode.IsApprovalMode() {
		if permission.Validate() != nil || permission.RunID != authority.run.ID || permission.MissionID != authority.mission.ID ||
			!s.review.permissionCapabilities.OperatorApprovalEnabled || s.review.permissionCapabilities.RuntimeAuthority == nil ||
			!s.review.permissionCapabilities.AllowsSnapshot(permission) {
			return apperror.New(apperror.CodePolicyDenied, "draft runtime authorization is unavailable")
		}
		// Readiness to request review is not permission to publish. Create binds
		// exact native consent through the common policy and transport guard.
		return s.store.CheckThreadGitIdle(ctx, bound.ThreadID, bound.RunID, lease)
	}
	return apperror.New(apperror.CodePolicyDenied, "select a current approval preference before creating a draft")
}

func (s *ThreadPullRequestService) Discover(ctx context.Context, threadID, connectionID, base string) (ThreadPullRequestDiscovery, error) {
	connection, client, remote, err := s.client(ctx, connectionID)
	if err != nil {
		return ThreadPullRequestDiscovery{}, err
	}
	bound, err := s.git.CaptureThreadGitContext(ctx, threadID)
	if err != nil {
		return ThreadPullRequestDiscovery{}, err
	}
	if err = requireThreadPRRepository(bound, connection.Repository); err != nil {
		return ThreadPullRequestDiscovery{}, err
	}
	qualification, err := client.Qualify(ctx, connection.Repository, 0, connection.Credential)
	if err != nil {
		return ThreadPullRequestDiscovery{}, githubReviewApplicationError(err)
	}
	if !qualification.Eligible || !qualification.Capability.Read {
		for _, diagnostic := range qualification.Diagnostics {
			if diagnostic.Level == githubreview.DiagnosticError {
				return ThreadPullRequestDiscovery{}, githubReviewApplicationError(&githubreview.Error{Code: githubreview.FailureCode(diagnostic.Code), Message: diagnostic.Message})
			}
		}
		return ThreadPullRequestDiscovery{}, apperror.New(apperror.CodeFailedPrecondition, "GitHub repository read qualification is unavailable")
	}
	if base == "" {
		base, err = remote.DefaultBranch(ctx, connection.Repository, connection.Credential)
		if err != nil {
			return ThreadPullRequestDiscovery{}, githubReviewApplicationError(err)
		}
	}
	value := ThreadPullRequestDiscovery{Version: ThreadPullRequestVersion, Context: bound, ConnectionID: connection.ID, Repository: connection.Repository, BaseBranch: base, PullRequests: []githubreview.PullRequest{}, WriteEnabled: connection.Network.WriteEnabled, WritePermissionVerified: qualification.Capability.Review, Diagnostics: qualification.Diagnostics, CheckedAt: s.now()}
	value.BaseSHA, err = remote.BranchSHA(ctx, connection.Repository, base, connection.Credential)
	if err != nil {
		return value, githubReviewApplicationError(err)
	}
	value.RemoteHeadSHA, err = remote.BranchSHA(ctx, connection.Repository, bound.Branch, connection.Credential)
	var remoteErr *githubreview.Error
	if err != nil && !(errors.As(err, &remoteErr) && remoteErr.Code == githubreview.FailureNotFound) {
		return value, githubReviewApplicationError(err)
	}
	value.HeadPublished = value.RemoteHeadSHA == bound.HeadSHA && value.RemoteHeadSHA != ""
	value.PullRequests, err = remote.ListPullRequests(ctx, connection.Repository, bound.Branch, base, connection.Credential)
	return value, githubReviewApplicationError(err)
}

func threadPRKey(threadID, key string) string {
	return runmutation.OperationKeyDigest(ThreadPullRequestVersion, threadID, key)
}
func validThreadPRKey(key string) bool {
	return key != "" && len(key) <= 256 && utf8.ValidString(key) && key == strings.TrimSpace(key) && !strings.ContainsAny(key, "\r\n\x00")
}
func threadPRFingerprint(p ThreadPullRequestPreview, authority ...string) string {
	p.ApprovalFingerprint = ""
	p.CreatedAt = time.Time{}
	stored := threadPRStoredIntent{ThreadPullRequestPreview: p}
	if len(authority) > 0 {
		stored.AuthorityFingerprint = authority[0]
	}
	encoded, _ := json.Marshal(stored)
	return runmutation.Fingerprint(ThreadPullRequestVersion, string(encoded))
}

func (s *ThreadPullRequestService) Preview(ctx context.Context, threadID string, request ThreadPullRequestPreviewRequest) (ThreadPullRequestPreviewResult, error) {
	if request.Version != ThreadPullRequestVersion || !validThreadPRKey(request.OperationKey) {
		return ThreadPullRequestPreviewResult{}, apperror.New(apperror.CodeInvalidArgument, "draft preview requires its original bounded operation key")
	}
	id := "thread-pr-" + threadPRKey(threadID, request.OperationKey)
	if row, found, err := s.store.GetRemoteOperation(ctx, id); err != nil {
		return ThreadPullRequestPreviewResult{}, err
	} else if found {
		p, err := s.intent(ctx, threadID, row)
		if err != nil {
			return ThreadPullRequestPreviewResult{}, err
		}
		if p.ConnectionID != request.ConnectionID || p.RunID != request.ExpectedRunID || p.Draft.HeadSHA != request.ExpectedHeadSHA || p.Draft.Title != request.Title || p.Draft.Body != request.Body || (request.BaseBranch != "" && p.Draft.BaseBranch != request.BaseBranch) {
			return ThreadPullRequestPreviewResult{}, apperror.New(apperror.CodeConflict, "the original draft operation key is bound to different content")
		}
		return s.previewResult(ctx, p, true)
	}
	discovery, err := s.Discover(ctx, threadID, request.ConnectionID, request.BaseBranch)
	if err != nil {
		return ThreadPullRequestPreviewResult{}, err
	}
	bound := discovery.Context
	if bound.RunID != request.ExpectedRunID || bound.HeadSHA != request.ExpectedHeadSHA {
		return ThreadPullRequestPreviewResult{}, apperror.New(apperror.CodeConflict, "task execution or commit changed; review the current Git state")
	}
	if len(discovery.PullRequests) > 0 {
		return ThreadPullRequestPreviewResult{Version: ThreadPullRequestVersion, ExistingPullRequests: discovery.PullRequests}, nil
	}
	if !discovery.HeadPublished || !discovery.WriteEnabled {
		return ThreadPullRequestPreviewResult{}, apperror.New(apperror.CodeFailedPrecondition, "push the reviewed commit and enable the selected GitHub connection before draft creation")
	}
	if err = s.authority(ctx, bound, nil); err != nil {
		return ThreadPullRequestPreviewResult{}, err
	}
	connection, _, _, err := s.client(ctx, request.ConnectionID)
	if err != nil {
		return ThreadPullRequestPreviewResult{}, err
	}
	p := ThreadPullRequestPreview{Version: ThreadPullRequestVersion, OperationID: id, ThreadID: threadID, RunID: bound.RunID, SessionID: bound.SessionID, WorkspaceID: bound.WorkspaceID, SourceWorkspaceID: bound.SourceWorkspaceID, ConnectionID: connection.ID, ConnectionGeneration: connection.Generation, BindingFingerprint: bound.StatusFingerprint, DraftOnly: true, CreatedAt: s.now(), Draft: githubreview.PullRequestDraft{Repository: connection.Repository, Credential: connection.Credential, HeadBranch: bound.Branch, HeadSHA: bound.HeadSHA, BaseBranch: discovery.BaseBranch, BaseSHA: discovery.BaseSHA, Title: request.Title, Body: request.Body, Marker: threadPRKey(threadID, request.OperationKey)}}
	if err = p.Draft.Validate(); err != nil {
		return ThreadPullRequestPreviewResult{}, apperror.New(apperror.CodeInvalidArgument, err.Error())
	}
	authorityFingerprint, _, err := s.reviewAuthorityFingerprint(ctx, bound, true)
	if err != nil {
		return ThreadPullRequestPreviewResult{}, err
	}
	p.ApprovalFingerprint = threadPRFingerprint(p, authorityFingerprint)
	encoded, err := json.Marshal(threadPRStoredIntent{ThreadPullRequestPreview: p, AuthorityFingerprint: authorityFingerprint})
	if err != nil {
		return ThreadPullRequestPreviewResult{}, err
	}
	if len(encoded) > 32768 {
		return ThreadPullRequestPreviewResult{}, apperror.New(apperror.CodeResourceExhausted, "draft intent exceeds the existing remote operation storage bound")
	}
	row, replayed, err := s.store.CreateRemoteOperation(ctx, gitmutation.RemoteRecord{ID: id, ProtocolVersion: "repository_remote.v1", OperationKeyDigest: threadPRKey(threadID, request.OperationKey), RequestFingerprint: p.ApprovalFingerprint, RunID: p.RunID, WorkspaceID: p.WorkspaceID, Operation: gitmutation.RemoteCreatePR, SpecJSON: string(encoded), RemoteHost: "github.com", RemotePort: "443", Protocol: "https", Branch: p.Draft.HeadBranch, PreHead: p.Draft.HeadSHA, CreatedAt: p.CreatedAt})
	if err != nil {
		return ThreadPullRequestPreviewResult{}, err
	}
	p, err = s.intent(ctx, threadID, row)
	if err != nil {
		return ThreadPullRequestPreviewResult{}, err
	}
	return s.previewResult(ctx, p, replayed)
}

func (s *ThreadPullRequestService) previewResult(ctx context.Context, p ThreadPullRequestPreview, replayed bool) (ThreadPullRequestPreviewResult, error) {
	a, err := s.store.GetApprovalByProposal(ctx, p.OperationID)
	if err != nil {
		if apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeNotFound {
			return ThreadPullRequestPreviewResult{}, err
		}
		a, err = s.store.EnsureApproval(ctx, approval.Proposal{IdempotencyKey: approval.ProposalIdempotencyKey(ThreadPullRequestApprovalTool, p.OperationID), ProposalID: p.OperationID, SessionID: p.SessionID, WorkspaceID: p.SourceWorkspaceID, ToolName: ThreadPullRequestApprovalTool, ActionClass: "github_pull_request_create", Mode: "per_call", Status: approval.StatusPending, RequestFingerprint: p.ApprovalFingerprint, RequestedBy: "http_thread_operator"})
	}
	if err != nil {
		return ThreadPullRequestPreviewResult{}, err
	}
	return ThreadPullRequestPreviewResult{Version: ThreadPullRequestVersion, Preview: &p, Approval: &a, ExistingPullRequests: []githubreview.PullRequest{}, Replayed: replayed}, nil
}

func (s *ThreadPullRequestService) intent(ctx context.Context, threadID string, row gitmutation.RemoteRecord) (ThreadPullRequestPreview, error) {
	p, authorityFingerprint, decodeErr := decodeThreadPRIntent(row.SpecJSON)
	if decodeErr != nil || p.Version != ThreadPullRequestVersion || !p.DraftOnly || p.ThreadID != threadID || p.OperationID != row.ID || p.RunID != row.RunID || p.WorkspaceID != row.WorkspaceID || row.Operation != gitmutation.RemoteCreatePR || p.Draft.Validate() != nil || p.ApprovalFingerprint != row.RequestFingerprint || threadPRFingerprint(p, authorityFingerprint) != row.RequestFingerprint {
		return p, apperror.New(apperror.CodeConflict, "stored draft intent does not match this task and original operation")
	}
	t, err := s.store.GetThreadByRun(ctx, row.RunID)
	if err != nil {
		return p, err
	}
	if t.ID != threadID || t.WorkspaceID != p.SourceWorkspaceID {
		return p, apperror.New(apperror.CodeConflict, "draft operation belongs to a different task or workspace")
	}
	return p, nil
}

func (s *ThreadPullRequestService) Create(ctx context.Context, threadID string, request ThreadPullRequestCreateRequest) (ThreadPullRequestResult, error) {
	if request.Version != ThreadPullRequestVersion || request.OperationID == "" || request.ApprovalID == "" {
		return ThreadPullRequestResult{}, apperror.New(apperror.CodeInvalidArgument, "draft creation requires an exact operation and approval")
	}
	row, found, err := s.store.GetRemoteOperation(ctx, request.OperationID)
	if err != nil {
		return ThreadPullRequestResult{}, err
	}
	if !found {
		return ThreadPullRequestResult{}, apperror.New(apperror.CodeNotFound, "draft intent was not received")
	}
	p, err := s.intent(ctx, threadID, row)
	if err != nil {
		return ThreadPullRequestResult{}, err
	}
	if row.StartedAt != nil || row.CompletedAt != nil {
		return s.observeRecord(ctx, p, row, true)
	}
	var result ThreadPullRequestResult
	err = withRunExecutionLease(ctx, s.store, p.RunID, "thread-pr:"+row.OperationKeyDigest, RunExecutionLeasePolicy{TTL: time.Minute, RenewInterval: 10 * time.Second}, func(ctx context.Context, lease domain.RunExecutionLease) error {
		bound, err := s.git.CaptureThreadGitContext(ctx, threadID)
		if err != nil {
			return err
		}
		if !threadPRMatchesContext(p, bound) {
			return apperror.New(apperror.CodeConflict, "the reviewed task Git state changed; create a fresh preview")
		}
		if err = s.authority(ctx, bound, &lease); err != nil {
			return err
		}
		connection, _, remote, err := s.client(ctx, p.ConnectionID)
		if err != nil {
			return err
		}
		if connection.Generation != p.ConnectionGeneration || connection.Repository != p.Draft.Repository || connection.Credential != p.Draft.Credential || !connection.Network.WriteEnabled {
			return apperror.New(apperror.CodeConflict, "the reviewed GitHub connection changed or write-back was disabled")
		}
		if err = requireThreadPRRepository(bound, connection.Repository); err != nil {
			return err
		}
		a, err := s.store.GetApproval(ctx, request.ApprovalID)
		if err != nil {
			return err
		}
		if a.Status != approval.StatusApproved || !threadPRApprovalMatches(p, a) {
			return apperror.New(apperror.CodePolicyDenied, "draft creation requires approval of this exact persisted intent")
		}
		permission, err := s.review.store.GetRunExecutionPermission(ctx, p.RunID)
		if err != nil {
			return err
		}
		var guards []toolcontract.DispatchGuard
		if permission.Mode.IsApprovalMode() {
			guard, err := s.dispatchGuard(ctx, p, row, request.ApprovalID, lease)
			if err != nil {
				return err
			}
			guards = append(guards, guard)
		}
		claimed, first, err := s.store.StartRemoteOperation(ctx, row.ID, row.RequestFingerprint, s.now())
		if err != nil {
			return err
		}
		if !first {
			result, err = s.observeRecord(ctx, p, claimed, true)
			return err
		}
		pr, createErr := remote.CreateDraft(ctx, p.Draft, guards...)
		result = ThreadPullRequestResult{Version: ThreadPullRequestVersion, ThreadID: threadID, RunID: p.RunID, OperationID: p.OperationID, State: "unknown", Preview: &p, Approval: &a, CheckedAt: s.now()}
		if createErr != nil {
			mapped := apperror.Normalize(githubReviewApplicationError(createErr))
			result.ErrorCode = string(apperror.CodeOf(mapped))
			result.ErrorMessage = mapped.Error()
			dispatchState, hasDispatchState := githubreview.DraftDispatchState(createErr)
			if (hasDispatchState && dispatchState == toolcontract.ReceiptOutcomeUnknown) || (!hasDispatchState &&
				(ambiguousGitHubWriteError(createErr) || errors.Is(createErr, context.Canceled) || errors.Is(createErr, context.DeadlineExceeded))) {
				return nil
			}
			failure, _ := json.Marshal(map[string]string{"code": result.ErrorCode, "message": result.ErrorMessage})
			claimed.StderrPrefix = string(failure)
		} else {
			claimed.PullRequestNumber = pr.Number
			claimed.PullRequestURL = pr.URL
			claimed.PostHead = pr.HeadSHA
			result.PullRequest = &pr
			result.HeadMatchesReviewed = pr.HeadSHA == p.Draft.HeadSHA
			result.State = "created"
		}
		_, _, saveErr := s.store.CompleteRemoteOperation(context.WithoutCancel(ctx), row.ID, claimed, s.now())
		if saveErr != nil {
			result.ReceiptSaved = false
			result.State = "unknown"
			result.ErrorCode = string(apperror.CodeUnavailable)
			result.ErrorMessage = "remote result could not be durably recorded; only observe the original operation"
			return nil
		}
		result.ReceiptSaved = true
		if createErr != nil {
			result.State = "failed"
		}
		return nil
	})
	return result, err
}

// Observe never reserves, starts, approves or completes a ledger record. Even a
// missing remote marker cannot prove that an in-flight POST will never arrive.
func (s *ThreadPullRequestService) Observe(ctx context.Context, threadID, key string) (ThreadPullRequestResult, error) {
	if !validThreadPRKey(key) {
		return ThreadPullRequestResult{}, apperror.New(apperror.CodeInvalidArgument, "original operation key is required")
	}
	if _, err := s.store.GetThread(ctx, threadID); err != nil {
		return ThreadPullRequestResult{}, err
	}
	row, found, err := s.store.GetRemoteOperation(ctx, "thread-pr-"+threadPRKey(threadID, key))
	if err != nil {
		return ThreadPullRequestResult{}, err
	}
	if !found {
		return ThreadPullRequestResult{Version: ThreadPullRequestVersion, ThreadID: threadID, State: "not_received", CheckedAt: s.now()}, nil
	}
	p, err := s.intent(ctx, threadID, row)
	if err != nil {
		return ThreadPullRequestResult{}, err
	}
	return s.observeRecord(ctx, p, row, true)
}

func (s *ThreadPullRequestService) observeRecord(ctx context.Context, p ThreadPullRequestPreview, row gitmutation.RemoteRecord, replayed bool) (ThreadPullRequestResult, error) {
	r := ThreadPullRequestResult{Version: ThreadPullRequestVersion, ThreadID: p.ThreadID, RunID: p.RunID, OperationID: p.OperationID, State: "proposed", Preview: &p, ReceiptSaved: row.CompletedAt != nil, Replayed: replayed, CheckedAt: s.now()}
	if a, err := s.store.GetApprovalByProposal(ctx, p.OperationID); err == nil {
		r.Approval = &a
	} else if apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeNotFound {
		return r, err
	}
	if row.StartedAt == nil && row.CompletedAt == nil {
		return r, nil
	}
	if row.CompletedAt != nil && row.PullRequestNumber == 0 {
		r.State = "failed"
		var saved map[string]string
		if json.Unmarshal([]byte(row.StderrPrefix), &saved) == nil {
			r.ErrorCode = saved["code"]
			r.ErrorMessage = saved["message"]
		}
		return r, nil
	}
	r.State = "unknown"
	connection, _, remote, err := s.client(ctx, p.ConnectionID)
	if err == nil && (connection.Repository != p.Draft.Repository || connection.Credential != p.Draft.Credential) {
		err = apperror.New(apperror.CodeConflict, "original GitHub connection identity changed; no remote recovery was attempted")
	}
	if row.PullRequestNumber > 0 {
		r.State = "created"
		r.HeadMatchesReviewed = row.PostHead == p.Draft.HeadSHA
		r.PullRequest = &githubreview.PullRequest{Repository: p.Draft.Repository, Number: row.PullRequestNumber, URL: row.PullRequestURL, HeadSHA: row.PostHead, HeadBranch: p.Draft.HeadBranch, BaseBranch: p.Draft.BaseBranch, BaseSHA: p.Draft.BaseSHA}
	}
	if err == nil {
		var pr githubreview.PullRequest
		var found bool
		if row.PullRequestNumber > 0 {
			pr, err = remote.GetPullRequest(ctx, p.Draft.Repository, row.PullRequestNumber, p.Draft.Credential)
			found = err == nil
		} else {
			pr, found, err = remote.ObserveDraft(ctx, p.Draft)
		}
		if err == nil && found {
			r.State = "created"
			r.PullRequest = &pr
			r.HeadMatchesReviewed = pr.HeadSHA == p.Draft.HeadSHA
		}
	}
	if err != nil {
		mapped := apperror.Normalize(githubReviewApplicationError(err))
		r.ErrorCode = string(apperror.CodeOf(mapped))
		r.ErrorMessage = mapped.Error()
	}
	return r, nil
}

func (s *ThreadPullRequestService) ImportCredential(ctx context.Context, threadID string, request ThreadPullRequestCredentialRequest) (GitHubReviewCredentialView, error) {
	if request.Version != ThreadPullRequestVersion || !credential.ValidSecret(request.Token) {
		return GitHubReviewCredentialView{}, apperror.New(apperror.CodeInvalidArgument, "a valid bounded token is required")
	}
	connection, _, _, err := s.client(ctx, request.ConnectionID)
	if err != nil {
		return GitHubReviewCredentialView{}, err
	}
	if connection.Credential.Kind != githubreview.AuthFineGrainedPAT && connection.Credential.Kind != githubreview.AuthOAuthUser {
		return GitHubReviewCredentialView{}, apperror.New(apperror.CodeInvalidArgument, "token import requires a PAT or OAuth user connection")
	}
	bound, err := s.git.CaptureThreadGitContext(ctx, threadID)
	if err != nil {
		return GitHubReviewCredentialView{}, err
	}
	if err = requireThreadPRRepository(bound, connection.Repository); err != nil {
		return GitHubReviewCredentialView{}, err
	}
	if err = s.review.authLifecycle.ImportToken(ctx, connection.Credential, request.Token); err != nil {
		return GitHubReviewCredentialView{}, apperror.New(apperror.CodeUnavailable, "system credential storage could not save the token")
	}
	return s.review.CredentialStatus(ctx, connection.ID)
}
