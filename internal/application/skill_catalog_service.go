package application

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"cyberagent-workbench/internal/agentpackages"
	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/plugins"
	"cyberagent-workbench/internal/skills"
)

// SkillCatalogStore is the bounded store surface the catalog service touches.
type SkillCatalogStore interface {
	CompareAndSetSkillCatalogPublisher(context.Context, skills.CatalogPublisher, string, string, string) (skills.CatalogPublisher, error)
	UpsertTrustedPublisher(context.Context, skills.CatalogPublisher, string) error
	RevokeSkillCatalogPublisher(context.Context, string, string) error
	GetSkillCatalogPublisher(context.Context, string) (skills.CatalogPublisher, bool, error)
	ListSkillCatalogPublishers(context.Context) ([]skills.CatalogPublisher, error)
	PinSkillCatalogVersion(context.Context, skills.CatalogPin, string, string) error
	SetSkillCatalogPinEnabled(context.Context, string, domain.ExecutionSurface, bool, string) error
	ListSkillCatalogPins(context.Context) ([]skills.CatalogPin, error)
	RecordSkillCatalogImport(context.Context, skills.CatalogImport, string) error
	ListSkillCatalogImports(context.Context, int) ([]skills.CatalogImport, error)
	ListSkillCatalogAudit(context.Context, int) ([]skills.CatalogAuditEvent, error)
	FindSkillCatalogImportByPackage(context.Context, string) (skills.CatalogImport, bool, error)
}

// SkillCatalogService owns legacy publisher/version pins and source import
// routing. All new imports use Plugin installation and review. Legacy archives
// retain their strict codec; only already recorded intents use legacy recovery.
type SkillCatalogService struct {
	store      SkillCatalogStore
	registry   *SkillPackageRegistryService
	HTTPClient *http.Client // nil uses the bounded default inside skills.FetchPinnedURL
}

func NewSkillCatalogService(store SkillCatalogStore, registry *SkillPackageRegistryService) *SkillCatalogService {
	return &SkillCatalogService{store: store, registry: registry}
}

type TrustSkillPublisherRequest struct {
	Name      string
	Team      string
	PublicKey string
	Actor     string
}

func (s *SkillCatalogService) TrustPublisher(ctx context.Context, request TrustSkillPublisherRequest) (skills.CatalogPublisher, error) {
	if s == nil || s.store == nil {
		return skills.CatalogPublisher{}, apperror.New(apperror.CodeFailedPrecondition, "Skill catalog service is not configured")
	}
	request.Name = strings.TrimSpace(request.Name)
	request.Actor = strings.TrimSpace(request.Actor)
	if request.Name == "" || request.Actor == "" {
		return skills.CatalogPublisher{}, apperror.New(apperror.CodeInvalidArgument, "publisher name and actor are required")
	}
	fingerprint, err := skills.PublisherFingerprintFromBase64(request.PublicKey)
	if err != nil {
		return skills.CatalogPublisher{}, apperror.Wrap(apperror.CodeInvalidArgument, "publisher public key is invalid", err)
	}
	publisher := skills.CatalogPublisher{
		Fingerprint: fingerprint, Name: request.Name, Team: request.Team,
		PublicKey: request.PublicKey,
	}
	if err := s.store.UpsertTrustedPublisher(ctx, publisher, request.Actor); err != nil {
		return skills.CatalogPublisher{}, apperror.Normalize(err)
	}
	value, _, err := s.store.GetSkillCatalogPublisher(ctx, fingerprint)
	return value, apperror.Normalize(err)
}

type RevokeSkillPublisherRequest struct {
	Fingerprint string
	Actor       string
}

func (s *SkillCatalogService) RevokePublisher(ctx context.Context, request RevokeSkillPublisherRequest) error {
	if s == nil || s.store == nil {
		return apperror.New(apperror.CodeFailedPrecondition, "Skill catalog service is not configured")
	}
	request.Actor = strings.TrimSpace(request.Actor)
	if request.Actor == "" {
		return apperror.New(apperror.CodeInvalidArgument, "actor is required")
	}
	return apperror.Normalize(s.store.RevokeSkillCatalogPublisher(ctx, request.Fingerprint, request.Actor))
}

