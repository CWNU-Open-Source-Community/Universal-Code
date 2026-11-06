package desktop

import (
	"context"
	"crypto/sha256"
	"cyberagent-workbench/internal/application"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/executionauth"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/store"
	terminalruntime "cyberagent-workbench/internal/terminal"
	"cyberagent-workbench/internal/toolcontract"
)

const DesktopUserTerminalProtocolVersion = "desktop_user_terminal.v1"

type UserTerminalController interface {
	Start(context.Context, DesktopTerminalStartRequest) (
		DesktopTerminalSession, error)
	Get(context.Context, string) (DesktopTerminalSession, error)
	Read(context.Context, DesktopTerminalReadRequest) (
		DesktopTerminalOutput, error)
	Write(context.Context, DesktopTerminalWriteRequest) (
		DesktopTerminalWriteResult, error)
	Resize(context.Context, DesktopTerminalResizeRequest) error
	Close(context.Context, DesktopTerminalCloseRequest) error
}

type DesktopTerminalStartRequest struct {
	ProtocolVersion      string `json:"protocol_version"`
	RunID                string `json:"run_id"`
	Columns              int    `json:"columns"`
	Rows                 int    `json:"rows"`
	ReplaceExisting      bool   `json:"replace_existing"`
	ConfirmDebugBoundary bool   `json:"confirm_debug_boundary"`
}

type DesktopTerminalReadRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	SessionID       string `json:"session_id"`
	Cursor          uint64 `json:"cursor"`
	MaxBytes        int    `json:"max_bytes"`
}

type DesktopTerminalWriteRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	SessionID       string `json:"session_id"`
	Data            string `json:"data"`
	UserConfirmed   bool   `json:"user_confirmed"`
}

type DesktopTerminalResizeRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	SessionID       string `json:"session_id"`
	Columns         int    `json:"columns"`
	Rows            int    `json:"rows"`
	UserConfirmed   bool   `json:"user_confirmed"`
}

type DesktopTerminalCloseRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	SessionID       string `json:"session_id"`
	UserConfirmed   bool   `json:"user_confirmed"`
}

type DesktopTerminalSession struct {
	Shell                 *terminalruntime.ShellInfo `json:"shell,omitempty"`
	ProtocolVersion       string                     `json:"protocol_version"`
	SessionID             string                     `json:"session_id"`
	RunID                 string                     `json:"run_id"`
	State                 string                     `json:"state"`
	Backend               string                     `json:"backend"`
	Columns               int                        `json:"columns"`
	Rows                  int                        `json:"rows"`
	OutputBaseCursor      uint64                     `json:"output_base_cursor"`
	OutputNextCursor      uint64                     `json:"output_next_cursor"`
	ExitCode              int                        `json:"exit_code"`
	UserOwned             bool                       `json:"user_owned"`
	AgentInputDefault     bool                       `json:"agent_input_default"`
	JobAssignedAtCreation bool                       `json:"job_assigned_at_creation"`
	KillOnJobClose        bool                       `json:"kill_on_job_close"`
	Persistent            bool                       `json:"persistent"`
	ProcessLocal          bool                       `json:"process_local"`
	RawOutputPersisted    bool                       `json:"raw_output_persisted"`
}

type DesktopTerminalOutput struct {
	ProtocolVersion string `json:"protocol_version"`
	SessionID       string `json:"session_id"`
	BaseCursor      uint64 `json:"base_cursor"`
	NextCursor      uint64 `json:"next_cursor"`
	DataBase64      string `json:"data_base64"`
	DataBytes       int    `json:"data_bytes"`
	Dropped         bool   `json:"dropped"`
	State           string `json:"state"`
}

type DesktopTerminalWriteResult struct {
	ProtocolVersion string `json:"protocol_version"`
	SessionID       string `json:"session_id"`
	BytesWritten    int    `json:"bytes_written"`
}

// desktopUserTerminalService keeps all path and durable state lookups in Go.
// Its Wails projection accepts only Run/session IDs and user keystrokes.
type desktopUserTerminalService struct {
	settings     application.DesktopTerminalSettingsReader
	store        *store.SQLiteStore
	manager      *terminalruntime.Manager
	capabilities domain.ExecutionPermissionRuntimeCapabilities
}

func newDesktopUserTerminalService(stateStore *store.SQLiteStore,
	manager *terminalruntime.Manager,
	capabilities domain.ExecutionPermissionRuntimeCapabilities,
) (*desktopUserTerminalService, error) {
	if stateStore == nil || manager == nil || !manager.Available() {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"desktop user terminal runtime is unavailable")
	}
	if err := capabilities.Validate(); err != nil {
		return nil, apperror.Wrap(apperror.CodeFailedPrecondition,
			"desktop terminal permission capabilities are invalid", err)
	}
	return &desktopUserTerminalService{
		store: stateStore, manager: manager, capabilities: capabilities,
	}, nil
}

