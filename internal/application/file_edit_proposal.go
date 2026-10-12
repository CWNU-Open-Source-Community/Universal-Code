package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/tools"
	"cyberagent-workbench/internal/workspace"
)

const (
	FileEditProposalProtocolVersion = "file_edit_proposal.v1"
	FileEditRecoveryProtocolVersion = "file_edit_proposal_recovery.v1"
	fileEditSourceTTL               = 5 * time.Minute
	maxFileEditSources              = 128
)

type FileEditProposalStore interface {
	fileedit.Store
	GetRun(context.Context, string) (domain.Run, error)
	GetMission(context.Context, string) (domain.Mission, error)
	GetSession(context.Context, string) (session.Session, error)
	GetWorkspaceInfo(context.Context, string) (session.WorkspaceInfo, error)
}

type FileEditProposalSource struct {
	ProtocolVersion string
	RunID           string
	WorkspaceID     string
	Path            string
	Content         string
	ContentSHA256   string
	Handle          string
	ExpiresAt       time.Time
	Editable        bool
}

type CreateFileEditProposalRequest struct {
	Version      string
	RunID        string
	SourceHandle string
	ProposedText string
}

type CreateFileEditProposalResult struct {
	Edit        fileedit.Edit
	Replayed    bool
	RunTerminal bool
}

// FileEditProposalRecovery is a read-only projection of one durable pending
// proposal. It intentionally carries no source handle: recovery can restore
// review context, but it cannot mutate, approve, or apply the stored proposal.
type FileEditProposalRecovery struct {
	ProtocolVersion    string
	RunID              string
	WorkspaceID        string
	EditID             string
	Path               string
	OriginalContent    string
	ProposedContent    string
	OriginalSHA256     string
	ProposedSHA256     string
	CurrentContentHash string
	Status             string
	Stale              bool
	ReviewRequired     bool
	Editable           bool
}

type fileEditSourceGrant struct {
	runID        string
	sessionID    string
	workspaceID  string
	drydockID    string
	generation   int64
	path         string
	originalHash string
	expiresAt    time.Time
	requestHash  string
	editID       string
	inFlight     bool
}

// FileEditProposalService is the only interactive editor write boundary.
// Renderer-controlled paths are accepted only by IssueSource, which is a
// read/issuance operation. Propose accepts an opaque, short-lived Go handle
// and can create only a pending FileEdit; it cannot approve or apply one.
type FileEditProposalService struct {
	store    FileEditProposalStore
	manager  *fileedit.Manager
	checker  policy.Checker
	drydocks *RunWorktreeService
	now      func() time.Time
	random   func([]byte) error
	mu       sync.Mutex
	sources  map[string]fileEditSourceGrant
}

func (s *FileEditProposalService) WithRunWorktree(drydocks *RunWorktreeService) *FileEditProposalService {
	if s != nil {
		s.drydocks = drydocks
	}
	return s
}

func NewFileEditProposalService(store FileEditProposalStore,
	checker policy.Checker,
) *FileEditProposalService {
	return &FileEditProposalService{store: store, manager: fileedit.NewManager(store),
		checker: checker, now: func() time.Time { return time.Now().UTC() },
		random:  func(value []byte) error { _, err := rand.Read(value); return err },
		sources: make(map[string]fileEditSourceGrant)}
}

func (s *FileEditProposalService) IssueSource(ctx context.Context, runID string,
	path string,
) (FileEditProposalSource, error) {
	return s.issueSource(ctx, runID, path, "")
}

// ReissueSource rotates an expired renderer handle only when the Workspace
// file still matches the digest previously issued by Go. The renderer draft
// is deliberately absent from this request.
func (s *FileEditProposalService) ReissueSource(ctx context.Context, runID string,
	path string, expectedSHA256 string,
) (FileEditProposalSource, error) {
	if !validSHA256Digest(expectedSHA256) {
		return FileEditProposalSource{}, apperror.New(apperror.CodeInvalidArgument,
			"file edit proposal reissue digest is invalid")
	}
	return s.issueSource(ctx, runID, path, expectedSHA256)
}

