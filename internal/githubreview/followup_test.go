package githubreview

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const followupHead = "2222222222222222222222222222222222222222"

func newFollowupFixture(t *testing.T, override func(http.ResponseWriter, *http.Request) bool) (*Client, FollowupSnapshotRequest) {
	t.Helper()
	now := time.Date(2026, 10, 17, 2, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer github_pat_abcdefghijklmnopqrstuvwxyz0123456789" || r.Header.Get("X-GitHub-Api-Version") != RESTAPIVersion {
			t.Error("follow-up did not use the authenticated read transport")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4990")
		if override != nil && override(w, r) {
			return
		}
		switch r.URL.Path {
		case "/repos/acme/widget/pulls/17":
			writeFixtureJSON(t, w, draftFixtureResponse("body", followupHead))
		case "/repos/acme/widget/pulls/17/reviews":
			writeFixtureJSON(t, w, []any{map[string]any{"id": 8, "node_id": "review_8", "body": "fix review", "state": "CHANGES_REQUESTED", "commit_id": followupHead, "submitted_at": now}})
		case "/repos/acme/widget/pulls/17/comments":
			writeFixtureJSON(t, w, []any{map[string]any{"id": 9, "node_id": "inline_9", "body": "fix line", "path": "main.go", "line": 7, "side": "RIGHT", "commit_id": followupHead, "updated_at": now}})
		case "/repos/acme/widget/issues/17/comments":
			writeFixtureJSON(t, w, []any{map[string]any{"id": 10, "node_id": "issue_10", "body": "fix issue github_pat_abcdefghijklmnopqrstuvwxyz0123456789 https://evil.example", "updated_at": now,
				"html_url": "https://github.com/acme/widget/pull/17#issuecomment-10", "user": map[string]string{"login": "reviewer"}}})
		case "/repos/acme/widget/commits/" + followupHead + "/check-runs":
			if r.URL.Query().Get("filter") != "latest" {
				t.Error("check observation must request the latest run for each check")
			}
			writeFixtureJSON(t, w, map[string]any{"total_count": 1, "check_runs": []any{map[string]any{"id": 20, "node_id": "check_20", "head_sha": followupHead, "name": "test", "status": "completed", "conclusion": "failure"}}})
		case "/repos/acme/widget/actions/runs":
			if r.URL.Query().Get("head_sha") != followupHead || r.URL.Query().Has("event") {
				t.Error("workflow observation must include push-triggered CI for the exact head")
			}
			writeFixtureJSON(t, w, map[string]any{"total_count": 1, "workflow_runs": []any{map[string]any{"id": 30, "workflow_id": 300, "run_number": 7, "name": "CI", "head_sha": followupHead, "status": "completed", "conclusion": "failure", "run_attempt": 2, "event": "push", "updated_at": now}}})
		case "/repos/acme/widget/commits/" + followupHead + "/status":
			writeFixtureJSON(t, w, map[string]any{"sha": followupHead, "total_count": 1, "state": "failure", "statuses": []any{map[string]any{"id": 40, "node_id": "status_40", "context": "legacy-test", "state": "failure", "description": "build failed", "updated_at": now}}})
		default:
			t.Errorf("unexpected follow-up request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client, ref := newPATTestClient(t, server)
	repo, _ := ParseRepository("acme/widget")
	client.now = func() time.Time { return now }
	return client, FollowupSnapshotRequest{Repository: repo, Number: 17, Credential: ref}
}

func TestReadFollowupSnapshotReadsFeedbackAndHeadPinnedCI(t *testing.T) {
	client, request := newFollowupFixture(t, nil)
	snapshot, err := client.ReadFollowupSnapshot(t.Context(), request)
	if err != nil || snapshot.State != EvidenceVerified || snapshot.Validate() != nil {
		t.Fatalf("follow-up failed: %#v %v", snapshot, err)
	}
	if len(snapshot.Reviews) != 1 || len(snapshot.InlineComments) != 1 || len(snapshot.IssueComments) != 1 ||
		len(snapshot.CheckRuns) != 1 || len(snapshot.WorkflowRuns) != 1 || len(snapshot.CommitStatuses) != 1 ||
		snapshot.WorkflowRuns[0].RunAttempt != 2 || snapshot.CommitStatuses[0].HeadSHA != followupHead {
		t.Fatalf("feedback/CI missing: %#v", snapshot)
	}
	comment := snapshot.IssueComments[0]
	if !comment.Body.Untrusted || comment.Author != "reviewer" || strings.Contains(comment.Body.Text, "github_pat_") || strings.Contains(comment.Body.Text, "evil.example") {
		t.Fatalf("ordinary PR comment was not sanitized: %#v", comment)
	}
	client.now = func() time.Time { return snapshot.FetchedAt.Add(time.Hour) }
	again, err := client.ReadFollowupSnapshot(t.Context(), request)
	if err != nil || again.FetchedAt == snapshot.FetchedAt || again.Fingerprint != snapshot.Fingerprint {
		t.Fatalf("unchanged poll acquired a different fingerprint: %v", err)
	}
	again.PullRequest.UpdatedAt = again.PullRequest.UpdatedAt.Add(time.Minute)
	again.WorkflowRuns[0].UpdatedAt = again.WorkflowRuns[0].UpdatedAt.Add(time.Minute)
	again.RateLimit.Remaining--
	if again.fingerprint() != snapshot.Fingerprint {
		t.Fatal("poll metadata affected the feedback fingerprint")
	}
}

func TestReadFollowupSnapshotPaginatesOrdinaryComments(t *testing.T) {
	client, request := newFollowupFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/repos/acme/widget/issues/17/comments" {
			return false
		}
		number, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if number == 1 {
			w.Header().Set("Link", `</repos/acme/widget/issues/17/comments?page=2>; rel="next"`)
		}
		writeFixtureJSON(t, w, []any{map[string]any{"id": number, "node_id": "comment_" + strconv.Itoa(number), "body": "page " + strconv.Itoa(number)}})
		return true
	})
	snapshot, err := client.ReadFollowupSnapshot(t.Context(), request)
	if err != nil || snapshot.State != EvidenceVerified || len(snapshot.IssueComments) != 2 || snapshot.Pagination[2].PagesRead != 2 {
		t.Fatalf("comment continuation lost: %#v %v", snapshot, err)
	}
}

