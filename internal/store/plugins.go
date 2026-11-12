package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/plugins"
)

const pluginInstallationColumns = `protocol_version, id, manifest_json, source_json,
	archive_sha256, package_fingerprint, archive_bytes, signature_present, signature_valid,
	publisher_fingerprint, publisher_public_key, state, enabled_capabilities_json,
	generation, supersedes_installation_id, staged_by, reviewed_by, reviewed_at,
	created_at, updated_at`

func (s *SQLiteStore) CreatePluginInstallation(ctx context.Context,
	installation plugins.Installation, archive []byte,
) (plugins.Installation, bool, error) {
	if err := installation.Validate(); err != nil || installation.State != plugins.StateStaged ||
		installation.Generation != 1 || len(archive) != installation.ArchiveBytes ||
		pluginStoreDigest(archive) != installation.ArchiveSHA256 {
		return plugins.Installation{}, false, apperror.New(apperror.CodeInvalidArgument,
			"plugin staging record or archive is invalid")
	}
	manifestJSON, _ := json.Marshal(installation.Manifest)
	if installation.Snapshot != nil {
		manifestJSON, _ = json.Marshal(installation.Snapshot)
	}
	sourceJSON, _ := json.Marshal(installation.Source)
	capabilitiesJSON, _ := json.Marshal(installation.EnabledCapabilities)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return plugins.Installation{}, false, err
	}
	defer tx.Rollback()
	if installation.ProtocolVersion == plugins.PortableInstallationProtocol {
		prior, err := getPluginInstallation(ctx, tx, installation.ID)
		if err == nil {
			if prior.ProtocolVersion != installation.ProtocolVersion || prior.PackageFingerprint != installation.PackageFingerprint ||
				prior.Source != installation.Source || prior.StagedBy != installation.StagedBy || prior.SupersedesInstallationID != installation.SupersedesInstallationID {
				return plugins.Installation{}, false, apperror.New(apperror.CodeConflict, "portable import operation key is already bound to different input")
			}
			return prior, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return plugins.Installation{}, false, err
		}
	}
	var existingID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM plugin_installations
		WHERE package_fingerprint = ?`, installation.PackageFingerprint).Scan(&existingID)
	if err == nil {
		existing, loadErr := getPluginInstallation(ctx, tx, existingID)
		if loadErr == nil && (existing.Source.Surface != installation.Source.Surface ||
			(installation.ProtocolVersion == plugins.PortableInstallationProtocol && existing.ID != installation.ID)) {
			return plugins.Installation{}, false, apperror.New(apperror.CodeConflict, "the acquired plugin already belongs to another installation operation")
		}
		return existing, true, loadErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return plugins.Installation{}, false, err
	}
	// The existing object store has one immutable provenance binding per archive.
	// Do not alias identical bytes from a new source or silently rewrite its owner.
	var retainedFingerprint string
	err = tx.QueryRowContext(ctx, `SELECT package_fingerprint FROM plugin_objects WHERE archive_sha256 = ?`, installation.ArchiveSHA256).Scan(&retainedFingerprint)
	if err == nil {
		return plugins.Installation{}, false, apperror.New(apperror.CodeConflict, "the acquired archive already has another retained provenance binding")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return plugins.Installation{}, false, err
	}
	if installation.SupersedesInstallationID != "" {
		var pluginID, version string
		if err := tx.QueryRowContext(ctx, `SELECT plugin_id, plugin_version
			FROM plugin_installations WHERE id = ?`, installation.SupersedesInstallationID).
			Scan(&pluginID, &version); err != nil {
			return plugins.Installation{}, false, err
		}
		if pluginID != installation.PackageID() || version == installation.Revision() {
			return plugins.Installation{}, false, apperror.New(apperror.CodeConflict,
				"plugin predecessor binding is invalid")
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_objects
		(package_fingerprint, archive_sha256, archive_bytes, archive, created_at)
		VALUES (?, ?, ?, ?, ?)`, installation.PackageFingerprint, installation.ArchiveSHA256,
		installation.ArchiveBytes, archive, ts(installation.CreatedAt)); err != nil {
		return plugins.Installation{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO plugin_installations
		(id, protocol_version, plugin_id, plugin_version, publisher, manifest_json,
		source_json, archive_sha256, package_fingerprint, archive_bytes,
		signature_present, signature_valid, publisher_fingerprint, publisher_public_key,
		state, enabled_capabilities_json, generation, supersedes_installation_id, staged_by,
		reviewed_by, reviewed_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', NULL, ?, ?)`,
		installation.ID, installation.ProtocolVersion, installation.PackageID(),
		installation.Revision(), installation.Manifest.Publisher, string(manifestJSON),
		string(sourceJSON), installation.ArchiveSHA256, installation.PackageFingerprint,
		installation.ArchiveBytes, boolInt(installation.SignaturePresent),
		boolInt(installation.SignatureValid), installation.PublisherFingerprint,
		installation.PublisherPublicKey, installation.State, string(capabilitiesJSON),
		installation.Generation, installation.SupersedesInstallationID, installation.StagedBy,
		ts(installation.CreatedAt), ts(installation.UpdatedAt))
	if err != nil {
		return plugins.Installation{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return plugins.Installation{}, false, err
	}
	return installation, false, nil
}

func (s *SQLiteStore) GetPluginInstallation(ctx context.Context, id string) (
	plugins.Installation, error,
) {
	value, err := getPluginInstallation(ctx, s.db, strings.TrimSpace(id))
	if errors.Is(err, sql.ErrNoRows) {
		return plugins.Installation{}, apperror.New(apperror.CodeNotFound,
			"plugin installation not found")
	}
	return value, err
}

func (s *SQLiteStore) ListPluginInstallations(ctx context.Context, pluginID string,
	limit int,
) ([]plugins.Installation, error) {
	if limit < 1 || limit > 1_000 {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"plugin installation list limit is invalid")
	}
	query := `SELECT ` + pluginInstallationColumns + ` FROM plugin_installations`
	args := []any{}
	if strings.TrimSpace(pluginID) != "" {
		query += ` WHERE plugin_id = ?`
		args = append(args, strings.TrimSpace(pluginID))
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]plugins.Installation, 0, limit)
	for rows.Next() {
		value, err := scanPluginInstallation(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *SQLiteStore) UpdatePluginInstallation(ctx context.Context,
	installation plugins.Installation, expectedGeneration int64,
) (plugins.Installation, error) {
	if err := installation.Validate(); err != nil || expectedGeneration < 1 ||
		installation.Generation != expectedGeneration+1 {
		return plugins.Installation{}, apperror.New(apperror.CodeInvalidArgument,
			"plugin installation update is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return plugins.Installation{}, err
	}
	defer tx.Rollback()
	before, err := getPluginInstallation(ctx, tx, installation.ID)
	if err != nil {
		return plugins.Installation{}, err
	}
	if before.Generation != expectedGeneration ||
		before.PackageFingerprint != installation.PackageFingerprint {
		return plugins.Installation{}, apperror.New(apperror.CodeConflict,
			"plugin installation changed concurrently")
	}
	if err := updatePluginInstallationTx(ctx, tx, installation, expectedGeneration); err != nil {
		return plugins.Installation{}, err
	}
	if err := insertPluginTransitionTx(ctx, tx, before, installation); err != nil {
		return plugins.Installation{}, err
	}
	if err := tx.Commit(); err != nil {
		return plugins.Installation{}, err
	}
	return installation, nil
}

func (s *SQLiteStore) RollbackPluginInstallation(ctx context.Context,
	current plugins.Installation, currentExpected int64,
	target plugins.Installation, targetExpected int64, publisherAuthority plugins.PublisherAuthority,
) (plugins.Installation, plugins.Installation, error) {
	if current.Validate() != nil || target.Validate() != nil || current.ID == target.ID ||
		current.PackageID() != target.PackageID() || current.ProtocolVersion != target.ProtocolVersion || current.Source.Surface != target.Source.Surface || current.Generation != currentExpected+1 ||
		target.Generation != targetExpected+1 || current.State != plugins.StateRolledBack ||
		target.State != plugins.StateEnabled || publisherAuthority.ExpectedGeneration < 0 {
		return plugins.Installation{}, plugins.Installation{},
			apperror.New(apperror.CodeInvalidArgument, "plugin rollback update is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return plugins.Installation{}, plugins.Installation{}, err
	}
	defer tx.Rollback()
	currentBefore, err := getPluginInstallation(ctx, tx, current.ID)
	if err != nil {
		return plugins.Installation{}, plugins.Installation{}, err
	}
	targetBefore, err := getPluginInstallation(ctx, tx, target.ID)
	if err != nil {
		return plugins.Installation{}, plugins.Installation{}, err
	}
	if currentBefore.Generation != currentExpected || targetBefore.Generation != targetExpected {
		return plugins.Installation{}, plugins.Installation{},
			apperror.New(apperror.CodeConflict, "plugin rollback changed concurrently")
	}
	// The retained target may have been excluded from a concurrent publisher
	// revocation because it was rolled back. Its installation generation alone
	// cannot fence that change. Read trust in this same write transaction.
	if err := validateRollbackPublisherTx(ctx, tx, targetBefore, publisherAuthority); err != nil {
		return plugins.Installation{}, plugins.Installation{}, err
	}
	if err := updatePluginInstallationTx(ctx, tx, current, currentExpected); err != nil {
		return plugins.Installation{}, plugins.Installation{}, err
	}
	if err := updatePluginInstallationTx(ctx, tx, target, targetExpected); err != nil {
		return plugins.Installation{}, plugins.Installation{}, err
	}
	if err := insertPluginTransitionTx(ctx, tx, currentBefore, current); err != nil {
		return plugins.Installation{}, plugins.Installation{}, err
	}
	if err := insertPluginTransitionTx(ctx, tx, targetBefore, target); err != nil {
		return plugins.Installation{}, plugins.Installation{}, err
	}
	if err := tx.Commit(); err != nil {
		return plugins.Installation{}, plugins.Installation{}, err
	}
	return current, target, nil
}

func (s *SQLiteStore) GetPluginPublisherTrust(ctx context.Context, fingerprint string) (
	plugins.PublisherTrust, bool, error,
) {
	return getPluginPublisherTrust(ctx, s.db, fingerprint)
}

func getPluginPublisherTrust(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, fingerprint string) (plugins.PublisherTrust, bool, error) {
	var value plugins.PublisherTrust
	var state, reviewedAt string
	err := queryer.QueryRowContext(ctx, `SELECT protocol_version, fingerprint, publisher,
		public_key, state, generation, reviewed_by, reviewed_at FROM plugin_publishers
		WHERE fingerprint = ?`, strings.TrimSpace(fingerprint)).Scan(&value.ProtocolVersion,
		&value.Fingerprint, &value.Publisher, &value.PublicKey, &state, &value.Generation,
		&value.ReviewedBy, &reviewedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return plugins.PublisherTrust{}, false, nil
	}
	if err != nil {
		return plugins.PublisherTrust{}, false, err
	}
	value.State = plugins.PublisherState(state)
	value.ReviewedAt = parseTS(reviewedAt)
	return value, true, value.Validate()
}

func validateRollbackPublisherTx(ctx context.Context, tx *sql.Tx, target plugins.Installation,
	authority plugins.PublisherAuthority,
) error {
	if !target.SignatureValid {
		if authority.ExpectedGeneration != 0 || !authority.ConfirmUntrusted {
			return apperror.New(apperror.CodePolicyDenied, "unsigned plugin rollback requires explicit confirmation")
		}
		return nil
	}
	trust, found, err := getPluginPublisherTrust(ctx, tx, target.PublisherFingerprint)
	if err != nil {
		return err
	}
	bound := found && trust.Publisher == target.Manifest.Publisher && trust.PublicKey == target.PublisherPublicKey
	if found && trust.State == plugins.PublisherRevoked {
		return apperror.New(apperror.CodePolicyDenied, "plugin publisher was revoked before the version switch")
	}
	generation := int64(0)
	if found {
		generation = trust.Generation
	}
	if generation != authority.ExpectedGeneration {
		return apperror.New(apperror.CodeConflict, "plugin publisher trust changed before the version switch")
	}
	if (!bound || trust.State != plugins.PublisherTrusted) && !authority.ConfirmUntrusted {
		return apperror.New(apperror.CodePolicyDenied, "untrusted plugin rollback requires explicit confirmation")
	}
	return nil
}

func (s *SQLiteStore) SetPluginPublisherTrust(ctx context.Context,
	trust plugins.PublisherTrust, expectedGeneration int64,
) (plugins.PublisherTrust, error) {
	if err := trust.Validate(); err != nil || expectedGeneration < 0 ||
		trust.Generation != expectedGeneration+1 {
		return plugins.PublisherTrust{}, apperror.New(apperror.CodeInvalidArgument,
			"plugin publisher trust update is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return plugins.PublisherTrust{}, err
	}
	defer tx.Rollback()
	if expectedGeneration == 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO plugin_publishers
			(fingerprint, protocol_version, publisher, public_key, state, generation,
			reviewed_by, reviewed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, trust.Fingerprint,
			trust.ProtocolVersion, trust.Publisher, trust.PublicKey, trust.State,
			trust.Generation, trust.ReviewedBy, ts(trust.ReviewedAt))
	} else {
		var result sql.Result
		result, err = tx.ExecContext(ctx, `UPDATE plugin_publishers SET state = ?,
			generation = ?, reviewed_by = ?, reviewed_at = ? WHERE fingerprint = ?
			AND publisher = ? AND public_key = ? AND generation = ?`, trust.State,
			trust.Generation, trust.ReviewedBy, ts(trust.ReviewedAt), trust.Fingerprint,
			trust.Publisher, trust.PublicKey, expectedGeneration)
		if err == nil {
			if changed, _ := result.RowsAffected(); changed != 1 {
				err = apperror.New(apperror.CodeConflict,
					"plugin publisher trust changed concurrently")
			}
		}
	}
	if err != nil {
		return plugins.PublisherTrust{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_publisher_reviews
		(id, fingerprint, state, generation, reviewed_by, reviewed_at)
		VALUES (?, ?, ?, ?, ?, ?)`, idgen.New("plugin-publisher-review"), trust.Fingerprint,
		trust.State, trust.Generation, trust.ReviewedBy, ts(trust.ReviewedAt)); err != nil {
		return plugins.PublisherTrust{}, err
	}
	return trust, tx.Commit()
}

func (s *SQLiteStore) RevokePluginPublisher(ctx context.Context,
	trust plugins.PublisherTrust, expectedGeneration int64,
) (plugins.PublisherTrust, error) {
	if err := trust.Validate(); err != nil || trust.State != plugins.PublisherRevoked ||
		trust.Generation != expectedGeneration+1 {
		return plugins.PublisherTrust{}, apperror.New(apperror.CodeInvalidArgument,
			"plugin publisher revocation is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return plugins.PublisherTrust{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE plugin_publishers SET state = ?, generation = ?,
		reviewed_by = ?, reviewed_at = ? WHERE fingerprint = ? AND generation = ?`,
		trust.State, trust.Generation, trust.ReviewedBy, ts(trust.ReviewedAt),
		trust.Fingerprint, expectedGeneration)
	if err != nil {
		return plugins.PublisherTrust{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return plugins.PublisherTrust{}, apperror.New(apperror.CodeConflict,
			"plugin publisher changed concurrently")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_publisher_reviews
		(id, fingerprint, state, generation, reviewed_by, reviewed_at)
		VALUES (?, ?, ?, ?, ?, ?)`, idgen.New("plugin-publisher-review"), trust.Fingerprint,
		trust.State, trust.Generation, trust.ReviewedBy, ts(trust.ReviewedAt)); err != nil {
		return plugins.PublisherTrust{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+pluginInstallationColumns+`
		FROM plugin_installations WHERE publisher_fingerprint = ?
		AND state NOT IN ('revoked', 'rolled_back')`, trust.Fingerprint)
	if err != nil {
		return plugins.PublisherTrust{}, err
	}
	values := make([]plugins.Installation, 0)
	for rows.Next() {
		value, scanErr := scanPluginInstallation(rows)
		if scanErr != nil {
			rows.Close()
			return plugins.PublisherTrust{}, scanErr
		}
		values = append(values, value)
	}
	if err := rows.Close(); err != nil {
		return plugins.PublisherTrust{}, err
	}
	for _, before := range values {
		after := before
		after.State = plugins.StateRevoked
		after.EnabledCapabilities = []plugins.Capability{}
		after.Generation++
		after.ReviewedBy = trust.ReviewedBy
		after.ReviewedAt = &trust.ReviewedAt
		after.UpdatedAt = trust.ReviewedAt
		if err := updatePluginInstallationTx(ctx, tx, after, before.Generation); err != nil {
			return plugins.PublisherTrust{}, err
		}
		if err := insertPluginTransitionTx(ctx, tx, before, after); err != nil {
			return plugins.PublisherTrust{}, err
		}
	}
	return trust, tx.Commit()
}

func (s *SQLiteStore) RecordHookAudit(ctx context.Context, audit hooks.AuditRecord) error {
	if strings.TrimSpace(audit.PluginID) == "" || strings.TrimSpace(audit.HookID) == "" ||
		!audit.Event.Valid() || (audit.Outcome != "completed" && audit.Outcome != "failed_closed" &&
		audit.Outcome != "failed_continue" && audit.Outcome != "output_capped") || audit.CreatedAt.IsZero() {
		return apperror.New(apperror.CodeInvalidArgument, "plugin hook audit is invalid")
	}
	if (audit.PluginFingerprint != "" || audit.Action != "" || audit.Rejected != nil) &&
		(len(audit.PluginFingerprint) != 64 || strings.Trim(audit.PluginFingerprint, "0123456789abcdef") != "" || !audit.Action.Valid() || audit.Rejected == nil ||
			*audit.Rejected != hookRejectedBinding(audit)) {
		return apperror.New(apperror.CodeInvalidArgument, "plugin hook decision binding is invalid")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO plugin_hook_audits
		(id, plugin_id, hook_id, event, run_id, workspace_id, tool_name, outcome, created_at, plugin_fingerprint, declared_action, rejected)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, idgen.New("hook-audit"), audit.PluginID,
		audit.HookID, audit.Event, audit.RunID, audit.WorkspaceID, audit.ToolName,
		audit.Outcome, ts(audit.CreatedAt), audit.PluginFingerprint, audit.Action, audit.Rejected)
	return err
}

type pluginInstallationScanner interface{ Scan(...any) error }

// hookRejectedBinding mirrors the engine's audit binding. Declared denies are
// rejected by their action alone; script denials come from the script's bounded
// output, so the engine's observed decision is authoritative for that action.
func hookRejectedBinding(audit hooks.AuditRecord) bool {
	if audit.Outcome == "failed_closed" {
		return true
	}
	if audit.Outcome != "completed" {
		return false
	}
	if audit.Action == hooks.ActionDeny {
		return true
	}
	if audit.Action == hooks.ActionScript {
		return *audit.Rejected
	}
	return false
}

func getPluginInstallation(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string,
) (plugins.Installation, error) {
	return scanPluginInstallation(queryer.QueryRowContext(ctx, `SELECT `+
		pluginInstallationColumns+` FROM plugin_installations WHERE id = ?`, id))
}

func scanPluginInstallation(scanner pluginInstallationScanner) (plugins.Installation, error) {
	var value plugins.Installation
	var manifestJSON, sourceJSON, state, capabilitiesJSON string
	var signaturePresent, signatureValid int
	var reviewedAt sql.NullString
	var createdAt, updatedAt string
	if err := scanner.Scan(&value.ProtocolVersion, &value.ID, &manifestJSON, &sourceJSON,
		&value.ArchiveSHA256, &value.PackageFingerprint, &value.ArchiveBytes,
		&signaturePresent, &signatureValid, &value.PublisherFingerprint,
		&value.PublisherPublicKey, &state, &capabilitiesJSON, &value.Generation,
		&value.SupersedesInstallationID, &value.StagedBy, &value.ReviewedBy, &reviewedAt,
		&createdAt, &updatedAt); err != nil {
		return plugins.Installation{}, err
	}
	var description any = &value.Manifest
	if value.ProtocolVersion == plugins.PortableInstallationProtocol {
		value.Snapshot = &plugins.PortableSnapshot{}
		description = value.Snapshot
	}
	if err := json.Unmarshal([]byte(manifestJSON), description); err != nil {
		return plugins.Installation{}, err
	}
	if err := json.Unmarshal([]byte(sourceJSON), &value.Source); err != nil {
		return plugins.Installation{}, err
	}
	if err := json.Unmarshal([]byte(capabilitiesJSON), &value.EnabledCapabilities); err != nil {
		return plugins.Installation{}, err
	}
	value.SignaturePresent, value.SignatureValid = signaturePresent != 0, signatureValid != 0
	value.State = plugins.State(state)
	value.CreatedAt, value.UpdatedAt = parseTS(createdAt), parseTS(updatedAt)
	if reviewedAt.Valid {
		timestamp := parseTS(reviewedAt.String)
		value.ReviewedAt = &timestamp
	}
	if err := value.Validate(); err != nil {
		return plugins.Installation{}, fmt.Errorf("stored plugin installation is invalid: %w", err)
	}
	return value, nil
}

// LoadPluginObject reads the same immutable object retained by installation,
// review and rollback. A caller must still recheck current installation authority.
func (s *SQLiteStore) LoadPluginObject(ctx context.Context, installationID string) ([]byte, error) {
	value, err := s.GetPluginInstallation(ctx, installationID)
	if err != nil {
		return nil, err
	}
	var raw []byte
	err = s.db.QueryRowContext(ctx, `SELECT archive FROM plugin_objects
		WHERE package_fingerprint=? AND archive_sha256=? AND archive_bytes=?`,
		value.PackageFingerprint, value.ArchiveSHA256, value.ArchiveBytes).Scan(&raw)
	if err != nil {
		return nil, err
	}
	if len(raw) != value.ArchiveBytes || len(raw) > plugins.MaxArchiveBytes || pluginStoreDigest(raw) != value.ArchiveSHA256 {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "installed plugin object differs from its retained identity")
	}
	return raw, nil
}

func updatePluginInstallationTx(ctx context.Context, tx *sql.Tx,
	value plugins.Installation, expectedGeneration int64,
) error {
	capabilitiesJSON, _ := json.Marshal(value.EnabledCapabilities)
	var reviewedAt any
	if value.ReviewedAt != nil {
		reviewedAt = ts(*value.ReviewedAt)
	}
	result, err := tx.ExecContext(ctx, `UPDATE plugin_installations SET state = ?,
		enabled_capabilities_json = ?, generation = ?, reviewed_by = ?, reviewed_at = ?,
		updated_at = ? WHERE id = ? AND package_fingerprint = ? AND generation = ?`,
		value.State, string(capabilitiesJSON), value.Generation, value.ReviewedBy,
		reviewedAt, ts(value.UpdatedAt), value.ID, value.PackageFingerprint,
		expectedGeneration)
	if err != nil {
		if isUniqueViolation(err) {
			return apperror.Wrap(apperror.CodeConflict,
				"another plugin version is already enabled", err)
		}
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return apperror.New(apperror.CodeConflict,
			"plugin installation changed concurrently")
	}
	return nil
}

func insertPluginTransitionTx(ctx context.Context, tx *sql.Tx,
	before, after plugins.Installation,
) error {
	capabilitiesJSON, _ := json.Marshal(after.EnabledCapabilities)
	_, err := tx.ExecContext(ctx, `INSERT INTO plugin_installation_transitions
		(id, installation_id, from_state, to_state, from_generation, to_generation,
		enabled_capabilities_json, actor, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		idgen.New("plugin-transition"), after.ID, before.State, after.State,
		before.Generation, after.Generation, string(capabilitiesJSON), after.ReviewedBy,
		ts(after.UpdatedAt))
	return err
}

func pluginStoreDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
