package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/redact"
)

func (a *API) usageStatistics(request *http.Request) (any, *Page, error) {
	values := request.URL.Query()
	for name, items := range values {
		if (name != "period" && name != "utc_offset_minutes") || len(items) != 1 || items[0] == "" {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument, "usage query parameter is invalid")
		}
	}
	period := values.Get("period")
	if period == "" {
		period = "30d"
	}
	if period != "today" && period != "30d" && period != "all" {
		return nil, nil, apperror.New(apperror.CodeInvalidArgument, "usage period must be today, 30d, or all")
	}
	offset := 0
	if raw := values.Get("utc_offset_minutes"); raw != "" {
		var err error
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < -840 || offset > 840 {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument, "usage UTC offset must be an integer between -840 and 840")
		}
	}
	source, ok := a.store.(interface {
		GetUsageStatistics(context.Context, string, int, time.Time) (domain.UsageStatistics, error)
	})
	if !ok {
		return nil, nil, apperror.New(apperror.CodeFailedPrecondition, "usage statistics are unavailable")
	}
	result, err := source.GetUsageStatistics(request.Context(), period, offset, time.Now().UTC())
	if err != nil {
		return nil, nil, err
	}
	for i := range result.RecentSessions {
		result.RecentSessions[i].Title = redact.String(result.RecentSessions[i].Title)
	}
	return result, nil, nil
}