func TestReadFollowupSnapshotKeepsFailuresAndMissingPagesIncomplete(t *testing.T) {
	for _, scenario := range []string{"forbidden", "missing_items", "null_items", "missing_link", "failed_next_page", "item_bound", "wrong_ci_head"} {
		t.Run(scenario, func(t *testing.T) {
			client, request := newFollowupFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
				switch scenario {
				case "forbidden":
					if strings.Contains(r.URL.Path, "/issues/") {
						http.Error(w, `{"message":"forbidden"}`, http.StatusForbidden)
						return true
					}
				case "missing_items", "null_items", "missing_link":
					if strings.HasSuffix(r.URL.Path, "/actions/runs") {
						value := map[string]any{"total_count": 0}
						if scenario == "null_items" {
							value["workflow_runs"] = nil
						} else if scenario == "missing_link" {
							value["workflow_runs"] = []any{}
							value["total_count"] = 1
						}
						writeFixtureJSON(t, w, value)
						return true
					}
				case "failed_next_page", "item_bound":
					if strings.Contains(r.URL.Path, "/issues/") {
						page, _ := strconv.Atoi(r.URL.Query().Get("page"))
						if scenario == "failed_next_page" && page == 2 {
							http.Error(w, `{"message":"unavailable"}`, http.StatusServiceUnavailable)
							return true
						}
						w.Header().Set("Link", `</next>; rel="next"`)
						comments := make([]any, MaxItemsPerPage)
						for index := range comments {
							id := page*100 + index
							comments[index] = map[string]any{"id": id, "node_id": "comment_" + strconv.Itoa(id), "body": "feedback"}
						}
						writeFixtureJSON(t, w, comments)
						return true
					}
				case "wrong_ci_head":
					if strings.HasSuffix(r.URL.Path, "/check-runs") {
						writeFixtureJSON(t, w, map[string]any{"total_count": 1, "check_runs": []any{map[string]any{"id": 99, "head_sha": strings.Repeat("9", 40), "status": "completed", "conclusion": "success"}}})
						return true
					}
				}
				return false
			})
			snapshot, err := client.ReadFollowupSnapshot(t.Context(), request)
			if err != nil || snapshot.State != EvidencePartial || len(snapshot.Omissions) == 0 || snapshot.Validate() != nil {
				t.Fatalf("incomplete evidence accepted as complete: %#v %v", snapshot, err)
			}
		})
	}
}

func TestReadFollowupSnapshotRechecksPRIdentity(t *testing.T) {
	for _, scenario := range []string{"head", "base", "closed", "final_read_failed"} {
		t.Run(scenario, func(t *testing.T) {
			reads := 0
			client, request := newFollowupFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/repos/acme/widget/pulls/17" {
					return false
				}
				reads++
				if reads < 2 {
					return false
				}
				if scenario == "final_read_failed" {
					http.Error(w, `{"message":"unavailable"}`, http.StatusServiceUnavailable)
					return true
				}
				pull := draftFixtureResponse("body", followupHead)
				if scenario == "closed" {
					pull["state"] = "closed"
				} else {
					pull[scenario].(map[string]any)["sha"] = strings.Repeat("9", 40)
				}
				writeFixtureJSON(t, w, pull)
				return true
			})
			snapshot, err := client.ReadFollowupSnapshot(t.Context(), request)
			if scenario == "final_read_failed" {
				if err == nil || snapshot.Fingerprint != "" {
					t.Fatal("failed final PR read produced a consumable snapshot")
				}
				return
			}
			if err != nil || snapshot.State != EvidenceStale || snapshot.PullRequest.HeadSHA != followupHead || snapshot.PullRequest.State != "open" {
				t.Fatalf("PR drift relabeled collected evidence: %#v %v", snapshot, err)
			}
		})
	}
}

func TestReadActionsIncludesPushFailureLogsForExactHead(t *testing.T) {
	client, request := newFollowupFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/repos/acme/widget/actions/runs/30/jobs":
			writeFixtureJSON(t, w, map[string]any{"total_count": 1, "jobs": []any{map[string]any{"id": 31, "run_id": 30, "head_sha": followupHead, "name": "test", "status": "completed", "conclusion": "failure"}}})
		case "/repos/acme/widget/actions/runs/30/artifacts":
			writeFixtureJSON(t, w, map[string]any{"total_count": 0, "artifacts": []any{}})
		case "/repos/acme/widget/actions/jobs/31/logs":
			_, _ = w.Write([]byte("push test failed"))
		default:
			return false
		}
		return true
	})
	capability := testCapability(request.Repository, request.Credential, time.Now().UTC(), true)
	jobs, _, _, err := client.readActions(t.Context(), "/repos/acme/widget", followupHead, SnapshotRequest{
		Repository: request.Repository, Number: request.Number, Credential: request.Credential, Capability: capability})
	if err != nil || len(jobs) != 1 || jobs[0].LogState != EvidenceVerified || jobs[0].FailedLog.Text != "push test failed" {
		t.Fatalf("push CI log missing: %#v %v", jobs, err)
	}
}
