package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/codeintel"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/githubreview"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/standardcodedelivery"
	"cyberagent-workbench/internal/toolcontract"
)

const GitHubReviewAPIProtocolVersion = "github-review-api.v1"

type GitHubReviewStore interface {
	GetRun(context.Context, string) (domain.Run, error)
	GetMission(context.Context, string) (domain.Mission, error)
	GetSession(context.Context, string) (session.Session, error)
	GetWorkspaceByID(context.Context, string) (session.WorkspaceRecord, error)
	GetRunMode(context.Context, string) (domain.RunModeSnapshot, error)
	GetRunExecutionPermission(context.Context, string) (domain.RunExecutionPermissionSnapshot, error)

	PutGitHubReviewConnection(context.Context, githubreview.Connection, int64) (
		githubreview.Connection, bool, error)
	GetGitHubReviewConnection(context.Context, string) (githubreview.Connection, bool, error)
	GetGitHubReviewConnectionByRepository(context.Context, string) (
		githubreview.Connection, bool, error)
	ListGitHubReviewConnections(context.Context, bool) ([]githubreview.Connection, error)
	SaveGitHubReviewSnapshot(context.Context, string, githubreview.Snapshot) (
		githubreview.Snapshot, bool, error)
	GetGitHubReviewSnapshot(context.Context, string) (githubreview.Snapshot, bool, error)
	ListGitHubReviewSnapshots(context.Context, string, int64, int) ([]githubreview.Snapshot, error)
	SaveGitHubReviewEvidence(context.Context, githubreview.EvidenceRecord) (
		githubreview.EvidenceRecord, bool, error)
	GetGitHubReviewEvidence(context.Context, string) (githubreview.EvidenceRecord, bool, error)
	ListGitHubReviewEvidence(context.Context, string, int) ([]githubreview.EvidenceRecord, error)
	CreateGitHubReviewWrite(context.Context, githubreview.WriteRecord) (
		githubreview.WriteRecord, bool, error)
	StartGitHubReviewWrite(context.Context, string, string, string, time.Time) (
		githubreview.WriteRecord, bool, error)
	CompleteGitHubReviewWrite(context.Context, string, githubreview.WriteReceipt, time.Time) (
		githubreview.WriteRecord, bool, error)
	GetGitHubReviewWrite(context.Context, string) (githubreview.WriteRecord, bool, error)
	ListGitHubReviewWrites(context.Context, string, githubreview.OperationStatus, int) (
		[]githubreview.WriteRecord, error)
	ListRunningGitHubReviewWrites(context.Context, int) ([]githubreview.WriteRecord, error)

	EnsureApproval(context.Context, approval.Proposal) (approval.Record, error)
	GetApproval(context.Context, string) (approval.Record, error)
}

type githubReviewRemote interface {
	Qualify(context.Context, githubreview.RepositoryIdentity, int64,
		githubreview.CredentialReference) (githubreview.Qualification, error)
	ReadSnapshot(context.Context, githubreview.SnapshotRequest) (githubreview.Snapshot, error)
	ExecuteWrite(context.Context, githubreview.WriteSpec, githubreview.WritePreview, ...toolcontract.DispatchGuard) (
		githubreview.WriteReceipt, error)
	RecoverWrite(context.Context, githubreview.WriteSpec, githubreview.WritePreview) (
		githubreview.WriteReceipt, error)
}

type githubReviewClientFactory func(*githubreview.AuthManager,
	githubreview.Connection) (githubReviewRemote, error)

type githubReviewClientEntry struct {
	generation int64
	auth       *githubreview.AuthManager
	client     githubReviewRemote
}

type GitHubReviewService struct {
	store                  GitHubReviewStore
	credentials            credential.Store
	authLifecycle          *githubreview.AuthLifecycle
	executor               *repository.AdvancedExecutor
	permissionCapabilities domain.ExecutionPermissionRuntimeCapabilities
	now                    func() time.Time
	clientFactory          githubReviewClientFactory
	mu                     sync.Mutex
	clients                map[string]githubReviewClientEntry
	delivery               StandardCodeDeliveryProjector
}

func (s *GitHubReviewService) WithStandardCodeDelivery(
	delivery StandardCodeDeliveryProjector,
) *GitHubReviewService {
	if s != nil {
		s.delivery = delivery
	}
	return s
}

func NewGitHubReviewService(store GitHubReviewStore, credentials credential.Store,
	executor *repository.AdvancedExecutor,
	permissionCapabilities domain.ExecutionPermissionRuntimeCapabilities,
) (*GitHubReviewService, error) {
	if store == nil || credentials == nil || !credentials.Available() || executor == nil ||
		!executor.Available() || permissionCapabilities.Validate() != nil ||
		!permissionCapabilities.OperatorApprovalEnabled {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"GitHub review service requires Git, system credential storage, and operator approval")
	}
	lifecycle, err := githubreview.NewAuthLifecycle(credentials)
	if err != nil {
		return nil, githubReviewApplicationError(err)
	}
	service := &GitHubReviewService{store: store, credentials: credentials, authLifecycle: lifecycle,
		executor: executor, permissionCapabilities: permissionCapabilities,
		now:     func() time.Time { return time.Now().UTC() },
		clients: make(map[string]githubReviewClientEntry)}
	service.clientFactory = func(auth *githubreview.AuthManager,
		connection githubreview.Connection,
	) (githubReviewRemote, error) {
		return githubreview.NewClient(auth, connection.Network)
	}
	return service, nil
}

type GitHubReviewConfigureRequest struct {
	ProtocolVersion    string                           `json:"protocol_version"`
	ConnectionID       string                           `json:"connection_id,omitempty"`
	Repository         githubreview.RepositoryIdentity  `json:"repository"`
	Credential         githubreview.CredentialReference `json:"credential"`
	ClientID           string                           `json:"client_id,omitempty"`
	AllowedLogHosts    []string                         `json:"allowed_log_hosts"`
	WriteEnabled       bool                             `json:"write_enabled"`
	Enabled            bool                             `json:"enabled"`
	ExpectedGeneration int64                            `json:"expected_generation"`
	RequestedBy        string                           `json:"requested_by"`
}

