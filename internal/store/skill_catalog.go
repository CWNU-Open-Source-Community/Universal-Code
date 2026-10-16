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
	"cyberagent-workbench/internal/skills"
)

// Catalog domain types live in the skills package so the application layer
// can depend on them without a store import cycle.
type SkillCatalogPublisher = skills.CatalogPublisher
type SkillCatalogPin = skills.CatalogPin
type SkillCatalogImport = skills.CatalogImport
type SkillCatalogAuditEvent = skills.CatalogAuditEvent

const (
	SkillCatalogTrustedEventType  = "catalog.trusted"
	SkillCatalogRevokedEventType  = "catalog.revoked"
	SkillCatalogPinnedEventType   = "catalog.pinned"
	SkillCatalogEnabledEventType  = "catalog.enabled"
	SkillCatalogDisabledEventType = "catalog.disabled"
	SkillImportCompletedEventType = "import.completed"
)

func validPublisherFingerprint(value string) bool {
	return len(value) == 64 && value == strings.ToLower(value)
}

// UpsertTrustedPublisher retains the CLI's explicit unconditional trust action.
// Every write advances the version used by observed HTTP control requests.
func (s *SQLiteStore) UpsertTrustedPublisher(ctx context.Context, publisher SkillCatalogPublisher, actor string) error {
	_, err := s.changeSkillCatalogPublisher(ctx, publisher, "trust", nil, actor)
	return err
}

func (s *SQLiteStore) RevokeSkillCatalogPublisher(ctx context.Context, fingerprint, actor string) error {
	_, err := s.changeSkillCatalogPublisher(ctx, SkillCatalogPublisher{Fingerprint: fingerprint}, "revoke", nil, actor)
	return err
}

// CompareAndSetSkillCatalogPublisher binds a decision to the exact observed
// version. Empty expectedUpdatedAt permits creation only, never re-trust.
func (s *SQLiteStore) CompareAndSetSkillCatalogPublisher(ctx context.Context, publisher SkillCatalogPublisher, action, expectedUpdatedAt, actor string) (SkillCatalogPublisher, error) {
	return s.changeSkillCatalogPublisher(ctx, publisher, action, &expectedUpdatedAt, actor)
}

