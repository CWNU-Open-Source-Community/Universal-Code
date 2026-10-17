package githubreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const FollowupSnapshotProtocolVersion = "github-pr-followup-snapshot.v1"

type FollowupSnapshotRequest struct {
	Repository RepositoryIdentity  `json:"repository"`
	Number     int64               `json:"number"`
	Credential CredentialReference `json:"credential"`
}

type FollowupWorkflowRun struct {
	ID         int64     `json:"id"`
	WorkflowID int64     `json:"workflow_id"`
	RunNumber  int64     `json:"run_number"`
	Name       string    `json:"name"`
	HeadSHA    string    `json:"head_sha"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	URL        string    `json:"url"`
	RunAttempt int       `json:"run_attempt"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type FollowupCommitStatus struct {
	ID          int64        `json:"id"`
	NodeID      string       `json:"node_id"`
	HeadSHA     string       `json:"head_sha"`
	Context     string       `json:"context"`
	State       string       `json:"state"`
	Description TextEvidence `json:"description"`
	TargetURL   string       `json:"target_url"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// FollowupSnapshot observes feedback and CI without downloading patches, logs,
// or artifacts. Remote text remains untrusted and never grants execution rights.
type FollowupSnapshot struct {
	ProtocolVersion string                 `json:"protocol_version"`
	PullRequest     PullRequest            `json:"pull_request"`
	Reviews         []Review               `json:"reviews"`
	InlineComments  []Comment              `json:"inline_comments"`
	IssueComments   []Comment              `json:"issue_comments"`
	CheckRuns       []CheckRun             `json:"check_runs"`
	WorkflowRuns    []FollowupWorkflowRun  `json:"workflow_runs"`
	CommitStatuses  []FollowupCommitStatus `json:"commit_statuses"`
	Pagination      []PageEvidence         `json:"pagination"`
	RateLimit       RateLimit              `json:"rate_limit"`
	State           EvidenceState          `json:"state"`
	Omissions       []string               `json:"omissions"`
	Fingerprint     string                 `json:"fingerprint"`
	FetchedAt       time.Time              `json:"fetched_at"`
}

func (s FollowupSnapshot) Validate() error {
	p := s.PullRequest
	if s.ProtocolVersion != FollowupSnapshotProtocolVersion || p.Repository.Validate() != nil ||
		p.Number <= 0 || !validIdentity(p.NodeID) || !validObjectID(p.HeadSHA) || !validObjectID(p.BaseSHA) ||
		(p.State != "open" && p.State != "closed") || (p.Merged && p.State != "closed") ||
		!s.State.Valid() || !validDigest(s.Fingerprint) || s.FetchedAt.IsZero() ||
		len(s.Reviews) > MaxReviews || len(s.InlineComments) > MaxComments || len(s.IssueComments) > MaxComments ||
		len(s.CheckRuns) > MaxCheckRuns || len(s.WorkflowRuns) > MaxCheckRuns || len(s.CommitStatuses) > MaxCheckRuns ||
		len(s.Pagination) != 6 || s.Fingerprint != s.fingerprint() {
		return errors.New("GitHub PR follow-up snapshot is invalid")
	}
	for _, page := range s.Pagination {
		if s.State == EvidenceVerified && !page.Complete {
			return errors.New("verified GitHub PR follow-up snapshot has incomplete pagination")
		}
	}
	return nil
}

func (s FollowupSnapshot) fingerprint() string {
	p := s.PullRequest
	parts := []string{FollowupSnapshotProtocolVersion, p.Repository.FullName, fmt.Sprint(p.Number), p.NodeID,
		p.HeadSHA, p.BaseSHA, p.HeadRepository, p.HeadBranch, p.BaseBranch, p.State,
		fmt.Sprint(p.Merged), fmt.Sprint(p.Draft), string(s.State)}
	items := make([]string, 0)
	for _, review := range s.Reviews {
		items = append(items, Fingerprint("review", fmt.Sprint(review.ID), review.NodeID, review.State, review.CommitSHA, review.Body.SHA256))
	}
	for kind, comments := range map[string][]Comment{"inline": s.InlineComments, "issue": s.IssueComments} {
		for _, comment := range comments {
			items = append(items, Fingerprint(kind, fmt.Sprint(comment.ID), comment.NodeID, comment.Body.SHA256,
				comment.Position.CommitSHA, comment.Position.Path, comment.Position.Side, fmt.Sprint(comment.Position.Line)))
		}
	}
	for _, check := range s.CheckRuns {
		items = append(items, Fingerprint("check", fmt.Sprint(check.ID), check.HeadSHA, check.Status, check.Conclusion,
			check.Title.SHA256, check.Summary.SHA256, check.Text.SHA256))
	}
	for _, run := range s.WorkflowRuns {
		items = append(items, Fingerprint("workflow", fmt.Sprint(run.ID), fmt.Sprint(run.WorkflowID), fmt.Sprint(run.RunNumber), run.HeadSHA, fmt.Sprint(run.RunAttempt), run.Status, run.Conclusion))
	}
	for _, status := range s.CommitStatuses {
		items = append(items, Fingerprint("status", fmt.Sprint(status.ID), status.HeadSHA, status.Context, status.State, status.Description.SHA256))
	}
	for _, page := range s.Pagination {
		items = append(items, Fingerprint("page", page.Resource, fmt.Sprint(page.Complete), page.OmittedReason))
	}
	for _, omission := range s.Omissions {
		items = append(items, Fingerprint("omission", omission))
	}
	sort.Strings(items)
	return Fingerprint(append(parts, items...)...)
}

// ReadFollowupSnapshot uses the existing authenticated, bounded GitHub transport.
// A failed section stays incomplete; a failed final identity read fails the poll.
func (c *Client) ReadFollowupSnapshot(ctx context.Context, request FollowupSnapshotRequest) (FollowupSnapshot, error) {
	if c == nil || request.Repository.Validate() != nil || request.Number <= 0 || request.Credential.Validate() != nil {
		return FollowupSnapshot{}, errors.New("GitHub PR follow-up request is invalid")
	}
	pull, err := c.GetPullRequest(ctx, request.Repository, request.Number, request.Credential)
	if err != nil {
		return FollowupSnapshot{}, err
	}
	s := FollowupSnapshot{ProtocolVersion: FollowupSnapshotProtocolVersion, PullRequest: pull,
		Reviews: []Review{}, InlineComments: []Comment{}, IssueComments: []Comment{}, CheckRuns: []CheckRun{},
		WorkflowRuns: []FollowupWorkflowRun{}, CommitStatuses: []FollowupCommitStatus{},
		Pagination: []PageEvidence{}, Omissions: []string{}, State: EvidenceVerified, FetchedAt: c.now().UTC()}
	repoPath := repositoryAPIPath(request.Repository)
	prPath := repoPath + "/pulls/" + strconv.FormatInt(request.Number, 10)
	commitPath := repoPath + "/commits/" + url.PathEscape(pull.HeadSHA)

	reviews, page, rate, readErr := fetchFollowupPages[reviewResponse](c, ctx, prPath+"/reviews", "", nil, request.Credential, MaxReviews, "reviews", "")
	for _, review := range reviews {
		if review.ID <= 0 || !validIdentity(review.NodeID) {
			readErr = malformedFollowup("review identity")
			break
		}
		s.Reviews = append(s.Reviews, Review{ID: review.ID, NodeID: sanitizeIdentity(review.NodeID, MaxIdentityRunes),
			Author: sanitizeIdentity(review.User.Login, 100), State: strings.ToUpper(sanitizeIdentity(review.State, 32)),
			CommitSHA: strings.ToLower(strings.TrimSpace(review.CommitID)), Body: SanitizeRemoteText(review.Body, MaxTextBytes), SubmittedAt: review.SubmittedAt})
	}
	s.recordPage(page, rate, readErr)
	for _, section := range []struct {
		path, resource string
		target         *[]Comment
	}{{prPath + "/comments", "inline_comments", &s.InlineComments},
		{repoPath + "/issues/" + strconv.FormatInt(request.Number, 10) + "/comments", "issue_comments", &s.IssueComments}} {
		comments, page, rate, readErr := fetchFollowupPages[commentResponse](c, ctx, section.path, "", nil, request.Credential, MaxComments, section.resource, "")
		for _, comment := range comments {
			if comment.ID <= 0 || !validIdentity(comment.NodeID) {
				readErr = malformedFollowup("comment identity")
				break
			}
			*section.target = append(*section.target, convertComment(comment, ""))
		}
		s.recordPage(page, rate, readErr)
	}

	checks, page, rate, readErr := fetchFollowupPages[checkRunResponse](c, ctx, commitPath+"/check-runs", "check_runs", url.Values{"filter": {"latest"}}, request.Credential, MaxCheckRuns, "check_runs", "")
	for _, check := range checks {
		if check.ID <= 0 || !strings.EqualFold(check.HeadSHA, pull.HeadSHA) {
			readErr = malformedFollowup("check run identity or head")
			break
		}
		s.CheckRuns = append(s.CheckRuns, CheckRun{ID: check.ID, NodeID: sanitizeIdentity(check.NodeID, MaxIdentityRunes),
			Name: sanitizeIdentity(check.Name, 300), Status: sanitizeIdentity(check.Status, 32), Conclusion: sanitizeIdentity(check.Conclusion, 32),
			HeadSHA: pull.HeadSHA, DetailsURL: safeGitHubURL(check.DetailsURL), Title: SanitizeRemoteText(check.Output.Title, MaxTextBytes),
			Summary: SanitizeRemoteText(check.Output.Summary, MaxTextBytes), Text: SanitizeRemoteText(check.Output.Text, MaxTextBytes),
			StartedAt: check.StartedAt, CompletedAt: check.CompletedAt})
	}
	s.recordPage(page, rate, readErr)

	runs, page, rate, readErr := fetchFollowupPages[followupWorkflowResponse](c, ctx, repoPath+"/actions/runs", "workflow_runs",
		url.Values{"head_sha": {pull.HeadSHA}}, request.Credential, MaxCheckRuns, "workflow_runs", "")
	for _, run := range runs {
		if run.ID <= 0 || run.WorkflowID <= 0 || run.RunNumber <= 0 || run.RunAttempt < 1 || !strings.EqualFold(run.HeadSHA, pull.HeadSHA) {
			readErr = malformedFollowup("workflow run identity or head")
			break
		}
		s.WorkflowRuns = append(s.WorkflowRuns, FollowupWorkflowRun{ID: run.ID, WorkflowID: run.WorkflowID, RunNumber: run.RunNumber, Name: sanitizeIdentity(run.Name, 300), HeadSHA: pull.HeadSHA,
			Status: sanitizeIdentity(run.Status, 32), Conclusion: sanitizeIdentity(run.Conclusion, 32),
			URL: safeGitHubURL(run.HTMLURL), RunAttempt: run.RunAttempt, UpdatedAt: run.UpdatedAt})
	}
	s.recordPage(page, rate, readErr)

	statuses, page, rate, readErr := fetchFollowupPages[followupStatusResponse](c, ctx, commitPath+"/status", "statuses", nil, request.Credential, MaxCheckRuns, "commit_statuses", pull.HeadSHA)
	for _, status := range statuses {
		if status.ID <= 0 || !validIdentity(status.NodeID) ||
			(status.State != "error" && status.State != "failure" && status.State != "pending" && status.State != "success") {
			readErr = malformedFollowup("commit status identity or state")
			break
		}
		s.CommitStatuses = append(s.CommitStatuses, FollowupCommitStatus{ID: status.ID, NodeID: sanitizeIdentity(status.NodeID, MaxIdentityRunes),
			HeadSHA: pull.HeadSHA, Context: sanitizeIdentity(status.Context, 300), State: status.State,
			Description: SanitizeRemoteText(status.Description, MaxTextBytes), TargetURL: safeGitHubURL(status.TargetURL),
			CreatedAt: status.CreatedAt, UpdatedAt: status.UpdatedAt})
	}
	s.recordPage(page, rate, readErr)

	current, err := c.GetPullRequest(ctx, request.Repository, request.Number, request.Credential)
	if err != nil {
		return FollowupSnapshot{}, err
	}
	if current.NodeID != pull.NodeID || current.HeadSHA != pull.HeadSHA || current.BaseSHA != pull.BaseSHA ||
		current.HeadRepository != pull.HeadRepository || current.HeadBranch != pull.HeadBranch || current.BaseBranch != pull.BaseBranch ||
		current.State != pull.State || current.Merged != pull.Merged || current.Draft != pull.Draft || !current.UpdatedAt.Equal(pull.UpdatedAt) {
		s.State = EvidenceStale
		s.Omissions = append(s.Omissions, "pull request changed during collection; poll again before consuming feedback")
	}
	s.Omissions = uniqueSorted(s.Omissions)
	s.Fingerprint = s.fingerprint()
	if err := s.Validate(); err != nil {
		return FollowupSnapshot{}, malformedFollowup("assembled snapshot")
	}
	encoded, err := json.Marshal(s)
	if err != nil || len(encoded) > MaxSnapshotBytes {
		return FollowupSnapshot{}, &Error{Code: FailureResponseBound, Message: "GitHub PR follow-up snapshot exceeded its byte bound"}
	}
	return s, nil
}

func (s *FollowupSnapshot) recordPage(page PageEvidence, rate RateLimit, err error) {
	if rate.Limit > 0 {
		if s.RateLimit.RetryAt.After(rate.RetryAt) {
			rate.RetryAt = s.RateLimit.RetryAt
		}
		s.RateLimit = rate
	}
	if err != nil {
		code := FailureUnavailable
		var typed *Error
		if errors.As(err, &typed) {
			code = typed.Code
			if typed.RetryAt.After(s.RateLimit.RetryAt) {
				s.RateLimit.RetryAt = typed.RetryAt
			}
		}
		page.Complete = false
		page.OmittedReason = string(code)
	}
	if !page.Complete {
		s.State = EvidencePartial
		s.Omissions = append(s.Omissions, page.Resource+" incomplete ("+page.OmittedReason+")")
	}
	s.Pagination = append(s.Pagination, page)
}

type followupWorkflowResponse struct {
	ID         int64     `json:"id"`
	WorkflowID int64     `json:"workflow_id"`
	RunNumber  int64     `json:"run_number"`
	Name       string    `json:"name"`
	HeadSHA    string    `json:"head_sha"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	HTMLURL    string    `json:"html_url"`
	RunAttempt int       `json:"run_attempt"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type followupStatusResponse struct {
	ID          int64     `json:"id"`
	NodeID      string    `json:"node_id"`
	Context     string    `json:"context"`
	State       string    `json:"state"`
	Description string    `json:"description"`
	TargetURL   string    `json:"target_url"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func malformedFollowup(section string) error {
	return &Error{Code: FailureMalformed, Message: "GitHub PR follow-up returned invalid " + section}
}

// Wrapped endpoints include total_count, so a missing continuation link or a
// moving count cannot turn a partial collection into an empty/passing result.
func fetchFollowupPages[T any](c *Client, ctx context.Context, path, field string, extra url.Values,
	ref CredentialReference, maxItems int, resource, expectedHead string,
) ([]T, PageEvidence, RateLimit, error) {
	page := PageEvidence{Resource: resource, Complete: true}
	result := make([]T, 0)
	var rate RateLimit
	expectedTotal := -1
	for number := 1; number <= MaxPages && len(result) < maxItems; number++ {
		query := cloneValues(extra)
		query.Set("per_page", strconv.Itoa(MaxItemsPerPage))
		query.Set("page", strconv.Itoa(number))
		var raw jsonRaw
		response, err := c.doJSON(ctx, http.MethodGet, path, query, nil, ref, &raw)
		if err != nil {
			return result, page, rate, err
		}
		rate = rateLimitFromHeaders(response.Header)
		page.PagesRead++
		if field != "" {
			var envelope map[string]jsonRaw
			if raw.decode(&envelope) != nil || len(envelope["total_count"]) == 0 || len(envelope[field]) == 0 {
				return result, page, rate, malformedFollowup(resource + " pagination")
			}
			if expectedHead != "" {
				var head string
				if envelope["sha"].decode(&head) != nil || !validObjectID(head) {
					return result, page, rate, malformedFollowup(resource + " commit SHA")
				}
				if !strings.EqualFold(head, expectedHead) {
					return result, page, rate, &Error{Code: FailureDrift, Message: "GitHub combined status belongs to a different commit"}
				}
			}
			var total int
			if string(envelope["total_count"]) == "null" || envelope["total_count"].decode(&total) != nil || total < 0 {
				return result, page, rate, malformedFollowup(resource + " count")
			}
			if expectedTotal >= 0 && total != expectedTotal {
				return result, page, rate, &Error{Code: FailurePaginationDrift, Message: "GitHub PR follow-up count changed during pagination"}
			}
			expectedTotal = total
			raw = envelope[field]
		}
		var items []T
		if strings.TrimSpace(string(raw)) == "null" || raw.decode(&items) != nil || items == nil || len(items) > MaxItemsPerPage {
			return result, page, rate, malformedFollowup(resource + " items")
		}
		remaining := maxItems - len(result)
		if len(items) > remaining {
			items = items[:remaining]
			page.Complete = false
			page.OmittedReason = "item bound"
		}
		result = append(result, items...)
		page.ItemsRead = len(result)
		if !parseLinkNext(response.Header) {
			if expectedTotal >= 0 && len(result) != expectedTotal {
				page.Complete = false
				page.OmittedReason = "pagination link or items missing"
			}
			break
		}
		page.NextCursorHash = Fingerprint("rest-page", path, strconv.Itoa(number+1))
		if len(items) == 0 {
			return result, page, rate, &Error{Code: FailurePaginationDrift, Message: "GitHub PR follow-up pagination returned an empty continuation"}
		}
		if number == MaxPages || len(result) >= maxItems {
			page.Complete = false
			page.OmittedReason = "pagination bound"
		}
	}
	return result, page, rate, nil
}
