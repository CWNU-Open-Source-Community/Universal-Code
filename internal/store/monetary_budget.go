package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/pricing"
)

// ImportPriceSnapshot atomically replaces the active operator price table.
// Importing the exact same content again is an idempotent replay; a changed
// document with the same fingerprint is impossible by construction.
func (s *SQLiteStore) ImportPriceSnapshot(ctx context.Context,
	snapshot pricing.Snapshot,
) (pricing.Snapshot, bool, error) {
	if err := snapshot.Validate(); err != nil {
		return pricing.Snapshot{}, false, apperror.New(apperror.CodeInvalidArgument,
			"price snapshot is invalid")
	}
	entries, err := json.Marshal(snapshot.Entries)
	if err != nil {
		return pricing.Snapshot{}, false, err
	}
	if len(entries) > pricing.MaxSnapshotBytes {
		return pricing.Snapshot{}, false, apperror.New(apperror.CodeInvalidArgument,
			"price snapshot entries exceed the size limit")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return pricing.Snapshot{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var existing struct {
		id, protocol, source, currency, importedBy, importedAt, validFrom, validUntil, fingerprint, entries string
	}
	err = tx.QueryRowContext(ctx, `SELECT id, protocol_version, source, currency,
		imported_by, imported_at, valid_from, valid_until, fingerprint, entries_json
		FROM provider_price_snapshots WHERE fingerprint = ?`, snapshot.Fingerprint).
		Scan(&existing.id, &existing.protocol, &existing.source, &existing.currency,
			&existing.importedBy, &existing.importedAt, &existing.validFrom, &existing.validUntil,
			&existing.fingerprint, &existing.entries)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return pricing.Snapshot{}, false, err
		}
		replayed, parseErr := decodePriceSnapshotRow(existing.id, existing.protocol,
			existing.source, existing.currency, existing.importedBy, existing.importedAt,
			existing.validFrom, existing.validUntil, existing.fingerprint, existing.entries)
		if parseErr != nil {
			return pricing.Snapshot{}, false, parseErr
		}
		return replayed, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return pricing.Snapshot{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE provider_price_snapshots SET active = 0
		WHERE active = 1`); err != nil {
		return pricing.Snapshot{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO provider_price_snapshots
		(id, protocol_version, source, currency, imported_by, imported_at,
		valid_from, valid_until, fingerprint, entries_json, active)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`, snapshot.ID,
		snapshot.ProtocolVersion, snapshot.Source, snapshot.Currency, snapshot.ImportedBy,
		ts(snapshot.ImportedAt), ts(snapshot.ValidFrom), ts(snapshot.ValidUntil),
		snapshot.Fingerprint, string(entries)); err != nil {
		return pricing.Snapshot{}, false, err
	}
	// The immutable snapshot row itself is the audit trail; there is no run
	// or mission context for a global operator import to attach an event to.
	if err := tx.Commit(); err != nil {
		return pricing.Snapshot{}, false, err
	}
	return snapshot, false, nil
}

func decodePriceSnapshotRow(id, protocol, source, currency, importedBy, importedAt,
	validFrom, validUntil, fingerprint, entriesJSON string,
) (pricing.Snapshot, error) {
	snapshot := pricing.Snapshot{
		ID: id, ProtocolVersion: protocol, Source: source, Currency: currency,
		ImportedBy: importedBy, Fingerprint: fingerprint,
	}
	var err error
	if snapshot.ImportedAt, err = parseMonetaryStoreTime(importedAt); err != nil {
		return pricing.Snapshot{}, err
	}
	if snapshot.ValidFrom, err = parseMonetaryStoreTime(validFrom); err != nil {
		return pricing.Snapshot{}, err
	}
	if snapshot.ValidUntil, err = parseMonetaryStoreTime(validUntil); err != nil {
		return pricing.Snapshot{}, err
	}
	var entries []pricing.Entry
	if err := json.Unmarshal([]byte(entriesJSON), &entries); err != nil {
		return pricing.Snapshot{}, errors.New("stored price snapshot entries are malformed")
	}
	snapshot.Entries = entries
	if err := snapshot.Validate(); err != nil {
		return pricing.Snapshot{}, errors.New("stored price snapshot is invalid")
	}
	return snapshot, nil
}

// ActivePriceSnapshot returns the currently active price table, if any.
func (s *SQLiteStore) ActivePriceSnapshot(ctx context.Context) (pricing.Snapshot, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, protocol_version, source, currency,
		imported_by, imported_at, valid_from, valid_until, fingerprint, entries_json
		FROM provider_price_snapshots WHERE active = 1`)
	var snapshot pricing.Snapshot
	var entriesJSON, importedAt, validFrom, validUntil string
	if err := row.Scan(&snapshot.ID, &snapshot.ProtocolVersion, &snapshot.Source,
		&snapshot.Currency, &snapshot.ImportedBy, &importedAt, &validFrom, &validUntil,
		&snapshot.Fingerprint, &entriesJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return pricing.Snapshot{}, false, nil
		}
		return pricing.Snapshot{}, false, err
	}
	var err error
	if snapshot.ImportedAt, err = parseMonetaryStoreTime(importedAt); err != nil {
		return pricing.Snapshot{}, false, err
	}
	if snapshot.ValidFrom, err = parseMonetaryStoreTime(validFrom); err != nil {
		return pricing.Snapshot{}, false, err
	}
	if snapshot.ValidUntil, err = parseMonetaryStoreTime(validUntil); err != nil {
		return pricing.Snapshot{}, false, err
	}
	var entries []pricing.Entry
	if err := json.Unmarshal([]byte(entriesJSON), &entries); err != nil {
		return pricing.Snapshot{}, false, errors.New("stored price snapshot entries are malformed")
	}
	snapshot.Entries = entries
	if err := snapshot.Validate(); err != nil {
		return pricing.Snapshot{}, false, errors.New("stored price snapshot is invalid")
	}
	return snapshot, true, nil
}

// ListPriceSnapshots returns the most recent imported tables, newest first.
func (s *SQLiteStore) ListPriceSnapshots(ctx context.Context, limit int) ([]pricing.Snapshot, error) {
	if limit <= 0 || limit > 64 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "price snapshot list limit is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, protocol_version, source, currency,
		imported_by, imported_at, valid_from, valid_until, fingerprint, entries_json
		FROM provider_price_snapshots ORDER BY imported_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pricing.Snapshot
	for rows.Next() {
		var snapshot pricing.Snapshot
		var entriesJSON, importedAt, validFrom, validUntil string
		if err := rows.Scan(&snapshot.ID, &snapshot.ProtocolVersion, &snapshot.Source,
			&snapshot.Currency, &snapshot.ImportedBy, &importedAt, &validFrom, &validUntil,
			&snapshot.Fingerprint, &entriesJSON); err != nil {
			return nil, err
		}
		var parseErr error
		if snapshot.ImportedAt, parseErr = parseMonetaryStoreTime(importedAt); parseErr != nil {
			return nil, parseErr
		}
		if snapshot.ValidFrom, parseErr = parseMonetaryStoreTime(validFrom); parseErr != nil {
			return nil, parseErr
		}
		if snapshot.ValidUntil, parseErr = parseMonetaryStoreTime(validUntil); parseErr != nil {
			return nil, parseErr
		}
		var entries []pricing.Entry
		if err := json.Unmarshal([]byte(entriesJSON), &entries); err != nil {
			return nil, errors.New("stored price snapshot entries are malformed")
		}
		snapshot.Entries = entries
		if err := snapshot.Validate(); err != nil {
			return nil, errors.New("stored price snapshot is invalid")
		}
		out = append(out, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ReserveModelCost reserves the upper-bound cost of one model attempt against
// the run aggregate before any Provider call. Replaying the same attempt
// identity with the same amount and price fingerprint is idempotent.
func (s *SQLiteStore) ReserveModelCost(ctx context.Context,
	request domain.MonetaryReserveRequest,
) (domain.MonetaryUsage, bool, error) {
	normalized, err := request.Normalize()
	if err != nil {
		return domain.MonetaryUsage{}, false, apperror.New(apperror.CodeInvalidArgument,
			"monetary reserve request is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	budget, status, missionID, err := loadMonetaryRunTx(ctx, tx, normalized.RunID)
	if err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	if status == domain.RunCompleted || status == domain.RunFailed || status == domain.RunCancelled {
		return domain.MonetaryUsage{}, false, apperror.New(apperror.CodeFailedPrecondition,
			fmt.Sprintf("run %s is terminal and cannot reserve model cost", normalized.RunID))
	}
	capMicros, err := pricing.USDToMicros(budget.MaxCostUSD)
	if err != nil {
		return domain.MonetaryUsage{}, false, apperror.New(apperror.CodeInvalidArgument,
			"run monetary cap is invalid")
	}
	if capMicros <= 0 {
		return domain.MonetaryUsage{}, false, nil
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_monetary_usage
		(run_id, reserved_micros, settled_micros, released_micros, updated_at)
		VALUES (?, 0, 0, 0, ?) ON CONFLICT(run_id) DO NOTHING`, normalized.RunID, ts(now)); err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	if err := reconcileMonetaryReservationsTx(ctx, tx, normalized.RunID); err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	if existing, found, err := getMonetaryReservationTx(ctx, tx, normalized.RunID,
		normalized.Scope, normalized.AttemptNumber); err != nil {
		return domain.MonetaryUsage{}, false, err
	} else if found {
		reservedMicros := existing.ReservedMicros
		if existing.Status == "settled" {
			// Settlement can extend the accounted exposure for an actual
			// overage. Replays still bind to the original reservation request.
			if err := tx.QueryRowContext(ctx, `SELECT json_extract(payload_json,'$.reserved_micros')
				FROM run_events WHERE run_id=? AND source='monetary_budget' AND type=?
				AND subject_id=? ORDER BY sequence LIMIT 1`, normalized.RunID,
				events.MonetaryBudgetReservedEvent, existing.ID).Scan(&reservedMicros); err != nil {
				return domain.MonetaryUsage{}, false, err
			}
		}
		if reservedMicros != normalized.ReservedMicros ||
			existing.PriceFingerprint != normalized.PriceFingerprint ||
			existing.Provider != normalized.Provider || existing.Model != normalized.Model {
			return domain.MonetaryUsage{}, false, apperror.New(apperror.CodeConflict,
				"model attempt already holds a different monetary reservation")
		}
		usage, usageErr := getMonetaryUsageTx(ctx, tx, normalized.RunID, capMicros)
		if usageErr != nil {
			return domain.MonetaryUsage{}, false, usageErr
		}
		if err := tx.Commit(); err != nil {
			return domain.MonetaryUsage{}, false, err
		}
		return usage, true, nil
	}
	aggregate, err := loadMonetaryAggregateTx(ctx, tx, normalized.RunID)
	if err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	openMicros := aggregate.ReservedMicros - aggregate.SettledMicros - aggregate.ReleasedMicros
	consumedMicros := aggregate.ReservedMicros - aggregate.ReleasedMicros
	if consumedMicros > capMicros-normalized.ReservedMicros {
		if !aggregate.ExhaustedAt.Valid {
			if _, err := tx.ExecContext(ctx, `UPDATE run_monetary_usage SET updated_at = ?,
				exhausted_at = ? WHERE run_id = ? AND exhausted_at IS NULL`, ts(now), ts(now),
				normalized.RunID); err != nil {
				return domain.MonetaryUsage{}, false, err
			}
			event, eventErr := events.New(normalized.RunID, missionID,
				events.MonetaryBudgetExhaustedEvent, "monetary_budget", normalized.RunID,
				map[string]any{"open_micros": openMicros, "cap_micros": capMicros,
					"reserved_micros": normalized.ReservedMicros, "scope": normalized.Scope})
			if eventErr != nil {
				return domain.MonetaryUsage{}, false, eventErr
			}
			if _, err := insertRunEventTx(ctx, tx, event); err != nil {
				return domain.MonetaryUsage{}, false, err
			}
		}
		if err := tx.Commit(); err != nil {
			return domain.MonetaryUsage{}, false, err
		}
		return domain.MonetaryUsage{}, false, apperror.New(apperror.CodeResourceExhausted,
			fmt.Sprintf("run %s exhausted its monetary budget", normalized.RunID))
	}
	result, err := tx.ExecContext(ctx, `UPDATE run_monetary_usage SET
		reserved_micros = reserved_micros + ?, updated_at = ? WHERE run_id = ? AND
		reserved_micros = ?`, normalized.ReservedMicros, ts(now), normalized.RunID,
		aggregate.ReservedMicros)
	if err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return domain.MonetaryUsage{}, false, errors.New("monetary budget changed concurrently")
	}
	reservationID := idgen.New("monetary-reservation")
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_monetary_reservations
		(id, run_id, scope, provider, model, attempt_number, reserved_micros,
		settled_micros, released_micros, status, estimate_source, price_fingerprint,
		created_at) VALUES (?, ?, ?, ?, ?, ?, ?, 0, 0, 'reserved', ?, ?, ?)`,
		reservationID, normalized.RunID, normalized.Scope, normalized.Provider,
		normalized.Model, normalized.AttemptNumber, normalized.ReservedMicros,
		normalized.EstimateSource, normalized.PriceFingerprint, ts(now)); err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	event, err := events.New(normalized.RunID, missionID, events.MonetaryBudgetReservedEvent,
		"monetary_budget", reservationID, map[string]any{"scope": normalized.Scope,
			"provider": normalized.Provider, "model": normalized.Model,
			"reserved_micros": normalized.ReservedMicros, "cap_micros": capMicros,
			"estimate_source": normalized.EstimateSource})
	if err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	if _, err := insertRunEventTx(ctx, tx, event); err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	usage, err := s.GetMonetaryUsage(ctx, normalized.RunID)
	if err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	return usage, false, nil
}

// SettleModelCost closes one reservation with the actual usage cost. The
// unused portion is released in the same transaction. Known charges above the
// estimate extend the accounted exposure so subsequent calls cannot spend it.
func (s *SQLiteStore) SettleModelCost(ctx context.Context,
	request domain.MonetarySettleRequest,
) (domain.MonetaryUsage, bool, error) {
	normalized, err := request.Normalize()
	if err != nil {
		return domain.MonetaryUsage{}, false, apperror.New(apperror.CodeInvalidArgument,
			"monetary settle request is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	budget, status, missionID, err := loadMonetaryRunTx(ctx, tx, normalized.RunID)
	if err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	_ = status
	capMicros, err := pricing.USDToMicros(budget.MaxCostUSD)
	if err != nil || capMicros <= 0 {
		return domain.MonetaryUsage{}, false, nil
	}
	reservation, found, err := getMonetaryReservationTx(ctx, tx, normalized.RunID,
		normalized.Scope, normalized.AttemptNumber)
	if err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	if !found {
		return domain.MonetaryUsage{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"model attempt has no monetary reservation to settle")
	}
	if reservation.Status == "settled" {
		usage, usageErr := getMonetaryUsageTx(ctx, tx, normalized.RunID, capMicros)
		if usageErr != nil {
			return domain.MonetaryUsage{}, false, usageErr
		}
		if err := tx.Commit(); err != nil {
			return domain.MonetaryUsage{}, false, err
		}
		return usage, true, nil
	}
	if reservation.Status == "released" {
		return domain.MonetaryUsage{}, false, apperror.New(apperror.CodeConflict,
			"model attempt monetary reservation was already released")
	}
	aggregate, err := loadMonetaryAggregateTx(ctx, tx, normalized.RunID)
	if err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	pricingStatus := normalized.PricingStatus
	if pricingStatus == "" {
		pricingStatus = domain.MonetaryPricingPriced
		if normalized.ActualMicros == math.MaxInt64 {
			pricingStatus = domain.MonetaryPricingUsageUnknown
		}
	}
	settledMicros := reservation.ReservedMicros
	if pricingStatus == domain.MonetaryPricingPriced {
		settledMicros = normalized.ActualMicros
	}
	overageMicros := max(0, settledMicros-reservation.ReservedMicros)
	releasedMicros := max(0, reservation.ReservedMicros-settledMicros)
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE run_monetary_usage SET
		reserved_micros = reserved_micros + ?, settled_micros = settled_micros + ?, released_micros = released_micros + ?,
		updated_at = ? WHERE run_id = ? AND settled_micros = ? AND released_micros = ?`,
		overageMicros, settledMicros, releasedMicros, ts(now), normalized.RunID, aggregate.SettledMicros,
		aggregate.ReleasedMicros)
	if err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return domain.MonetaryUsage{}, false, errors.New("monetary budget changed concurrently")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_monetary_reservations SET
		reserved_micros = reserved_micros + ?, settled_micros = ?, released_micros = ?, status = 'settled', settled_at = ?
		WHERE run_id = ? AND scope = ? AND attempt_number = ?`, overageMicros, settledMicros, releasedMicros,
		ts(now), normalized.RunID, normalized.Scope, normalized.AttemptNumber); err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	event, err := events.New(normalized.RunID, missionID, events.MonetaryBudgetSettledEvent,
		"monetary_budget", reservation.ID, map[string]any{"scope": normalized.Scope,
			"settled_micros": settledMicros, "released_micros": releasedMicros,
			"actual_micros":   normalized.ActualMicros,
			"under_reserved":  overageMicros > 0,
			"pricing_status":  pricingStatus,
			"estimate_source": reservation.EstimateSource})
	if err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	if _, err := insertRunEventTx(ctx, tx, event); err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	usage, err := s.GetMonetaryUsage(ctx, normalized.RunID)
	if err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	return usage, false, nil
}

// ReleaseModelCost releases one open reservation without settling.
func (s *SQLiteStore) ReleaseModelCost(ctx context.Context,
	request domain.MonetaryReleaseRequest,
) (domain.MonetaryUsage, bool, error) {
	normalized, err := request.Normalize()
	if err != nil {
		return domain.MonetaryUsage{}, false, apperror.New(apperror.CodeInvalidArgument,
			"monetary release request is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	budget, _, missionID, err := loadMonetaryRunTx(ctx, tx, normalized.RunID)
	if err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	capMicros, err := pricing.USDToMicros(budget.MaxCostUSD)
	if err != nil || capMicros <= 0 {
		return domain.MonetaryUsage{}, false, nil
	}
	reservation, found, err := getMonetaryReservationTx(ctx, tx, normalized.RunID,
		normalized.Scope, normalized.AttemptNumber)
	if err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	if !found {
		return domain.MonetaryUsage{}, false, nil
	}
	if reservation.Status == "released" {
		usage, usageErr := getMonetaryUsageTx(ctx, tx, normalized.RunID, capMicros)
		if usageErr != nil {
			return domain.MonetaryUsage{}, false, usageErr
		}
		if err := tx.Commit(); err != nil {
			return domain.MonetaryUsage{}, false, err
		}
		return usage, true, nil
	}
	if reservation.Status == "settled" {
		return domain.MonetaryUsage{}, false, apperror.New(apperror.CodeConflict,
			"model attempt monetary reservation was already settled")
	}
	if normalized.Scope == domain.MonetaryScopeRoot || normalized.Scope == domain.MonetaryScopeSpecialist || normalized.Scope == domain.MonetaryScopeReadOnlyFanout {
		evidence, e := monetaryModelEvidenceTx(ctx, tx, normalized.RunID, reservation.ID, normalized.Scope, normalized.AttemptNumber, reservation.Provider, reservation.Model)
		if e != nil {
			return domain.MonetaryUsage{}, false, e
		}
		if evidence.Sent {
			return domain.MonetaryUsage{}, false, apperror.New(apperror.CodeConflict, "sent model monetary reservation must be settled, not released")
		}
	}
	aggregate, err := loadMonetaryAggregateTx(ctx, tx, normalized.RunID)
	if err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	releasedMicros := reservation.ReservedMicros - reservation.SettledMicros
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE run_monetary_usage SET
		released_micros = released_micros + ?, updated_at = ?
		WHERE run_id = ? AND released_micros = ?`, releasedMicros, ts(now),
		normalized.RunID, aggregate.ReleasedMicros)
	if err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return domain.MonetaryUsage{}, false, errors.New("monetary budget changed concurrently")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_monetary_reservations SET
		released_micros = ?, status = 'released', released_at = ?
		WHERE run_id = ? AND scope = ? AND attempt_number = ?`, releasedMicros, ts(now),
		normalized.RunID, normalized.Scope, normalized.AttemptNumber); err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	event, err := events.New(normalized.RunID, missionID, events.MonetaryBudgetReleasedEvent,
		"monetary_budget", reservation.ID, map[string]any{"scope": normalized.Scope,
			"released_micros": releasedMicros})
	if err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	if _, err := insertRunEventTx(ctx, tx, event); err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	usage, err := s.GetMonetaryUsage(ctx, normalized.RunID)
	if err != nil {
		return domain.MonetaryUsage{}, false, err
	}
	return usage, false, nil
}

// GetMonetaryUsage projects the run aggregate. Untracked runs (no cap) return
// Tracked=false with zero counters.
func (s *SQLiteStore) GetMonetaryUsage(ctx context.Context, runID string) (domain.MonetaryUsage, error) {
	runID = strings.TrimSpace(runID)
	var budgetJSON string
	var runCreatedAt string
	if err := s.db.QueryRowContext(ctx, `SELECT budget_json, created_at FROM runs WHERE id = ?`,
		runID).Scan(&budgetJSON, &runCreatedAt); err != nil {
		return domain.MonetaryUsage{}, err
	}
	var budget domain.Budget
	if err := json.Unmarshal([]byte(budgetJSON), &budget); err != nil {
		return domain.MonetaryUsage{}, err
	}
	capMicros, err := pricing.USDToMicros(budget.MaxCostUSD)
	if err != nil || capMicros <= 0 {
		return domain.MonetaryUsage{}, nil
	}
	var hasAggregate, hasReservations bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_monetary_usage WHERE run_id=?),
		EXISTS(SELECT 1 FROM run_monetary_reservations WHERE run_id=?)`, runID, runID).Scan(&hasAggregate, &hasReservations); err != nil {
		return domain.MonetaryUsage{}, err
	}
	if !hasAggregate {
		if hasReservations {
			return domain.MonetaryUsage{}, apperror.New(apperror.CodeFailedPrecondition, "monetary reservations are missing their aggregate")
		}
		// The Run is real, but no call has reserved money yet. This is a
		// read-only initial projection, not a fabricated ledger update. Avoid
		// acquiring the write transaction used to reconcile existing receipts.
		createdAt, err := parseMonetaryStoreTime(runCreatedAt)
		if err != nil {
			return domain.MonetaryUsage{}, err
		}
		usage := domain.MonetaryUsage{RunID: runID, Currency: pricing.CurrencyUSD, CapMicros: capMicros,
			RemainingMicros: capMicros, Tracked: true, UpdatedAt: createdAt}
		return usage, usage.Validate()
	}
	tx, txErr := s.db.BeginTx(ctx, &sql.TxOptions{})
	if txErr != nil {
		return domain.MonetaryUsage{}, txErr
	}
	defer func() { _ = tx.Rollback() }()
	if err := reconcileMonetaryReservationsTx(ctx, tx, runID); err != nil {
		return domain.MonetaryUsage{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.MonetaryUsage{}, err
	}
	return getMonetaryUsageTx(ctx, s.db, runID, capMicros)
}