type GitHubReviewConfigureResult struct {
	ProtocolVersion string                  `json:"protocol_version"`
	Connection      githubreview.Connection `json:"connection"`
	Replayed        bool                    `json:"replayed"`
}

func (s *GitHubReviewService) Configure(ctx context.Context,
	request GitHubReviewConfigureRequest,
) (GitHubReviewConfigureResult, error) {
	request.ConnectionID = strings.TrimSpace(request.ConnectionID)
	request.ClientID = strings.TrimSpace(request.ClientID)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	if s == nil || s.store == nil || s.now == nil ||
		request.ProtocolVersion != GitHubReviewAPIProtocolVersion ||
		request.Repository.Validate() != nil || request.Credential.Validate() != nil ||
		request.ExpectedGeneration < 0 || request.RequestedBy == "" {
		return GitHubReviewConfigureResult{}, apperror.New(apperror.CodeInvalidArgument,
			"GitHub review connection request is invalid")
	}
	existing, found, err := s.store.GetGitHubReviewConnectionByRepository(ctx,
		request.Repository.FullName)
	if err != nil {
		return GitHubReviewConfigureResult{}, apperror.Normalize(err)
	}
	now := s.now().UTC()
	connection := githubreview.Connection{ProtocolVersion: githubreview.ConnectionProtocolVersion,
		ID: request.ConnectionID, Repository: request.Repository,
		Credential: request.Credential, ClientID: request.ClientID,
		Network: githubreview.NetworkScope{Host: "github.com", APIHost: "api.github.com",
			OAuthHost: "github.com", AllowedLogHosts: append([]string(nil), request.AllowedLogHosts...),
			ReadEnabled: true, WriteEnabled: request.WriteEnabled},
		Enabled: request.Enabled, Generation: 1, CreatedAt: now, UpdatedAt: now}
	connection.Normalize()
	if found {
		if connection.ID != "" && connection.ID != existing.ID {
			return GitHubReviewConfigureResult{}, apperror.New(apperror.CodeConflict,
				"GitHub review repository is already bound to another connection identity")
		}
		connection.ID = existing.ID
		connection.Generation = existing.Generation + 1
		connection.CreatedAt = existing.CreatedAt
		if sameGitHubReviewConnectionSettings(existing, connection) {
			return GitHubReviewConfigureResult{ProtocolVersion: GitHubReviewAPIProtocolVersion,
				Connection: existing, Replayed: true}, nil
		}
	}
	if connection.ID == "" {
		connection.ID = idgen.New("github-review-connection")
	}
	if err := connection.Validate(); err != nil {
		return GitHubReviewConfigureResult{}, apperror.Wrap(apperror.CodeInvalidArgument,
			"GitHub review connection is invalid", err)
	}
	stored, replayed, err := s.store.PutGitHubReviewConnection(ctx, connection,
		request.ExpectedGeneration)
	if err != nil {
		return GitHubReviewConfigureResult{}, apperror.Normalize(err)
	}
	s.mu.Lock()
	delete(s.clients, stored.ID)
	s.mu.Unlock()
	return GitHubReviewConfigureResult{ProtocolVersion: GitHubReviewAPIProtocolVersion,
		Connection: stored, Replayed: replayed}, nil
}

func sameGitHubReviewConnectionSettings(left, right githubreview.Connection) bool {
	return left.ID == right.ID && left.Repository == right.Repository &&
		left.Credential == right.Credential && left.ClientID == right.ClientID &&
		left.Network.Host == right.Network.Host && left.Network.APIHost == right.Network.APIHost &&
		left.Network.OAuthHost == right.Network.OAuthHost &&
		left.Network.ReadEnabled == right.Network.ReadEnabled &&
		left.Network.WriteEnabled == right.Network.WriteEnabled &&
		slices.Equal(left.Network.AllowedLogHosts, right.Network.AllowedLogHosts) &&
		left.Enabled == right.Enabled
}

type GitHubReviewCredentialView struct {
	ProtocolVersion string                        `json:"protocol_version"`
	Connection      githubreview.Connection       `json:"connection"`
	Credential      githubreview.CredentialStatus `json:"credential"`
}

func (s *GitHubReviewService) ListConnections(ctx context.Context,
	enabledOnly bool,
) ([]GitHubReviewCredentialView, error) {
	if s == nil || s.store == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"GitHub review service is unavailable")
	}
	connections, err := s.store.ListGitHubReviewConnections(ctx, enabledOnly)
	if err != nil {
		return nil, apperror.Normalize(err)
	}
	result := make([]GitHubReviewCredentialView, 0, len(connections))
	for _, connection := range connections {
		_, auth, _, loadErr := s.loadClient(ctx, connection.ID, false)
		if loadErr != nil {
			return nil, loadErr
		}
		status, statusErr := auth.Status(ctx, connection.Credential)
		if statusErr != nil {
			return nil, githubReviewApplicationError(statusErr)
		}
		result = append(result, GitHubReviewCredentialView{
			ProtocolVersion: GitHubReviewAPIProtocolVersion,
			Connection:      connection, Credential: status})
	}
	return result, nil
}

func (s *GitHubReviewService) BeginDeviceFlow(ctx context.Context,
	connectionID string,
) (githubreview.DeviceAuthorization, error) {
	connection, auth, _, err := s.loadClient(ctx, connectionID, true)
	if err != nil {
		return githubreview.DeviceAuthorization{}, err
	}
	if connection.Credential.Kind != githubreview.AuthGitHubAppDevice {
		return githubreview.DeviceAuthorization{}, apperror.New(apperror.CodeFailedPrecondition,
			"GitHub device authorization requires a GitHub App connection")
	}
	value, err := auth.BeginDeviceFlow(ctx, connection.Credential)
	return value, githubReviewApplicationError(err)
}

