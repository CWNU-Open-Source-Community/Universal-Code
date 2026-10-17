package githubreview

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type SnapshotRequest struct {
	Repository RepositoryIdentity  `json:"repository"`
	Number     int64               `json:"number"`
	Credential CredentialReference `json:"credential"`
	Capability CapabilitySnapshot  `json:"capability"`
}

func (r SnapshotRequest) Validate() error {
	if r.Repository.Validate() != nil || r.Number <= 0 || r.Credential.Validate() != nil ||
		r.Capability.Validate() != nil ||
		r.Capability.Repository.FullName != r.Repository.FullName ||
		r.Capability.Credential != r.Credential || !r.Capability.Read {
		return errors.New("GitHub review snapshot request is invalid")
	}
	return nil
}

type pullResponse struct {
	Number    int64     `json:"number"`
	NodeID    string    `json:"node_id"`
	State     string    `json:"state"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Draft     bool      `json:"draft"`
	Merged    bool      `json:"merged"`
	UpdatedAt time.Time `json:"updated_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	Base struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			NodeID   string `json:"node_id"`
			FullName string `json:"full_name"`
			Private  bool   `json:"private"`
		} `json:"repo"`
	} `json:"base"`
	Head struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
}

type compareResponse struct {
	MergeBaseCommit struct {
		SHA string `json:"sha"`
	} `json:"merge_base_commit"`
}

type changedFileResponse struct {
	SHA              string `json:"sha"`
	Filename         string `json:"filename"`
	Status           string `json:"status"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
	Changes          int    `json:"changes"`
	BlobURL          string `json:"blob_url"`
	RawURL           string `json:"raw_url"`
	PreviousFilename string `json:"previous_filename"`
	Patch            string `json:"patch"`
}

type reviewResponse struct {
	ID          int64     `json:"id"`
	NodeID      string    `json:"node_id"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	CommitID    string    `json:"commit_id"`
	SubmittedAt time.Time `json:"submitted_at"`
	User        struct {
		Login string `json:"login"`
	} `json:"user"`
}

