package desktop

import (
	"context"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/scheduler"
)

// The ordinary desktop worker selects explicitly confirmed observation and
// execution jobs. The service applies this restriction before reconciliation and
// claiming, so opening the app cannot activate legacy scheduled work.
type scheduledConfirmedRunner struct {
	service *application.ScheduledJobService
}

func (r scheduledConfirmedRunner) RunDue(ctx context.Context, owner string, now time.Time) (bool, error) {
	return r.service.RunDueConfirmed(ctx, owner, now)
}

type scheduledDesktopWorkerHealth struct {
	*scheduler.Worker
	confirmedOnly bool
}

func (h scheduledDesktopWorkerHealth) SelectionScope() string {
	if h.confirmedOnly {
		return "confirmed_jobs"
	}
	return "all_jobs"
}