func (s *GitHubReviewService) PollDeviceFlow(ctx context.Context,
	connectionID, sessionID string,
) (githubreview.DevicePollResult, error) {
	connection, auth, _, err := s.loadClient(ctx, connectionID, true)
	if err != nil {
		return githubreview.DevicePollResult{}, err
	}
	if connection.Credential.Kind != githubreview.AuthGitHubAppDevice {
		return githubreview.DevicePollResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"GitHub device authorization requires a GitHub App connection")
	}
	value, err := auth.PollDeviceFlow(ctx, strings.TrimSpace(sessionID))
	return value, githubReviewApplicationError(err)
}

func (s *GitHubReviewService) CredentialStatus(ctx context.Context,
	connectionID string,
) (GitHubReviewCredentialView, error) {
	connection, auth, _, err := s.loadClient(ctx, connectionID, false)
	if err != nil {
		return GitHubReviewCredentialView{}, err
	}
	status, err := auth.Status(ctx, connection.Credential)
	if err != nil {
		return GitHubReviewCredentialView{}, githubReviewApplicationError(err)
	}
	return GitHubReviewCredentialView{ProtocolVersion: GitHubReviewAPIProtocolVersion,
		Connection: connection, Credential: status}, nil
}

func (s *GitHubReviewService) Disconnect(ctx context.Context,
	connectionID string,
) (GitHubReviewCredentialView, error) {
	connection, auth, _, err := s.loadClient(ctx, connectionID, false)
	if err != nil {
		return GitHubReviewCredentialView{}, err
	}
	if err := auth.Disconnect(ctx, connection.Credential); err != nil {
		return GitHubReviewCredentialView{}, githubReviewApplicationError(err)
	}
	return s.CredentialStatus(ctx, connection.ID)
}

type GitHubReviewQualificationResult struct {
	ProtocolVersion string                     `json:"protocol_version"`
	Connection      githubreview.Connection    `json:"connection"`
	Qualification   githubreview.Qualification `json:"qualification"`
}

func (s *GitHubReviewService) Qualify(ctx context.Context, connectionID string,
	prNumber int64,
) (GitHubReviewQualificationResult, error) {
	connection, _, client, err := s.loadClient(ctx, connectionID, true)
	if err != nil {
		return GitHubReviewQualificationResult{}, err
	}
	value, err := client.Qualify(ctx, connection.Repository, prNumber,
		connection.Credential)
	if err != nil {
		return GitHubReviewQualificationResult{}, githubReviewApplicationError(err)
	}
	return GitHubReviewQualificationResult{ProtocolVersion: GitHubReviewAPIProtocolVersion,
		Connection: connection, Qualification: value}, nil
}

type GitHubReviewFetchRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	ConnectionID    string `json:"connection_id"`
	PullRequest     int64  `json:"pull_request"`
}

type GitHubReviewFetchResult struct {
	ProtocolVersion string                `json:"protocol_version"`
	Snapshot        githubreview.Snapshot `json:"snapshot"`
	Replayed        bool                  `json:"replayed"`
}

func (s *GitHubReviewService) Fetch(ctx context.Context,
	request GitHubReviewFetchRequest,
) (GitHubReviewFetchResult, error) {
	if request.ProtocolVersion != GitHubReviewAPIProtocolVersion ||
		strings.TrimSpace(request.ConnectionID) == "" || request.PullRequest <= 0 {
		return GitHubReviewFetchResult{}, apperror.New(apperror.CodeInvalidArgument,
			"GitHub review fetch request is invalid")
	}
	connection, _, client, err := s.loadClient(ctx, request.ConnectionID, true)
	if err != nil {
		return GitHubReviewFetchResult{}, err
	}
	qualification, err := client.Qualify(ctx, connection.Repository,
		request.PullRequest, connection.Credential)
	if err != nil {
		return GitHubReviewFetchResult{}, githubReviewApplicationError(err)
	}
	if !qualification.Eligible || qualification.Capability.Validate() != nil {
		return GitHubReviewFetchResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"GitHub review qualification is not eligible for this repository and pull request")
	}
	snapshot, err := client.ReadSnapshot(ctx, githubreview.SnapshotRequest{
		Repository: connection.Repository, Number: request.PullRequest,
		Credential: connection.Credential, Capability: qualification.Capability})
	if err != nil {
		return GitHubReviewFetchResult{}, githubReviewApplicationError(err)
	}
	stored, replayed, err := s.store.SaveGitHubReviewSnapshot(ctx, connection.ID, snapshot)
	if err != nil {
		return GitHubReviewFetchResult{}, apperror.Normalize(err)
	}
	return GitHubReviewFetchResult{ProtocolVersion: GitHubReviewAPIProtocolVersion,
		Snapshot: stored, Replayed: replayed}, nil
}

type GitHubReviewEvidenceRequest struct {
	ProtocolVersion string                        `json:"protocol_version"`
	RunID           string                        `json:"run_id"`
	SnapshotID      string                        `json:"snapshot_id"`
	Semantic        map[string][]codeintel.Result `json:"-"`
}

type GitHubReviewEvidenceResult struct {
	ProtocolVersion string                      `json:"protocol_version"`
	Evidence        githubreview.EvidenceRecord `json:"evidence"`
	Replayed        bool                        `json:"replayed"`
}