type commentResponse struct {
	ID               int64     `json:"id"`
	NodeID           string    `json:"node_id"`
	Body             string    `json:"body"`
	Path             string    `json:"path"`
	Side             string    `json:"side"`
	Line             int       `json:"line"`
	StartSide        string    `json:"start_side"`
	StartLine        int       `json:"start_line"`
	OriginalPosition int       `json:"original_position"`
	OriginalLine     int       `json:"original_line"`
	CommitID         string    `json:"commit_id"`
	OriginalCommitID string    `json:"original_commit_id"`
	HTMLURL          string    `json:"html_url"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	User             struct {
		Login string `json:"login"`
	} `json:"user"`
}

func (c *Client) ReadSnapshot(ctx context.Context, request SnapshotRequest) (Snapshot, error) {
	if c == nil || request.Validate() != nil {
		return Snapshot{}, errors.New("GitHub review snapshot request is invalid")
	}
	now := c.now().UTC()
	snapshot := Snapshot{ProtocolVersion: SnapshotProtocolVersion,
		Capability: request.Capability, RequestedReviewers: []string{},
		Files: []ChangedFile{}, Reviews: []Review{}, Threads: []ReviewThread{},
		LooseComments: []Comment{}, CheckSuites: []CheckSuite{}, CheckRuns: []CheckRun{},
		Jobs: []WorkflowJob{}, Artifacts: []ArtifactMetadata{}, Pagination: []PageEvidence{},
		State: EvidenceVerified, Omissions: []string{}, FetchedAt: now}
	repoPath := repositoryAPIPath(request.Repository)
	var pull pullResponse
	response, err := c.doJSON(ctx, http.MethodGet,
		repoPath+"/pulls/"+strconv.FormatInt(request.Number, 10), nil, nil,
		request.Credential, &pull)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.RateLimit = rateLimitFromHeaders(response.Header)
	identity, err := pullIdentity(request.Repository, pull)
	if err != nil {
		return Snapshot{}, &Error{Code: FailureMalformed,
			Message: "GitHub pull request metadata is inconsistent"}
	}
	comparePath := repoPath + "/compare/" + url.PathEscape(identity.BaseSHA) + "..." +
		url.PathEscape(identity.HeadSHA)
	var comparison compareResponse
	response, err = c.doJSON(ctx, http.MethodGet, comparePath, nil, nil,
		request.Credential, &comparison)
	if err != nil {
		return Snapshot{}, err
	}
	identity.MergeBaseSHA = strings.ToLower(strings.TrimSpace(comparison.MergeBaseCommit.SHA))
	if err := identity.Validate(); err != nil {
		return Snapshot{}, &Error{Code: FailureMalformed,
			Message: "GitHub merge-base identity is invalid"}
	}
	snapshot.Identity = identity
	snapshot.Title = SanitizeRemoteText(pull.Title, MaxTextBytes)
	snapshot.Body = SanitizeRemoteText(pull.Body, MaxTextBytes)
	snapshot.Author = sanitizeIdentity(pull.User.Login, 100)
	snapshot.RateLimit = rateLimitFromHeaders(response.Header)

	successfulSections := 0
	if files, page, readErr := c.readChangedFiles(ctx, repoPath, request); readErr != nil {
		snapshot.addReadFailure("changed_files", readErr)
	} else {
		snapshot.Files = files
		snapshot.Pagination = append(snapshot.Pagination, page)
		if !page.Complete {
			snapshot.markPartial("changed-file pagination reached its configured bound")
		}
		for _, file := range files {
			if file.Patch.Text == "" && file.Status != "removed" {
				snapshot.markPartial("GitHub omitted or truncated at least one changed-file patch")
				break
			}
		}
		successfulSections++
	}
	if reviews, page, readErr := c.readReviews(ctx, repoPath, request); readErr != nil {
		snapshot.addReadFailure("reviews", readErr)
	} else {
		snapshot.Reviews = reviews
		snapshot.Pagination = append(snapshot.Pagination, page)
		if !page.Complete {
			snapshot.markPartial("review pagination reached its configured bound")
		}
		successfulSections++
	}
	if comments, page, readErr := c.readComments(ctx, repoPath, request); readErr != nil {
		snapshot.addReadFailure("inline_comments", readErr)
	} else {
		snapshot.LooseComments = comments
		snapshot.Pagination = append(snapshot.Pagination, page)
		if !page.Complete {
			snapshot.markPartial("inline-comment pagination reached its configured bound")
		}
		successfulSections++
	}
	if threads, page, rate, readErr := c.readThreads(ctx, request); readErr != nil {
		snapshot.addReadFailure("review_threads", readErr)
	} else {
		snapshot.Threads = threads
		snapshot.Pagination = append(snapshot.Pagination, page)
		if rate.Limit > 0 {
			snapshot.RateLimit = rate
		}
		if !page.Complete {
			snapshot.markPartial("review-thread or nested comment pagination is incomplete")
		}
		successfulSections++
	}
	if reviewers, readErr := c.readRequestedReviewers(ctx, repoPath, request); readErr != nil {
		snapshot.addReadFailure("requested_reviewers", readErr)
	} else {
		snapshot.RequestedReviewers = reviewers
		successfulSections++
	}
	if suites, runs, pages, readErr := c.readChecks(ctx, repoPath, identity.HeadSHA, request); readErr != nil {
		snapshot.addReadFailure("checks", readErr)
	} else {
		snapshot.CheckSuites = suites
		snapshot.CheckRuns = runs
		snapshot.Pagination = append(snapshot.Pagination, pages...)
		for _, page := range pages {
			if !page.Complete {
				snapshot.markPartial(page.Resource + " pagination reached its configured bound")
			}
		}
		successfulSections++
	}
	if jobs, artifacts, pages, readErr := c.readActions(ctx, repoPath, identity.HeadSHA, request); readErr != nil {
		snapshot.addReadFailure("actions", readErr)
	} else {
		snapshot.Jobs = jobs
		snapshot.Artifacts = artifacts
		snapshot.Pagination = append(snapshot.Pagination, pages...)
		for _, page := range pages {
			if !page.Complete {
				snapshot.markPartial(page.Resource + " pagination reached its configured bound")
			}
		}
		for _, job := range jobs {
			if job.Conclusion == "failure" && job.LogState != EvidenceVerified {
				snapshot.markPartial("at least one failed Actions job has no verified bounded log")
				break
			}
		}
		successfulSections++
	}
	if successfulSections == 0 {
		snapshot.State = EvidenceUnavailable
	}
	// Files and review discussions span several requests. Head-pinned checks do
	// not make that whole collection atomic: re-read the PR identity at the end.
	var current pullResponse
	if _, readErr := c.doJSON(ctx, http.MethodGet, repoPath+"/pulls/"+strconv.FormatInt(request.Number, 10), nil, nil, request.Credential, &current); readErr != nil {
		// This read is mandatory like the initial identity/compare requests. A
		// partial CI section is useful, but an unverified current identity must
		// not become a new successful refresh that replaces the prior snapshot.
		return Snapshot{}, readErr
	} else if current.Number != pull.Number || current.NodeID != pull.NodeID || current.Head.SHA != pull.Head.SHA || current.Base.SHA != pull.Base.SHA || current.Head.Ref != pull.Head.Ref || current.Base.Ref != pull.Base.Ref || current.State != pull.State || current.Merged != pull.Merged || current.Draft != pull.Draft || current.UpdatedAt != pull.UpdatedAt || current.Base.Repo.FullName != pull.Base.Repo.FullName || current.Base.Repo.NodeID != pull.Base.Repo.NodeID || current.Head.Repo.FullName != pull.Head.Repo.FullName {
		snapshot.State = EvidenceStale
		snapshot.Omissions = append(snapshot.Omissions, "pull request identity changed during collection; refresh before acting on this snapshot")
	}
	snapshot.Finalize()
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, &Error{Code: FailureMalformed,
			Message: "assembled GitHub review snapshot is invalid"}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return Snapshot{}, &Error{Code: FailureMalformed,
			Message: "assembled GitHub review snapshot could not be encoded"}
	}
	if len(encoded) > MaxSnapshotBytes {
		return Snapshot{}, &Error{Code: FailureResponseBound,
			Message: "assembled GitHub review snapshot exceeded its byte bound"}
	}
	return snapshot, nil
}

func pullIdentity(repository RepositoryIdentity, pull pullResponse) (PullRequestIdentity, error) {
	if pull.Number <= 0 || !strings.EqualFold(pull.Base.Repo.FullName, repository.FullName) {
		return PullRequestIdentity{}, errors.New("pull request does not belong to repository")
	}
	repository.NodeID = sanitizeIdentity(pull.Base.Repo.NodeID, MaxIdentityRunes)
	repository.Private = pull.Base.Repo.Private
	identity := PullRequestIdentity{Repository: repository, Number: pull.Number,
		NodeID: sanitizeIdentity(pull.NodeID, MaxIdentityRunes),
		State:  strings.ToLower(strings.TrimSpace(pull.State)), Draft: pull.Draft,
		Merged:    pull.Merged,
		Fork:      pull.Head.Repo.FullName != "" && !strings.EqualFold(pull.Head.Repo.FullName, pull.Base.Repo.FullName),
		BaseRef:   sanitizeIdentity(pull.Base.Ref, 255),
		BaseSHA:   strings.ToLower(strings.TrimSpace(pull.Base.SHA)),
		HeadRef:   sanitizeIdentity(pull.Head.Ref, 255),
		HeadSHA:   strings.ToLower(strings.TrimSpace(pull.Head.SHA)),
		UpdatedAt: pull.UpdatedAt}
	return identity, nil
}

func (s *Snapshot) markPartial(reason string) {
	if s.State == EvidenceVerified {
		s.State = EvidencePartial
	}
	s.Omissions = append(s.Omissions, boundedPlainText(reason, 500))
}

func (s *Snapshot) addReadFailure(resource string, err error) {
	code := "unavailable"
	var typed *Error
	if errors.As(err, &typed) {
		code = string(typed.Code)
	}
	s.markPartial(resource + " unavailable (" + code + ")")
	s.Pagination = append(s.Pagination, PageEvidence{Resource: resource,
		Complete: false, OmittedReason: code})
}

func (c *Client) readChangedFiles(ctx context.Context, repoPath string,
	request SnapshotRequest,
) ([]ChangedFile, PageEvidence, error) {
	raw, page, err := fetchArrayPages[changedFileResponse](c, ctx,
		repoPath+"/pulls/"+strconv.FormatInt(request.Number, 10)+"/files",
		nil, request.Credential, MaxChangedFiles, "changed_files")
	if err != nil {
		return nil, page, err
	}
	files := make([]ChangedFile, 0, len(raw))
	for _, item := range raw {
		path := sanitizeRepositoryPath(item.Filename)
		if path == "" {
			page.Complete = false
			page.OmittedReason = "invalid changed-file path"
			continue
		}
		files = append(files, ChangedFile{Path: path,
			PreviousPath: sanitizeRepositoryPath(item.PreviousFilename),
			Status:       sanitizeIdentity(item.Status, 32), SHA: strings.ToLower(strings.TrimSpace(item.SHA)),
			Additions: clampNonNegative(item.Additions), Deletions: clampNonNegative(item.Deletions),
			Changes: clampNonNegative(item.Changes), BlobURL: safeGitHubURL(item.BlobURL),
			RawURL: safeGitHubURL(item.RawURL), Patch: SanitizeRemoteText(item.Patch, MaxPatchBytes)})
	}
	return files, page, nil
}

func (c *Client) readReviews(ctx context.Context, repoPath string,
	request SnapshotRequest,
) ([]Review, PageEvidence, error) {
	raw, page, err := fetchArrayPages[reviewResponse](c, ctx,
		repoPath+"/pulls/"+strconv.FormatInt(request.Number, 10)+"/reviews",
		nil, request.Credential, MaxReviews, "reviews")
	if err != nil {
		return nil, page, err
	}
	result := make([]Review, 0, len(raw))
	for _, item := range raw {
		result = append(result, Review{ID: item.ID,
			NodeID:    sanitizeIdentity(item.NodeID, MaxIdentityRunes),
			Author:    sanitizeIdentity(item.User.Login, 100),
			State:     strings.ToUpper(sanitizeIdentity(item.State, 32)),
			CommitSHA: strings.ToLower(strings.TrimSpace(item.CommitID)),
			Body:      SanitizeRemoteText(item.Body, MaxTextBytes), SubmittedAt: item.SubmittedAt})
	}
	return result, page, nil
}

func (c *Client) readComments(ctx context.Context, repoPath string,
	request SnapshotRequest,
) ([]Comment, PageEvidence, error) {
	raw, page, err := fetchArrayPages[commentResponse](c, ctx,
		repoPath+"/pulls/"+strconv.FormatInt(request.Number, 10)+"/comments",
		nil, request.Credential, MaxComments, "inline_comments")
	if err != nil {
		return nil, page, err
	}
	result := make([]Comment, 0, len(raw))
	for _, item := range raw {
		result = append(result, convertComment(item, ""))
	}
	return result, page, nil
}

func convertComment(item commentResponse, threadID string) Comment {
	return Comment{ID: item.ID, NodeID: sanitizeIdentity(item.NodeID, MaxIdentityRunes),
		ThreadID: sanitizeIdentity(threadID, MaxIdentityRunes),
		Author:   sanitizeIdentity(item.User.Login, 100),
		Body:     SanitizeRemoteText(item.Body, MaxTextBytes),
		Position: Position{Path: sanitizeRepositoryPath(item.Path),
			Side: strings.ToUpper(sanitizeIdentity(item.Side, 16)), Line: clampNonNegative(item.Line),
			StartSide:         strings.ToUpper(sanitizeIdentity(item.StartSide, 16)),
			StartLine:         clampNonNegative(item.StartLine),
			OriginalPosition:  clampNonNegative(item.OriginalPosition),
			OriginalLine:      clampNonNegative(item.OriginalLine),
			CommitSHA:         strings.ToLower(strings.TrimSpace(item.CommitID)),
			OriginalCommitSHA: strings.ToLower(strings.TrimSpace(item.OriginalCommitID))},
		URL: safeGitHubURL(item.HTMLURL), CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}
}

const reviewThreadsQuery = `query($owner:String!,$name:String!,$number:Int!,$cursor:String){
  repository(owner:$owner,name:$name){pullRequest(number:$number){
    reviewThreads(first:100,after:$cursor){nodes{
      id isResolved isOutdated path line startLine diffSide startDiffSide
      comments(first:100){nodes{id databaseId body url createdAt updatedAt path line originalLine
        author{login} commit{oid} originalCommit{oid}}
        pageInfo{hasNextPage endCursor}}
    } pageInfo{hasNextPage endCursor}}
  }} rateLimit{limit remaining used resetAt}}`

type threadGraphQLData struct {
	Repository struct {
		PullRequest *struct {
			ReviewThreads struct {
				Nodes []struct {
					ID            string `json:"id"`
					IsResolved    bool   `json:"isResolved"`
					IsOutdated    bool   `json:"isOutdated"`
					Path          string `json:"path"`
					Line          int    `json:"line"`
					StartLine     int    `json:"startLine"`
					DiffSide      string `json:"diffSide"`
					StartDiffSide string `json:"startDiffSide"`
					Comments      struct {
						Nodes []struct {
							ID           string    `json:"id"`
							DatabaseID   int64     `json:"databaseId"`
							Body         string    `json:"body"`
							URL          string    `json:"url"`
							CreatedAt    time.Time `json:"createdAt"`
							UpdatedAt    time.Time `json:"updatedAt"`
							Path         string    `json:"path"`
							Line         int       `json:"line"`
							OriginalLine int       `json:"originalLine"`
							Author       *struct {
								Login string `json:"login"`
							} `json:"author"`
							Commit *struct {
								OID string `json:"oid"`
							} `json:"commit"`
							OriginalCommit *struct {
								OID string `json:"oid"`
							} `json:"originalCommit"`
						} `json:"nodes"`
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"comments"`
				} `json:"nodes"`
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"reviewThreads"`
		} `json:"pullRequest"`
	} `json:"repository"`
	RateLimit struct {
		Limit     int       `json:"limit"`
		Remaining int       `json:"remaining"`
		Used      int       `json:"used"`
		ResetAt   time.Time `json:"resetAt"`
	} `json:"rateLimit"`
}

func (c *Client) readThreads(ctx context.Context, request SnapshotRequest) (
	[]ReviewThread, PageEvidence, RateLimit, error,
) {
	page := PageEvidence{Resource: "review_threads", Complete: true}
	threads := make([]ReviewThread, 0)
	commentCount := 0
	var cursor any
	var rate RateLimit
	for page.PagesRead < MaxPages && len(threads) < MaxThreads {
		variables := map[string]any{"owner": request.Repository.Owner,
			"name": request.Repository.Name, "number": request.Number, "cursor": cursor}
		var data threadGraphQLData
		_, err := c.doGraphQL(ctx, reviewThreadsQuery, variables, request.Credential, &data)
		if err != nil {
			return nil, page, rate, err
		}
		if data.Repository.PullRequest == nil {
			return nil, page, rate, &Error{Code: FailureNotFound,
				Message: "GitHub pull request was not returned by GraphQL"}
		}
		page.PagesRead++
		for _, node := range data.Repository.PullRequest.ReviewThreads.Nodes {
			if len(threads) >= MaxThreads {
				page.Complete = false
				page.OmittedReason = "thread item bound"
				break
			}
			thread := ReviewThread{ID: sanitizeIdentity(node.ID, MaxIdentityRunes),
				Resolved: node.IsResolved, Outdated: node.IsOutdated,
				Path:      sanitizeRepositoryPath(node.Path),
				Side:      strings.ToUpper(sanitizeIdentity(node.DiffSide, 16)),
				Line:      clampNonNegative(node.Line),
				StartSide: strings.ToUpper(sanitizeIdentity(node.StartDiffSide, 16)),
				StartLine: clampNonNegative(node.StartLine), Comments: []Comment{}}
			if node.Comments.PageInfo.HasNextPage {
				page.Complete = false
				page.OmittedReason = "nested thread comment pagination"
			}
			for _, rawComment := range node.Comments.Nodes {
				if commentCount >= MaxComments {
					page.Complete = false
					page.OmittedReason = "review-thread comment item bound"
					break
				}
				comment := commentResponse{ID: rawComment.DatabaseID, NodeID: rawComment.ID,
					Body: rawComment.Body, Path: rawComment.Path, Side: node.DiffSide,
					Line: rawComment.Line, StartSide: node.StartDiffSide,
					StartLine: node.StartLine, OriginalLine: rawComment.OriginalLine,
					HTMLURL: rawComment.URL, CreatedAt: rawComment.CreatedAt,
					UpdatedAt: rawComment.UpdatedAt}
				if rawComment.Author != nil {
					comment.User.Login = rawComment.Author.Login
				}
				if rawComment.Commit != nil {
					comment.CommitID = rawComment.Commit.OID
				}
				if rawComment.OriginalCommit != nil {
					comment.OriginalCommitID = rawComment.OriginalCommit.OID
				}
				thread.Comments = append(thread.Comments, convertComment(comment, thread.ID))
				commentCount++
			}
			threads = append(threads, thread)
		}
		page.ItemsRead = len(threads)
		rate = RateLimit{Limit: data.RateLimit.Limit, Remaining: data.RateLimit.Remaining,
			Used: data.RateLimit.Used, ResetAt: data.RateLimit.ResetAt,
			Resource: "graphql"}
		info := data.Repository.PullRequest.ReviewThreads.PageInfo
		if !info.HasNextPage {
			break
		}
		if info.EndCursor == "" {
			return nil, page, rate, &Error{Code: FailurePaginationDrift,
				Message: "GitHub review-thread pagination cursor is missing"}
		}
		cursor = info.EndCursor
		page.NextCursorHash = Fingerprint("cursor", info.EndCursor)
	}
	if page.NextCursorHash != "" && (page.PagesRead >= MaxPages || len(threads) >= MaxThreads) {
		page.Complete = false
		if page.OmittedReason == "" {
			page.OmittedReason = "pagination bound"
		}
	}
	return threads, page, rate, nil
}

func (c *Client) readRequestedReviewers(ctx context.Context, repoPath string,
	request SnapshotRequest,
) ([]string, error) {
	var response struct {
		Users []struct {
			Login string `json:"login"`
		} `json:"users"`
		Teams []struct {
			Slug string `json:"slug"`
		} `json:"teams"`
	}
	_, err := c.doJSON(ctx, http.MethodGet,
		repoPath+"/pulls/"+strconv.FormatInt(request.Number, 10)+"/requested_reviewers",
		nil, nil, request.Credential, &response)
	if err != nil {
		return nil, err
	}
	values := make([]string, 0, len(response.Users)+len(response.Teams))
	for _, user := range response.Users {
		values = append(values, sanitizeIdentity(user.Login, 100))
	}
	for _, team := range response.Teams {
		values = append(values, "team:"+sanitizeIdentity(team.Slug, 100))
	}
	return uniqueSorted(values), nil
}

type checkSuiteResponse struct {
	ID         int64     `json:"id"`
	NodeID     string    `json:"node_id"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	HeadSHA    string    `json:"head_sha"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	App        *struct {
		Name string `json:"name"`
	} `json:"app"`
}

type checkRunResponse struct {
	ID          int64     `json:"id"`
	NodeID      string    `json:"node_id"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion"`
	HeadSHA     string    `json:"head_sha"`
	DetailsURL  string    `json:"details_url"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	Output      struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
		Text    string `json:"text"`
	} `json:"output"`
}

func (c *Client) readChecks(ctx context.Context, repoPath, headSHA string,
	request SnapshotRequest,
) ([]CheckSuite, []CheckRun, []PageEvidence, error) {
	suites, suitePage, err := fetchWrappedPages[checkSuiteResponse](c, ctx,
		repoPath+"/commits/"+url.PathEscape(headSHA)+"/check-suites",
		"check_suites", nil, request.Credential, MaxCheckSuites, "check_suites")
	if err != nil {
		return nil, nil, nil, err
	}
	runs, runPage, err := fetchWrappedPages[checkRunResponse](c, ctx,
		repoPath+"/commits/"+url.PathEscape(headSHA)+"/check-runs",
		"check_runs", nil, request.Credential, MaxCheckRuns, "check_runs")
	if err != nil {
		return nil, nil, nil, err
	}
	suiteViews := make([]CheckSuite, 0, len(suites))
	for _, item := range suites {
		app := ""
		if item.App != nil {
			app = sanitizeIdentity(item.App.Name, 200)
		}
		suiteViews = append(suiteViews, CheckSuite{ID: item.ID,
			NodeID:     sanitizeIdentity(item.NodeID, MaxIdentityRunes),
			Status:     sanitizeIdentity(item.Status, 32),
			Conclusion: sanitizeIdentity(item.Conclusion, 32),
			HeadSHA:    strings.ToLower(strings.TrimSpace(item.HeadSHA)), App: app,
			CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt})
	}
	runViews := make([]CheckRun, 0, len(runs))
	for _, item := range runs {
		runViews = append(runViews, CheckRun{ID: item.ID,
			NodeID: sanitizeIdentity(item.NodeID, MaxIdentityRunes),
			Name:   sanitizeIdentity(item.Name, 300), Status: sanitizeIdentity(item.Status, 32),
			Conclusion: sanitizeIdentity(item.Conclusion, 32),
			HeadSHA:    strings.ToLower(strings.TrimSpace(item.HeadSHA)),
			DetailsURL: safeGitHubURL(item.DetailsURL),
			Title:      SanitizeRemoteText(item.Output.Title, MaxTextBytes),
			Summary:    SanitizeRemoteText(item.Output.Summary, MaxTextBytes),
			Text:       SanitizeRemoteText(item.Output.Text, MaxTextBytes),
			StartedAt:  item.StartedAt, CompletedAt: item.CompletedAt})
	}
	return suiteViews, runViews, []PageEvidence{suitePage, runPage}, nil
}

type workflowRunResponse struct {
	ID         int64  `json:"id"`
	HeadSHA    string `json:"head_sha"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