func (s *FileEditProposalService) issueSource(ctx context.Context, runID string,
	path string, expectedSHA256 string,
) (FileEditProposalSource, error) {
	if s == nil || s.store == nil || s.manager == nil || s.checker == nil ||
		s.now == nil || s.random == nil {
		return FileEditProposalSource{}, apperror.New(apperror.CodeFailedPrecondition,
			"file edit proposal dependencies are required")
	}
	if !validControlIdentity(runID) || !validProposalSourcePath(path) {
		return FileEditProposalSource{}, apperror.New(apperror.CodeInvalidArgument,
			"file edit proposal source request is invalid")
	}
	binding, err := s.loadBinding(ctx, runID)
	if err != nil {
		return FileEditProposalSource{}, err
	}
	if err := checkRunProjectPaths(binding.run, path); err != nil {
		return FileEditProposalSource{}, err
	}
	snapshot, err := workspace.Explore(binding.workspace.RootPath,
		binding.workspace.ID, path)
	if err != nil {
		return FileEditProposalSource{}, apperror.Normalize(err)
	}
	if snapshot.Kind != "file" || snapshot.Truncated || snapshot.RedactionCount != 0 ||
		snapshot.Path != path || snapshot.Content == "" && snapshot.TotalBytes != 0 {
		return FileEditProposalSource{}, apperror.New(apperror.CodeFailedPrecondition,
			"only complete unredacted UTF-8 Workspace files can be edited")
	}
	currentHash, err := fileedit.CurrentHash(binding.workspace.RootPath, path)
	if err != nil {
		return FileEditProposalSource{}, apperror.Normalize(err)
	}
	if currentHash != snapshot.Provenance.ContentSHA256 {
		return FileEditProposalSource{}, apperror.New(apperror.CodeConflict,
			"workspace file changed while the proposal source was issued")
	}
	if expectedSHA256 != "" && currentHash != expectedSHA256 {
		return FileEditProposalSource{}, apperror.New(apperror.CodeConflict,
			"workspace file changed since the editor source was issued")
	}
	raw := make([]byte, 32)
	if err := s.random(raw); err != nil {
		return FileEditProposalSource{}, apperror.Wrap(apperror.CodeInternal,
			"file edit proposal source could not be issued", err)
	}
	handle := base64.RawURLEncoding.EncodeToString(raw)
	digest := sourceHandleDigest(handle)
	expiresAt := s.now().UTC().Add(fileEditSourceTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(s.now().UTC())
	if len(s.sources) >= maxFileEditSources {
		return FileEditProposalSource{}, apperror.New(apperror.CodeResourceExhausted,
			"too many active file edit proposal sources")
	}
	if _, exists := s.sources[digest]; exists {
		return FileEditProposalSource{}, apperror.New(apperror.CodeInternal,
			"file edit proposal source collision")
	}
	grant := fileEditSourceGrant{runID: binding.run.ID,
		sessionID: binding.session.ID, workspaceID: binding.workspace.ID,
		path: snapshot.Path, originalHash: currentHash, expiresAt: expiresAt}
	if binding.target.Drydock != nil {
		grant.drydockID, grant.generation = binding.target.Drydock.ID, binding.target.Drydock.Generation
	}
	s.sources[digest] = grant
	return FileEditProposalSource{ProtocolVersion: FileEditProposalProtocolVersion,
		RunID: binding.run.ID, WorkspaceID: binding.workspace.ID, Path: snapshot.Path,
		Content: snapshot.Content, ContentSHA256: currentHash, Handle: handle,
		ExpiresAt: expiresAt, Editable: true}, nil
}

// Recover returns the exact persisted bodies for one pending proposal after
// checking their integrity and current Workspace binding. It never reopens the
// proposal for mutation and reports a changed or missing target as stale.
func (s *FileEditProposalService) Recover(ctx context.Context, runID string,
	editID string,
) (FileEditProposalRecovery, error) {
	if s == nil || s.store == nil || s.manager == nil || s.checker == nil {
		return FileEditProposalRecovery{}, apperror.New(
			apperror.CodeFailedPrecondition, "file edit proposal dependencies are required")
	}
	if !validControlIdentity(runID) || !validControlIdentity(editID) {
		return FileEditProposalRecovery{}, apperror.New(apperror.CodeInvalidArgument,
			"file edit proposal recovery identity is invalid")
	}
	binding, err := s.loadBinding(ctx, runID)
	if err != nil {
		return FileEditProposalRecovery{}, err
	}
	edit, err := s.store.GetFileEdit(ctx, editID)
	if err != nil {
		return FileEditProposalRecovery{}, apperror.Normalize(err)
	}
	if edit.SessionID != binding.session.ID || edit.WorkspaceID != binding.workspace.ID {
		return FileEditProposalRecovery{}, apperror.New(apperror.CodeNotFound,
			"file edit proposal does not belong to the requested Run")
	}
	if edit.Status != fileedit.StatusProposed {
		return FileEditProposalRecovery{}, apperror.New(apperror.CodeConflict,
			"only a pending file edit proposal can be recovered")
	}
	if edit.SecretsRedacted || !validPersistedOriginal(edit.OriginalHash, edit.OriginalText) ||
		!validSHA256Digest(edit.ProposedHash) ||
		fileedit.HashText(edit.ProposedText) != edit.ProposedHash {
		return FileEditProposalRecovery{}, apperror.New(apperror.CodeFailedPrecondition,
			"file edit proposal bodies cannot be recovered safely")
	}
	if err := checkRunProjectPaths(binding.run, edit.Path, edit.DestinationPath); err != nil {
		return FileEditProposalRecovery{}, err
	}
	currentHash, err := fileedit.CurrentHash(binding.workspace.RootPath, edit.Path)
	if err != nil {
		return FileEditProposalRecovery{}, apperror.Normalize(err)
	}
	return FileEditProposalRecovery{
		ProtocolVersion: FileEditRecoveryProtocolVersion,
		RunID:           runID, WorkspaceID: edit.WorkspaceID, EditID: edit.ID,
		Path: edit.Path, OriginalContent: edit.OriginalText,
		ProposedContent: edit.ProposedText, OriginalSHA256: edit.OriginalHash,
		ProposedSHA256: edit.ProposedHash, CurrentContentHash: currentHash,
		Status: edit.Status, Stale: currentHash != edit.OriginalHash,
		ReviewRequired: true, Editable: false,
	}, nil
}

func (s *FileEditProposalService) Propose(ctx context.Context,
	request CreateFileEditProposalRequest,
) (CreateFileEditProposalResult, error) {
	if s == nil || s.store == nil || s.manager == nil || s.checker == nil ||
		s.now == nil || s.sources == nil {
		return CreateFileEditProposalResult{}, apperror.New(
			apperror.CodeFailedPrecondition, "file edit proposal dependencies are required")
	}
	if request.Version != FileEditProposalProtocolVersion ||
		!validControlIdentity(request.RunID) || !validSourceHandle(request.SourceHandle) ||
		!validProposedText(request.ProposedText) {
		return CreateFileEditProposalResult{}, apperror.New(apperror.CodeInvalidArgument,
			"file edit proposal request is invalid")
	}
	if redact.String(request.ProposedText) != request.ProposedText {
		return CreateFileEditProposalResult{}, apperror.New(apperror.CodePolicyDenied,
			"file edit proposal content contains secret-like material")
	}
	digest := sourceHandleDigest(request.SourceHandle)
	requestHash := textDigest(request.ProposedText)
	now := s.now().UTC()
	s.mu.Lock()
	s.pruneLocked(now)
	grant, found := s.sources[digest]
	if !found || !now.Before(grant.expiresAt) || grant.runID != request.RunID {
		s.mu.Unlock()
		return CreateFileEditProposalResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"file edit proposal source is unavailable or expired")
	}
	if grant.requestHash != "" && grant.requestHash != requestHash {
		s.mu.Unlock()
		return CreateFileEditProposalResult{}, apperror.New(apperror.CodeConflict,
			"file edit proposal source was already used for different content")
	}
	if grant.inFlight {
		s.mu.Unlock()
		return CreateFileEditProposalResult{}, apperror.New(apperror.CodeConflict,
			"file edit proposal source is already being consumed")
	}
	if grant.requestHash == "" {
		grant.requestHash = requestHash
		grant.editID = idgen.New("edit")
	}
	grant.inFlight = true
	s.sources[digest] = grant
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		latest, exists := s.sources[digest]
		if exists && latest.editID == grant.editID && latest.requestHash == requestHash {
			latest.inFlight = false
			s.sources[digest] = latest
		}
		s.mu.Unlock()
	}()
	if existing, getErr := s.store.GetFileEdit(ctx, grant.editID); getErr == nil {
		if err := validateProposalResult(existing, grant, requestHash); err != nil {
			return CreateFileEditProposalResult{}, err
		}
		return CreateFileEditProposalResult{Edit: existing, Replayed: true}, nil
	} else if apperror.CodeOf(apperror.Normalize(getErr)) != apperror.CodeNotFound {
		return CreateFileEditProposalResult{}, apperror.Normalize(getErr)
	}
	binding, err := s.loadBinding(ctx, request.RunID)
	if err != nil {
		return CreateFileEditProposalResult{}, err
	}
	if err := checkRunProjectPaths(binding.run, grant.path); err != nil {
		return CreateFileEditProposalResult{}, err
	}
	if binding.session.ID != grant.sessionID || binding.workspace.ID != grant.workspaceID {
		return CreateFileEditProposalResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"file edit proposal source no longer matches the Run binding")
	}
	if grant.drydockID != "" && (binding.target.Drydock == nil ||
		binding.target.Drydock.ID != grant.drydockID || binding.target.Drydock.Generation != grant.generation) {
		return CreateFileEditProposalResult{}, apperror.New(apperror.CodeConflict,
			"file edit proposal source belongs to an earlier Drydock generation")
	}
	decision := s.checker.CheckToolCall(tools.Call{Name: "replace_file",
		Args:       map[string]string{"path": grant.path, "content": request.ProposedText},
		WorkingDir: binding.workspace.RootPath})
	if !decision.Allowed {
		return CreateFileEditProposalResult{}, apperror.New(apperror.CodePolicyDenied,
			decision.Reason)
	}
	edit, err := s.manager.Propose(ctx, fileedit.Proposal{ID: grant.editID,
		SessionID:   grant.sessionID,
		WorkspaceID: grant.workspaceID, WorkspaceRoot: binding.workspace.RootPath,
		Path: grant.path, ProposedText: request.ProposedText,
		ExpectedOriginalHash: grant.originalHash})
	if err != nil {
		return CreateFileEditProposalResult{}, apperror.Normalize(err)
	}
	if err := validateProposalResult(edit, grant, requestHash); err != nil {
		return CreateFileEditProposalResult{}, err
	}
	return CreateFileEditProposalResult{Edit: edit}, nil
}