type PinSkillVersionRequest struct {
	SkillName string
	Surface   domain.ExecutionSurface
	Version   string
	Actor     string
}

func (s *SkillCatalogService) PinVersion(ctx context.Context, request PinSkillVersionRequest) error {
	if s == nil || s.store == nil {
		return apperror.New(apperror.CodeFailedPrecondition, "Skill catalog service is not configured")
	}
	request.SkillName = strings.TrimSpace(request.SkillName)
	request.Version = strings.TrimSpace(request.Version)
	request.Actor = strings.TrimSpace(request.Actor)
	if request.SkillName == "" || request.Version == "" || !request.Surface.Valid() || request.Actor == "" {
		return apperror.New(apperror.CodeInvalidArgument, "skill name, version, surface, and actor are required")
	}
	installed, found, err := s.registry.store.GetInstalledPackageByRef(ctx, request.SkillName, request.Version)
	if err != nil {
		return apperror.Normalize(err)
	}
	if !found {
		return apperror.New(apperror.CodeNotFound, "Skill package version is not installed")
	}
	publisherFingerprint := ""
	if ledger, found, err := s.store.FindSkillCatalogImportByPackage(ctx, installed.Installation.PackageFingerprint); err != nil {
		return apperror.Normalize(err)
	} else if found {
		publisherFingerprint = ledger.PublisherFingerprint
	}
	return apperror.Normalize(s.store.PinSkillCatalogVersion(ctx, skills.CatalogPin{
		SkillName: request.SkillName, Surface: request.Surface, Version: request.Version,
	}, request.Actor, publisherFingerprint))
}

type SetSkillEnabledRequest struct {
	SkillName string
	Surface   domain.ExecutionSurface
	Enabled   bool
	Actor     string
}

func (s *SkillCatalogService) SetEnabled(ctx context.Context, request SetSkillEnabledRequest) error {
	if s == nil || s.store == nil {
		return apperror.New(apperror.CodeFailedPrecondition, "Skill catalog service is not configured")
	}
	request.SkillName = strings.TrimSpace(request.SkillName)
	request.Actor = strings.TrimSpace(request.Actor)
	if request.SkillName == "" || !request.Surface.Valid() || request.Actor == "" {
		return apperror.New(apperror.CodeInvalidArgument, "skill name, surface, and actor are required")
	}
	return apperror.Normalize(s.store.SetSkillCatalogPinEnabled(ctx, request.SkillName, request.Surface, request.Enabled, request.Actor))
}

func (s *SkillCatalogService) ListPublishers(ctx context.Context) ([]skills.CatalogPublisher, error) {
	if s == nil || s.store == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "Skill catalog service is not configured")
	}
	values, err := s.store.ListSkillCatalogPublishers(ctx)
	return values, apperror.Normalize(err)
}

func (s *SkillCatalogService) ListPins(ctx context.Context) ([]skills.CatalogPin, error) {
	if s == nil || s.store == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "Skill catalog service is not configured")
	}
	values, err := s.store.ListSkillCatalogPins(ctx)
	return values, apperror.Normalize(err)
}

func (s *SkillCatalogService) ListImports(ctx context.Context, limit int) ([]skills.CatalogImport, error) {
	if s == nil || s.store == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "Skill catalog service is not configured")
	}
	values, err := s.store.ListSkillCatalogImports(ctx, limit)
	return values, apperror.Normalize(err)
}

func (s *SkillCatalogService) ListAudit(ctx context.Context, limit int) ([]skills.CatalogAuditEvent, error) {
	if s == nil || s.store == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "Skill catalog service is not configured")
	}
	values, err := s.store.ListSkillCatalogAudit(ctx, limit)
	return values, apperror.Normalize(err)
}

type ImportSkillFromURLRequest struct {
	URL              string
	SHA256           string
	Surface          domain.ExecutionSurface
	OperationKey     string
	InstalledBy      string
	ConfirmUntrusted bool
}