type workflowJobResponse struct {
	ID          int64     `json:"id"`
	RunID       int64     `json:"run_id"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion"`
	HeadSHA     string    `json:"head_sha"`
	HTMLURL     string    `json:"html_url"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	Steps       []struct {
		Number      int       `json:"number"`
		Name        string    `json:"name"`
		Status      string    `json:"status"`
		Conclusion  string    `json:"conclusion"`
		StartedAt   time.Time `json:"started_at"`
		CompletedAt time.Time `json:"completed_at"`
	} `json:"steps"`
}

type artifactResponse struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	SizeInBytes int64     `json:"size_in_bytes"`
	Expired     bool      `json:"expired"`
	Digest      string    `json:"digest"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func (c *Client) readActions(ctx context.Context, repoPath, headSHA string,
	request SnapshotRequest,
) ([]WorkflowJob, []ArtifactMetadata, []PageEvidence, error) {
	runs, runPage, err := fetchWrappedPages[workflowRunResponse](c, ctx,
		repoPath+"/actions/runs", "workflow_runs",
		url.Values{"head_sha": []string{headSHA}}, request.Credential,
		MaxWorkflowRuns, "workflow_runs")
	if err != nil {
		return nil, nil, nil, err
	}
	jobs := make([]WorkflowJob, 0)
	artifacts := make([]ArtifactMetadata, 0)
	pages := []PageEvidence{runPage}
	failedLogs := 0
	for _, run := range runs {
		if !strings.EqualFold(run.HeadSHA, headSHA) {
			return nil, nil, pages, &Error{Code: FailureDrift, Message: "GitHub Actions run belongs to a different commit"}
		}
		if len(jobs) >= MaxJobs || len(artifacts) >= MaxArtifacts {
			break
		}
		rawJobs, jobPage, readErr := fetchWrappedPages[workflowJobResponse](c, ctx,
			repoPath+"/actions/runs/"+strconv.FormatInt(run.ID, 10)+"/jobs", "jobs",
			nil, request.Credential, MaxJobs-len(jobs),
			"workflow_jobs:"+strconv.FormatInt(run.ID, 10))
		if readErr != nil {
			return nil, nil, pages, readErr
		}
		pages = append(pages, jobPage)
		for _, rawJob := range rawJobs {
			if !strings.EqualFold(rawJob.HeadSHA, headSHA) {
				return nil, nil, pages, &Error{Code: FailureDrift, Message: "GitHub Actions job belongs to a different commit"}
			}
			job := WorkflowJob{ID: rawJob.ID, RunID: run.ID,
				Name:       sanitizeIdentity(rawJob.Name, 300),
				Status:     sanitizeIdentity(rawJob.Status, 32),
				Conclusion: sanitizeIdentity(rawJob.Conclusion, 32),
				HeadSHA:    strings.ToLower(strings.TrimSpace(rawJob.HeadSHA)),
				URL:        safeGitHubURL(rawJob.HTMLURL), Steps: []JobStep{},
				FailedLog: EmptyTextEvidence(), LogState: EvidenceNotRun,
				StartedAt: rawJob.StartedAt, CompletedAt: rawJob.CompletedAt}
			for _, rawStep := range rawJob.Steps {
				job.Steps = append(job.Steps, JobStep{Number: rawStep.Number,
					Name:       sanitizeIdentity(rawStep.Name, 300),
					Status:     sanitizeIdentity(rawStep.Status, 32),
					Conclusion: sanitizeIdentity(rawStep.Conclusion, 32),
					StartedAt:  rawStep.StartedAt, CompletedAt: rawStep.CompletedAt})
			}
			if job.Conclusion == "failure" {
				if failedLogs >= MaxFailedJobLogs {
					job.LogState = EvidencePartial
					job.LogReason = "failed-job log count bound"
				} else if !request.Capability.Logs {
					job.LogState = EvidenceUnavailable
					job.LogReason = "signed log host is not allowed by connection network scope"
				} else {
					failedLogs++
					log, logErr := c.readJobLog(ctx, repoPath, rawJob.ID, request.Credential)
					if logErr != nil {
						job.LogState = EvidenceUnavailable
						var typed *Error
						if errors.As(logErr, &typed) {
							job.LogReason = string(typed.Code)
						} else {
							job.LogReason = "unavailable"
						}
					} else {
						job.FailedLog = log
						job.LogState = EvidenceVerified
					}
				}
			}
			jobs = append(jobs, job)
		}
		rawArtifacts, artifactPage, readErr := fetchWrappedPages[artifactResponse](c, ctx,
			repoPath+"/actions/runs/"+strconv.FormatInt(run.ID, 10)+"/artifacts",
			"artifacts", nil, request.Credential, MaxArtifacts-len(artifacts),
			"artifacts:"+strconv.FormatInt(run.ID, 10))
		if readErr != nil {
			return nil, nil, pages, readErr
		}
		pages = append(pages, artifactPage)
		for _, item := range rawArtifacts {
			artifacts = append(artifacts, ArtifactMetadata{ID: item.ID,
				Name: sanitizeIdentity(item.Name, 300), SizeBytes: maxInt64(item.SizeInBytes, 0),
				Expired: item.Expired, Digest: sanitizeIdentity(item.Digest, 200),
				CreatedAt: item.CreatedAt, ExpiresAt: item.ExpiresAt})
		}
	}
	return jobs, artifacts, pages, nil
}

func (c *Client) readJobLog(ctx context.Context, repoPath string, jobID int64,
	ref CredentialReference,
) (TextEvidence, error) {
	response, err := c.doBytes(ctx,
		c.restBase+repoPath+"/actions/jobs/"+strconv.FormatInt(jobID, 10)+"/logs",
		ref, MaxCompressedLogBytes)
	if err != nil {
		return TextEvidence{}, err
	}
	if len(response.Body) >= 4 && bytes.Equal(response.Body[:4], []byte{'P', 'K', 3, 4}) {
		return sanitizeZipLog(response.Body)
	}
	return SanitizeRemoteText(string(response.Body), MaxLogExcerptBytes), nil
}

func sanitizeZipLog(data []byte) (TextEvidence, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(reader.File) > MaxLogArchiveEntries {
		return TextEvidence{}, &Error{Code: FailureMalformed,
			Message: "GitHub Actions log archive is invalid or has too many entries"}
	}
	var combined strings.Builder
	var observed uint64
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		observed += file.UncompressedSize64
		if observed > MaxUncompressedLogBytes {
			return TextEvidence{}, &Error{Code: FailureResponseBound,
				Message: "GitHub Actions log archive exceeded its uncompressed bound"}
		}
		opened, openErr := file.Open()
		if openErr != nil {
			return TextEvidence{}, &Error{Code: FailureMalformed,
				Message: "open bounded GitHub Actions log entry"}
		}
		remaining := MaxLogExcerptBytes - combined.Len()
		if remaining > 0 {
			content, readErr := io.ReadAll(io.LimitReader(opened, int64(remaining)+1))
			_ = opened.Close()
			if readErr != nil {
				return TextEvidence{}, &Error{Code: FailureMalformed,
					Message: "read bounded GitHub Actions log entry"}
			}
			if combined.Len() > 0 {
				combined.WriteByte('\n')
			}
			combined.Write(content)
		} else {
			_ = opened.Close()
		}
	}
	evidence := SanitizeRemoteText(combined.String(), MaxLogExcerptBytes)
	if observed > uint64(evidence.StoredBytes) {
		evidence.Truncated = true
		evidence.OriginalBytes = int(minUint64(observed, uint64(^uint(0)>>1)))
	}
	return evidence, nil
}