func (s *GitHubReviewService) BuildEvidence(ctx context.Context,
	request GitHubReviewEvidenceRequest,
) (GitHubReviewEvidenceResult, error) {
	if request.ProtocolVersion != GitHubReviewAPIProtocolVersion ||
		strings.TrimSpace(request.RunID) == "" || strings.TrimSpace(request.SnapshotID) == "" {
		return GitHubReviewEvidenceResult{}, apperror.New(apperror.CodeInvalidArgument,
			"GitHub review evidence request is invalid")
	}
	authority, err := s.loadRunBinding(ctx, request.RunID, false)
	if err != nil {
		return GitHubReviewEvidenceResult{}, err
	}
	snapshot, found, err := s.store.GetGitHubReviewSnapshot(ctx, request.SnapshotID)
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeNotFound, "GitHub review snapshot was not found")
		}
		return GitHubReviewEvidenceResult{}, apperror.Normalize(err)
	}
	diff, err := s.executor.CaptureReviewDiffEvidence(ctx, authority.workspace.RootPath,
		snapshot.Identity.BaseSHA, snapshot.Identity.HeadSHA)
	if err != nil {
		return GitHubReviewEvidenceResult{}, gitAdvancedApplicationError(err)
	}
	graph, err := githubreview.BuildEvidenceGraph(snapshot, diff,
		githubReviewSemanticResults(request.Semantic),
		s.now().UTC())
	if err != nil {
		return GitHubReviewEvidenceResult{}, apperror.Wrap(apperror.CodeFailedPrecondition,
			"build GitHub review evidence graph", err)
	}
	recordFingerprint := githubreview.Fingerprint("github-review-evidence-record",
		authority.run.ID, authority.workspace.ID, graph.Fingerprint)
	record := githubreview.EvidenceRecord{ID: "ghg-" + recordFingerprint[:32],
		RunID: authority.run.ID, WorkspaceID: authority.workspace.ID, Graph: graph}
	stored, replayed, err := s.store.SaveGitHubReviewEvidence(ctx, record)
	if err != nil {
		return GitHubReviewEvidenceResult{}, apperror.Normalize(err)
	}
	return GitHubReviewEvidenceResult{ProtocolVersion: GitHubReviewAPIProtocolVersion,
		Evidence: stored, Replayed: replayed}, nil
}

func githubReviewSemanticResults(input map[string][]codeintel.Result) map[string][]githubreview.SemanticResult {
	if len(input) == 0 {
		return nil
	}
	result := make(map[string][]githubreview.SemanticResult, len(input))
	for path, observations := range input {
		converted := make([]githubreview.SemanticResult, 0, len(observations))
		for _, observation := range observations {
			state := githubreview.EvidenceUnavailable
			switch observation.State {
			case codeintel.EvidenceCurrent:
				state = githubreview.EvidenceVerified
			case codeintel.EvidencePartial:
				state = githubreview.EvidencePartial
			case codeintel.EvidenceStale:
				state = githubreview.EvidenceStale
			case codeintel.EvidenceUnavailable:
				state = githubreview.EvidenceUnavailable
			}
			value := githubreview.SemanticResult{Valid: observation.Validate() == nil,
				State: state, Tool: observation.Tool,
				Commit:                observation.Provenance.Commit,
				DocumentSHA256:        observation.Provenance.DocumentSHA256,
				ServerGeneration:      observation.Provenance.ServerGeneration,
				CapabilityFingerprint: observation.Provenance.CapabilityFingerprint,
				QueryFingerprint:      observation.Provenance.QueryFingerprint,
				Warnings:              append([]string(nil), observation.Warnings...),
				Items:                 make([]githubreview.SemanticItem, 0, len(observation.Items))}
			for _, item := range observation.Items {
				semanticItem := githubreview.SemanticItem{Path: item.Path, Name: item.Name,
					Relationship: item.Relationship}
				if item.Range != nil {
					semanticItem.HasRange = true
					semanticItem.StartLine = item.Range.Start.Line
					semanticItem.StartCharacter = item.Range.Start.Character
				}
				value.Items = append(value.Items, semanticItem)
			}
			converted = append(converted, value)
		}
		result[path] = converted
	}
	return result
}

type GitHubReviewWriteReviewRequest struct {
	ProtocolVersion string                 `json:"protocol_version"`
	RunID           string                 `json:"run_id"`
	ConnectionID    string                 `json:"connection_id"`
	SnapshotID      string                 `json:"snapshot_id"`
	OperationKey    string                 `json:"operation_key"`
	RequestedBy     string                 `json:"requested_by"`
	Spec            githubreview.WriteSpec `json:"spec"`
}

type GitHubReviewWriteReviewResult struct {
	ProtocolVersion string                    `json:"protocol_version"`
	Preview         githubreview.WritePreview `json:"preview"`
	Operation       githubreview.WriteRecord  `json:"operation"`
	Approval        approval.Record           `json:"approval"`
	Replayed        bool                      `json:"replayed"`
}

