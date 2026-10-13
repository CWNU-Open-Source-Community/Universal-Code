package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runmutation"
)

const threadModelRoutePreferenceKeyPrefix = "thread.model-route.preference."
const threadModelRouteOperationKeyPrefix = "thread.model-route.operation."

func insertInitialThreadModelRoutePreferenceTx(ctx context.Context, tx *sql.Tx,
	run domain.Run, pin domain.InitialThreadModelRoutePin, requestedBy string,
) error {
	if pin.Empty() {
		return nil
	}
	if err := pin.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"initial Thread model route pin is invalid", err)
	}
	threadID := domain.InitialThreadID(run.ID)
	preference := domain.ThreadModelRoutePreference{
		ProtocolVersion: domain.ThreadModelRouteProtocolVersion,
		ThreadID:        threadID, Selected: true, Provider: pin.Provider, Model: pin.Model,
		UpdatedAt: run.CreatedAt.UTC(),
	}
	if err := preference.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"initial Thread model route preference is invalid", err)
	}
	if requestedBy != strings.TrimSpace(requestedBy) || !domain.ValidAgentID(requestedBy) {
		return apperror.New(apperror.CodeInvalidArgument,
			"initial Thread model route requester is invalid")
	}
	if err := validateSelectedCustomProviderTx(ctx, tx, domain.ThreadModelRouteMutation{
		ThreadID: threadID, Action: domain.ThreadModelRouteSelect,
		Provider: pin.Provider, Model: pin.Model, CustomProvider: pin.CustomProvider,
		ExpectedProviderDefinitionRevision: pin.ExpectedProviderDefinitionRevision,
	}); err != nil {
		return err
	}
	encodedPreference, err := json.Marshal(preference)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO provider_setting (key, value, updated_at)
		VALUES (?, ?, ?)`, threadModelRoutePreferenceKeyPrefix+threadID,
		string(encodedPreference), ts(preference.UpdatedAt)); err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"provider": preference.Provider, "model": preference.Model,
		"requested_by": requestedBy, "applies_to": "current_and_next",
		"active_run_unchanged": false, "initial_selection": true,
	})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO thread_events
		(thread_id, run_id, type, source, payload_json, created_at)
		VALUES (?, ?, 'thread.model_route_selected', 'run_creation', ?, ?)`,
		threadID, run.ID, string(payload), ts(preference.UpdatedAt))
	return err
}

type storedThreadModelRouteOperation struct {
	RequestFingerprint string                            `json:"request_fingerprint"`
	ThreadID           string                            `json:"thread_id"`
	Action             domain.ThreadModelRouteAction     `json:"action"`
	Result             domain.ThreadModelRoutePreference `json:"result"`
}

func (s *SQLiteStore) GetThreadModelRoutePreference(ctx context.Context,
	threadID string,
) (domain.ThreadModelRoutePreference, bool, error) {
	threadID = strings.TrimSpace(threadID)
	if !domain.ValidAgentID(threadID) {
		return domain.ThreadModelRoutePreference{}, false, apperror.New(
			apperror.CodeInvalidArgument, "Thread model route Thread id is invalid")
	}
	value, found, err := s.GetProviderSetting(ctx,
		threadModelRoutePreferenceKeyPrefix+threadID)
	if err != nil || !found {
		return domain.ThreadModelRoutePreference{}, found, err
	}
	var preference domain.ThreadModelRoutePreference
	if err := json.Unmarshal([]byte(value), &preference); err != nil {
		return domain.ThreadModelRoutePreference{}, false, err
	}
	if preference.ThreadID != threadID || preference.Validate() != nil {
		return domain.ThreadModelRoutePreference{}, false,
			errors.New("stored Thread model route preference is invalid")
	}
	return preference, true, nil
}