func fetchArrayPages[T any](c *Client, ctx context.Context, path string,
	extra url.Values, ref CredentialReference, maxItems int, resource string,
) ([]T, PageEvidence, error) {
	pageEvidence := PageEvidence{Resource: resource, Complete: true}
	result := make([]T, 0)
	for page := 1; page <= MaxPages && len(result) < maxItems; page++ {
		query := cloneValues(extra)
		query.Set("per_page", strconv.Itoa(MaxItemsPerPage))
		query.Set("page", strconv.Itoa(page))
		var items []T
		response, err := c.doJSON(ctx, http.MethodGet, path, query, nil, ref, &items)
		if err != nil {
			return nil, pageEvidence, err
		}
		pageEvidence.PagesRead++
		remaining := maxItems - len(result)
		if len(items) > remaining {
			items = items[:remaining]
			pageEvidence.Complete = false
			pageEvidence.OmittedReason = "item bound"
		}
		result = append(result, items...)
		pageEvidence.ItemsRead = len(result)
		hasNext := parseLinkNext(response.Header)
		if !hasNext {
			break
		}
		pageEvidence.NextCursorHash = Fingerprint("rest-page", path, strconv.Itoa(page+1))
		if page == MaxPages || len(result) >= maxItems {
			pageEvidence.Complete = false
			if pageEvidence.OmittedReason == "" {
				pageEvidence.OmittedReason = "pagination bound"
			}
		}
	}
	return result, pageEvidence, nil
}