func (s *GitHubReviewService) ReviewWrite(ctx context.Context,
	request GitHubReviewWriteReviewRequest,
) (GitHubReviewWriteReviewResult, error) {
	request.RunID = strings.TrimSpace(request.RunID)
	request.ConnectionID = strings.TrimSpace(request.ConnectionID)
	request.SnapshotID = strings.TrimSpace(request.SnapshotID)
	request.OperationKey = strings.TrimSpace(request.OperationKey)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	request.Spec.Normalize()
	if request.ProtocolVersion != GitHubReviewAPIProtocolVersion || request.RunID == "" ||
		request.ConnectionID == "" || request.SnapshotID == "" || request.OperationKey == "" ||
		request.RequestedBy == "" || request.Spec.Validate() != nil {
		return GitHubReviewWriteReviewResult{}, apperror.New(apperror.CodeInvalidArgument,
			"GitHub review write review request is invalid")
	}
	authority, err := s.loadRunBinding(ctx, request.RunID, true)
	if err != nil {
		return GitHubReviewWriteReviewResult{}, err
	}
	connection, _, client, err := s.loadClient(ctx, request.ConnectionID, true)
	if err != nil {
		return GitHubReviewWriteReviewResult{}, err
	}
	if !connection.Network.WriteEnabled {
		return GitHubReviewWriteReviewResult{}, apperror.New(apperror.CodePolicyDenied,
			"GitHub review write-back is disabled for this connection")
	}
	snapshot, found, err := s.store.GetGitHubReviewSnapshot(ctx, request.SnapshotID)
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeNotFound, "GitHub review snapshot was not found")
		}
		return GitHubReviewWriteReviewResult{}, apperror.Normalize(err)
	}
	if snapshot.Identity.Repository.FullName != connection.Repository.FullName ||
		request.Spec.Identity != snapshot.Identity || request.Spec.Credential != connection.Credential ||
		request.Spec.CapabilityGeneration != snapshot.Capability.Generation {
		return GitHubReviewWriteReviewResult{}, apperror.New(apperror.CodeConflict,
			"GitHub review write does not match the persisted snapshot and connection")
	}
	qualification, err := client.Qualify(ctx, connection.Repository,
		snapshot.Identity.Number, connection.Credential)
	if err != nil {
		return GitHubReviewWriteReviewResult{}, githubReviewApplicationError(err)
	}
	if !qualification.Eligible ||
		qualification.Capability.Generation != request.Spec.CapabilityGeneration ||
		!capabilityAllowsWrite(qualification.Capability, request.Spec.Operation) {
		return GitHubReviewWriteReviewResult{}, apperror.New(apperror.CodePolicyDenied,
			"GitHub review write capability is missing or changed")
	}
	preview, err := githubreview.NewWritePreview(request.Spec, s.now().UTC())
	if err != nil {
		return GitHubReviewWriteReviewResult{}, apperror.Wrap(apperror.CodeInvalidArgument,
			"GitHub review write preview is invalid", err)
	}
	approvalFingerprint, err := s.writeApprovalFingerprint(ctx, authority, connection, request.Spec, preview, true)
	if err != nil {
		return GitHubReviewWriteReviewResult{}, err
	}
	operationKey := runmutation.OperationKeyDigest("github_review_write.v1",
		authority.run.ID, request.OperationKey)
	record := githubreview.WriteRecord{ID: idgen.New("github-review-write"),
		ProtocolVersion:    githubreview.WriteProtocolVersion,
		OperationKeySHA256: operationKey,
		RequestFingerprint: githubreview.Fingerprint("github-review-write-request",
			authority.run.ID, authority.workspace.ID, connection.ID,
			fmt.Sprint(connection.Generation), snapshot.ID, preview.ID, request.RequestedBy),
		ApprovalFingerprint: approvalFingerprint,
		RunID:               authority.run.ID, SessionID: authority.session.ID,
		WorkspaceID: authority.workspace.ID, ConnectionID: connection.ID,
		Preview: preview, Spec: request.Spec, Status: githubreview.OperationProposed,
		CreatedAt: s.now().UTC()}
	record, replayed, err := s.store.CreateGitHubReviewWrite(ctx, record)
	if err != nil {
		return GitHubReviewWriteReviewResult{}, apperror.Normalize(err)
	}
	if replayed && record.ApprovalID != "" {
		approvalRecord, approvalErr := s.store.GetApproval(ctx, record.ApprovalID)
		if approvalErr != nil {
			return GitHubReviewWriteReviewResult{}, apperror.Normalize(approvalErr)
		}
		if approvalRecord.Status != approval.StatusApproved || !githubReviewApprovalMatches(record, approvalRecord) {
			return GitHubReviewWriteReviewResult{}, apperror.New(apperror.CodeConflict,
				"replayed GitHub review write approval binding is invalid")
		}
		return GitHubReviewWriteReviewResult{ProtocolVersion: GitHubReviewAPIProtocolVersion,
			Preview: record.Preview, Operation: record, Approval: approvalRecord,
			Replayed: true}, nil
	}
	approvalRecord, err := s.store.EnsureApproval(ctx, approval.Proposal{
		IdempotencyKey: approval.ProposalIdempotencyKey(githubreview.ApprovalToolName,
			record.ID),
		ProposalID: record.ID, SessionID: authority.session.ID,
		WorkspaceID: authority.workspace.ID, ToolName: githubreview.ApprovalToolName,
		ActionClass: githubreview.ApprovalActionClass, Mode: "per_call",
		Status: approval.StatusPending, RequestFingerprint: record.ApprovalFingerprint,
		RequestedBy: request.RequestedBy, CreatedAt: record.CreatedAt,
		UpdatedAt: record.CreatedAt})
	if err != nil {
		return GitHubReviewWriteReviewResult{}, apperror.Normalize(err)
	}
	if approvalRecord.Status == approval.StatusDenied || !githubReviewApprovalMatches(record, approvalRecord) {
		return GitHubReviewWriteReviewResult{}, apperror.New(apperror.CodeConflict,
			"GitHub review write approval binding is invalid or denied")
	}
	return GitHubReviewWriteReviewResult{ProtocolVersion: GitHubReviewAPIProtocolVersion,
		Preview: preview, Operation: record, Approval: approvalRecord,
		Replayed: replayed}, nil
}

type GitHubReviewWriteExecuteRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	RunID           string `json:"run_id"`
	OperationID     string `json:"operation_id"`
	ApprovalID      string `json:"approval_id"`
	RequestedBy     string `json:"requested_by"`
}

type GitHubReviewWriteExecuteResult struct {
	ProtocolVersion string                    `json:"protocol_version"`
	Operation       githubreview.WriteRecord  `json:"operation"`
	Receipt         githubreview.WriteReceipt `json:"receipt"`
	Replayed        bool                      `json:"replayed"`
}

