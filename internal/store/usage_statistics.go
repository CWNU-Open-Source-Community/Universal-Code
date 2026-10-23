package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
)

type usageReceipt struct {
	runID, source, subject, kind, threadID, title string
	sequence, compactionID                        int64
	at                                            time.Time
	legacyUsage                                   *llm.Usage
	payload                                       struct {
		Provider      string     `json:"provider"`
		Model         string     `json:"model"`
		Dispatch      string     `json:"dispatch"`
		ErrorCode     string     `json:"error_code"`
		Usage         *llm.Usage `json:"usage"`
		UsageUnknown  bool       `json:"usage_unknown"`
		InputTokens   *int64     `json:"input_tokens"`
		OutputTokens  *int64     `json:"output_tokens"`
		TotalTokens   *int64     `json:"total_tokens"`
		MonetaryKey   int64      `json:"monetary_attempt_number"`
		ModelAttempt  int64      `json:"model_attempt"`
		Purpose       string     `json:"purpose"`
		ExecutionID   string     `json:"execution_id"`
		Shard         int        `json:"shard"`
		FanoutAttempt int        `json:"attempt"`
	}
}

func (r usageReceipt) callKey() string {
	// A fan-out shard has multiple transport attempts with the same subject.
	subject := r.subject
	if subject == "" {
		subject = fmt.Sprintf("sequence:%d", r.sequence)
	}
	return fmt.Sprintf("%s/%s/%s/%d", r.runID, r.source, subject, r.payload.FanoutAttempt)
}

func (r usageReceipt) monetaryIdentity() (string, int64) {
	switch r.source {
	case "specialist_model_gateway":
		return domain.MonetaryScopeSpecialist, r.payload.MonetaryKey
	case "thread_context_compaction":
		return domain.MonetaryScopeContextCompaction, r.compactionID
	case "readonly_fanout":
		if r.payload.ExecutionID == "" || r.payload.FanoutAttempt <= 0 {
			return domain.MonetaryScopeReadOnlyFanout, 0
		}
		return domain.MonetaryScopeReadOnlyFanout, int64(llm.ReadOnlyFanoutMonetaryAttemptNumber(r.payload.ExecutionID, r.payload.Shard, r.payload.FanoutAttempt))
	default:
		return domain.MonetaryScopeRoot, r.payload.MonetaryKey
	}
}

// GetUsageStatistics reads a consistent local ledger without settling, repairing,
// or acquiring a writer lock. Terminal receipts alone contribute tokens, so
// checkpoints, started events, and monetary ledgers cannot double count usage.
func (s *SQLiteStore) GetUsageStatistics(ctx context.Context, period string, utcOffsetMinutes int, now time.Time) (domain.UsageStatistics, error) {
	return s.getUsageStatistics(ctx, "", period, utcOffsetMinutes, now)
}

// GetThreadUsageStatistics includes every Run bound to the selected Thread,
// using the same receipt and settlement rules as application-wide usage.
func (s *SQLiteStore) GetThreadUsageStatistics(ctx context.Context, threadID, period string, utcOffsetMinutes int, now time.Time) (domain.UsageStatistics, error) {
	if threadID == "" {
		return domain.UsageStatistics{}, apperror.New(apperror.CodeInvalidArgument, "usage Thread identity is required")
	}
	return s.getUsageStatistics(ctx, threadID, period, utcOffsetMinutes, now)
}