func fetchWrappedPages[T any](c *Client, ctx context.Context, path, field string,
	extra url.Values, ref CredentialReference, maxItems int, resource string,
) ([]T, PageEvidence, error) {
	pageEvidence := PageEvidence{Resource: resource, Complete: true}
	result := make([]T, 0)
	for page := 1; page <= MaxPages && len(result) < maxItems; page++ {
		query := cloneValues(extra)
		query.Set("per_page", strconv.Itoa(MaxItemsPerPage))
		query.Set("page", strconv.Itoa(page))
		var envelope map[string]jsonRaw
		response, err := c.doJSON(ctx, http.MethodGet, path, query, nil, ref, &envelope)
		if err != nil {
			return nil, pageEvidence, err
		}
		raw, exists := envelope[field]
		if !exists {
			return nil, pageEvidence, &Error{Code: FailureMalformed,
				Message: "GitHub paginated response omitted " + resource}
		}
		var items []T
		if err := raw.decode(&items); err != nil {
			return nil, pageEvidence, &Error{Code: FailureMalformed,
				Message: "GitHub paginated " + resource + " response is malformed"}
		}
		pageEvidence.PagesRead++
		remaining := maxItems - len(result)
		if len(items) > remaining {
			items = items[:remaining]
			pageEvidence.Complete = false
			pageEvidence.OmittedReason = "item bound"
		}
		result = append(result, items...)
		pageEvidence.ItemsRead = len(result)
		hasNext := parseLinkNext(response.Header)
		if !hasNext {
			break
		}
		pageEvidence.NextCursorHash = Fingerprint("rest-page", path, strconv.Itoa(page+1))
		if page == MaxPages || len(result) >= maxItems {
			pageEvidence.Complete = false
			if pageEvidence.OmittedReason == "" {
				pageEvidence.OmittedReason = "pagination bound"
			}
		}
	}
	return result, pageEvidence, nil
}

type jsonRaw []byte

func (r *jsonRaw) UnmarshalJSON(data []byte) error {
	*r = append((*r)[:0], data...)
	return nil
}

func (r jsonRaw) decode(output any) error {
	decoder := json.NewDecoder(bytes.NewReader(r))
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func cloneValues(input url.Values) url.Values {
	result := url.Values{}
	for key, values := range input {
		result[key] = append([]string(nil), values...)
	}
	return result
}

func sanitizeRepositoryPath(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\x00") ||
		strings.Contains(value, "../") || value == ".." || len([]rune(value)) > 4096 {
		return ""
	}
	return sanitizeIdentity(value, 4096)
}

func safeGitHubURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" {
		return ""
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "github.com", "api.github.com", "raw.githubusercontent.com":
		return parsed.String()
	default:
		return ""
	}
}

func clampNonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func maxInt64(value, floor int64) int64 {
	if value < floor {
		return floor
	}
	return value
}

func minUint64(left, right uint64) uint64 {
	if left < right {
		return left
	}
	return right
}