type ImportSkillFromSourceResult struct {
	Portable  *plugins.Installation `json:"portable,omitempty"`
	Installed skills.InstalledPackage
	Signed    bool
	Import    skills.CatalogImport
}

// ImportFromURL downloads exactly the pinned HTTPS bytes, strictly validates
// them as a v1/v2 package, installs through the existing Registry flow, and
// records the pinned import in the ledger.
func (s *SkillCatalogService) ImportFromURL(ctx context.Context, request ImportSkillFromURLRequest) (ImportSkillFromSourceResult, error) {
	if s == nil || s.store == nil || s.registry == nil {
		return ImportSkillFromSourceResult{}, apperror.New(apperror.CodeFailedPrecondition, "Skill catalog service is not configured")
	}
	raw, err := skills.FetchPinnedURL(ctx, request.URL, request.SHA256, s.HTTPClient)
	if err != nil {
		return ImportSkillFromSourceResult{}, apperror.Wrap(apperror.CodeInvalidArgument, "Skill URL import failed", err)
	}
	return s.importRaw(ctx, raw, "url", request.URL, request.SHA256, request.Surface,
		request.OperationKey, request.InstalledBy, request.ConfirmUntrusted)
}

type ImportSkillFromGitRequest struct {
	RepoURL          string
	CommitSHA        string
	Surface          domain.ExecutionSurface
	OperationKey     string
	InstalledBy      string
	ConfirmUntrusted bool
	StagingRoot      string
}

// ImportFromGit stages the exact commit with the local git binary (no hooks,
// scripts, or submodules run), packages the validated directory, installs
// through the Registry flow, and records the pinned commit in the ledger.
func (s *SkillCatalogService) ImportFromGit(ctx context.Context, request ImportSkillFromGitRequest) (ImportSkillFromSourceResult, error) {
	if s == nil || s.store == nil || s.registry == nil {
		return ImportSkillFromSourceResult{}, apperror.New(apperror.CodeFailedPrecondition, "Skill catalog service is not configured")
	}
	if strings.TrimSpace(request.StagingRoot) == "" {
		request.StagingRoot = os.TempDir()
	}
	location, err := url.Parse(request.RepoURL)
	if err != nil {
		return ImportSkillFromSourceResult{}, err
	}
	rootName := strings.TrimSuffix(path.Base(location.Path), ".git")
	if !filepath.IsLocal(rootName) || rootName == "." {
		return ImportSkillFromSourceResult{}, apperror.New(apperror.CodeInvalidArgument, "Git source needs a named repository root")
	}
	stagingParent := filepath.Join(request.StagingRoot, "skill-git-import-"+idgen.New("stage"))
	staging := filepath.Join(stagingParent, rootName)
	defer func() { _ = os.RemoveAll(stagingParent) }()
	if err := os.MkdirAll(stagingParent, 0o700); err != nil {
		return ImportSkillFromSourceResult{}, err
	}
	if err := skills.FetchGitCommit(ctx, request.RepoURL, request.CommitSHA, staging); err != nil {
		return ImportSkillFromSourceResult{}, apperror.Wrap(apperror.CodeInvalidArgument, "Skill Git import failed", err)
	}
	format, err := agentpackages.DetectDirectory(ctx, staging)
	if err != nil {
		return ImportSkillFromSourceResult{}, err
	}
	if format != agentpackages.FormatLegacySkill {
		return s.importPortableDirectory(ctx, staging, plugins.InstallSource{Kind: "git", URI: request.RepoURL, Commit: request.CommitSHA}, request.Surface, request.OperationKey, request.InstalledBy, request.ConfirmUntrusted)
	}
	raw, err := skills.BuildPackageFromDir(staging)
	if err != nil {
		return ImportSkillFromSourceResult{}, apperror.Wrap(apperror.CodeInvalidArgument, "Skill Git staging failed validation", err)
	}
	return s.importRaw(ctx, raw, "git", request.RepoURL, request.CommitSHA, request.Surface,
		request.OperationKey, request.InstalledBy, request.ConfirmUntrusted)
}