func (s *SQLiteStore) getUsageStatistics(ctx context.Context, threadID, period string, utcOffsetMinutes int, now time.Time) (domain.UsageStatistics, error) {
	if period == "" {
		period = "30d"
	}
	if (period != "today" && period != "30d" && period != "all") || utcOffsetMinutes < -840 || utcOffsetMinutes > 840 || now.IsZero() {
		return domain.UsageStatistics{}, apperror.New(apperror.CodeInvalidArgument, "usage period or UTC offset is invalid")
	}
	location := time.FixedZone("usage", utcOffsetMinutes*60)
	local := now.In(location)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	start := today
	if period == "30d" {
		start = today.AddDate(0, 0, -29)
	}
	activityStart := today.AddDate(0, 0, -181)
	result := domain.UsageStatistics{GeneratedAt: now.UTC(), Period: period, UTCOffsetMinutes: utcOffsetMinutes,
		StartDate: start.Format(time.DateOnly), EndDate: today.Format(time.DateOnly),
		Cost: domain.UsageCost{Currency: "USD"}, Models: []domain.UsageModel{}, RecentSessions: []domain.UsageSession{},
		Days: []domain.UsageDay{}, ActivityStartDate: activityStart.Format(time.DateOnly), ActivityEndDate: today.Format(time.DateOnly),
		ActivityDays: []domain.UsageActivityDay{}}
	tx, finish, err := s.beginThreadRequestObservation(ctx)
	if err != nil {
		return result, err
	}
	defer finish()
	var scopeArgs []any
	if threadID != "" {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM threads WHERE id=? AND status<>?`, threadID, domain.ThreadDeleted).Scan(&exists); err != nil {
			return result, err
		}
		scopeArgs = append(scopeArgs, threadID)
	}
	receiptQuery := `SELECT e.run_id,e.sequence,e.source,e.type,COALESCE(e.subject_id,''),e.created_at,e.payload_json,
		COALESCE(t.id,''),COALESCE(t.title,''),COALESCE(c.id,0),
		CASE WHEN f.usage_recorded=1 THEN f.input_tokens END,
		CASE WHEN f.usage_recorded=1 THEN f.output_tokens END,
		CASE WHEN f.usage_recorded=1 THEN f.total_tokens END
		FROM run_events e LEFT JOIN thread_runs binding ON binding.run_id=e.run_id
		LEFT JOIN threads t ON t.id=binding.thread_id
		LEFT JOIN thread_context_compactions c ON e.source='thread_context_compaction' AND c.run_id=e.run_id AND c.operation_id=e.subject_id
		LEFT JOIN readonly_fanout_model_calls f ON e.source='readonly_fanout' AND f.run_id=e.run_id
		AND f.execution_id=json_extract(e.payload_json,'$.execution_id') AND f.shard_ordinal=json_extract(e.payload_json,'$.shard')
		AND f.attempt_number=json_extract(e.payload_json,'$.attempt') AND f.provider=json_extract(e.payload_json,'$.provider') AND f.model=json_extract(e.payload_json,'$.model')
		WHERE ((e.source IN ('model_gateway','specialist_model_gateway') AND e.type IN ('model.started','model.completed','model.failed'))
		OR (e.source='thread_context_compaction' AND e.type IN ('context_compaction.model_completed','context_compaction.model_failed'))
		OR (e.source='readonly_fanout' AND e.type IN ('readonly_fanout.shard_completed','readonly_fanout.shard_failed','readonly_fanout.shard_cancelled')))`
	if threadID != "" {
		receiptQuery += ` AND binding.thread_id=?`
	}
	rows, err := tx.QueryContext(ctx, receiptQuery+` ORDER BY e.run_id,e.sequence`, scopeArgs...)
	if err != nil {
		return result, err
	}
	var receipts, starts []usageReceipt
	seen := make(map[string]bool)
	for rows.Next() {
		var r usageReceipt
		var at, raw string
		var input, output, total sql.NullInt64
		if err = rows.Scan(&r.runID, &r.sequence, &r.source, &r.kind, &r.subject, &at, &raw, &r.threadID, &r.title, &r.compactionID, &input, &output, &total); err != nil {
			break
		}
		if r.at, err = parseMonetaryStoreTime(at); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &r.payload); err != nil {
			break
		}
		if input.Valid && output.Valid && total.Valid {
			r.legacyUsage = &llm.Usage{InputTokens: int(input.Int64), OutputTokens: int(output.Int64), TotalTokens: int(total.Int64)}
		}
		if r.at.After(now) || r.payload.Dispatch == "not_sent" || (r.source == "readonly_fanout" && (r.payload.ErrorCode == "request_not_dispatched" || r.payload.FanoutAttempt <= 0)) {
			continue
		}
		if r.kind == "model.started" {
			starts = append(starts, r)
			continue
		}
		key := r.callKey()
		if seen[key] {
			continue
		}
		seen[key] = true
		receipts = append(receipts, r)
	}
	readErr := rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return result, err
	}
	if readErr != nil {
		return result, readErr
	}
	if closeErr != nil {
		return result, closeErr
	}

	// Index receipts once; settlement matching stays local to one attempt.
	type monetaryKey struct {
		runID, scope, provider, model string
		number                        int64
	}
	byIdentity := make(map[monetaryKey][]usageReceipt)
	startsByIdentity := make(map[monetaryKey][]usageReceipt)
	for _, r := range receipts {
		scope, number := r.monetaryIdentity()
		if r.source == "model_gateway" && number == 0 && r.payload.Purpose == "" {
			number = r.payload.ModelAttempt
		}
		if number != 0 {
			key := monetaryKey{r.runID, scope, r.payload.Provider, r.payload.Model, number}
			byIdentity[key] = append(byIdentity[key], r)
		}
	}
	for _, r := range starts {
		if r.source == "model_gateway" && r.payload.MonetaryKey == 0 && r.payload.Purpose == "" {
			key := monetaryKey{r.runID, domain.MonetaryScopeRoot, r.payload.Provider, r.payload.Model, r.payload.ModelAttempt}
			startsByIdentity[key] = append(startsByIdentity[key], r)
		}
	}

	// Cost is independently dated by the actual reservation settlement. The
	// settled amount can contain a conservative reservation estimate, so only
	// a priced settlement's actual_micros is a known cost.
	costQuery := `SELECT r.run_id,r.scope,r.attempt_number,r.provider,r.model,r.created_at,r.settled_at,r.settled_micros,
		COALESCE((SELECT MIN(sequence) FROM run_events WHERE run_id=r.run_id AND source='monetary_budget' AND type='monetary.budget_reserved' AND subject_id=r.id),0),
		COALESCE(e.payload_json,'{}') FROM run_monetary_reservations r
		LEFT JOIN run_events e ON e.run_id=r.run_id AND e.sequence=(SELECT MIN(sequence) FROM run_events WHERE run_id=r.run_id AND source='monetary_budget' AND type='monetary.budget_settled' AND subject_id=r.id)
		WHERE r.status='settled'`
	if threadID != "" {
		costQuery += ` AND EXISTS (SELECT 1 FROM thread_runs binding WHERE binding.run_id=r.run_id AND binding.thread_id=?)`
	}
	rows, err = tx.QueryContext(ctx, costQuery+` ORDER BY r.run_id,r.scope,r.attempt_number`, scopeArgs...)
	if err != nil {
		return result, err
	}
	settledCalls := make(map[string]bool)
	for rows.Next() {
		var runID, scope, provider, model, createdAt, settledAt, raw string
		var number, amount, reservedSequence int64
		if err = rows.Scan(&runID, &scope, &number, &provider, &model, &createdAt, &settledAt, &amount, &reservedSequence, &raw); err != nil {
			break
		}
		var at time.Time
		if at, err = parseMonetaryStoreTime(settledAt); err != nil {
			break
		}
		if at.After(now) {
			continue
		}
		if period == "all" && at.Before(start) {
			start = at.In(location)
		}
		if period == "all" || !at.Before(start) {
			var settlement struct {
				Actual *int64 `json:"actual_micros"`
				Status string `json:"pricing_status"`
			}
			if err = json.Unmarshal([]byte(raw), &settlement); err != nil {
				break
			}
			if settlement.Actual != nil && *settlement.Actual >= 0 && *settlement.Actual < math.MaxInt64 &&
				(settlement.Status == "" || settlement.Status == domain.MonetaryPricingPriced) && *settlement.Actual == amount {
				result.Cost.PricedCalls++
				result.Cost.KnownCostMicros, err = supervisorAddCounter(result.Cost.KnownCostMicros, *settlement.Actual, "usage cost")
				if err != nil {
					break
				}
			} else {
				result.Cost.UnpricedCalls++
			}
		}
		// Exact modern identities and auxiliary identities are durable. Legacy
		// root reservations bind the first start following their reservation;
		// terminal-only imports must have one unambiguous matching receipt.
		key := monetaryKey{runID, scope, provider, model, number}
		matches := byIdentity[key]
		if scope == domain.MonetaryScopeRoot && number < 1<<61 {
			created := parseTS(createdAt)
			var first *usageReceipt
			for i := range startsByIdentity[key] {
				r := &startsByIdentity[key][i]
				if r.sequence > reservedSequence && (reservedSequence != 0 || !r.at.Before(created)) {
					first = r
					break
				}
			}
			var candidates []usageReceipt
			for _, r := range matches {
				if first != nil {
					if r.subject == first.subject && r.sequence > first.sequence {
						candidates = append(candidates, r)
					}
				} else if r.sequence > reservedSequence && (reservedSequence != 0 || !r.at.Before(created)) {
					candidates = append(candidates, r)
				}
			}
			matches = candidates
		}
		if len(matches) == 1 {
			settledCalls[matches[0].callKey()] = true
		}
	}
	readErr, closeErr = rows.Err(), rows.Close()
	if err != nil {
		return result, err
	}
	if readErr != nil {
		return result, readErr
	}
	if closeErr != nil {
		return result, closeErr
	}

	models := make(map[[2]string]*domain.UsageModel)
	sessions := make(map[string]*domain.UsageSession)
	days := make(map[string]*domain.UsageDay)
	activity := make(map[string]*domain.UsageActivityDay)
	for _, r := range receipts {
		usage, e := usageReceiptTotals(r)
		if e != nil {
			return result, e
		}
		date := r.at.In(location).Format(time.DateOnly)
		if !r.at.Before(activityStart) {
			day := activity[date]
			if day == nil {
				day = &domain.UsageActivityDay{Date: date}
				activity[date] = day
			}
			day.Calls++
			day.TotalTokens, err = supervisorAddCounter(day.TotalTokens, usage.TotalTokens, "usage activity tokens")
			if err != nil {
				return result, err
			}
		}
		if period == "all" && r.at.Before(start) {
			start = r.at.In(location)
		}
		if period != "all" && r.at.Before(start) {
			continue
		}
		if !settledCalls[r.callKey()] {
			result.Cost.UntrackedCalls++
		}
		if err = addUsageTotals(&result.Totals, usage); err != nil {
			return result, err
		}
		key := [2]string{r.payload.Provider, r.payload.Model}
		if models[key] == nil {
			models[key] = &domain.UsageModel{Provider: key[0], Model: key[1]}
		}
		if err = addUsageTotals(&models[key].Usage, usage); err != nil {
			return result, err
		}
		if days[date] == nil {
			days[date] = &domain.UsageDay{Date: date}
		}
		if err = addUsageTotals(&days[date].Usage, usage); err != nil {
			return result, err
		}
		if r.threadID == "" {
			result.UnassignedCalls++
			continue
		}
		if sessions[r.threadID] == nil {
			sessions[r.threadID] = &domain.UsageSession{ThreadID: r.threadID, Title: r.title}
		}
		session := sessions[r.threadID]
		if r.at.After(session.LastActivityAt) {
			session.LastActivityAt = r.at.UTC()
		}
		if err = addUsageTotals(&session.Usage, usage); err != nil {
			return result, err
		}
	}
	result.StartDate = start.Format(time.DateOnly)
	for _, model := range models {
		result.Models = append(result.Models, *model)
	}
	sort.Slice(result.Models, func(i, j int) bool {
		a, b := result.Models[i], result.Models[j]
		if a.Usage.TotalTokens != b.Usage.TotalTokens {
			return a.Usage.TotalTokens > b.Usage.TotalTokens
		}
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		return a.Model < b.Model
	})
	for _, session := range sessions {
		result.RecentSessions = append(result.RecentSessions, *session)
	}
	sort.Slice(result.RecentSessions, func(i, j int) bool {
		a, b := result.RecentSessions[i], result.RecentSessions[j]
		if !a.LastActivityAt.Equal(b.LastActivityAt) {
			return a.LastActivityAt.After(b.LastActivityAt)
		}
		return a.ThreadID < b.ThreadID
	})
	if len(result.RecentSessions) > 8 {
		result.RecentSessions = result.RecentSessions[:8]
	}
	for _, day := range days {
		result.Days = append(result.Days, *day)
	}
	sort.Slice(result.Days, func(i, j int) bool { return result.Days[i].Date < result.Days[j].Date })
	for _, day := range activity {
		result.ActivityDays = append(result.ActivityDays, *day)
	}
	sort.Slice(result.ActivityDays, func(i, j int) bool { return result.ActivityDays[i].Date < result.ActivityDays[j].Date })
	return result, nil
}

func usageReceiptTotals(r usageReceipt) (domain.UsageTotals, error) {
	total := domain.UsageTotals{Calls: 1}
	usage := r.payload.Usage
	complete := !r.payload.UsageUnknown
	if usage == nil && r.source == "readonly_fanout" {
		usage = r.legacyUsage
		if usage == nil && (r.payload.InputTokens != nil || r.payload.OutputTokens != nil || r.payload.TotalTokens != nil) {
			usage = &llm.Usage{}
			complete = complete && r.payload.InputTokens != nil && r.payload.OutputTokens != nil
			if r.payload.InputTokens != nil {
				usage.InputTokens = int(*r.payload.InputTokens)
			}
			if r.payload.OutputTokens != nil {
				usage.OutputTokens = int(*r.payload.OutputTokens)
			}
			if r.payload.TotalTokens != nil {
				usage.TotalTokens = int(*r.payload.TotalTokens)
			}
		}
	}
	if usage == nil {
		return total, nil
	}
	u := *usage
	var err error
	total.InputTokens, total.OutputTokens, total.TotalTokens, err = supervisorUsage(u)
	if err != nil {
		return total, err
	}
	total.CacheReadTokens, total.CacheCreationTokens = int64(u.CacheReadTokens), int64(u.CacheCreationTokens)
	if complete {
		total.UsageReportedCalls = 1
	}
	if u.CacheUsageReported {
		total.CacheUsageReportedCalls = 1
	}
	return total, nil
}

func addUsageTotals(target *domain.UsageTotals, value domain.UsageTotals) error {
	for _, counter := range []struct {
		target *int64
		value  int64
	}{
		{&target.InputTokens, value.InputTokens}, {&target.OutputTokens, value.OutputTokens},
		{&target.TotalTokens, value.TotalTokens}, {&target.CacheReadTokens, value.CacheReadTokens},
		{&target.CacheCreationTokens, value.CacheCreationTokens}, {&target.Calls, value.Calls},
		{&target.UsageReportedCalls, value.UsageReportedCalls}, {&target.CacheUsageReportedCalls, value.CacheUsageReportedCalls},
	} {
		var err error
		*counter.target, err = supervisorAddCounter(*counter.target, counter.value, "usage statistics")
		if err != nil {
			return err
		}
	}
	return nil
}