func (s *GitHubReviewService) ExecuteWrite(ctx context.Context,
	request GitHubReviewWriteExecuteRequest,
) (GitHubReviewWriteExecuteResult, error) {
	request.RunID = strings.TrimSpace(request.RunID)
	request.OperationID = strings.TrimSpace(request.OperationID)
	request.ApprovalID = strings.TrimSpace(request.ApprovalID)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	if request.ProtocolVersion != GitHubReviewAPIProtocolVersion || request.RunID == "" ||
		request.OperationID == "" || request.ApprovalID == "" || request.RequestedBy == "" {
		return GitHubReviewWriteExecuteResult{}, apperror.New(apperror.CodeInvalidArgument,
			"GitHub review write execution request is invalid")
	}
	record, found, err := s.store.GetGitHubReviewWrite(ctx, request.OperationID)
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeNotFound, "GitHub review write was not found")
		}
		return GitHubReviewWriteExecuteResult{}, apperror.Normalize(err)
	}
	if record.RunID != request.RunID || record.ApprovalID != "" &&
		record.ApprovalID != request.ApprovalID {
		return GitHubReviewWriteExecuteResult{}, apperror.New(apperror.CodeConflict,
			"GitHub review write does not belong to this exact request")
	}
	if record.Status.Terminal() {
		return GitHubReviewWriteExecuteResult{ProtocolVersion: GitHubReviewAPIProtocolVersion,
			Operation: record, Receipt: record.Receipt, Replayed: true}, nil
	}
	if record.Status == githubreview.OperationRunning {
		return GitHubReviewWriteExecuteResult{ProtocolVersion: GitHubReviewAPIProtocolVersion,
				Operation: record, Replayed: true}, apperror.New(apperror.CodeFailedPrecondition,
				"GitHub review write already began; use recovery instead of replaying it")
	}
	authority, err := s.loadRunBinding(ctx, request.RunID, true)
	if err != nil {
		return GitHubReviewWriteExecuteResult{}, err
	}
	if authority.session.ID != record.SessionID || authority.workspace.ID != record.WorkspaceID {
		return GitHubReviewWriteExecuteResult{}, apperror.New(apperror.CodeConflict,
			"GitHub review Run or Workspace binding changed after approval")
	}
	connection, _, client, err := s.loadClient(ctx, record.ConnectionID, true)
	if err != nil {
		return GitHubReviewWriteExecuteResult{}, err
	}
	if !connection.Network.WriteEnabled {
		return GitHubReviewWriteExecuteResult{}, apperror.New(apperror.CodePolicyDenied,
			"GitHub review write-back was disabled after approval")
	}
	if connection.Credential != record.Spec.Credential ||
		connection.Repository.FullName != record.Spec.Identity.Repository.FullName {
		return GitHubReviewWriteExecuteResult{}, apperror.New(apperror.CodeConflict,
			"GitHub review connection changed after approval")
	}
	approvalRecord, err := s.store.GetApproval(ctx, request.ApprovalID)
	if err != nil {
		return GitHubReviewWriteExecuteResult{}, apperror.Normalize(err)
	}
	if approvalRecord.Status != approval.StatusApproved || !githubReviewApprovalMatches(record, approvalRecord) {
		return GitHubReviewWriteExecuteResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"GitHub review write requires exact one-time approval")
	}
	guard, err := s.writeDispatchGuard(ctx, record, request.ApprovalID)
	if err != nil {
		return GitHubReviewWriteExecuteResult{}, err
	}
	qualification, err := client.Qualify(ctx, connection.Repository,
		record.Spec.Identity.Number, connection.Credential)
	if err != nil {
		return GitHubReviewWriteExecuteResult{}, githubReviewApplicationError(err)
	}
	if !qualification.Eligible ||
		qualification.Capability.Generation != record.Preview.CapabilityGeneration ||
		!capabilityAllowsWrite(qualification.Capability, record.Preview.Operation) {
		return GitHubReviewWriteExecuteResult{}, apperror.New(apperror.CodePolicyDenied,
			"GitHub review write capability was revoked or changed after approval")
	}
	record, startReplayed, err := s.store.StartGitHubReviewWrite(ctx, record.ID,
		approvalRecord.ID, record.ApprovalFingerprint, s.now().UTC())
	if err != nil {
		return GitHubReviewWriteExecuteResult{}, apperror.Normalize(err)
	}
	if startReplayed {
		return GitHubReviewWriteExecuteResult{ProtocolVersion: GitHubReviewAPIProtocolVersion,
				Operation: record, Receipt: record.Receipt, Replayed: true},
			apperror.New(apperror.CodeFailedPrecondition,
				"GitHub review write already began; use recovery instead of replaying it")
	}
	receipt, executeErr := client.ExecuteWrite(ctx, record.Spec, record.Preview, guard)
	dispatchState, hasDispatchState := githubreview.WriteDispatchState(executeErr)
	if executeErr != nil && ((hasDispatchState && dispatchState == toolcontract.ReceiptOutcomeUnknown) ||
		(!hasDispatchState && ambiguousGitHubWriteError(executeErr))) {
		return GitHubReviewWriteExecuteResult{ProtocolVersion: GitHubReviewAPIProtocolVersion,
			Operation: record, Receipt: receipt}, githubReviewApplicationError(executeErr)
	}
	completed, replayed, completeErr := s.store.CompleteGitHubReviewWrite(
		context.WithoutCancel(ctx), record.ID, receipt, receipt.CompletedAt)
	result := GitHubReviewWriteExecuteResult{ProtocolVersion: GitHubReviewAPIProtocolVersion,
		Operation: completed, Receipt: receipt, Replayed: replayed}
	if completeErr != nil {
		return result, apperror.Normalize(completeErr)
	}
	if executeErr != nil {
		return result, githubReviewApplicationError(executeErr)
	}
	return result, nil
}