func (s *desktopUserTerminalService) Start(ctx context.Context,
	request DesktopTerminalStartRequest,
) (DesktopTerminalSession, error) {
	if request.ProtocolVersion != DesktopUserTerminalProtocolVersion ||
		!request.ConfirmDebugBoundary || !validWorkspaceIdentity(request.RunID) {
		return DesktopTerminalSession{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"desktop user terminal requires an explicit Debug confirmation")
	}
	binding, err := s.loadTerminalBinding(ctx, request.RunID)
	if err != nil {
		return DesktopTerminalSession{}, err
	}
	sessionID := idgen.New("user-terminal")
	expected, err := terminalBindingFingerprint(binding)
	if err != nil {
		return DesktopTerminalSession{}, err
	}
	rootSHA256, err := terminalruntime.WorkspaceRootSHA256(binding.Root)
	if err != nil {
		return DesktopTerminalSession{}, err
	}
	subject := executionauth.SubjectRef{RunID: request.RunID, ActorID: "desktop_operator"}
	authorizer := executionauth.NewPolicyAuthorizer(func(checkCtx context.Context,
		actual executionauth.SubjectRef, operation toolcontract.Operation, approvalRef string,
	) (executionauth.OperationAuthority, error) {
		if actual != subject || approvalRef != "" || operation.ID != sessionID ||
			operation.ToolID != "user_terminal" || operation.Kind != toolcontract.OperationProcess ||
			operation.Component != (toolcontract.ComponentRef{PackageID: "builtin", ComponentID: "user_terminal"}) ||
			len(operation.Targets) != 2 ||
			operation.Targets[0] != (toolcontract.Target{Kind: "process", Locator: sessionID}) ||
			operation.Targets[1] != (toolcontract.Target{Kind: "directory", Locator: "workspace:" + binding.Scope.WorkspaceID + ":" + rootSHA256}) {
			return executionauth.OperationAuthority{}, terminalBindingDenied()
		}
		current, err := s.loadTerminalBinding(checkCtx, request.RunID)
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		fingerprint, err := terminalBindingFingerprint(current)
		if err != nil || fingerprint != expected {
			return executionauth.OperationAuthority{}, terminalBindingDenied()
		}
		return executionauth.OperationAuthority{
			Mode: domain.ExecutionApprovalFull, BindingFingerprint: fingerprint,
			RuntimeAvailable: s.manager.Available(), FullActivated: true,
			// A user-owned host terminal has unrestricted effects. Its cwd is
			// not isolation evidence for Ask or Auto.
			EffectsVerified: false,
		}, nil
	})
	shellID := "auto"
	if s.settings != nil {
		preferences, err := s.settings.Load(ctx)
		if err != nil {
			return DesktopTerminalSession{}, err
		}
		shellID = preferences.Settings.DefaultShell
	}
	session, err := s.manager.Start(ctx, terminalruntime.StartRequest{
		ShellID: shellID,
		ID:      sessionID, Scope: binding.Scope, WorkspaceRoot: binding.Root,
		Interaction: binding.Interaction, CurrentProfile: binding.Profile,
		CurrentPermission: binding.Permission, Authorizer: authorizer,
		Columns: request.Columns, Rows: request.Rows,
		RequestedBy: subject.ActorID, OperatorConfirmed: true,
		ReplaceExisting: request.ReplaceExisting,
	})
	if err != nil {
		return DesktopTerminalSession{}, apperror.Wrap(
			apperror.CodeFailedPrecondition,
			"desktop user terminal start was denied", err)
	}
	return projectDesktopTerminalSession(session), nil
}

// This binding is private process state. The durable preference and the
// renderer's confirmation cannot recreate its runtime epoch or generation.
type desktopTerminalBinding struct {
	Scope       terminalruntime.SessionScope
	Root        string
	MissionID   string
	SessionID   string
	Mode        domain.RunModeSnapshot
	Profile     domain.RunExecutionProfileSnapshot
	Interaction domain.RunExecutionInteractionSnapshot
	Permission  domain.RunExecutionPermissionSnapshot
	Epoch       string
	Generation  uint64
}

