package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/toolbudget"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/toolrun"
)

const (
	StatusActive   = "active"
	StatusArchived = "archived"
)

type Session struct {
	ID          string
	WorkspaceID string
	Title       string
	Route       string
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func New(workspaceID string, title string, route string) Session {
	if strings.TrimSpace(route) == "" {
		route = "learn"
	}
	if strings.TrimSpace(title) == "" {
		title = "New session"
	}
	now := time.Now().UTC()
	return Session{
		ID:          newID("sess"),
		WorkspaceID: strings.TrimSpace(workspaceID),
		Title:       strings.TrimSpace(title),
		Route:       strings.TrimSpace(route),
		Status:      StatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func (s Session) Validate() error {
	if strings.TrimSpace(s.ID) == "" {
		return errors.New("session id is required")
	}
	if strings.TrimSpace(s.Title) == "" {
		return errors.New("session title is required")
	}
	if strings.TrimSpace(s.Route) == "" {
		return errors.New("session route is required")
	}
	if s.Status != StatusActive && s.Status != StatusArchived {
		return fmt.Errorf("invalid session status %q", s.Status)
	}
	if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
		return errors.New("session timestamps are required")
	}
	return nil
}

type Message struct {
	ID            int64
	SessionID     string
	Role          string
	Content       string
	Provenance    ContextProvenance
	TokenEstimate int
	Compacted     bool
	CreatedAt     time.Time
}

type WorkspaceInfo struct {
	ID       string
	Name     string
	RootPath string
}

type WorkspaceRecord struct {
	ID        string
	Name      string
	RootPath  string
	CreatedAt time.Time
}

type Store interface {
	fileedit.Store
	toolrun.Store
	approval.Store
	approval.GrantStore
	toolbudget.Store
	artifact.Store
	policy.DecisionRecorder

	SaveSession(ctx context.Context, session Session) error
	GetSession(ctx context.Context, id string) (Session, error)
	ListSessions(ctx context.Context) ([]Session, error)
	GetWorkspaceInfo(ctx context.Context, id string) (WorkspaceInfo, error)
	SaveSessionMessage(ctx context.Context, message Message) (Message, error)
	ListSessionMessages(ctx context.Context, sessionID string, includeCompacted bool) ([]Message, error)
	MarkSessionMessagesCompacted(ctx context.Context, sessionID string, throughID int64) (int64, error)
	SaveContextSummary(ctx context.Context, summary contextmgr.Summary) (contextmgr.Summary, error)
	LatestContextSummary(ctx context.Context, taskID string) (contextmgr.Summary, bool, error)
}

type Manager struct {
	store          Store
	router         *llm.Router
	checker        policy.Checker
	runChat        RunChatExecutor
	contextMgr     *contextmgr.Manager
	gateway        *toolgateway.Gateway
	fileEdits      *toolgateway.FileEditAdapter
	toolRuns       *toolgateway.ToolRunAdapter
	lifecycleHooks *hooks.Engine
}

type SendResult struct {
	Session          Session
	UserMessage      Message
	ReplyMessage     Message
	Text             string
	Command          bool
	Compacted        bool
	SummaryID        int64
	FileEditID       string
	ToolRunID        string
	RunID            string
	RunAction        string
	RunStatus        string
	Queued           bool
	SteeringID       string
	SteeringSequence int64
	SteeringStatus   string
	SteeringReplayed bool
}

type RunChatResult struct {
	RunID            string
	ContextManaged   bool // Executor owns compaction while holding its execution lease.
	ContextCompacted bool
	ContextSummaryID int64
	UserMessage      Message
	ReplyMessage     Message
	Text             string
	Action           string
	RunStatus        string
	Queued           bool
	SteeringID       string
	SteeringSequence int64
	SteeringStatus   string
	SteeringReplayed bool
}

type SendOptions struct {
	OperationKey string
}

type RunChatOptions struct {
	OperationKey string
}

type TurnMessages struct {
	User      Message
	Assistant Message
}

type RunChatExecutor interface {
	ExecuteSessionTurn(ctx context.Context, sess Session, input string) (RunChatResult, bool, error)
}

type IdempotentRunChatExecutor interface {
	ExecuteSessionTurnWithOptions(ctx context.Context, sess Session, input string,
		options RunChatOptions) (RunChatResult, bool, error)
}

// A Run-bound executor checks commands against the actual Run before the
// command itself or any change to Session history is persisted.
type RunChatCommandPreparer interface {
	PrepareSessionCommand(ctx context.Context, sess Session, input string) (Session, error)
}

func NewManager(store Store, router *llm.Router, checker policy.Checker) *Manager {
	gateway := toolgateway.New(store, checker).WithWorkspaceRootResolver(func(ctx context.Context, workspaceID string) (string, error) {
		workspace, err := store.GetWorkspaceInfo(ctx, workspaceID)
		return workspace.RootPath, err
	})
	return &Manager{
		store:      store,
		router:     router,
		checker:    checker,
		contextMgr: contextmgr.NewManager(store, contextmgr.DefaultConfig()),
		gateway:    gateway,
		fileEdits:  gateway.FileEdits(),
		toolRuns:   gateway.ToolRuns(),
	}
}

func (m *Manager) WithRunChatExecutor(executor RunChatExecutor) *Manager {
	if m != nil {
		m.runChat = executor
	}
	return m
}

func (m *Manager) WithLifecycleHooks(engine *hooks.Engine) *Manager {
	if m == nil {
		return m
	}
	m.lifecycleHooks = engine
	if m.gateway != nil && engine != nil {
		m.gateway.WithLifecycleHooks(engine)
	}
	if m.contextMgr != nil {
		m.contextMgr.WithBeforeCompactGuard(func(ctx context.Context, taskID,
			workspaceID string, sourceMessages, preservedMessages int,
		) error {
			return executeSessionLifecycleBoundary(ctx, engine, hooks.Compaction,
				"", workspaceID, map[string]any{
					"task_id": taskID, "source_messages": sourceMessages,
					"preserved_messages": preservedMessages,
				})
		})
	}
	return m
}

func (m *Manager) Create(ctx context.Context, workspaceID string, title string, route string) (Session, error) {
	session := New(workspaceID, title, route)
	if err := executeSessionLifecycleBoundary(ctx, m.lifecycleHooks, hooks.SessionOpened,
		"", session.WorkspaceID, map[string]any{
			"session_id": session.ID, "source": "session_manager",
		}); err != nil {
		return Session{}, err
	}
	if err := m.store.SaveSession(ctx, session); err != nil {
		return Session{}, err
	}
	return session, nil
}

func executeSessionLifecycleBoundary(ctx context.Context, engine *hooks.Engine,
	event hooks.Event, runID, workspaceID string, payload any,
) error {
	_, err := hooks.ExecuteBoundary(ctx, engine, hooks.Input{Event: event,
		RunID: runID, WorkspaceID: workspaceID}, payload)
	if err == nil {
		return nil
	}
	var denied hooks.DeniedError
	if errors.As(err, &denied) {
		return apperror.New(apperror.CodePolicyDenied,
			"restricted lifecycle hook denied the operation")
	}
	return apperror.Wrap(apperror.CodeUnavailable,
		"restricted lifecycle hooks are unavailable", err)
}

func (m *Manager) Send(ctx context.Context, sessionID string, input string) (SendResult, error) {
	return m.SendWithOptions(ctx, sessionID, input, SendOptions{})
}

func (m *Manager) SendWithOptions(ctx context.Context, sessionID string, input string,
	options SendOptions,
) (SendResult, error) {
	rawInput := strings.TrimLeftFunc(input, unicode.IsSpace)
	input = strings.TrimSpace(rawInput)
	operationKeyProvided := options.OperationKey != ""
	options.OperationKey = strings.TrimSpace(options.OperationKey)
	if operationKeyProvided && options.OperationKey == "" {
		return SendResult{}, apperror.New(apperror.CodeInvalidArgument,
			"Session operation key cannot be blank")
	}
	if input == "" {
		return SendResult{}, errors.New("message is required")
	}
	sess, err := m.store.GetSession(ctx, sessionID)
	if err != nil {
		return SendResult{}, err
	}
	if sess.Status != StatusActive {
		return SendResult{}, fmt.Errorf("session %s is not active", sess.ID)
	}

	if strings.HasPrefix(input, "/") {
		command, _ := splitSlashCommand(rawInput)
		if command == "read" || command == "ls" {
			input = rawInput
		}
		if options.OperationKey != "" {
			return SendResult{}, apperror.New(apperror.CodeInvalidArgument,
				"Session operation keys apply only to queued model input, not slash commands")
		}
		if preparer, ok := m.runChat.(RunChatCommandPreparer); ok {
			sess, err = preparer.PrepareSessionCommand(ctx, sess, input)
			if err != nil {
				return SendResult{}, err
			}
		}
		userMsg, err := m.store.SaveSessionMessage(ctx, NewMessage(sess.ID, "user", input))
		if err != nil {
			return SendResult{}, err
		}
		return m.handleSlash(ctx, sess, userMsg, input)
	}
	if m.runChat != nil {
		var runResult RunChatResult
		var handled bool
		if options.OperationKey != "" {
			executor, ok := m.runChat.(IdempotentRunChatExecutor)
			if !ok {
				return SendResult{}, apperror.New(apperror.CodeFailedPrecondition,
					"Run chat executor does not support durable Session retry identity")
			}
			runResult, handled, err = executor.ExecuteSessionTurnWithOptions(ctx, sess, input,
				RunChatOptions(options))
		} else {
			runResult, handled, err = m.runChat.ExecuteSessionTurn(ctx, sess, input)
		}
		if err != nil {
			return SendResult{}, err
		}
		if handled {
			compacted, summaryID := runResult.ContextCompacted, runResult.ContextSummaryID
			if !runResult.Queued && !runResult.ContextManaged {
				compacted, summaryID, err = m.compactAfterTurn(ctx, sess)
				if err != nil {
					return SendResult{}, err
				}
			}
			return SendResult{
				Session: sess, UserMessage: runResult.UserMessage, ReplyMessage: runResult.ReplyMessage,
				Text: runResult.Text, Compacted: compacted, SummaryID: summaryID,
				RunID: runResult.RunID, RunAction: runResult.Action, RunStatus: runResult.RunStatus,
				Queued: runResult.Queued, SteeringID: runResult.SteeringID,
				SteeringSequence: runResult.SteeringSequence,
				SteeringStatus:   runResult.SteeringStatus,
				SteeringReplayed: runResult.SteeringReplayed,
			}, nil
		}
		if options.OperationKey != "" {
			return SendResult{}, apperror.New(apperror.CodeFailedPrecondition,
				"durable Session retry identity requires a Run-bound Session")
		}
	}
	if options.OperationKey != "" {
		return SendResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"durable Session retry identity requires a Run chat executor")
	}
	userMsg, err := m.store.SaveSessionMessage(ctx, NewMessage(sess.ID, "user", input))
	if err != nil {
		return SendResult{}, err
	}
	return m.chat(ctx, sess, userMsg)
}

func (m *Manager) History(ctx context.Context, sessionID string, includeCompacted bool) ([]Message, error) {
	return m.store.ListSessionMessages(ctx, sessionID, includeCompacted)
}

// RequireSession returns one session or a not-found error. ListSessionMessages
// cannot distinguish an absent session from an empty one, so read-only
// inspection surfaces call this first to keep "no messages" meaningful.
func (m *Manager) RequireSession(ctx context.Context, sessionID string) (Session, error) {
	return m.store.GetSession(ctx, sessionID)
}

func (m *Manager) List(ctx context.Context) ([]Session, error) {
	return m.store.ListSessions(ctx)
}

func splitSlashCommand(input string) (command, argument string) {
	end := strings.IndexFunc(input, unicode.IsSpace)
	if end < 0 {
		return strings.TrimPrefix(input, "/"), ""
	}
	_, size := utf8.DecodeRuneInString(input[end:])
	return strings.TrimPrefix(input[:end], "/"), input[end+size:]
}

func (m *Manager) handleSlash(ctx context.Context, sess Session, userMsg Message, input string) (SendResult, error) {
	command, argument := splitSlashCommand(input)

	var text string
	var compacted bool
	var summaryID int64
	var fileEditID string
	var toolRunID string
	replySourceKind := SourceGoCommandResult
	replySourceRef := "/" + command
	switch command {
	case "help":
		text = "Commands: /help, /compact, /model, /model <route>, /workspace, /ls [path], /read <path>, /write <path> <content>, /run <command>. Workspace commands accept double-quoted paths. /write and /run require explicit approval unless an active scoped Session grant applies."
	case "compact":
		result, err := m.compactActiveMessages(ctx, sess)
		if err != nil {
			return SendResult{}, err
		}
		compacted = result.Compacted
		summaryID = result.Summary.ID
		text = fmt.Sprintf("Context compacted: summary=%d removed=%d preserved=%d", result.Summary.ID, result.RemovedMessages, len(result.Preserved))
	case "model":
		args := strings.Fields(argument)
		if len(args) > 0 {
			sess.Route = args[0]
			sess.UpdatedAt = time.Now().UTC()
			if err := m.store.SaveSession(ctx, sess); err != nil {
				return SendResult{}, err
			}
		}
		ref, err := m.resolveModelRef(sess.Route)
		if err != nil {
			return SendResult{}, err
		}
		text = fmt.Sprintf("Session route: %s -> %s/%s", sess.Route, ref.Provider, ref.Model)
	case "workspace":
		if sess.WorkspaceID == "" {
			text = "No workspace is attached to this session."
		} else {
			text = "Workspace: " + sess.WorkspaceID
		}
	case "ls", "read":
		path := argument
		if strings.HasPrefix(path, "\"") {
			unquoted, err := strconv.Unquote(path)
			if err != nil {
				text = "Invalid quoted workspace path."
				break
			}
			path = unquoted
		}
		var success bool
		sourceKind := SourceWorkspaceFile
		if command == "ls" {
			text, replySourceRef, success = m.handleWorkspaceList(ctx, sess, path)
			sourceKind = SourceWorkspaceList
		} else {
			text, replySourceRef, success = m.handleWorkspaceRead(ctx, sess, path)
		}
		if success {
			replySourceKind = sourceKind
		} else {
			replySourceRef = "/" + command
		}
	case "write":
		path, content, ok := parseWriteCommand(argument)
		if !ok {
			text = "Usage: /write <workspace-relative-path> <replacement-content>. This creates a file edit proposal and does not write immediately."
			break
		}
		edit, response := m.handleWorkspaceWrite(ctx, sess, path, content)
		text = response
		fileEditID = edit.ID
		if edit.ID != "" {
			replySourceKind = SourceWorkspaceDiff
			replySourceRef = edit.Path
		}
	case "run":
		args := strings.Fields(argument)
		if len(args) == 0 {
			text = "Usage: /run <command>. v0.1 creates a tool proposal and does not execute directly from session chat."
			break
		}
		requested := strings.Join(args, " ")
		run, err := m.toolRuns.ProposeShell(ctx, sess.ID, sess.WorkspaceID, requested)
		if err != nil {
			return SendResult{}, err
		}
		toolRunID = run.ID
		replySourceKind = SourceToolResult
		replySourceRef = run.ID
		if run.Status == toolrun.StatusDenied {
			text = fmt.Sprintf("Tool run %s denied by policy: %s", run.ID, run.PolicyReason)
			break
		}
		if run.Status == toolrun.StatusCompleted {
			text = fmt.Sprintf("Tool run %s authorized by an active Session grant and completed as a dry run: %s", run.ID, requested)
			break
		}
		text = fmt.Sprintf("Tool run %s proposed: %s. Review with `cyberagent tool show %s`, approve with `cyberagent tool approve %s`, or deny with `cyberagent tool deny %s`.", run.ID, requested, run.ID, run.ID, run.ID)
	default:
		text = "Unknown slash command. Try /help."
	}

	reply, err := m.store.SaveSessionMessage(ctx,
		NewEvidenceMessage(sess.ID, replySourceKind, replySourceRef, text))
	if err != nil {
		return SendResult{}, err
	}
	return SendResult{
		Session:      sess,
		UserMessage:  userMsg,
		ReplyMessage: reply,
		Text:         text,
		Command:      true,
		Compacted:    compacted,
		SummaryID:    summaryID,
		FileEditID:   fileEditID,
		ToolRunID:    toolRunID,
	}, nil
}

func (m *Manager) handleWorkspaceList(ctx context.Context, sess Session, path string) (string, string, bool) {
	workspace, ok, err := m.workspaceInfo(ctx, sess)
	if err != nil {
		return "Workspace lookup failed: " + err.Error(), "", false
	}
	if !ok {
		return "No workspace is attached to this session.", "", false
	}
	if path == "" {
		path = "."
	}
	outcome, err := m.gateway.Invoke(ctx, toolgateway.ToolCall{
		Name: toolgateway.ListWorkspaceTool, SessionID: sess.ID, WorkspaceID: workspace.ID,
		WorkspaceRoot: workspace.RootPath, RequestedBy: "session_slash_command",
		Arguments: map[string]string{
			"path":      path,
			"max_depth": "2",
		},
	})
	if err != nil {
		if outcome.Result != nil && strings.TrimSpace(outcome.Result.Stderr) != "" {
			return "Workspace list failed: " + outcome.Result.Stderr, "", false
		}
		return "Workspace list failed: " + err.Error(), "", false
	}
	if !outcome.Decision.Allowed {
		return "Workspace list denied by policy: " + outcome.Decision.Reason, "", false
	}
	if outcome.Result == nil {
		return "Workspace list failed: tool gateway returned no result", "", false
	}
	return fmt.Sprintf("Workspace list %s:\n%s", path, outcome.Result.Stdout), path, true
}

func (m *Manager) handleWorkspaceRead(ctx context.Context, sess Session, path string) (string, string, bool) {
	workspace, ok, err := m.workspaceInfo(ctx, sess)
	if err != nil {
		return "Workspace lookup failed: " + err.Error(), "", false
	}
	if !ok {
		return "No workspace is attached to this session.", "", false
	}
	if path == "" {
		return "Usage: /read <workspace-relative-path>", "", false
	}
	outcome, err := m.gateway.Invoke(ctx, toolgateway.ToolCall{
		Name: toolgateway.ReadFileTool, SessionID: sess.ID, WorkspaceID: workspace.ID,
		WorkspaceRoot: workspace.RootPath, RequestedBy: "session_slash_command",
		Arguments: map[string]string{
			"path":      path,
			"max_bytes": "16384",
		},
	})
	if err != nil {
		if outcome.Result != nil && strings.TrimSpace(outcome.Result.Stderr) != "" {
			return "Workspace read failed: " + outcome.Result.Stderr, "", false
		}
		return "Workspace read failed: " + err.Error(), "", false
	}
	if !outcome.Decision.Allowed {
		return "Workspace read denied by policy: " + outcome.Decision.Reason, "", false
	}
	if outcome.Result == nil {
		return "Workspace read failed: tool gateway returned no result", "", false
	}
	return fmt.Sprintf("Workspace file %s:\n%s", path, outcome.Result.Stdout), path, true
}

func (m *Manager) handleWorkspaceWrite(ctx context.Context, sess Session, path string, content string) (fileedit.Edit, string) {
	workspace, ok, err := m.workspaceInfo(ctx, sess)
	if err != nil {
		return fileedit.Edit{}, "Workspace lookup failed: " + err.Error()
	}
	if !ok {
		return fileedit.Edit{}, "No workspace is attached to this session."
	}
	edit, err := m.fileEdits.Propose(ctx, fileedit.Proposal{
		SessionID:     sess.ID,
		WorkspaceID:   workspace.ID,
		WorkspaceRoot: workspace.RootPath,
		Path:          path,
		ProposedText:  content,
	})
	if err != nil {
		return fileedit.Edit{}, "File edit proposal failed: " + err.Error()
	}
	if edit.Status == fileedit.StatusApplied {
		response := fmt.Sprintf("File edit %s for %s was authorized by an active Session grant and applied.", edit.ID, edit.Path)
		if edit.SecretsRedacted {
			response += " Sensitive values were redacted before the edit was stored."
		}
		return edit, response
	}
	response := fmt.Sprintf("File edit %s proposed for %s. Inspect with `cyberagent edit show %s`. Run-bound edits require `edit review-approve <run-id> %s` followed by `edit apply <run-id> %s --operation-key <key>`; deny with `cyberagent edit deny %s`.\n\n%s",
		edit.ID, edit.Path, edit.ID, edit.ID, edit.ID, edit.ID, edit.Diff)
	if edit.SecretsRedacted {
		response += "\nSensitive values were redacted before the proposal was stored."
	}
	return edit, response
}

func parseWriteCommand(argument string) (string, string, bool) {
	argument = strings.TrimSpace(argument)
	if strings.HasPrefix(argument, "\"") {
		literal, err := strconv.QuotedPrefix(argument)
		if err != nil {
			return "", "", false
		}
		path, err := strconv.Unquote(literal)
		content, separated := strings.CutPrefix(argument[len(literal):], " ")
		return path, content, err == nil && path != "" && separated
	}
	path, content, separated := strings.Cut(argument, " ")
	if !separated || path == "" {
		return "", "", false
	}
	return path, content, true
}

func (m *Manager) workspaceInfo(ctx context.Context, sess Session) (WorkspaceInfo, bool, error) {
	if strings.TrimSpace(sess.WorkspaceID) == "" {
		return WorkspaceInfo{}, false, nil
	}
	workspace, err := m.store.GetWorkspaceInfo(ctx, sess.WorkspaceID)
	if err != nil {
		return WorkspaceInfo{}, false, err
	}
	return workspace, true, nil
}

func (m *Manager) chat(ctx context.Context, sess Session, userMsg Message) (SendResult, error) {
	ref, err := m.resolveModelRef(sess.Route)
	if err != nil {
		return SendResult{}, err
	}
	if m.router.RequiresPrivateAssistantHistory(ref) {
		return SendResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"this provider requires Root Supervisor private assistant history; legacy Session chat is unsupported")
	}
	prompt, compacted, err := m.sessionChatPrompt(ctx, sess, ref, true)
	if err != nil {
		return SendResult{}, err
	}
	req := llm.ChatRequest{
		Messages: toLLMMessages(prompt),
		Metadata: map[string]string{
			"session_id":   sess.ID,
			"workspace_id": sess.WorkspaceID,
		},
	}
	resp, err := m.chatRoute(ctx, sess.Route, req)
	if err != nil {
		return SendResult{}, err
	}
	decision := m.checker.CheckText("assistant_response", resp.Text)
	if recorder, ok := m.store.(policy.DecisionRecorder); ok {
		if err := recorder.RecordPolicyDecision(ctx, policy.DecisionRecord{
			SessionID: sess.ID,
			SubjectID: sess.ID,
			Context:   "assistant_response",
			Decision:  decision,
		}); err != nil {
			return SendResult{}, err
		}
	}
	if !decision.Allowed {
		return SendResult{}, fmt.Errorf("policy denied assistant response: %s", decision.Reason)
	}
	reply, err := m.store.SaveSessionMessage(ctx, NewMessage(sess.ID, "assistant", resp.Text))
	if err != nil {
		return SendResult{}, err
	}

	return SendResult{
		Session:      sess,
		UserMessage:  userMsg,
		ReplyMessage: reply,
		Text:         resp.Text,
		Compacted:    compacted.Compacted,
		SummaryID:    compacted.Summary.ID,
	}, nil
}