type ImportSkillFromDirectoryRequest struct {
	Directory        string
	Surface          domain.ExecutionSurface
	OperationKey     string
	InstalledBy      string
	ConfirmUntrusted bool
}

// ImportFromDirectory selects the native or explicit legacy format before
// installation. Native components retain their original files and metadata;
// the old signed codec remains authoritative for explicit legacy directories.
func (s *SkillCatalogService) ImportFromDirectory(ctx context.Context, request ImportSkillFromDirectoryRequest) (ImportSkillFromSourceResult, error) {
	if s == nil || s.store == nil || s.registry == nil {
		return ImportSkillFromSourceResult{}, apperror.New(apperror.CodeFailedPrecondition, "Skill catalog service is not configured")
	}
	absolute, err := filepath.Abs(request.Directory)
	if err != nil {
		return ImportSkillFromSourceResult{}, err
	}
	format, err := agentpackages.DetectDirectory(ctx, absolute)
	if err != nil {
		return ImportSkillFromSourceResult{}, err
	}
	if format != agentpackages.FormatLegacySkill {
		return s.importPortableDirectory(ctx, absolute, plugins.InstallSource{Kind: "local_directory", URI: absolute}, request.Surface, request.OperationKey, request.InstalledBy, request.ConfirmUntrusted)
	}
	raw, err := skills.BuildPackageFromDir(request.Directory)
	if err != nil {
		return ImportSkillFromSourceResult{}, apperror.Wrap(apperror.CodeInvalidArgument, "Skill directory import failed", err)
	}
	return s.importRaw(ctx, raw, "local", request.Directory, "directory", request.Surface,
		request.OperationKey, request.InstalledBy, request.ConfirmUntrusted)
}

func (s *SkillCatalogService) importRaw(ctx context.Context, raw []byte, sourceKind, source, pin string,
	surface domain.ExecutionSurface, operationKey, installedBy string, confirmUntrusted bool,
) (ImportSkillFromSourceResult, error) {
	parsed, err := skills.ParsePackageAny(raw)
	if err != nil {
		return ImportSkillFromSourceResult{}, apperror.Wrap(apperror.CodeInvalidArgument, "Skill package failed strict validation", err)
	}
	preview := parsed.Preview()
	publisherFingerprint := ""
	if parsed.V2 != nil {
		publisherFingerprint = parsed.V2.PublisherFingerprint
	}
	pluginSource := plugins.InstallSource{Kind: sourceKind, URI: source}
	switch sourceKind {
	case "url":
		pluginSource.Kind = "https"
	case "local":
		pluginSource.Kind = "local_directory"
		pluginSource.URI, err = filepath.Abs(source)
		if err != nil {
			return ImportSkillFromSourceResult{}, err
		}
	case "git":
		pluginSource.Commit = pin
	}
	result, err := s.registry.Import(ctx, ImportSkillPackageRequest{
		Raw: raw, Source: pluginSource, Surface: surface, OperationKey: operationKey, InstalledBy: installedBy,
		ConfirmUntrusted: confirmUntrusted,
	})
	if err != nil {
		return ImportSkillFromSourceResult{}, err
	}
	if result.Installation != nil {
		return ImportSkillFromSourceResult{Portable: result.Installation, Signed: parsed.V2 != nil}, nil
	}
	ledger := skills.CatalogImport{
		ID: idgen.New("skill-import"), SourceKind: sourceKind, Source: source, Pin: pin,
		ArchiveSHA256:        preview.ArchiveSHA256,
		PackageFingerprint:   result.Package.Installation.PackageFingerprint,
		PublisherFingerprint: publisherFingerprint,
	}
	actor := strings.TrimSpace(installedBy)
	if actor == "" {
		actor = "cli_operator"
	}
	if err := s.store.RecordSkillCatalogImport(ctx, ledger, actor); err != nil {
		return ImportSkillFromSourceResult{}, apperror.Normalize(err)
	}
	ledger.ImportedBy = actor
	ledger.CreatedAt = time.Now().UTC()
	return ImportSkillFromSourceResult{Installed: result.Package, Signed: parsed.V2 != nil, Import: ledger}, nil
}