// ListSelectedThreadModelRoutePreferences returns dependencies that still need
// their provider, including archived Threads that may be restored.
func (s *SQLiteStore) ListSelectedThreadModelRoutePreferences(ctx context.Context,
	provider string,
) ([]domain.ThreadModelRoutePreference, error) {
	provider = strings.TrimSpace(provider)
	if !domain.ValidAgentID(provider) {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"Thread model route Provider id is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT value FROM provider_setting
		WHERE key LIKE ? ESCAPE '\' AND json_valid(value)
			AND json_extract(value, '$.selected') = 1
			AND json_extract(value, '$.provider') = ?
			AND NOT EXISTS (SELECT 1 FROM threads
				WHERE provider_setting.key = ? || threads.id AND threads.status = ?)
		ORDER BY key`, threadModelRoutePreferenceKeyPrefix+"%", provider,
		threadModelRoutePreferenceKeyPrefix, domain.ThreadDeleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	preferences := make([]domain.ThreadModelRoutePreference, 0)
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return nil, err
		}
		var preference domain.ThreadModelRoutePreference
		if json.Unmarshal([]byte(encoded), &preference) != nil ||
			preference.Validate() != nil || !preference.Selected ||
			preference.Provider != provider {
			return nil, errors.New("stored selected Thread model route preference is invalid")
		}
		preferences = append(preferences, preference)
	}
	return preferences, rows.Err()
}

// ChangeThreadModelRoutePreference commits the Thread preference, its audit
// event, and the idempotency receipt in one SQLite transaction. It never
// updates runs or sessions; a running Run therefore remains immutable.
func (s *SQLiteStore) ChangeThreadModelRoutePreference(ctx context.Context,
	mutation domain.ThreadModelRouteMutation,
) (domain.ThreadModelRouteMutationResult, error) {
	if mutation.Version != domain.ThreadModelRouteControlProtocolVersion ||
		!domain.ValidAgentID(mutation.ThreadID) || !domain.ValidAgentID(mutation.RequestedBy) ||
		mutation.OperationKey == "" || mutation.OperationKey != strings.TrimSpace(mutation.OperationKey) ||
		mutation.RequestFingerprint == "" || mutation.At.IsZero() ||
		(mutation.Action != domain.ThreadModelRouteSelect &&
			mutation.Action != domain.ThreadModelRouteReset) ||
		(mutation.Action == domain.ThreadModelRouteSelect && mutation.CustomProvider &&
			mutation.ExpectedProviderDefinitionRevision == 0) ||
		(mutation.Action == domain.ThreadModelRouteSelect && !mutation.CustomProvider &&
			mutation.ExpectedProviderDefinitionRevision != 0) ||
		(mutation.Action == domain.ThreadModelRouteReset &&
			(mutation.CustomProvider || mutation.ExpectedProviderDefinitionRevision != 0)) {
		return domain.ThreadModelRouteMutationResult{}, apperror.New(
			apperror.CodeInvalidArgument, "Thread model route mutation is invalid")
	}
	preference := domain.ThreadModelRoutePreference{
		ProtocolVersion: domain.ThreadModelRouteProtocolVersion,
		ThreadID:        mutation.ThreadID, Selected: mutation.Action == domain.ThreadModelRouteSelect,
		Provider: mutation.Provider, Model: mutation.Model, UpdatedAt: mutation.At.UTC(),
	}
	if err := preference.Validate(); err != nil {
		return domain.ThreadModelRouteMutationResult{}, apperror.Wrap(
			apperror.CodeInvalidArgument, err.Error(), err)
	}
	operationDigest := runmutation.Fingerprint(
		"thread_model_route_operation.v1", mutation.OperationKey)
	operationKey := threadModelRouteOperationKeyPrefix + operationDigest
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.ThreadModelRouteMutationResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var encodedOperation string
	err = tx.QueryRowContext(ctx, `SELECT value FROM provider_setting WHERE key = ?`,
		operationKey).Scan(&encodedOperation)
	if err == nil {
		var stored storedThreadModelRouteOperation
		if json.Unmarshal([]byte(encodedOperation), &stored) != nil ||
			stored.RequestFingerprint != mutation.RequestFingerprint ||
			stored.ThreadID != mutation.ThreadID || stored.Action != mutation.Action ||
			stored.Result.Validate() != nil {
			return domain.ThreadModelRouteMutationResult{}, apperror.New(
				apperror.CodeConflict,
				"Thread model route idempotency key was reused for another request")
		}
		if err := tx.Commit(); err != nil {
			return domain.ThreadModelRouteMutationResult{}, err
		}
		return domain.ThreadModelRouteMutationResult{Preference: stored.Result,
			Replayed: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.ThreadModelRouteMutationResult{}, err
	}
	threadRecord, err := scanThread(tx.QueryRowContext(ctx, threadSelect+` WHERE id = ?`,
		mutation.ThreadID))
	if err != nil {
		return domain.ThreadModelRouteMutationResult{}, err
	}
	if threadRecord.Status != domain.ThreadActive {
		return domain.ThreadModelRouteMutationResult{}, apperror.New(
			apperror.CodeFailedPrecondition, "only an active Thread can change its model route")
	}
	if preference.Selected {
		if err := validateSelectedCustomProviderTx(ctx, tx, mutation); err != nil {
			return domain.ThreadModelRouteMutationResult{}, err
		}
	}
	encodedPreference, err := json.Marshal(preference)
	if err != nil {
		return domain.ThreadModelRouteMutationResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO provider_setting (key, value, updated_at)
		VALUES (?, ?, ?) ON CONFLICT(key) DO UPDATE SET
		value=excluded.value, updated_at=excluded.updated_at`,
		threadModelRoutePreferenceKeyPrefix+mutation.ThreadID,
		string(encodedPreference), ts(preference.UpdatedAt)); err != nil {
		return domain.ThreadModelRouteMutationResult{}, err
	}
	eventType := "thread.model_route_selected"
	if !preference.Selected {
		eventType = "thread.model_route_reset"
	}
	payload, _ := json.Marshal(map[string]any{
		"provider": preference.Provider, "model": preference.Model,
		"requested_by": mutation.RequestedBy, "applies_to": "next_run",
		"active_run_unchanged": true,
	})
	if _, err := tx.ExecContext(ctx, `INSERT INTO thread_events
		(thread_id, run_id, type, source, payload_json, created_at)
		VALUES (?, ?, ?, 'thread_model_route', ?, ?)`, mutation.ThreadID,
		nullableString(threadRecord.ActiveRunID), eventType, string(payload),
		ts(preference.UpdatedAt)); err != nil {
		return domain.ThreadModelRouteMutationResult{}, err
	}
	operation := storedThreadModelRouteOperation{RequestFingerprint: mutation.RequestFingerprint,
		ThreadID: mutation.ThreadID, Action: mutation.Action, Result: preference}
	encodedOperationBytes, err := json.Marshal(operation)
	if err != nil {
		return domain.ThreadModelRouteMutationResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO provider_setting (key, value, updated_at)
		VALUES (?, ?, ?)`, operationKey, string(encodedOperationBytes),
		ts(preference.UpdatedAt)); err != nil {
		return domain.ThreadModelRouteMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ThreadModelRouteMutationResult{}, err
	}
	return domain.ThreadModelRouteMutationResult{Preference: preference}, nil
}