func (m *Manager) compactAfterTurn(ctx context.Context, sess Session) (bool, int64, error) {
	ref, err := m.resolveModelRef(sess.Route)
	if err != nil {
		return false, 0, err
	}
	_, result, err := m.sessionChatPrompt(ctx, sess, ref, false)
	return result.Compacted, result.Summary.ID, err
}

func (m *Manager) sessionChatPrompt(ctx context.Context, sess Session, ref llm.ModelRef, replayOriginals bool) ([]contextmgr.Message, contextmgr.Result, error) {
	var result contextmgr.Result
	active, err := m.store.ListSessionMessages(ctx, sess.ID, false)
	if err != nil {
		return nil, result, err
	}
	summary, _, err := m.store.LatestContextSummary(ctx, sess.ID)
	if err != nil {
		return nil, result, err
	}
	window := m.router.ContextWindow(ref)
	limit, err := window.InputLimit(window.DefaultOutputTokens)
	if err != nil {
		return nil, result, err
	}
	recent := toContextMessages(active)
	prompt, err := m.contextMgr.BuildPrompt(systemPrompt(sess), summary, recent)
	if err != nil {
		return nil, result, err
	}
	estimate := func(messages []contextmgr.Message) int {
		n := 8
		for _, message := range messages {
			n += contextmgr.EstimateMessageTokens(message.Role, message.Content)
		}
		return n
	}
	tokens := estimate(prompt)
	if len(active) > 1 && tokens >= contextmgr.AutoCompactInputThreshold(limit) {
		result, err = m.compactSessionMessages(ctx, sess, active)
		if err != nil {
			return nil, result, err
		}
		summary, recent = result.Summary, result.Preserved
		prompt, err = m.contextMgr.BuildPrompt(systemPrompt(sess), summary, recent)
		if err != nil {
			return nil, result, err
		}
		tokens = estimate(prompt)
	}
	if tokens > limit {
		return nil, result, apperror.New(apperror.CodeResourceExhausted, "session context exceeds the model input window")
	}
	if replayOriginals && strings.TrimSpace(summary.Content) != "" && len(recent) > 0 {
		originals, err := m.compactedOperatorOriginals(ctx, sess.ID, recent[0].SourceMessageID, min(MaxRecentOperatorTokens, limit-tokens), summary)
		if err != nil {
			return nil, result, err
		}
		if len(originals) > 0 {
			prompt, err = m.contextMgr.BuildSessionPrompt(ctx, sess.ID, systemPrompt(sess), summary, recent, originals)
			if err != nil {
				return nil, result, err
			}
		}
	}
	return prompt, result, nil
}

