package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/scheduler"
)

type scheduledPaginationClock struct {
	scheduler.RealClock
	at time.Time
}

func (c *scheduledPaginationClock) Now() time.Time { return c.at }

func TestScheduledJobPagesReachOldActiveSchedulesAndKeepExactScope(t *testing.T) {
	f := newAPIFixture(t)
	ctx := t.Context()
	runs := application.NewRunService(f.store)
	_, owner, err := runs.Create(ctx, application.CreateRunRequest{
		Goal: "scheduled pagination owner", Profile: "code", Surface: "code", Phase: "plan",
		WorkspaceID: f.workspace.ID, Budget: domain.Budget{MaxTurns: 2, MaxToolCalls: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.run, err = runs.Start(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Second)
	clock := &scheduledPaginationClock{at: base}
	service := application.NewScheduledJobService(f.store).WithClock(clock)
	f.api.scheduledJobController = service
	f.api.scheduledJobControlEnabled = true
	create := func(runID string, index int) domain.ScheduledJob {
		t.Helper()
		result, err := service.Create(ctx, application.CreateScheduledJobRequest{
			Version: domain.ScheduledJobProtocolVersion, RunID: runID, TargetRunID: runID,
			Schedule: domain.ScheduledJobSchedule{Kind: domain.ScheduledJobOnce, Timezone: "UTC",
				AnchorAt: base.Add(10 * time.Minute), MisfirePolicy: domain.ScheduledJobMisfireRunOnce},
			DeadlineAt: base.Add(30 * time.Minute), StopOnTargetTerminal: true,
			MaxRounds: 1, MaxElapsedSeconds: 3600,
			Retry:        domain.ScheduledJobRetryPolicy{MaxAttempts: 2, InitialBackoffSeconds: 1, MaxBackoffSeconds: 10},
			Notification: domain.ScheduledJobNotifyAll, ExecutionMode: domain.ScheduledJobReadOnly,
			OperationKey: fmt.Sprintf("scheduled-pagination-create-%03d", index), RequestedBy: "pagination_test",
		})
		if err != nil {
			t.Fatal(err)
		}
		return result.Job
	}
	jobs := make([]domain.ScheduledJob, 105)
	for index := range jobs {
		// Exercise equal timestamps and variable-width RFC3339Nano fractions.
		clock.at = base.Add(time.Duration(index/2) * time.Nanosecond)
		jobs[index] = create(f.run.ID, index)
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].CreatedAt.Equal(jobs[j].CreatedAt) {
			return jobs[i].ID > jobs[j].ID
		}
		return jobs[i].CreatedAt.After(jobs[j].CreatedAt)
	})
	read := func(path string) (ScheduledJobListView, *Page) {
		t.Helper()
		response := f.get(t, path)
		if response.Code != http.StatusOK {
			t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
		}
		var result ScheduledJobListView
		envelope := decodeData(t, response, &result)
		if envelope.Page == nil || envelope.Page.Truncated {
			t.Fatalf("missing or unexpectedly truncated page: %+v", envelope.Page)
		}
		return result, envelope.Page
	}
	first, firstPage := read(ScheduledJobsPath + "?limit=100")
	if len(first.Items) != 100 || firstPage.NextCursor == "" {
		t.Fatalf("first page items=%d page=%+v", len(first.Items), firstPage)
	}
	legacy, err := service.List(ctx, "", 100)
	if err != nil || len(legacy) != len(first.Items) {
		t.Fatalf("existing first-page call failed: %v", err)
	}
	for i := range legacy {
		if legacy[i].ID != first.Items[i].ID {
			t.Fatal("existing first-page call diverged from stable creation order")
		}
	}
	if _, err := service.Transition(ctx, application.TransitionScheduledJobRequest{
		Version: domain.ScheduledJobControlProtocolVersion, RunID: f.run.ID, JobID: first.Items[0].ID,
		Action: domain.ScheduledJobPause, ExpectedRevision: first.Items[0].Revision,
		OperationKey: "scheduled-pagination-pause-new", RequestedBy: "pagination_test",
	}); err != nil {
		t.Fatal(err)
	}
	_, foreignRun, err := runs.Create(ctx, application.CreateRunRequest{
		Goal: "different schedule owner", Profile: "code", Surface: "code", Phase: "plan", WorkspaceID: f.workspace.ID,
		Budget: domain.Budget{MaxTurns: 2, MaxToolCalls: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.at = base.Add(time.Minute)
	foreign := create(foreignRun.ID, 105)
	second, secondPage := read(ScheduledJobsPath + "?limit=100&cursor=" + url.QueryEscape(firstPage.NextCursor))
	if len(second.Items) != 5 || secondPage.NextCursor != "" {
		t.Fatalf("continuation items=%d page=%+v", len(second.Items), secondPage)
	}
	ids := func(values []domain.ScheduledJob) []string {
		out := make([]string, len(values))
		for i, job := range values {
			out[i] = job.ID
		}
		return out
	}
	if !slices.Equal(ids(append(first.Items, second.Items...)), ids(jobs)) {
		t.Fatal("status change or later insertion shifted the stable continuation")
	}
	old := second.Items[len(second.Items)-1]
	if old.Status != domain.ScheduledJobActive {
		t.Fatal("old active schedule was not reachable")
	}
	for _, action := range []string{"pause", "cancel"} {
		response := performSessionMessageRequest(t, f.api, http.MethodPost,
			"/api/v1/runs/"+old.OwnerRunID+"/scheduled-jobs/"+old.ID+"/"+action,
			testControlToken, "scheduled-pagination-old-"+action, "application/json",
			strings.NewReader(fmt.Sprintf(`{"version":"scheduled-job-control.v1","expected_revision":%d}`, old.Revision)))
		if response.Code != http.StatusAccepted {
			t.Fatalf("old schedule %s status=%d body=%s", action, response.Code, response.Body.String())
		}
		var result struct {
			Data ScheduledJobControlView `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		old = result.Data.Job
	}
	if old.Status != domain.ScheduledJobCancelled {
		t.Fatal("old schedule could not be cancelled")
	}
	filterPath := ScheduledJobsPath + "?limit=100&run_id=" + f.run.ID
	filtered, filteredPage := read(filterPath)
	filteredNext, _ := read(filterPath + "&cursor=" + url.QueryEscape(filteredPage.NextCursor))
	if len(filtered.Items) != 100 || len(filteredNext.Items) != 5 {
		t.Fatal("Run filter lost its continuation")
	}
	if !slices.Equal(ids(append(filtered.Items, filteredNext.Items...)), ids(jobs)) {
		t.Fatal("filtered pages included a different owner or lost a schedule")
	}
	foreignPage, _ := read(ScheduledJobsPath + "?run_id=" + foreignRun.ID)
	if len(foreignPage.Items) != 1 || foreignPage.Items[0].ID != foreign.ID {
		t.Fatal("exact Run filter returned another owner's schedules")
	}
	runPath := "/api/v1/runs/" + f.run.ID + "/scheduled-jobs"
	_, scopedPage := read(runPath + "?limit=100")
	scopedNext, _ := read(runPath + "?limit=100&cursor=" + url.QueryEscape(scopedPage.NextCursor))
	if len(scopedNext.Items) != 5 {
		t.Fatal("Run route lost its continuation")
	}
	widened, _ := json.Marshal(stableListCursor{Version: stableListCursorVersion,
		Scope: pageScope(ScheduledJobsPath, url.Values{}), BeforeCreatedAt: base.Format(time.RFC3339Nano),
		BeforeID: old.ID, Consumed: maxStoreCursorOffset})
	for _, path := range []string{
		ScheduledJobsPath + "?cursor=invalid!", ScheduledJobsPath + "?limit=101",
		ScheduledJobsPath + "?cursor=" + base64.RawURLEncoding.EncodeToString(widened),
		ScheduledJobsPath + "?cursor=" + firstPage.NextCursor + "&run_id=" + f.run.ID,
		ScheduledJobsPath + "?cursor=" + filteredPage.NextCursor + "&run_id=" + foreignRun.ID,
		runPath + "?cursor=" + filteredPage.NextCursor,
		runPath + "?run_id=" + f.run.ID,
	} {
		assertAPIError(t, f.get(t, path), http.StatusBadRequest, "INVALID_ARGUMENT")
	}
	for _, limit := range []int{0, 102} {
		if _, err := service.ListPage(ctx, "", time.Time{}, "", limit); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
			t.Fatalf("store page accepted limit %d: %v", limit, err)
		}
	}
}