type GitHubReviewProjection struct {
	ProtocolVersion      string                        `json:"protocol_version"`
	RunID                string                        `json:"run_id"`
	Connection           githubreview.Connection       `json:"connection"`
	Credential           githubreview.CredentialStatus `json:"credential"`
	Snapshots            []githubreview.Snapshot       `json:"snapshots"`
	Evidence             []githubreview.EvidenceRecord `json:"evidence"`
	Writes               []githubreview.WriteRecord    `json:"writes"`
	StandardCodeDelivery *standardcodedelivery.Report  `json:"standard_code_delivery,omitempty"`
}

func (s *GitHubReviewService) Projection(ctx context.Context, runID,
	connectionID string, pullRequest int64, limit int,
) (GitHubReviewProjection, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(connectionID) == "" ||
		pullRequest < 0 || limit < 1 || limit > 200 {
		return GitHubReviewProjection{}, apperror.New(apperror.CodeInvalidArgument,
			"GitHub review projection request is invalid")
	}
	authority, err := s.loadRunBinding(ctx, runID, false)
	if err != nil {
		return GitHubReviewProjection{}, err
	}
	connection, auth, _, err := s.loadClient(ctx, connectionID, false)
	if err != nil {
		return GitHubReviewProjection{}, err
	}
	status, err := auth.Status(ctx, connection.Credential)
	if err != nil {
		return GitHubReviewProjection{}, githubReviewApplicationError(err)
	}
	snapshots, err := s.store.ListGitHubReviewSnapshots(ctx, connection.ID,
		pullRequest, limit)
	if err != nil {
		return GitHubReviewProjection{}, apperror.Normalize(err)
	}
	evidence, err := s.store.ListGitHubReviewEvidence(ctx, authority.run.ID, limit)
	if err != nil {
		return GitHubReviewProjection{}, apperror.Normalize(err)
	}
	writes, err := s.store.ListGitHubReviewWrites(ctx, authority.run.ID, "", limit)
	if err != nil {
		return GitHubReviewProjection{}, apperror.Normalize(err)
	}
	projection := GitHubReviewProjection{ProtocolVersion: GitHubReviewAPIProtocolVersion,
		RunID: authority.run.ID, Connection: connection, Credential: status,
		Snapshots: snapshots, Evidence: evidence, Writes: writes}
	if s.delivery != nil {
		report, found, deliveryErr := s.delivery.Current(ctx, authority.run.ID)
		if deliveryErr != nil {
			return GitHubReviewProjection{}, deliveryErr
		}
		if found {
			if report.Binding.RunID != authority.run.ID ||
				report.Binding.MissionID != authority.mission.ID ||
				report.Binding.SessionID != authority.session.ID ||
				report.Binding.SourceWorkspaceID != authority.workspace.ID {
				return GitHubReviewProjection{}, apperror.New(apperror.CodeConflict,
					"Standard Code delivery escaped its GitHub Review binding")
			}
			projection.StandardCodeDelivery = &report
		}
	}
	return projection, nil
}

type GitHubReviewReconcileResult struct {
	ProtocolVersion string   `json:"protocol_version"`
	Examined        int      `json:"examined"`
	Recovered       int      `json:"recovered"`
	Failed          int      `json:"failed"`
	Deferred        int      `json:"deferred"`
	OperationIDs    []string `json:"operation_ids"`
}

func (s *GitHubReviewService) ReconcileStartup(ctx context.Context,
	limit int,
) (GitHubReviewReconcileResult, error) {
	result := GitHubReviewReconcileResult{ProtocolVersion: GitHubReviewAPIProtocolVersion,
		OperationIDs: []string{}}
	if s == nil || s.store == nil || limit < 1 || limit > 500 {
		return result, apperror.New(apperror.CodeInvalidArgument,
			"GitHub review recovery limit is invalid")
	}
	records, err := s.store.ListRunningGitHubReviewWrites(ctx, limit)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	result.Examined = len(records)
	for _, record := range records {
		approvalRecord, approvalErr := s.store.GetApproval(ctx, record.ApprovalID)
		if approvalErr != nil || approvalRecord.Status != approval.StatusApproved ||
			approvalRecord.ProposalID != record.ID ||
			approvalRecord.RequestFingerprint != record.ApprovalFingerprint {
			result.Deferred++
			continue
		}
		_, _, client, clientErr := s.loadClient(ctx, record.ConnectionID, true)
		if clientErr != nil {
			result.Deferred++
			continue
		}
		receipt, recoverErr := client.RecoverWrite(ctx, record.Spec, record.Preview)
		if recoverErr != nil && ambiguousGitHubWriteError(recoverErr) {
			result.Deferred++
			continue
		}
		completed, _, completeErr := s.store.CompleteGitHubReviewWrite(
			context.WithoutCancel(ctx), record.ID, receipt, receipt.CompletedAt)
		if completeErr != nil {
			return result, apperror.Normalize(completeErr)
		}
		result.OperationIDs = append(result.OperationIDs, completed.ID)
		if receipt.Status == githubreview.ReceiptRecovered {
			result.Recovered++
		} else {
			result.Failed++
		}
	}
	return result, nil
}

type githubReviewRunBinding struct {
	run        domain.Run
	mission    domain.Mission
	session    session.Session
	workspace  session.WorkspaceRecord
	mode       domain.RunModeSnapshot
	permission domain.RunExecutionPermissionSnapshot
}

