package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/modelregistry"
)

// CompareAndSwapProviderDefinitionCollection is the storage-level half of the
// custom Provider definition CAS. It shares SQLite's immediate write
// transaction boundary with ChangeThreadModelRoutePreference, so a definition
// cannot be deleted, disabled, or lose a model after the service checks Thread
// preferences but before the collection is committed. Permanently deleted
// Threads no longer need their selected provider; archived Threads still do.
func (s *SQLiteStore) CompareAndSwapProviderDefinitionCollection(ctx context.Context,
	expectedRevision uint64, next modelregistry.ProviderDefinitionCollection,
) error {
	if s == nil || s.db == nil || ctx == nil {
		return apperror.New(apperror.CodeInvalidArgument,
			"custom Provider definition collection dependencies are required")
	}
	if next.Revision == 0 || next.Revision != expectedRevision+1 {
		return apperror.New(apperror.CodeInvalidArgument,
			"custom Provider definition collection revision transition is invalid")
	}
	encoded, err := modelregistry.EncodeProviderDefinitionCollection(next)
	if err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"custom Provider definition collection is invalid", err)
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	current, err := readProviderDefinitionCollectionTx(ctx, tx)
	if err != nil {
		return err
	}
	if current.Revision != expectedRevision {
		return apperror.New(apperror.CodeConflict,
			"custom Provider definition collection revision is stale")
	}
	if err := validateSelectedThreadRoutesForDefinitionCollectionTx(
		ctx, tx, current, next); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO provider_setting (key, value, updated_at)
		VALUES (?, ?, ?) ON CONFLICT(key) DO UPDATE SET
		value=excluded.value, updated_at=excluded.updated_at`,
		modelregistry.ProviderDefinitionsSettingKey, encoded, ts(time.Now().UTC())); err != nil {
		return err
	}
	return tx.Commit()
}

func readProviderDefinitionCollectionTx(ctx context.Context, tx *sql.Tx) (
	modelregistry.ProviderDefinitionCollection, error,
) {
	var encoded string
	err := tx.QueryRowContext(ctx, `SELECT value FROM provider_setting WHERE key = ?`,
		modelregistry.ProviderDefinitionsSettingKey).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return modelregistry.EmptyProviderDefinitionCollection(), nil
	}
	if err != nil {
		return modelregistry.ProviderDefinitionCollection{}, err
	}
	collection, err := modelregistry.DecodeProviderDefinitionCollection(encoded)
	if err != nil {
		return modelregistry.ProviderDefinitionCollection{}, apperror.Wrap(
			apperror.CodeFailedPrecondition,
			"durable custom Provider definitions are invalid", err)
	}
	return collection, nil
}

func validateSelectedThreadRoutesForDefinitionCollectionTx(ctx context.Context,
	tx *sql.Tx, current, next modelregistry.ProviderDefinitionCollection,
) error {
	customIDs := make(map[string]struct{}, len(current.Providers)+len(next.Providers))
	nextByID := make(map[string]modelregistry.ProviderDefinition, len(next.Providers))
	for _, definition := range current.Providers {
		customIDs[definition.ID] = struct{}{}
	}
	for _, definition := range next.Providers {
		customIDs[definition.ID] = struct{}{}
		nextByID[definition.ID] = definition
	}

	rows, err := tx.QueryContext(ctx, `SELECT value FROM provider_setting
		WHERE key LIKE ? ESCAPE '\'
			AND NOT EXISTS (SELECT 1 FROM threads
				WHERE provider_setting.key = ? || threads.id AND threads.status = ?)
		ORDER BY key`, threadModelRoutePreferenceKeyPrefix+"%",
		threadModelRoutePreferenceKeyPrefix, domain.ThreadDeleted)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return err
		}
		var preference domain.ThreadModelRoutePreference
		if json.Unmarshal([]byte(encoded), &preference) != nil || preference.Validate() != nil {
			return apperror.New(apperror.CodeFailedPrecondition,
				"stored Thread model route preference is invalid")
		}
		if !preference.Selected {
			continue
		}
		if _, custom := customIDs[preference.Provider]; !custom {
			continue
		}
		definition, retained := nextByID[preference.Provider]
		if !retained || !definition.Enabled ||
			!providerDefinitionContainsModel(definition, preference.Model) {
			return apperror.New(apperror.CodeFailedPrecondition,
				"custom Provider definition mutation would invalidate a selected Thread model route")
		}
	}
	return rows.Err()
}

func validateSelectedCustomProviderTx(ctx context.Context, tx *sql.Tx,
	mutation domain.ThreadModelRouteMutation,
) error {
	collection, err := readProviderDefinitionCollectionTx(ctx, tx)
	if err != nil {
		return err
	}
	var definition modelregistry.ProviderDefinition
	found := false
	for _, current := range collection.Providers {
		if current.ID == mutation.Provider {
			definition = current
			found = true
			break
		}
	}
	if !mutation.CustomProvider {
		if found {
			return apperror.New(apperror.CodeConflict,
				"custom Provider definition changed concurrently")
		}
		return nil
	}
	if !found || definition.Revision != mutation.ExpectedProviderDefinitionRevision {
		return apperror.New(apperror.CodeConflict,
			"custom Provider definition changed concurrently")
	}
	if !definition.Enabled || !providerDefinitionContainsModel(definition, mutation.Model) {
		return apperror.New(apperror.CodeFailedPrecondition,
			"selected custom Provider model route is no longer available")
	}
	return nil
}

func providerDefinitionContainsModel(definition modelregistry.ProviderDefinition,
	model string,
) bool {
	model = strings.TrimSpace(model)
	for _, current := range definition.Models {
		if current == model {
			return true
		}
	}
	return false
}