func (s *desktopUserTerminalService) loadTerminalBinding(ctx context.Context,
	runID string,
) (desktopTerminalBinding, error) {
	if ctx == nil {
		return desktopTerminalBinding{}, terminalBindingDenied()
	}
	if err := ctx.Err(); err != nil {
		return desktopTerminalBinding{}, err
	}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return desktopTerminalBinding{}, classifyTerminalLookup(err, "desktop terminal Run lookup failed")
	}
	if run.Terminal() {
		return desktopTerminalBinding{}, terminalBindingDenied()
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return desktopTerminalBinding{}, classifyTerminalLookup(err, "desktop terminal Mission lookup failed")
	}
	workspace, err := s.store.GetWorkspaceByID(ctx, mission.WorkspaceID)
	if err != nil {
		return desktopTerminalBinding{}, classifyTerminalLookup(err, "desktop terminal Workspace lookup failed")
	}
	mode, err := s.store.GetRunMode(ctx, run.ID)
	if err != nil {
		return desktopTerminalBinding{}, classifyTerminalLookup(err, "desktop terminal mode lookup failed")
	}
	profile, err := s.store.GetRunExecutionProfile(ctx, run.ID)
	if err != nil {
		return desktopTerminalBinding{}, classifyTerminalLookup(err, "desktop terminal profile lookup failed")
	}
	interaction, err := s.store.GetRunExecutionInteraction(ctx, run.ID)
	if err != nil {
		return desktopTerminalBinding{}, classifyTerminalLookup(err, "desktop terminal interaction lookup failed")
	}
	permission, err := s.store.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		return desktopTerminalBinding{}, classifyTerminalLookup(err, "desktop terminal permission lookup failed")
	}
	generation, active := s.capabilities.FullAccessGeneration(permission)
	epoch := s.capabilities.RuntimeAuthority.RuntimeEpoch()
	root := filepath.Clean(workspace.RootPath)
	_, rootErr := terminalruntime.WorkspaceRootSHA256(root)
	if rootErr != nil || !validWorkspaceIdentity(mission.WorkspaceID) ||
		workspace.ID != mission.WorkspaceID || mode.Validate() != nil ||
		profile.Validate() != nil || interaction.Validate() != nil || permission.Validate() != nil ||
		mode.RunID != run.ID || mode.MissionID != mission.ID ||
		profile.RunID != run.ID || profile.MissionID != mission.ID ||
		interaction.RunID != run.ID || interaction.MissionID != mission.ID ||
		permission.RunID != run.ID || permission.MissionID != mission.ID ||
		mode.Surface != domain.ExecutionSurfaceCode ||
		profile.Profile != domain.RunExecutionProfileLocal ||
		interaction.Mode != domain.RunExecutionInteractionDebug ||
		interaction.WorkspaceTrust != domain.WorkspaceTrustTrusted ||
		interaction.ExecutionProfileRevision != profile.Revision ||
		permission.ProtocolVersion != domain.RunApprovalPermissionProtocolVersion ||
		permission.Mode != domain.RunExecutionPermissionFull || !active || generation == 0 || epoch == "" {
		return desktopTerminalBinding{}, terminalBindingDenied()
	}
	return desktopTerminalBinding{
		Scope: terminalruntime.SessionScope{
			WorkspaceID: mission.WorkspaceID, RunID: run.ID,
			InteractionSnapshotID: interaction.ID, InteractionRevision: interaction.Revision,
			ExecutionProfileRevision: profile.Revision,
			PermissionSnapshotID:     permission.ID, PermissionRevision: permission.Revision,
			PermissionMode: permission.Mode, Mode: interaction.Mode,
		},
		Root: root, MissionID: mission.ID, SessionID: run.SessionID,
		Mode: mode, Profile: profile, Interaction: interaction, Permission: permission,
		Epoch: epoch, Generation: generation,
	}, nil
}