func (s *SQLiteStore) changeSkillCatalogPublisher(ctx context.Context, publisher SkillCatalogPublisher, action string, expected *string, actor string) (SkillCatalogPublisher, error) {
	invalid := func() (SkillCatalogPublisher, error) {
		return SkillCatalogPublisher{}, apperror.New(apperror.CodeInvalidArgument, "Skill catalog publisher decision is invalid")
	}
	if !validPublisherFingerprint(publisher.Fingerprint) || (action != "trust" && action != "revoke") {
		return invalid()
	}
	if action == "trust" && (strings.TrimSpace(publisher.Name) == "" || len(publisher.Name) > 96 || strings.TrimSpace(publisher.PublicKey) == "" || len(publisher.PublicKey) > 128) {
		return invalid()
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return invalid()
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return SkillCatalogPublisher{}, err
	}
	defer func() { _ = tx.Rollback() }()
	previous, found, err := scanSkillCatalogPublisher(tx.QueryRowContext(ctx, `SELECT fingerprint, name, team, public_key, trust_class,
 trusted_by, trusted_at, revoked_by, revoked_at, created_at, updated_at FROM skill_catalog_publishers WHERE fingerprint = ?`, publisher.Fingerprint))
	if err != nil {
		return SkillCatalogPublisher{}, err
	}
	if expected != nil && ((!found && *expected != "") || (found && (*expected == "" || *expected != ts(previous.UpdatedAt)))) {
		return SkillCatalogPublisher{}, apperror.New(apperror.CodeConflict, "Skill catalog publisher changed; refresh before deciding")
	}
	if action == "revoke" && !found {
		return SkillCatalogPublisher{}, apperror.New(apperror.CodeNotFound, "Skill catalog publisher was not found")
	}
	now := time.Now().UTC()
	// Clock resolution or a backwards wall-clock adjustment must not reuse an
	// observed version, including when the intervening decision came from CLI.
	if found && !now.After(previous.UpdatedAt) {
		now = previous.UpdatedAt.Add(time.Nanosecond)
	}
	eventType := SkillCatalogTrustedEventType
	payload := map[string]any{}
	if action == "trust" {
		_, err = tx.ExecContext(ctx, `INSERT INTO skill_catalog_publishers
   (fingerprint,name,team,public_key,trust_class,trusted_by,trusted_at,created_at,updated_at)
   VALUES (?,?,?,?,'trusted',?,?,?,?)
   ON CONFLICT(fingerprint) DO UPDATE SET name=excluded.name,team=excluded.team,
    public_key=excluded.public_key,trust_class='trusted',trusted_by=excluded.trusted_by,
    trusted_at=excluded.trusted_at,revoked_by='',revoked_at=NULL,updated_at=excluded.updated_at`,
			publisher.Fingerprint, publisher.Name, publisher.Team, publisher.PublicKey, actor, ts(now), ts(now), ts(now))
		publisher.TrustClass = "trusted"
		publisher.TrustedBy = actor
		publisher.TrustedAt = &now
		publisher.RevokedBy = ""
		publisher.RevokedAt = nil
		publisher.CreatedAt = now
		if found {
			publisher.CreatedAt = previous.CreatedAt
		}
		payload = map[string]any{"name": publisher.Name, "team": publisher.Team}
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE skill_catalog_publishers SET trust_class='revoked',revoked_by=?,revoked_at=?,updated_at=? WHERE fingerprint=?`, actor, ts(now), ts(now), publisher.Fingerprint)
		publisher = previous
		publisher.TrustClass = "revoked"
		publisher.RevokedBy = actor
		publisher.RevokedAt = &now
		eventType = SkillCatalogRevokedEventType
	}
	if err != nil {
		return SkillCatalogPublisher{}, err
	}
	publisher.UpdatedAt = now
	if err := insertSkillCatalogAuditTx(ctx, tx, eventType, publisher.Fingerprint, payload, actor, now); err != nil {
		return SkillCatalogPublisher{}, err
	}
	if err := tx.Commit(); err != nil {
		return SkillCatalogPublisher{}, err
	}
	return publisher, nil
}

func (s *SQLiteStore) GetSkillCatalogPublisher(ctx context.Context, fingerprint string) (SkillCatalogPublisher, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT fingerprint, name, team, public_key, trust_class,
		trusted_by, trusted_at, revoked_by, revoked_at, created_at, updated_at
		FROM skill_catalog_publishers WHERE fingerprint = ?`, fingerprint)
	return scanSkillCatalogPublisher(row)
}

func scanSkillCatalogPublisher(row interface{ Scan(...any) error }) (SkillCatalogPublisher, bool, error) {
	var publisher SkillCatalogPublisher
	var trustedAt, revokedAt sql.NullString
	var created, updated string
	err := row.Scan(&publisher.Fingerprint, &publisher.Name, &publisher.Team, &publisher.PublicKey,
		&publisher.TrustClass, &publisher.TrustedBy, &trustedAt, &publisher.RevokedBy, &revokedAt,
		&created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return SkillCatalogPublisher{}, false, nil
	}
	if err != nil {
		return SkillCatalogPublisher{}, false, err
	}
	if trustedAt.Valid {
		if parsed := parseTS(trustedAt.String); !parsed.IsZero() {
			publisher.TrustedAt = &parsed
		}
	}
	if revokedAt.Valid {
		if parsed := parseTS(revokedAt.String); !parsed.IsZero() {
			publisher.RevokedAt = &parsed
		}
	}
	publisher.CreatedAt = parseTS(created)
	publisher.UpdatedAt = parseTS(updated)
	return publisher, true, nil
}