func (m *Manager) compactedOperatorOriginals(ctx context.Context, sessionID string, beforeID int64, remaining int, summary contextmgr.Summary) ([]contextmgr.Message, error) {
	reader, ok := m.store.(interface {
		ListCompactedSessionOperatorMessages(context.Context, string, int64, int) ([]Message, error)
	})
	if !ok || beforeID <= 0 || remaining <= 0 {
		return nil, nil
	}
	messages, err := reader.ListCompactedSessionOperatorMessages(ctx, sessionID, beforeID, remaining)
	if err != nil {
		return nil, err
	}
	var originals []contextmgr.Message
	var newestOmitted contextmgr.Message
	upperID, initialBudget := beforeID, remaining
	for _, message := range messages {
		if message.SessionID != sessionID || !message.Compacted || message.ID >= beforeID {
			return nil, apperror.New(apperror.CodeFailedPrecondition, "compacted operator history does not match the Session prefix")
		}
		beforeID = message.ID
		projected, verified, err := ProjectCompactedOperator(message)
		if err != nil {
			return nil, err
		}
		if !verified {
			continue
		}
		cost := contextmgr.EstimateMessageTokens(projected.Role, projected.Content)
		if cost <= remaining {
			remaining -= cost
			originals = append(originals, projected)
		} else if newestOmitted.SourceMessageID == 0 {
			newestOmitted = projected
		}
	}
	slices.Reverse(originals)
	if remaining <= 0 || len(originals) >= MaxRecentOperatorMessages {
		return originals, nil
	}
	if oversized, ok := m.store.(interface {
		LatestOversizedSessionOperator(context.Context, string, int64, int64, int) (Message, bool, error)
	}); ok {
		message, found, err := oversized.LatestOversizedSessionOperator(ctx, sessionID, newestOmitted.SourceMessageID, upperID, initialBudget)
		if err != nil {
			return nil, err
		}
		if found {
			if message.SessionID != sessionID || !message.Compacted || message.ID <= newestOmitted.SourceMessageID || message.ID >= upperID {
				return nil, apperror.New(apperror.CodeFailedPrecondition, "oversized operator source does not match the Session prefix")
			}
			projected, verified, err := ProjectCompactedOperator(message)
			if err != nil {
				return nil, err
			}
			if verified {
				newestOmitted = projected
			}
		}
	}
	if newestOmitted.SourceMessageID != 0 {
		projected := newestOmitted
		if contextmgr.EstimateMessageTokens(projected.Role, projected.Content) > remaining {
			excerpt, fits, err := contextmgr.FitOperatorExcerpt(projected, summary, remaining)
			if err != nil || !fits {
				return originals, err
			}
			projected = excerpt
		}
		at := sort.Search(len(originals), func(i int) bool { return originals[i].SourceMessageID >= projected.SourceMessageID })
		originals = slices.Insert(originals, at, projected)
	}
	return originals, nil
}

