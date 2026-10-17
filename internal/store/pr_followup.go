package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
)

const prFollowupLeaseTTL = 30 * time.Second

type prFollowupInternal struct {
	Observed       map[string]string      `json:"observed"`
	Initialized    bool                   `json:"initialized"`
	Pending        *domain.PRFollowupTurn `json:"pending,omitempty"`
	ReviewedTurnID string                 `json:"reviewed_turn_id,omitempty"`
}

type prFollowupQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// CreatePRFollowup returns created=false for the same ID and request fingerprint.
// The caller derives the stable ID from the user's operation key.
func (s *SQLiteStore) CreatePRFollowup(ctx context.Context, state domain.PRFollowupState, requestFingerprint string) (domain.PRFollowupState, bool, error) {
	if strings.TrimSpace(requestFingerprint) == "" || strings.TrimSpace(state.Record.ID) == "" {
		return domain.PRFollowupState{}, false, apperror.New(apperror.CodeInvalidArgument, "PR followup id and request fingerprint are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.PRFollowupState{}, false, err
	}
	defer tx.Rollback()
	current, fingerprint, err := getPRFollowup(ctx, tx, state.Record.ID)
	if err == nil {
		if fingerprint != requestFingerprint {
			return domain.PRFollowupState{}, false, prFollowupConflict("PR followup operation key was used for a different request")
		}
		return current, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.PRFollowupState{}, false, err
	}
	if state.Record.CreatedAt.IsZero() {
		state.Record.CreatedAt = time.Now().UTC()
	}
	state.Record.UpdatedAt = state.Record.CreatedAt
	state.Record.Revision = 1
	if state.Record.Status == "" {
		state.Record.Status = domain.PRFollowupActive
	}
	if state.Record.NextCheckAt.IsZero() {
		state.Record.NextCheckAt = state.Record.CreatedAt
	}
	if err = state.Record.Validate(); err != nil {
		return domain.PRFollowupState{}, false, apperror.Wrap(apperror.CodeInvalidArgument, err.Error(), err)
	}
	if state.Record.Status != domain.PRFollowupActive || state.Record.TurnsStarted != 0 || state.Pending != nil {
		return domain.PRFollowupState{}, false, apperror.New(apperror.CodeInvalidArgument, "new PR followup must be active with no started turn")
	}
	var open int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pr_followups WHERE thread_id=? AND status IN ('active','paused','blocked','ready')`, state.Record.ThreadID).Scan(&open); err != nil {
		return domain.PRFollowupState{}, false, err
	}
	if open != 0 {
		return domain.PRFollowupState{}, false, prFollowupConflict("Thread already has an unfinished PR followup")
	}
	state.LeaseOwner, state.LeaseToken, state.LeaseUntil = "", "", time.Time{}
	recordJSON, internalJSON, err := marshalPRFollowup(state)
	if err != nil {
		return domain.PRFollowupState{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO pr_followups
		(id,thread_id,request_fingerprint,status,revision,next_check_at,deadline_at,record_json,state_json)
		VALUES (?,?,?,?,?,?,?,?,?)`, state.Record.ID, state.Record.ThreadID, requestFingerprint,
		state.Record.Status, state.Record.Revision, state.Record.NextCheckAt.UnixNano(), state.Record.DeadlineAt.UnixNano(), recordJSON, internalJSON)
	if err != nil {
		return domain.PRFollowupState{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return domain.PRFollowupState{}, false, err
	}
	return state, true, nil
}

func (s *SQLiteStore) GetPRFollowup(ctx context.Context, id string) (domain.PRFollowupState, error) {
	state, _, err := getPRFollowup(ctx, s.db, id)
	if errors.Is(err, sql.ErrNoRows) {
		return state, apperror.New(apperror.CodeNotFound, "PR followup was not found")
	}
	return state, err
}

func (s *SQLiteStore) ListPRFollowups(ctx context.Context, threadID string, limit int) ([]domain.PRFollowup, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT record_json FROM pr_followups`
	args := []any{}
	if threadID != "" {
		query += ` WHERE thread_id=?`
		args = append(args, threadID)
	}
	query += ` ORDER BY rowid DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []domain.PRFollowup{}
	for rows.Next() {
		var payload string
		if err = rows.Scan(&payload); err != nil {
			return nil, err
		}
		var record domain.PRFollowup
		if err = json.Unmarshal([]byte(payload), &record); err != nil {
			return nil, err
		}
		values = append(values, record)
	}
	return values, rows.Err()
}

// ClaimDuePRFollowup also claims expired subscriptions so the worker can record
// their exhausted status. Claiming and renewing never change the public revision.
func (s *SQLiteStore) ClaimDuePRFollowup(ctx context.Context, owner string, now time.Time) (domain.PRFollowupState, bool, error) {
	if strings.TrimSpace(owner) == "" || now.IsZero() {
		return domain.PRFollowupState{}, false, apperror.New(apperror.CodeInvalidArgument, "PR followup lease owner and time are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.PRFollowupState{}, false, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM pr_followups WHERE status='active' AND next_check_at<=? AND lease_until<=? ORDER BY next_check_at,id LIMIT 1`, now.UnixNano(), now.UnixNano()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.PRFollowupState{}, false, nil
	}
	if err != nil {
		return domain.PRFollowupState{}, false, err
	}
	state, _, err := getPRFollowup(ctx, tx, id)
	if err != nil {
		return domain.PRFollowupState{}, false, err
	}
	state.LeaseOwner, state.LeaseToken, state.LeaseUntil = owner, idgen.New("pr-followup-lease"), now.UTC().Add(prFollowupLeaseTTL)
	result, err := tx.ExecContext(ctx, `UPDATE pr_followups SET lease_owner=?,lease_token=?,lease_until=? WHERE id=? AND status='active' AND revision=? AND lease_until<=?`,
		state.LeaseOwner, state.LeaseToken, state.LeaseUntil.UnixNano(), id, state.Record.Revision, now.UnixNano())
	if err != nil {
		return domain.PRFollowupState{}, false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.PRFollowupState{}, false, err
	}
	if changed != 1 {
		return domain.PRFollowupState{}, false, prFollowupConflict("PR followup claim changed concurrently")
	}
	if err = tx.Commit(); err != nil {
		return domain.PRFollowupState{}, false, err
	}
	return state, true, nil
}

// SavePRFollowup uses an empty owner/token for a user control operation. Workers
// must carry their claimed credentials. Lease times are read from the database,
// so an intervening renewal cannot be overwritten by an older worker snapshot.
func (s *SQLiteStore) SavePRFollowup(ctx context.Context, state domain.PRFollowupState, expectedRevision int64) (domain.PRFollowupState, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.PRFollowupState{}, err
	}
	defer tx.Rollback()
	current, _, err := getPRFollowup(ctx, tx, state.Record.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.PRFollowupState{}, apperror.New(apperror.CodeNotFound, "PR followup was not found")
	}
	if err != nil {
		return domain.PRFollowupState{}, err
	}
	if current.Record.Revision != expectedRevision {
		return domain.PRFollowupState{}, prFollowupConflict("PR followup revision changed concurrently")
	}
	now := time.Now().UTC()
	if state.LeaseOwner != "" || state.LeaseToken != "" {
		if state.LeaseOwner == "" || state.LeaseToken == "" || current.Record.Status != domain.PRFollowupActive ||
			state.LeaseOwner != current.LeaseOwner || state.LeaseToken != current.LeaseToken || !current.LeaseUntil.After(now) {
			return domain.PRFollowupState{}, prFollowupConflict("PR followup worker lease was lost or expired")
		}
	}
	if err = validatePRFollowupUpdate(current, state); err != nil {
		return domain.PRFollowupState{}, err
	}
	state.Record.Revision, state.Record.UpdatedAt = expectedRevision+1, now
	if err = state.Record.Validate(); err != nil {
		return domain.PRFollowupState{}, apperror.Wrap(apperror.CodeInvalidArgument, err.Error(), err)
	}
	state.LeaseOwner, state.LeaseToken, state.LeaseUntil = current.LeaseOwner, current.LeaseToken, current.LeaseUntil
	leaseUntil := int64(0)
	if state.Record.Status == domain.PRFollowupActive && !state.LeaseUntil.IsZero() {
		leaseUntil = state.LeaseUntil.UnixNano()
	} else {
		state.LeaseOwner, state.LeaseToken, state.LeaseUntil = "", "", time.Time{}
	}
	recordJSON, internalJSON, err := marshalPRFollowup(state)
	if err != nil {
		return domain.PRFollowupState{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE pr_followups SET status=?,revision=?,next_check_at=?,record_json=?,state_json=?,lease_owner=?,lease_token=?,lease_until=? WHERE id=? AND revision=?`,
		state.Record.Status, state.Record.Revision, state.Record.NextCheckAt.UnixNano(), recordJSON, internalJSON,
		state.LeaseOwner, state.LeaseToken, leaseUntil, state.Record.ID, expectedRevision)
	if err != nil {
		return domain.PRFollowupState{}, apperror.Normalize(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.PRFollowupState{}, err
	}
	if changed != 1 {
		return domain.PRFollowupState{}, prFollowupConflict("PR followup update changed concurrently")
	}
	if err = tx.Commit(); err != nil {
		return domain.PRFollowupState{}, err
	}
	return state, nil
}

func (s *SQLiteStore) RenewPRFollowupLease(ctx context.Context, id, owner, token string, now time.Time) (bool, error) {
	if id == "" || owner == "" || token == "" || now.IsZero() {
		return false, apperror.New(apperror.CodeInvalidArgument, "PR followup lease identity and time are required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE pr_followups SET lease_until=MAX(lease_until,?) WHERE id=? AND status='active' AND lease_owner=? AND lease_token=? AND lease_until>? AND deadline_at>?`,
		now.Add(prFollowupLeaseTTL).UnixNano(), id, owner, token, now.UnixNano(), now.UnixNano())
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}

func (s *SQLiteStore) ReleasePRFollowupLease(ctx context.Context, id, owner, token string) error {
	if id == "" || owner == "" || token == "" {
		return apperror.New(apperror.CodeInvalidArgument, "PR followup lease identity is required")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE pr_followups SET lease_owner='',lease_token='',lease_until=0 WHERE id=? AND lease_owner=? AND lease_token=?`, id, owner, token)
	return err
}

func getPRFollowup(ctx context.Context, query prFollowupQuerier, id string) (domain.PRFollowupState, string, error) {
	var state domain.PRFollowupState
	var recordJSON, internalJSON, fingerprint string
	var leaseUntil int64
	if err := query.QueryRowContext(ctx, `SELECT record_json,state_json,request_fingerprint,lease_owner,lease_token,lease_until FROM pr_followups WHERE id=?`, id).
		Scan(&recordJSON, &internalJSON, &fingerprint, &state.LeaseOwner, &state.LeaseToken, &leaseUntil); err != nil {
		return state, "", err
	}
	if err := json.Unmarshal([]byte(recordJSON), &state.Record); err != nil {
		return state, "", err
	}
	var internal prFollowupInternal
	if err := json.Unmarshal([]byte(internalJSON), &internal); err != nil {
		return state, "", err
	}
	state.Observed, state.Initialized, state.Pending, state.ReviewedTurnID = internal.Observed, internal.Initialized, internal.Pending, internal.ReviewedTurnID
	if leaseUntil != 0 {
		state.LeaseUntil = time.Unix(0, leaseUntil).UTC()
	}
	return state, fingerprint, nil
}

func marshalPRFollowup(state domain.PRFollowupState) (string, string, error) {
	recordJSON, err := json.Marshal(state.Record)
	if err != nil {
		return "", "", err
	}
	internalJSON, err := json.Marshal(prFollowupInternal{Observed: state.Observed, Initialized: state.Initialized, Pending: state.Pending, ReviewedTurnID: state.ReviewedTurnID})
	return string(recordJSON), string(internalJSON), err
}

func validatePRFollowupUpdate(old, next domain.PRFollowupState) error {
	immutable := func(p domain.PRFollowup) domain.PRFollowup {
		p.CreatedAt, p.DeadlineAt = p.CreatedAt.UTC(), p.DeadlineAt.UTC()
		p.Status, p.Revision, p.TurnsStarted = "", 0, 0
		p.NextCheckAt, p.UpdatedAt, p.LastCheckedAt = time.Time{}, time.Time{}, nil
		p.LastHeadSHA, p.LastResult, p.LastError = "", "", ""
		return p
	}
	if !reflect.DeepEqual(immutable(old.Record), immutable(next.Record)) || (old.Record.Terminal() && next.Record.Status != old.Record.Status) {
		return prFollowupConflict("PR followup identity or terminal status cannot change")
	}
	if old.Initialized && !next.Initialized {
		return prFollowupConflict("PR followup initialization cannot be cleared")
	}
	for key := range old.Observed {
		if _, present := next.Observed[key]; !present {
			return prFollowupConflict("PR followup observations cannot be removed")
		}
	}
	newTurn := next.Pending != nil && (old.Pending == nil || old.Pending.ID != next.Pending.ID)
	if old.Pending != nil {
		if next.Pending == nil {
			return prFollowupConflict("PR followup pending turn cannot be removed")
		}
		if newTurn && old.Pending.State != domain.PRFollowupTurnCompleted && old.Pending.State != domain.PRFollowupTurnFailed {
			return prFollowupConflict("PR followup pending turn has not finished")
		}
		if !newTurn && (next.Pending.OperationKey != old.Pending.OperationKey || next.Pending.Content != old.Pending.Content ||
			!next.Pending.CreatedAt.Equal(old.Pending.CreatedAt) ||
			(old.Pending.RunID != "" && next.Pending.RunID != old.Pending.RunID) ||
			(old.Pending.MessageID != "" && next.Pending.MessageID != old.Pending.MessageID)) {
			return prFollowupConflict("PR followup pending submission cannot change")
		}
	}
	turns := old.Record.TurnsStarted
	if newTurn {
		turns++
	}
	if next.Record.TurnsStarted != turns {
		return prFollowupConflict("PR followup turn count must increase only for a new pending turn")
	}
	if next.Pending != nil {
		p := next.Pending
		if p.ID == "" || p.OperationKey == "" || strings.TrimSpace(p.Content) == "" || p.CreatedAt.IsZero() || p.UpdatedAt.Before(p.CreatedAt) {
			return apperror.New(apperror.CodeInvalidArgument, "PR followup pending turn is invalid")
		}
		switch p.State {
		case domain.PRFollowupTurnQueued, domain.PRFollowupTurnRunning, domain.PRFollowupTurnWaitingApproval,
			domain.PRFollowupTurnCompleted, domain.PRFollowupTurnFailed, domain.PRFollowupTurnUnknown:
		default:
			return apperror.New(apperror.CodeInvalidArgument, "PR followup pending turn state is invalid")
		}
	}
	return nil
}

func prFollowupConflict(message string) error {
	return apperror.New(apperror.CodeConflict, message)
}