func terminalBindingFingerprint(binding desktopTerminalBinding) (string, error) {
	raw, err := json.Marshal(binding)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func terminalBindingDenied() error {
	return apperror.New(apperror.CodePolicyDenied,
		"desktop user terminal requires current Code/Local/Debug bindings and activated Full")
}

func (s *desktopUserTerminalService) Get(_ context.Context,
	sessionID string,
) (DesktopTerminalSession, error) {
	session, err := s.manager.Get(strings.TrimSpace(sessionID))
	if err != nil {
		return DesktopTerminalSession{}, apperror.Wrap(
			apperror.CodeNotFound, "desktop terminal session was not found", err)
	}
	return projectDesktopTerminalSession(session), nil
}

func (s *desktopUserTerminalService) Read(_ context.Context,
	request DesktopTerminalReadRequest,
) (DesktopTerminalOutput, error) {
	if request.ProtocolVersion != DesktopUserTerminalProtocolVersion {
		return DesktopTerminalOutput{}, apperror.New(
			apperror.CodeInvalidArgument,
			"desktop terminal read protocol is invalid")
	}
	page, err := s.manager.Read(strings.TrimSpace(request.SessionID),
		request.Cursor, request.MaxBytes)
	if err != nil {
		return DesktopTerminalOutput{}, apperror.Wrap(
			apperror.CodeNotFound, "desktop terminal output is unavailable", err)
	}
	return DesktopTerminalOutput{
		ProtocolVersion: DesktopUserTerminalProtocolVersion,
		SessionID:       page.SessionID, BaseCursor: page.BaseCursor,
		NextCursor: page.NextCursor,
		DataBase64: base64.StdEncoding.EncodeToString(page.Data),
		DataBytes:  len(page.Data), Dropped: page.Dropped,
		State: string(page.State),
	}, nil
}

func (s *desktopUserTerminalService) Write(ctx context.Context,
	request DesktopTerminalWriteRequest,
) (DesktopTerminalWriteResult, error) {
	if request.ProtocolVersion != DesktopUserTerminalProtocolVersion {
		return DesktopTerminalWriteResult{}, apperror.New(
			apperror.CodeInvalidArgument,
			"desktop terminal input protocol is invalid")
	}
	count, err := s.manager.WriteUser(ctx, terminalruntime.UserInputRequest{
		SessionID: strings.TrimSpace(request.SessionID),
		Data:      []byte(request.Data), RequestedBy: "desktop_operator",
		UserConfirmed: request.UserConfirmed,
	})
	if err != nil {
		return DesktopTerminalWriteResult{}, apperror.Wrap(
			apperror.CodeFailedPrecondition,
			"desktop terminal user input was denied", err)
	}
	return DesktopTerminalWriteResult{
		ProtocolVersion: DesktopUserTerminalProtocolVersion,
		SessionID:       strings.TrimSpace(request.SessionID), BytesWritten: count,
	}, nil
}

func (s *desktopUserTerminalService) Resize(_ context.Context,
	request DesktopTerminalResizeRequest,
) error {
	if request.ProtocolVersion != DesktopUserTerminalProtocolVersion {
		return apperror.New(apperror.CodeInvalidArgument,
			"desktop terminal resize protocol is invalid")
	}
	return s.manager.Resize(strings.TrimSpace(request.SessionID),
		request.Columns, request.Rows, "desktop_operator",
		request.UserConfirmed)
}

func (s *desktopUserTerminalService) Close(_ context.Context,
	request DesktopTerminalCloseRequest,
) error {
	if request.ProtocolVersion != DesktopUserTerminalProtocolVersion {
		return apperror.New(apperror.CodeInvalidArgument,
			"desktop terminal close protocol is invalid")
	}
	return s.manager.Close(strings.TrimSpace(request.SessionID),
		"desktop_operator", request.UserConfirmed)
}

func (s *desktopUserTerminalService) reconcileBindings(ctx context.Context) int {
	if s == nil || s.store == nil || s.manager == nil || ctx == nil || ctx.Err() != nil {
		return 0
	}
	// Preserve Run-wide lease revocation on termination, including leases for
	// a terminal that has already exited. Other drift uses the pinned resolver.
	closed := 0
	for _, session := range s.manager.ActiveSessions() {
		run, err := s.store.GetRun(ctx, session.Scope.RunID)
		if ctx.Err() != nil {
			return closed
		}
		if err == nil && run.Terminal() {
			_ = s.manager.CloseForRunTermination(run.ID)
			closed++
		}
	}
	return closed + s.manager.ReconcileBindings(ctx)
}

func projectDesktopTerminalSession(
	session terminalruntime.Session,
) DesktopTerminalSession {
	return DesktopTerminalSession{
		Shell:           session.Shell,
		ProtocolVersion: DesktopUserTerminalProtocolVersion,
		SessionID:       session.ID, RunID: session.Scope.RunID,
		State: string(session.State), Backend: session.Backend,
		Columns: session.Columns, Rows: session.Rows,
		OutputBaseCursor: session.OutputBaseCursor,
		OutputNextCursor: session.OutputNextCursor, ExitCode: session.ExitCode,
		UserOwned:             session.UserOwned,
		AgentInputDefault:     session.AgentInputDefault,
		JobAssignedAtCreation: session.JobAssignedAtCreation,
		KillOnJobClose:        session.KillOnJobClose, Persistent: session.Persistent,
		ProcessLocal:       session.ProcessLocal,
		RawOutputPersisted: session.RawOutputPersisted,
	}
}

func classifyTerminalLookup(err error, message string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return apperror.New(apperror.CodeNotFound, message)
	}
	return apperror.Wrap(apperror.CodeUnavailable, message, err)
}