func (s *GitHubReviewService) loadRunBinding(ctx context.Context, runID string,
	mutation bool,
) (githubReviewRunBinding, error) {
	var value githubReviewRunBinding
	var err error
	if value.run, err = s.store.GetRun(ctx, strings.TrimSpace(runID)); err != nil {
		return value, apperror.Normalize(err)
	}
	if value.mission, err = s.store.GetMission(ctx, value.run.MissionID); err != nil {
		return value, apperror.Normalize(err)
	}
	if value.session, err = s.store.GetSession(ctx, value.run.SessionID); err != nil {
		return value, apperror.Normalize(err)
	}
	if value.workspace, err = s.store.GetWorkspaceByID(ctx,
		value.mission.WorkspaceID); err != nil {
		return value, apperror.Normalize(err)
	}
	if value.mode, err = s.store.GetRunMode(ctx, value.run.ID); err != nil {
		return value, apperror.Normalize(err)
	}
	if value.run.MissionID != value.mission.ID ||
		value.run.SessionID != value.session.ID ||
		value.mission.WorkspaceID != value.workspace.ID ||
		value.session.WorkspaceID != value.workspace.ID ||
		value.session.Status != session.StatusActive ||
		strings.TrimSpace(value.workspace.RootPath) == "" ||
		value.mode.RunID != value.run.ID || value.mode.MissionID != value.mission.ID ||
		value.mode.Surface != domain.ExecutionSurfaceCode {
		return value, apperror.New(apperror.CodeFailedPrecondition,
			"GitHub review requires an active Code Surface Run and Workspace binding")
	}
	if !mutation {
		return value, nil
	}
	if value.run.Status != domain.RunRunning ||
		value.mode.Phase != domain.ExecutionPhaseDeliver {
		return value, apperror.New(apperror.CodeFailedPrecondition,
			"GitHub review writes require a running Code/Deliver Run")
	}
	if value.permission, err = s.store.GetRunExecutionPermission(ctx,
		value.run.ID); err != nil {
		return value, apperror.Normalize(err)
	}
	// Readiness can create a pending review; it cannot authorize the write.
	// Retired modes remain readable but cannot issue new mutation authority.
	if !value.permission.Mode.IsApprovalMode() || value.permission.Validate() != nil ||
		value.permission.RunID != value.run.ID || value.permission.MissionID != value.mission.ID ||
		!s.permissionCapabilities.OperatorApprovalEnabled || s.permissionCapabilities.RuntimeAuthority == nil ||
		!s.permissionCapabilities.AllowsSnapshot(value.permission) {
		return value, apperror.New(apperror.CodePolicyDenied,
			"GitHub review write requires an active approval mode and runtime authority")
	}
	return value, nil
}

func (s *GitHubReviewService) loadClient(ctx context.Context, connectionID string,
	requireEnabled bool,
) (githubreview.Connection, *githubreview.AuthManager, githubReviewRemote, error) {
	if s == nil || s.store == nil || s.credentials == nil || s.clientFactory == nil {
		return githubreview.Connection{}, nil, nil, apperror.New(
			apperror.CodeFailedPrecondition, "GitHub review service is unavailable")
	}
	connection, found, err := s.store.GetGitHubReviewConnection(ctx,
		strings.TrimSpace(connectionID))
	if err != nil || !found {
		if err == nil {
			err = apperror.New(apperror.CodeNotFound, "GitHub review connection was not found")
		}
		return githubreview.Connection{}, nil, nil, apperror.Normalize(err)
	}
	if requireEnabled && !connection.Enabled {
		return githubreview.Connection{}, nil, nil, apperror.New(
			apperror.CodeFailedPrecondition, "GitHub review connection is disabled")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cached, ok := s.clients[connection.ID]; ok && cached.generation == connection.Generation {
		return connection, cached.auth, cached.client, nil
	}
	auth, err := s.authLifecycle.NewManager(connection.ClientID)
	if err != nil {
		return githubreview.Connection{}, nil, nil, githubReviewApplicationError(err)
	}
	client, err := s.clientFactory(auth, connection)
	if err != nil {
		return githubreview.Connection{}, nil, nil, githubReviewApplicationError(err)
	}
	s.clients[connection.ID] = githubReviewClientEntry{generation: connection.Generation,
		auth: auth, client: client}
	return connection, auth, client, nil
}

func capabilityAllowsWrite(capability githubreview.CapabilitySnapshot,
	operation githubreview.WriteOperation,
) bool {
	switch operation {
	case githubreview.WriteReply:
		return capability.Reply
	case githubreview.WriteResolve, githubreview.WriteUnresolve:
		return capability.Resolve
	case githubreview.WriteSubmitReview:
		return capability.Review
	case githubreview.WriteRequestReviewer:
		return capability.RequestReviewer
	default:
		return false
	}
}

func ambiguousGitHubWriteError(err error) bool {
	var typed *githubreview.Error
	if !errors.As(err, &typed) {
		return false
	}
	switch typed.Code {
	case githubreview.FailureOffline, githubreview.FailureRateLimit,
		githubreview.FailureCancelled, githubreview.FailureUnavailable,
		githubreview.FailureMalformed:
		return true
	default:
		return false
	}
}

func githubReviewApplicationError(err error) error {
	if err == nil {
		return nil
	}
	var typed *githubreview.Error
	if !errors.As(err, &typed) {
		return apperror.Normalize(err)
	}
	code := apperror.CodeFailedPrecondition
	switch typed.Code {
	case githubreview.FailureAuthentication, githubreview.FailurePermission,
		githubreview.FailureSSO, githubreview.FailureNetworkPolicy:
		code = apperror.CodePolicyDenied
	case githubreview.FailureOffline, githubreview.FailureUnavailable,
		githubreview.FailureMalformed:
		code = apperror.CodeUnavailable
	case githubreview.FailureRateLimit, githubreview.FailureResponseBound:
		code = apperror.CodeResourceExhausted
	case githubreview.FailureNotFound:
		code = apperror.CodeNotFound
	case githubreview.FailureDrift, githubreview.FailurePaginationDrift,
		githubreview.FailureConflict:
		code = apperror.CodeConflict
	case githubreview.FailureCancelled:
		code = apperror.CodeCancelled
	}
	return apperror.New(code, typed.Error())
}