func validateProposalResult(edit fileedit.Edit, grant fileEditSourceGrant,
	requestHash string,
) error {
	if edit.ID != grant.editID || edit.SessionID != grant.sessionID ||
		edit.WorkspaceID != grant.workspaceID ||
		edit.Path != grant.path || edit.OriginalHash != grant.originalHash ||
		edit.ProposedHash != requestHash {
		return apperror.New(apperror.CodeInternal,
			"file edit proposal result violated its exact source binding")
	}
	if edit.Status != fileedit.StatusProposed {
		return apperror.New(apperror.CodeConflict,
			"file edit proposal is no longer pending review")
	}
	return nil
}

type fileEditProposalBinding struct {
	run       domain.Run
	mission   domain.Mission
	session   session.Session
	workspace session.WorkspaceInfo
	target    RunFileWorkspace
}

func (s *FileEditProposalService) loadBinding(ctx context.Context,
	runID string,
) (fileEditProposalBinding, error) {
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return fileEditProposalBinding{}, apperror.Normalize(err)
	}
	if run.Status != domain.RunRunning || !validControlIdentity(run.SessionID) {
		return fileEditProposalBinding{}, apperror.New(apperror.CodeFailedPrecondition,
			"file edit proposals require a running Session-bound Run")
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return fileEditProposalBinding{}, apperror.Normalize(err)
	}
	linkedSession, err := s.store.GetSession(ctx, run.SessionID)
	if err != nil {
		return fileEditProposalBinding{}, apperror.Normalize(err)
	}
	if linkedSession.Status != session.StatusActive ||
		linkedSession.WorkspaceID != mission.WorkspaceID {
		return fileEditProposalBinding{}, apperror.New(apperror.CodeFailedPrecondition,
			"file edit proposal Session binding is inactive or inconsistent")
	}
	target, err := ResolveRunFileWorkspace(ctx, s.store, run, mission, s.drydocks)
	if err != nil {
		return fileEditProposalBinding{}, apperror.Normalize(err)
	}
	return fileEditProposalBinding{run: run, mission: mission,
		session: linkedSession, workspace: target.Workspace, target: target}, nil
}

func (s *FileEditProposalService) pruneLocked(now time.Time) {
	for digest, grant := range s.sources {
		if !now.Before(grant.expiresAt) {
			delete(s.sources, digest)
		}
	}
}

func validProposalSourcePath(value string) bool {
	if value == "" || value != strings.TrimSpace(value) ||
		utf8.RuneCountInString(value) > workspace.MaxExplorerPathRunes ||
		strings.ContainsRune(value, 0) {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return false
		}
	}
	return true
}

func validSourceHandle(value string) bool {
	if len(value) != 43 || value != strings.TrimSpace(value) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(raw) == 32
}

func validProposedText(value string) bool {
	return utf8.ValidString(value) && len([]byte(value)) <= fileedit.MaxContentBytes
}

func sourceHandleDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func textDigest(value string) string {
	return fileedit.HashText(value)
}

func validSHA256Digest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validPersistedOriginal(hash string, content string) bool {
	if hash == "missing" {
		return content == ""
	}
	return validSHA256Digest(hash) && fileedit.HashText(content) == hash
}