func (m *Manager) compactActiveMessages(ctx context.Context, sess Session) (contextmgr.Result, error) {
	active, err := m.store.ListSessionMessages(ctx, sess.ID, false)
	if err != nil {
		return contextmgr.Result{}, err
	}
	return m.compactSessionMessages(ctx, sess, active)
}

func (m *Manager) compactSessionMessages(ctx context.Context, sess Session, active []Message) (contextmgr.Result, error) {
	result, err := m.contextMgr.Compact(ctx, sess.ID, sess.WorkspaceID, toContextMessages(active))
	if err != nil {
		return contextmgr.Result{}, err
	}
	if result.RemovedMessages > 0 {
		through := active[result.RemovedMessages-1].ID
		if _, err := m.store.MarkSessionMessagesCompacted(ctx, sess.ID, through); err != nil {
			return contextmgr.Result{}, err
		}
	}
	return result, nil
}

func (m *Manager) chatRoute(ctx context.Context, route string, req llm.ChatRequest) (*llm.ChatResponse, error) {
	if strings.Contains(route, "/") {
		ref, err := llm.ParseModelRef(route)
		if err != nil {
			return nil, err
		}
		return m.router.ChatModelRef(ctx, ref, req)
	}
	return m.router.Chat(ctx, route, req)
}

func (m *Manager) resolveModelRef(route string) (llm.ModelRef, error) {
	if strings.Contains(route, "/") {
		return llm.ParseModelRef(route)
	}
	return m.router.Resolve(route), nil
}

func systemPrompt(sess Session) string {
	return "You are Universal Code, a local-first coding agent. Prefer safe, scoped, auditable actions. " +
		UntrustedContextPolicy + " CTF-specific solving is deferred unless explicitly requested."
}

func toContextMessages(messages []Message) []contextmgr.Message {
	out := make([]contextmgr.Message, 0, len(messages))
	for _, msg := range messages {
		out = append(out, ProjectContextMessage(msg))
	}
	return out
}

func toLLMMessages(messages []contextmgr.Message) []llm.Message {
	out := make([]llm.Message, 0, len(messages))
	for _, msg := range messages {
		out = append(out, llm.Message{Role: msg.Role, Content: msg.Content})
	}
	return out
}

func newID(prefix string) string {
	return idgen.New(prefix)
}