func (s *SQLiteStore) ListSkillCatalogPublishers(ctx context.Context) ([]SkillCatalogPublisher, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT fingerprint, name, team, public_key, trust_class,
		trusted_by, trusted_at, revoked_by, revoked_at, created_at, updated_at
		FROM skill_catalog_publishers ORDER BY name, fingerprint`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var publishers []SkillCatalogPublisher
	for rows.Next() {
		var publisher SkillCatalogPublisher
		var trustedAt, revokedAt sql.NullString
		var created, updated string
		if err := rows.Scan(&publisher.Fingerprint, &publisher.Name, &publisher.Team, &publisher.PublicKey,
			&publisher.TrustClass, &publisher.TrustedBy, &trustedAt, &publisher.RevokedBy, &revokedAt,
			&created, &updated); err != nil {
			return nil, err
		}
		if trustedAt.Valid {
			if parsed := parseTS(trustedAt.String); !parsed.IsZero() {
				publisher.TrustedAt = &parsed
			}
		}
		if revokedAt.Valid {
			if parsed := parseTS(revokedAt.String); !parsed.IsZero() {
				publisher.RevokedAt = &parsed
			}
		}
		publisher.CreatedAt = parseTS(created)
		publisher.UpdatedAt = parseTS(updated)
		publishers = append(publishers, publisher)
	}
	return publishers, rows.Err()
}

// PinSkillCatalogVersion pins the active version for a skill + surface after
// verifying the package is installed and, for signed packages, that its
// publisher is trusted. It also audits the upgrade/rollback transition.
func (s *SQLiteStore) PinSkillCatalogVersion(ctx context.Context, pin SkillCatalogPin, actor, publisherFingerprint string) error {
	if strings.TrimSpace(pin.SkillName) == "" || strings.TrimSpace(pin.Version) == "" || !pin.Surface.Valid() {
		return apperror.New(apperror.CodeInvalidArgument, "Skill catalog pin is invalid")
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return apperror.New(apperror.CodeInvalidArgument, "Skill catalog actor is required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	installation, found, err := getPackageInstallationByRef(ctx, tx, pin.SkillName, pin.Version)
	if err != nil {
		return err
	}
	if !found || installation.Surface != pin.Surface {
		return apperror.New(apperror.CodeNotFound, "pinned Skill package version is not installed for this surface")
	}
	// A non-empty publisher fingerprint means the import ledger recorded a
	// signed package; pinning it is the trust decision and requires a trusted,
	// non-revoked publisher. Unsigned imports carry no fingerprint and stay
	// operator_installed_untrusted by the existing installation invariant.
	if publisherFingerprint != "" {
		var trustClass string
		err := tx.QueryRowContext(ctx, `SELECT trust_class FROM skill_catalog_publishers
			WHERE fingerprint = ?`, publisherFingerprint).Scan(&trustClass)
		if errors.Is(err, sql.ErrNoRows) || trustClass != "trusted" {
			return apperror.New(apperror.CodePolicyDenied, "Skill publisher is not trusted in the catalog")
		}
		if err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	var previous sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT version FROM skill_catalog_pins
		WHERE skill_name = ? AND surface = ?`, pin.SkillName, pin.Surface).Scan(&previous); err != nil &&
		!errors.Is(err, sql.ErrNoRows) {
		return err
	}
	transition := "pinned"
	if previous.Valid && previous.String != "" {
		if previous.String == pin.Version {
			transition = "repinned"
		} else {
			transition = "upgraded_or_rolled_back"
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO skill_catalog_pins
		(skill_name, surface, version, enabled, pinned_by, created_at, updated_at)
		VALUES (?, ?, ?, 1, ?, ?, ?)
		ON CONFLICT(skill_name, surface) DO UPDATE SET version = excluded.version,
			enabled = 1, pinned_by = excluded.pinned_by, updated_at = excluded.updated_at`,
		pin.SkillName, pin.Surface, pin.Version, actor, ts(now), ts(now)); err != nil {
		return err
	}
	if err := insertSkillCatalogAuditTx(ctx, tx, SkillCatalogPinnedEventType,
		pin.SkillName+"@"+pin.Version, map[string]any{
			"surface": pin.Surface, "previous_version": previous.String, "transition": transition,
		}, actor, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) SetSkillCatalogPinEnabled(ctx context.Context, skillName string, surface domain.ExecutionSurface, enabled bool, actor string) error {
	if strings.TrimSpace(skillName) == "" || !surface.Valid() {
		return apperror.New(apperror.CodeInvalidArgument, "Skill catalog pin identity is invalid")
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return apperror.New(apperror.CodeInvalidArgument, "Skill catalog actor is required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE skill_catalog_pins SET enabled = ?, updated_at = ?
		WHERE skill_name = ? AND surface = ?`, boolInt(enabled), ts(now), skillName, surface)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return apperror.New(apperror.CodeNotFound, "skill catalog pin was not found")
	}
	eventType := SkillCatalogDisabledEventType
	if enabled {
		eventType = SkillCatalogEnabledEventType
	}
	if err := insertSkillCatalogAuditTx(ctx, tx, eventType, skillName, map[string]any{
		"surface": surface, "enabled": enabled}, actor, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) ListSkillCatalogPins(ctx context.Context) ([]SkillCatalogPin, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT skill_name, surface, version, enabled, pinned_by,
		created_at, updated_at FROM skill_catalog_pins ORDER BY surface, skill_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pins []SkillCatalogPin
	for rows.Next() {
		var pin SkillCatalogPin
		var enabled int
		var created, updated string
		if err := rows.Scan(&pin.SkillName, &pin.Surface, &pin.Version, &enabled, &pin.PinnedBy,
			&created, &updated); err != nil {
			return nil, err
		}
		pin.Enabled = enabled == 1
		pin.CreatedAt = parseTS(created)
		pin.UpdatedAt = parseTS(updated)
		pins = append(pins, pin)
	}
	return pins, rows.Err()
}