// ReleaseOpenMonetaryReservations releases unused reservations on a terminal
// Run. Sent root model calls retain their ceiling until their outcome is known.
func (s *SQLiteStore) ReleaseOpenMonetaryReservations(ctx context.Context, runID string) (int, error) {
	runID = strings.TrimSpace(runID)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	// A request already sent to a provider is not free on Run cancel.
	if err := reconcileMonetaryReservationsTx(ctx, tx, runID); err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, scope, attempt_number, provider, model, reserved_micros,
		settled_micros FROM run_monetary_reservations WHERE run_id = ? AND status = 'reserved'`,
		runID)
	if err != nil {
		return 0, err
	}
	type openReservation struct {
		provider, model string
		id              string
		scope           string
		attemptNumber   int64
		reservedMicros  int64
		settledMicros   int64
	}
	var open []openReservation
	for rows.Next() {
		var current openReservation
		if err := rows.Scan(&current.id, &current.scope, &current.attemptNumber, &current.provider, &current.model,
			&current.reservedMicros, &current.settledMicros); err != nil {
			rows.Close()
			return 0, err
		}
		open = append(open, current)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(open) == 0 {
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return 0, nil
	}
	now := time.Now().UTC()
	releasedCount := 0
	for _, reservation := range open {
		releasedMicros := reservation.reservedMicros - reservation.settledMicros
		if reservation.scope == domain.MonetaryScopeReadOnlyFanout {
			// A fan-out reservation has no durable proof of non-dispatch.
			// Recovery or cancellation cannot make its unknown charge free.
			continue
		}
		if reservation.scope == domain.MonetaryScopeRoot || reservation.scope == domain.MonetaryScopeSpecialist {
			evidence, err := monetaryModelEvidenceTx(ctx, tx, runID, reservation.id, reservation.scope, reservation.attemptNumber, reservation.provider, reservation.model)
			if err != nil {
				return 0, err
			}
			if evidence.Sent {
				continue
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE run_monetary_usage SET
			released_micros = released_micros + ?, updated_at = ? WHERE run_id = ?`,
			releasedMicros, ts(now), runID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE run_monetary_reservations SET
			released_micros = ?, status = 'released', released_at = ? WHERE id = ? AND
			status = 'reserved'`, releasedMicros, ts(now), reservation.id); err != nil {
			return 0, err
		}
		releasedCount++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return releasedCount, nil
}

type monetaryReservationRow struct {
	ID               string
	Provider         string
	Model            string
	ReservedMicros   int64
	SettledMicros    int64
	ReleasedMicros   int64
	Status           string
	EstimateSource   string
	PriceFingerprint string
}

func getMonetaryReservationTx(ctx context.Context, tx *sql.Tx, runID, scope string,
	attemptNumber int64,
) (monetaryReservationRow, bool, error) {
	row := tx.QueryRowContext(ctx, `SELECT id, provider, model, reserved_micros,
		settled_micros, released_micros, status, estimate_source, price_fingerprint
		FROM run_monetary_reservations WHERE run_id = ? AND scope = ? AND attempt_number = ?`,
		runID, scope, attemptNumber)
	var current monetaryReservationRow
	err := row.Scan(&current.ID, &current.Provider, &current.Model, &current.ReservedMicros,
		&current.SettledMicros, &current.ReleasedMicros, &current.Status,
		&current.EstimateSource, &current.PriceFingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return monetaryReservationRow{}, false, nil
	}
	if err != nil {
		return monetaryReservationRow{}, false, err
	}
	return current, true, nil
}

type monetaryAggregateRow struct {
	ReservedMicros int64
	SettledMicros  int64
	ReleasedMicros int64
	UpdatedAt      time.Time
	ExhaustedAt    sql.NullString
}

func loadMonetaryAggregateTx(ctx context.Context, tx *sql.Tx, runID string) (monetaryAggregateRow, error) {
	row := tx.QueryRowContext(ctx, `SELECT reserved_micros, settled_micros, released_micros,
		updated_at, exhausted_at FROM run_monetary_usage WHERE run_id = ?`, runID)
	var current monetaryAggregateRow
	var updatedAt string
	if err := row.Scan(&current.ReservedMicros, &current.SettledMicros, &current.ReleasedMicros,
		&updatedAt, &current.ExhaustedAt); err != nil {
		return monetaryAggregateRow{}, err
	}
	var err error
	if current.UpdatedAt, err = parseMonetaryStoreTime(updatedAt); err != nil {
		return monetaryAggregateRow{}, err
	}
	return current, nil
}

func getMonetaryUsageTx(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, runID string, capMicros int64) (domain.MonetaryUsage, error) {
	row := queryer.QueryRowContext(ctx, `SELECT reserved_micros, settled_micros,
		released_micros, updated_at, exhausted_at FROM run_monetary_usage WHERE run_id = ?`,
		runID)
	var aggregate monetaryAggregateRow
	var updatedAt string
	if err := row.Scan(&aggregate.ReservedMicros, &aggregate.SettledMicros,
		&aggregate.ReleasedMicros, &updatedAt, &aggregate.ExhaustedAt); err != nil {
		return domain.MonetaryUsage{}, err
	}
	var err error
	if aggregate.UpdatedAt, err = parseMonetaryStoreTime(updatedAt); err != nil {
		return domain.MonetaryUsage{}, err
	}
	usage := domain.MonetaryUsage{
		RunID: runID, Currency: pricing.CurrencyUSD, CapMicros: capMicros,
		ReservedMicros: aggregate.ReservedMicros, SettledMicros: aggregate.SettledMicros,
		ReleasedMicros: aggregate.ReleasedMicros, UpdatedAt: aggregate.UpdatedAt,
		Tracked: true, EstimateSource: pricing.SourceOperatorImport,
	}
	usage.RemainingMicros = max(0, capMicros-(usage.ReservedMicros-usage.ReleasedMicros))
	if err := queryer.QueryRowContext(ctx, `SELECT COUNT(DISTINCT subject_id) FROM run_events
		WHERE run_id=? AND source='monetary_budget' AND type=? AND
		(json_extract(payload_json,'$.pricing_status') IN ('usage_unknown','price_missing','cache_price_missing')
		OR (json_extract(payload_json,'$.pricing_status') IS NULL AND json_extract(payload_json,'$.actual_micros')=?))`,
		runID, events.MonetaryBudgetSettledEvent, int64(math.MaxInt64)).Scan(&usage.UnpricedCalls); err != nil {
		return domain.MonetaryUsage{}, err
	}
	if aggregate.ExhaustedAt.Valid {
		exhausted, parseErr := parseMonetaryStoreTime(aggregate.ExhaustedAt.String)
		if parseErr != nil {
			return domain.MonetaryUsage{}, parseErr
		}
		usage.ExhaustedAt = &exhausted
	}
	if err := usage.Validate(); err != nil {
		return domain.MonetaryUsage{}, err
	}
	return usage, nil
}

func parseMonetaryStoreTime(value string) (time.Time, error) {
	parsed := parseTS(value)
	if parsed.IsZero() {
		return time.Time{}, errors.New("stored monetary timestamp is invalid")
	}
	return parsed, nil
}

// reconcileMonetaryReservationsTx closes open reservations whose model
// attempt already reached a durable terminal event. A settle that failed
// transiently, or a worker crash between reserve and settle, therefore
// self-heals on the next reserve or usage read instead of leaking capacity.
func reconcileMonetaryReservationsTx(ctx context.Context, tx *sql.Tx, runID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,scope,attempt_number,provider,model,reserved_micros
		FROM run_monetary_reservations WHERE run_id=? AND status='reserved'`, runID)
	if err != nil {
		return err
	}
	type openRow struct {
		id, scope, provider, model    string
		attemptNumber, reservedMicros int64
	}
	var open []openRow
	for rows.Next() {
		var r openRow
		if err := rows.Scan(&r.id, &r.scope, &r.attemptNumber, &r.provider, &r.model, &r.reservedMicros); err != nil {
			rows.Close()
			return err
		}
		open = append(open, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range open {
		evidence, err := monetaryModelEvidenceTx(ctx, tx, runID, r.id, r.scope, r.attemptNumber, r.provider, r.model)
		if err != nil {
			return err
		}
		if evidence.EventType == "" {
			continue
		}
		var payload struct {
			Usage         *llm.Usage `json:"usage"`
			UsageUnknown  bool       `json:"usage_unknown"`
			ToolCallCount int        `json:"tool_call_count"`
		}
		if err := json.Unmarshal([]byte(evidence.PayloadJSON), &payload); err != nil {
			return err
		}
		settle := !evidence.NotSent
		settledMicros := int64(0)
		pricingStatus := domain.MonetaryPricingUsageUnknown
		actualMicros := int64(math.MaxInt64)
		if settle {
			// Even an invalid response can contain billable known usage. An
			// absent/incomplete breakdown retains the reserved upper bound.
			settledMicros = r.reservedMicros
			if !monetaryTerminalUnknown(payload.Usage, payload.UsageUnknown) && payload.ToolCallCount >= 0 {
				entry, ok, err := modelReservationPriceTx(ctx, tx, runID, r.scope, r.attemptNumber)
				if err != nil {
					return err
				}
				if ok {
					cost, costErr := entry.UsageCost(pricing.TokenUsage{
						InputTokens: int64(payload.Usage.InputTokens), OutputTokens: int64(payload.Usage.OutputTokens),
						CacheReadTokens: int64(payload.Usage.CacheReadTokens), CacheCreationTokens: int64(payload.Usage.CacheCreationTokens),
						CacheCreation5mTokens: int64(payload.Usage.CacheCreation5mTokens), CacheCreation1hTokens: int64(payload.Usage.CacheCreation1hTokens),
						CacheCreationBreakdownKnown: payload.Usage.CacheCreationBreakdownKnown,
					}, int64(payload.ToolCallCount))
					if costErr == nil {
						actualMicros, pricingStatus = cost, domain.MonetaryPricingPriced
						settledMicros = cost
					} else {
						pricingStatus = domain.MonetaryPricingCachePriceMissing
					}
				} else {
					pricingStatus = domain.MonetaryPricingPriceMissing
				}
			}
		}
		overageMicros := max(0, settledMicros-r.reservedMicros)
		releasedMicros := max(0, r.reservedMicros-settledMicros)
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx, `UPDATE run_monetary_usage SET reserved_micros=reserved_micros+?,settled_micros=settled_micros+?,released_micros=released_micros+?,updated_at=? WHERE run_id=?`, overageMicros, settledMicros, releasedMicros, ts(now), runID); err != nil {
			return err
		}
		if settle {
			_, err = tx.ExecContext(ctx, `UPDATE run_monetary_reservations SET reserved_micros=reserved_micros+?,settled_micros=?,released_micros=?,status='settled',settled_at=? WHERE id=? AND status='reserved'`, overageMicros, settledMicros, releasedMicros, ts(now), r.id)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE run_monetary_reservations SET released_micros=?,status='released',released_at=? WHERE id=? AND status='reserved'`, releasedMicros, ts(now), r.id)
		}
		if err != nil {
			return err
		}
		if settle {
			var missionID string
			if err := tx.QueryRowContext(ctx, `SELECT mission_id FROM runs WHERE id=?`, runID).Scan(&missionID); err != nil {
				return err
			}
			event, err := events.New(runID, missionID, events.MonetaryBudgetSettledEvent, "monetary_budget", r.id,
				map[string]any{"scope": r.scope, "settled_micros": settledMicros, "released_micros": releasedMicros,
					"actual_micros": actualMicros, "under_reserved": overageMicros > 0,
					"pricing_status": pricingStatus, "reconciled": true})
			if err != nil {
				return err
			}
			if _, err := insertRunEventTx(ctx, tx, event); err != nil {
				return err
			}
		}
	}
	return nil
}

// activePriceEntryTx loads the currently active price entry for one
// provider/model pair, if the table has one.
func activePriceEntryTx(ctx context.Context, tx *sql.Tx, provider, model string) (pricing.Entry, bool, error) {
	var entriesJSON string
	err := tx.QueryRowContext(ctx, `SELECT entries_json FROM provider_price_snapshots
		WHERE active = 1`).Scan(&entriesJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return pricing.Entry{}, false, nil
	}
	if err != nil {
		return pricing.Entry{}, false, err
	}
	var entries []pricing.Entry
	if err := json.Unmarshal([]byte(entriesJSON), &entries); err != nil {
		return pricing.Entry{}, false, errors.New("stored price snapshot entries are malformed")
	}
	for _, entry := range entries {
		if entry.Provider == provider && entry.Model == model {
			return entry, true, nil
		}
	}
	return pricing.Entry{}, false, nil
}

func loadMonetaryRunTx(ctx context.Context, tx *sql.Tx, runID string) (
	domain.Budget, domain.RunStatus, string, error,
) {
	var budgetJSON string
	var status domain.RunStatus
	var missionID string
	if err := tx.QueryRowContext(ctx, `SELECT budget_json, status, mission_id FROM runs
		WHERE id = ?`, runID).Scan(&budgetJSON, &status, &missionID); err != nil {
		return domain.Budget{}, "", "", err
	}
	var budget domain.Budget
	if err := json.Unmarshal([]byte(budgetJSON), &budget); err != nil {
		return domain.Budget{}, "", "", fmt.Errorf("decode run budget: %w", err)
	}
	if err := budget.Validate(); err != nil {
		return domain.Budget{}, "", "", err
	}
	return budget, status, missionID, nil
}