// ActiveSkillCatalogPin returns the pinned version and enable state for a
// skill + surface, or found=false when no pin exists (existing flows stay open).
func (s *SQLiteStore) ActiveSkillCatalogPin(ctx context.Context, skillName string, surface domain.ExecutionSurface) (SkillCatalogPin, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT skill_name, surface, version, enabled, pinned_by,
		created_at, updated_at FROM skill_catalog_pins WHERE skill_name = ? AND surface = ?`,
		skillName, surface)
	var pin SkillCatalogPin
	var enabled int
	var created, updated string
	err := row.Scan(&pin.SkillName, &pin.Surface, &pin.Version, &enabled, &pin.PinnedBy,
		&created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return SkillCatalogPin{}, false, nil
	}
	if err != nil {
		return SkillCatalogPin{}, false, err
	}
	pin.Enabled = enabled == 1
	pin.CreatedAt = parseTS(created)
	pin.UpdatedAt = parseTS(updated)
	return pin, true, nil
}

// RecordSkillCatalogImport appends the pinned-import ledger row and its audit
// event. The ledger pins source + hash so re-imports can detect drift.
func (s *SQLiteStore) RecordSkillCatalogImport(ctx context.Context, imp SkillCatalogImport, actor string) error {
	if strings.TrimSpace(imp.ID) == "" || len(imp.ID) > 256 ||
		(imp.SourceKind != "url" && imp.SourceKind != "git" && imp.SourceKind != "local") ||
		len(imp.ArchiveSHA256) != 64 || len(imp.PackageFingerprint) != 64 ||
		strings.TrimSpace(imp.Pin) == "" || len(imp.Pin) > 128 {
		return apperror.New(apperror.CodeInvalidArgument, "Skill catalog import record is invalid")
	}
	if imp.PublisherFingerprint != "" && !validPublisherFingerprint(imp.PublisherFingerprint) {
		return apperror.New(apperror.CodeInvalidArgument, "Skill catalog import publisher fingerprint is invalid")
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return apperror.New(apperror.CodeInvalidArgument, "Skill catalog actor is required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO skill_catalog_imports
		(id, source_kind, source, pin, archive_sha256, package_fingerprint,
		publisher_fingerprint, imported_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		imp.ID, imp.SourceKind, imp.Source, imp.Pin, imp.ArchiveSHA256, imp.PackageFingerprint,
		imp.PublisherFingerprint, actor, ts(now)); err != nil {
		return err
	}
	if err := insertSkillCatalogAuditTx(ctx, tx, SkillImportCompletedEventType, imp.ID,
		map[string]any{"source_kind": imp.SourceKind, "source": imp.Source, "pin": imp.Pin,
			"archive_sha256": imp.ArchiveSHA256}, actor, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) ListSkillCatalogImports(ctx context.Context, limit int) ([]SkillCatalogImport, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, source_kind, source, pin, archive_sha256,
		package_fingerprint, publisher_fingerprint, imported_by, created_at
		FROM skill_catalog_imports ORDER BY created_at DESC, id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var imports []SkillCatalogImport
	for rows.Next() {
		var imp SkillCatalogImport
		var created string
		if err := rows.Scan(&imp.ID, &imp.SourceKind, &imp.Source, &imp.Pin, &imp.ArchiveSHA256,
			&imp.PackageFingerprint, &imp.PublisherFingerprint, &imp.ImportedBy, &created); err != nil {
			return nil, err
		}
		imp.CreatedAt = parseTS(created)
		imports = append(imports, imp)
	}
	return imports, rows.Err()
}

func (s *SQLiteStore) ListSkillCatalogAudit(ctx context.Context, limit int) ([]SkillCatalogAuditEvent, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sequence, event_type, subject, payload_json,
		actor, created_at FROM skill_catalog_audit ORDER BY sequence DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []SkillCatalogAuditEvent
	for rows.Next() {
		var event SkillCatalogAuditEvent
		var created string
		if err := rows.Scan(&event.Sequence, &event.EventType, &event.Subject, &event.PayloadJSON,
			&event.Actor, &created); err != nil {
			return nil, err
		}
		event.CreatedAt = parseTS(created)
		events = append(events, event)
	}
	return events, rows.Err()
}

// FindSkillCatalogImportByPackage resolves the import ledger row (and thus the
// publisher fingerprint) for an installed package fingerprint.
func (s *SQLiteStore) FindSkillCatalogImportByPackage(ctx context.Context,
	packageFingerprint string,
) (SkillCatalogImport, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, source_kind, source, pin, archive_sha256,
		package_fingerprint, publisher_fingerprint, imported_by, created_at
		FROM skill_catalog_imports WHERE package_fingerprint = ?
		ORDER BY created_at DESC LIMIT 1`, packageFingerprint)
	var imp SkillCatalogImport
	var created string
	err := row.Scan(&imp.ID, &imp.SourceKind, &imp.Source, &imp.Pin, &imp.ArchiveSHA256,
		&imp.PackageFingerprint, &imp.PublisherFingerprint, &imp.ImportedBy, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return SkillCatalogImport{}, false, nil
	}
	if err != nil {
		return SkillCatalogImport{}, false, err
	}
	imp.CreatedAt = parseTS(created)
	return imp, true, nil
}
func insertSkillCatalogAuditTx(ctx context.Context, tx *sql.Tx, eventType, subject string,
	payload map[string]any, actor string, now time.Time,
) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO skill_catalog_audit
		(event_type, subject, payload_json, actor, created_at) VALUES (?, ?, ?, ?, ?)`,
		eventType, subject, string(encoded), actor, ts(now))
	return err
}
