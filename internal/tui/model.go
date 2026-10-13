package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolrun"
)

type Model struct {
	session            session.Session
	sessionManager     *session.Manager
	toolManager        ToolManager
	workspaceStore     WorkspaceStore
	runStateStore      RunStateStore
	sessionToolManager SessionToolManager
	activeCalls        ActiveCallController
	input              textinput.Model
	messages           []session.Message
	toolRuns           []toolrun.ToolRun
	workspace          workspaceContext
	runContext         runContext
	status             string
	busy               bool
	width              int
	height             int
	focus              focusArea
	messageScroll      int
	selectedTool       int
	toolScroll         int
	activityView       activityView
	selectedWorkItem   int
	workItemScroll     int
	selectedNote       int
	noteScroll         int
	selectedRound      int
	roundScroll        int
	selectedEvent      int
	eventScroll        int
	selectedAgent      int
	agentScroll        int
	selectedFinding    int
	findingScroll      int
	selectedEdit       int
	editScroll         int
	editDetailOpen     bool
	editDetailScroll   int
	eventPollError     bool
	live               activeCallView
	liveGeneration     uint64
	liveDiscoverCancel context.CancelFunc
	actionCancel       context.CancelFunc
	liveSubscription   ActiveCallSubscription
	cancelPending      bool
}

type actionDoneMsg struct {
	result actionResult
	err    error
}

type steeringQueuedMsg struct {
	result domain.OperatorSteeringEnqueueResult
	err    error
}

type actionResult struct {
	session        session.Session
	messages       []session.Message
	toolRuns       []toolrun.ToolRun
	workspace      workspaceContext
	runContext     runContext
	status         string
	selectedToolID string
}

type WorkspaceStore interface {
	GetWorkspaceByID(ctx context.Context, id string) (store.WorkspaceRecord, error)
}

type ToolManager interface {
	List(context.Context, toolrun.ListFilter) ([]toolrun.ToolRun, error)
	Get(context.Context, string) (toolrun.ToolRun, error)
	Approve(context.Context, string) (toolrun.ToolRun, error)
	Deny(context.Context, string, string) (toolrun.ToolRun, error)
}

type SessionToolManager interface {
	ApproveForSession(ctx context.Context, id string, expectedSessionID string, reason string, grantedBy string,
		idempotencyKey string) (toolrun.ToolRun, approval.SessionGrant, error)
}

type RunStateStore interface {
	GetRunBySession(ctx context.Context, sessionID string) (domain.Run, bool, error)
	GetRunMode(ctx context.Context, runID string) (domain.RunModeSnapshot, error)
	ListRunEventsAfterSequence(ctx context.Context, runID string,
		afterSequence int64, limit int) ([]events.Event, error)
	LatestRunEventSequence(ctx context.Context, runID string) (int64, error)
	ListWorkItems(ctx context.Context, filter domain.WorkItemFilter) ([]domain.WorkItem, error)
	ListNotes(ctx context.Context, filter domain.NoteFilter) ([]domain.Note, error)
	ListRunSupervisorToolRoundsPage(ctx context.Context, runID string, offset int,
		limit int) ([]domain.SupervisorToolRound, error)
	ListPlanDeliveryProposals(ctx context.Context, runID string, limit int) ([]domain.PlanDeliveryProposal, error)
	GetPlanDeliveryProposal(ctx context.Context, id string) (domain.PlanDeliveryProposal, error)
	GetPlanDeliverySelectionByRun(ctx context.Context, runID string) (domain.PlanDeliverySelection, bool, error)
	ListDeliveryCheckpoints(ctx context.Context, runID string, limit int) ([]domain.DeliveryCheckpoint, error)
	DeliveryGateEnforced(ctx context.Context, runID string) (bool, error)
	ListSessionGrants(ctx context.Context, filter approval.GrantListFilter) ([]approval.SessionGrant, error)
	ListAgentNodes(ctx context.Context, runID string) ([]domain.AgentNode, error)
	GetAgentCompletion(ctx context.Context, agentID string) (domain.AgentCompletion, bool, error)
	ListFindingReportSummariesPage(ctx context.Context, runID string,
		offset int, limit int) ([]domain.FindingReportSummary, error)
	ListFileEditPreviewsPage(ctx context.Context, filter fileedit.ListFilter,
		offset int, limit int) ([]fileedit.Preview, error)
	EnqueueOperatorSteering(ctx context.Context,
		request domain.EnqueueOperatorSteeringRequest) (domain.OperatorSteeringEnqueueResult, error)
	ListOperatorSteering(ctx context.Context, runID string,
		limit int) ([]domain.OperatorSteeringMessage, error)
	GetOperatorSteeringQueueSummary(ctx context.Context,
		runID string) (domain.OperatorSteeringQueueSummary, error)
}

type externalSkillProjectionStore interface {
	GetExternalSkillProjectionByRun(ctx context.Context, runID string) (
		skills.ExternalSkillProjection, bool, error)
}

type workspaceContext struct {
	ID       string
	Name     string
	RootPath string
	Items    []workspaceItem
	Error    string
}

type workspaceItem struct {
	Name   string
	Count  int
	Exists bool
	Error  string
}

type runContext struct {
	Found                bool
	Run                  domain.Run
	Mode                 domain.RunModeSnapshot
	WorkItems            []domain.WorkItem
	Notes                []domain.Note
	ToolRounds           []domain.SupervisorToolRound
	Grants               []approval.SessionGrant
	Events               []events.Event
	EventSequence        int64
	Agents               []agentContext
	FindingReports       []domain.FindingReportSummary
	FileEdits            []fileEditContext
	FileEditsTruncated   bool
	PlanProposal         *domain.PlanDeliveryProposal
	PlanSelection        *domain.PlanDeliverySelection
	DeliveryGateEnforced bool
	DeliveryCheckpoints  []domain.DeliveryCheckpoint
	Steering             operatorSteeringContext
	ExternalSkills       *skills.ExternalSkillProjection
}

type operatorSteeringContext struct {
	Pending   int
	Prepared  int
	Committed int
	Cancelled int
	Messages  []operatorSteeringMetadata
}

type operatorSteeringMetadata struct {
	ID        string
	Sequence  int64
	Status    domain.OperatorSteeringStatus
	CreatedAt time.Time
}

type agentContext struct {
	Node       domain.AgentNode
	Completion *domain.AgentCompletion
}

type activityView string

const (
	activityTools        activityView = "tools"
	activityPlan         activityView = "plan"
	activitySkills       activityView = "skills"
	activityWork         activityView = "work"
	activityNotes        activityView = "notes"
	activityRounds       activityView = "rounds"
	activityEvents       activityView = "events"
	activityAgents       activityView = "agents"
	activityFindings     activityView = "findings"
	activityEdits        activityView = "edits"
	activityQueue        activityView = "queue"
	maxTUIRunItems                    = 50
	maxTUIToolRounds                  = 20
	maxTUIFindingReports              = 10
)

type focusArea string

const (
	focusInput focusArea = "input"
	focusTools focusArea = "tools"
)

func NewModel(ctx context.Context, sess session.Session, sessionManager *session.Manager, toolManager ToolManager, workspaceStores ...WorkspaceStore) (*Model, error) {
	input := textinput.New()
	input.Placeholder = "Message or slash command"
	input.Prompt = ""
	input.CharLimit = 4000
	input.Width = 80
	input.Focus()

	var workspaceStore WorkspaceStore
	var runStateStore RunStateStore
	if len(workspaceStores) > 0 {
		workspaceStore = workspaceStores[0]
		runStateStore, _ = any(workspaceStore).(RunStateStore)
	}
	sessionToolManager, _ := toolManager.(SessionToolManager)
	model := &Model{
		session: sess, sessionManager: sessionManager, toolManager: toolManager,
		workspaceStore: workspaceStore, runStateStore: runStateStore,
		sessionToolManager: sessionToolManager, input: input, status: "ready",
		width: 100, height: 32, focus: focusInput, activityView: activityTools,
	}
	if err := model.Refresh(ctx); err != nil {
		return nil, err
	}
	if len(model.runContext.Events) > 0 {
		model.selectedEvent = len(model.runContext.Events) - 1
	}
	return model, nil
}

func (m *Model) Init() tea.Cmd {
	if m.runStateStore == nil || !m.runContext.Found || m.runContext.Run.Terminal() {
		return textinput.Blink
	}
	return tea.Batch(textinput.Blink, m.scheduleRunEventPoll(tuiEventPollInterval))
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case runEventPollTickMsg:
		return m, m.pollRunEventsCmd(msg)
	case runEventPollResultMsg:
		return m.handleRunEventPollResult(msg)
	case activeCallSubscribedMsg:
		return m, m.handleActiveCallSubscribed(msg)
	case activeCallEventMsg:
		return m, m.handleActiveCallEvent(msg)
	case activeCallCancelDoneMsg:
		m.handleActiveCallCancelDone(msg)
		return m, nil
	case steeringQueuedMsg:
		if msg.err != nil {
			m.status = "queue error: " + msg.err.Error()
			return m, nil
		}
		m.status = fmt.Sprintf("queued steering %s at sequence %d",
			msg.result.Message.ID, msg.result.Message.Sequence)
		return m, nil
	case actionDoneMsg:
		m.stopLiveTracking(msg.err)
		m.busy = false
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
			return m, nil
		}
		m.applyActionResult(msg.result)
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.Width = max(20, msg.Width-4)
	case tea.KeyMsg:
		if m.editDetailOpen {
			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit
			case "esc", "enter", "b":
				m.editDetailOpen = false
				m.editDetailScroll = 0
				m.status = m.selectedEditStatus()
				return m, nil
			case "up", "k":
				m.scrollEditDetail(-1)
				return m, nil
			case "down", "j":
				m.scrollEditDetail(1)
				return m, nil
			case "pgup":
				m.scrollEditDetail(-8)
				return m, nil
			case "pgdown":
				m.scrollEditDetail(8)
				return m, nil
			default:
				return m, nil
			}
		}
		switch msg.String() {
		case "ctrl+c", "esc":
			if m.busy {
				m.status = "action running; Ctrl+X cancels the model call"
				return m, nil
			}
			return m, tea.Quit
		case "ctrl+x":
			if m.activeCalls == nil {
				m.status = "active call control unavailable"
				return m, nil
			}
			if m.cancelPending {
				m.status = "model cancellation already pending"
				return m, nil
			}
			m.cancelPending = true
			m.status = "requesting model cancellation..."
			return m, m.cancelActiveCallCmd(m.liveGeneration, m.session.ID)
		}
		if m.busy {
			if m.focus == focusInput && msg.String() == "enter" {
				value := strings.TrimSpace(m.input.Value())
				m.input.SetValue("")
				if value == "" {
					return m, nil
				}
				if strings.HasPrefix(value, "/") {
					m.status = "slash commands are unavailable while an action is running"
					return m, nil
				}
				if m.runStateStore == nil || !m.runContext.Found {
					m.status = "operator steering queue unavailable"
					return m, nil
				}
				m.status = "queueing operator guidance..."
				return m, m.enqueueSteeringCmd(value)
			}
			if m.focus == focusInput {
				m.input, cmd = m.input.Update(msg)
				return m, cmd
			}
			m.status = "busy; waiting for current action"
			return m, nil
		}
		switch msg.String() {
		case "ctrl+r":
			return m.startAction("refreshing...", m.refreshCmd("refreshed"))
		case "tab":
			m.ToggleFocus()
			return m, nil
		case "pgup":
			m.ScrollMessages(8)
			return m, nil
		case "pgdown":
			m.ScrollMessages(-8)
			return m, nil
		case "up", "k":
			if m.focus == focusInput && msg.String() == "k" {
				break
			}
			if m.focus == focusTools {
				m.SelectPreviousActivityItem()
			} else {
				m.ScrollMessages(1)
			}
			return m, nil
		case "down", "j":
			if m.focus == focusInput && msg.String() == "j" {
				break
			}
			if m.focus == focusTools {
				m.SelectNextActivityItem()
			} else {
				m.ScrollMessages(-1)
			}
			return m, nil
		case "left", "h":
			if m.focus == focusTools {
				m.PreviousActivityView()
				return m, nil
			}
		case "right", "l":
			if m.focus == focusTools {
				m.NextActivityView()
				return m, nil
			}
		case "a":
			if m.focus == focusTools {
				return m.selectedToolAction("approving", m.approveToolCmd)
			}
		case "g":
			if m.focus == focusTools {
				return m.selectedToolAction("authorizing session for", m.approveToolForSessionCmd)
			}
		case "d":
			if m.focus == focusTools {
				return m.selectedToolAction("denying", func(id string) tea.Cmd {
					return m.denyToolCmd(id, "denied from tui")
				})
			}
		case "enter":
			if m.focus == focusTools {
				if m.activityView == activityEdits {
					m.openSelectedEditDetail()
					return m, nil
				}
				return m.selectedToolAction("approving", m.approveToolCmd)
			}
			value := strings.TrimSpace(m.input.Value())
			m.input.SetValue("")
			if value == "" {
				return m, nil
			}
			if value == "/quit" || value == "/exit" {
				return m, tea.Quit
			}
			return m.startSubmitAction(statusForInput(value), value)
		}
	}
	if m.focus == focusInput {
		m.input, cmd = m.input.Update(msg)
	}
	return m, cmd
}

func (m *Model) View() string {
	return m.Snapshot()
}

func (m *Model) Snapshot() string {
	if m.editDetailOpen {
		return m.renderEditDetailScreen()
	}
	width := max(80, m.width)
	height := max(24, m.height)
	contentHeight := max(8, height-8)
	sideWidth := 34
	messageWidth := width - sideWidth - 6
	if width < 110 {
		messageWidth = width - 4
		sideWidth = width - 4
	}

	headerText := fmt.Sprintf("Universal Code  session=%s  route=%s", m.session.ID, m.session.Route)
	if m.runContext.Found {
		headerText = fmt.Sprintf("Universal Code  run=%s  status=%s  mode=%s/%s  session=%s  route=%s",
			m.runContext.Run.ID, m.runContext.Run.Status, m.runContext.Mode.Surface,
			m.runContext.Mode.Phase, m.session.ID, m.session.Route)
	}
	header := headerStyle.Width(width).Render(truncate(headerText, max(20, width-2)))
	messages := panelStyle.Width(messageWidth).Height(contentHeight).Render(m.renderMessages(messageWidth-2, contentHeight-2))
	workspaceHeight := min(8, max(6, contentHeight/3))
	toolHeight := max(6, contentHeight-workspaceHeight-1)
	workspace := panelStyle.Width(sideWidth).Height(workspaceHeight).Render(m.renderWorkspace(sideWidth-2, workspaceHeight-2))
	tools := panelStyle.Width(sideWidth).Height(toolHeight).Render(m.renderActivity(sideWidth-2, toolHeight-2))
	sidebar := lipgloss.JoinVertical(lipgloss.Left, workspace, tools)
	body := lipgloss.JoinHorizontal(lipgloss.Top, messages, "  ", sidebar)
	if width < 110 {
		body = lipgloss.JoinVertical(lipgloss.Left, messages, workspace, tools)
	}
	inputPrefix := "> "
	if m.focus == focusTools {
		inputPrefix = "  "
	}
	input := inputStyle.Width(width).Render(inputPrefix + m.input.View())
	status := statusStyle.Width(width).Render(m.statusLine())
	footer := footerStyle.Width(width).Render(footerHelp(width))
	return lipgloss.JoinVertical(lipgloss.Left, header, body, input, status, footer)
}

func (m *Model) Submit(ctx context.Context, input string) error {
	result, err := m.submitAction(ctx, m.session, input)
	if err != nil {
		return err
	}
	m.applyActionResult(result)
	return nil
}

func (m *Model) submitAction(ctx context.Context, sess session.Session, input string) (actionResult, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return m.refreshAction(ctx, sess, m.status, "")
	}
	switch {
	case input == "/approve-session" || strings.HasPrefix(input, "/approve-session "):
		id := strings.TrimSpace(strings.TrimPrefix(input, "/approve-session"))
		return m.approveForSessionAction(ctx, sess, id)
	case input == "/approve" || strings.HasPrefix(input, "/approve "):
		id := strings.TrimSpace(strings.TrimPrefix(input, "/approve"))
		return m.approveAction(ctx, sess, id)
	case input == "/deny" || strings.HasPrefix(input, "/deny "):
		fields := strings.Fields(input)
		if len(fields) < 2 {
			return actionResult{}, fmt.Errorf("usage: /deny <tool-run-id> [reason]")
		}
		reason := strings.TrimSpace(strings.TrimPrefix(input, fields[0]+" "+fields[1]))
		return m.denyAction(ctx, sess, fields[1], reason)
	case input == "/tools":
		return m.refreshAction(ctx, sess, "tool list refreshed", "")
	default:
		result, err := m.sessionManager.Send(ctx, sess.ID, input)
		if err != nil {
			return actionResult{}, err
		}
		status := "sent"
		selectedID := ""
		if result.ToolRunID != "" {
			status = "tool proposed: " + result.ToolRunID
			selectedID = result.ToolRunID
		} else if result.RunID != "" {
			status = fmt.Sprintf("run %s: %s -> %s", result.RunID, result.RunAction, result.RunStatus)
			if result.Queued {
				status += fmt.Sprintf("; steering=%s sequence=%d", result.SteeringID,
					result.SteeringSequence)
			}
			if result.Compacted {
				status += fmt.Sprintf("; context compacted: summary=%d", result.SummaryID)
			}
		} else if result.Compacted {
			status = fmt.Sprintf("context compacted: summary=%d", result.SummaryID)
		}
		return m.refreshAction(ctx, result.Session, status, selectedID)
	}
}

func (m *Model) ToggleFocus() {
	if m.focus == focusInput {
		m.focus = focusTools
		m.input.Blur()
		m.status = "activity focus: " + string(m.activityView)
		m.normalizeActivitySelection()
		return
	}
	m.focus = focusInput
	m.input.Focus()
	m.status = "input focus"
}

func (m *Model) ScrollMessages(delta int) {
	m.messageScroll += delta
	if m.messageScroll < 0 {
		m.messageScroll = 0
	}
	maxScroll := max(0, len(m.messageLines(80))-1)
	if m.messageScroll > maxScroll {
		m.messageScroll = maxScroll
	}
	m.status = fmt.Sprintf("message scroll: %d", m.messageScroll)
}

func (m *Model) SelectNextTool() {
	if len(m.toolRuns) == 0 {
		m.status = "no tool runs"
		return
	}
	m.selectedTool++
	m.normalizeSelection()
	m.status = "selected " + m.toolRuns[m.selectedTool].ID
}

func (m *Model) SelectPreviousTool() {
	if len(m.toolRuns) == 0 {
		m.status = "no tool runs"
		return
	}
	m.selectedTool--
	m.normalizeSelection()
	m.status = "selected " + m.toolRuns[m.selectedTool].ID
}

func (m *Model) NextActivityView() {
	m.setActivityView(activityViewAt(m.activityView, 1))
}

func (m *Model) PreviousActivityView() {
	m.setActivityView(activityViewAt(m.activityView, -1))
}

func (m *Model) setActivityView(view activityView) {
	switch view {
	case activityTools, activityPlan, activitySkills, activityWork, activityNotes, activityRounds,
		activityEvents, activityAgents, activityFindings, activityEdits, activityQueue:
		m.activityView = view
	default:
		m.activityView = activityTools
	}
	m.normalizeActivitySelection()
	m.status = "activity view: " + string(m.activityView)
}

func activityViewAt(current activityView, delta int) activityView {
	views := activityViews()
	index := 0
	for candidate, view := range views {
		if view == current {
			index = candidate
			break
		}
	}
	index = (index + delta) % len(views)
	if index < 0 {
		index += len(views)
	}
	return views[index]
}

func (m *Model) SelectNextActivityItem() {
	switch m.activityView {
	case activityTools:
		m.SelectNextTool()
	case activityPlan:
		m.status = "Plan/Delivery view is read-only"
	case activitySkills:
		m.status = "external Skills view is read-only"
	case activityQueue:
		m.status = "operator steering view is read-only"
	case activityWork:
		m.selectedWorkItem++
		m.normalizeActivitySelection()
		m.status = m.selectedWorkItemStatus()
	case activityNotes:
		m.selectedNote++
		m.normalizeActivitySelection()
		m.status = m.selectedNoteStatus()
	case activityRounds:
		m.selectedRound++
		m.normalizeActivitySelection()
		m.status = m.selectedRoundStatus()
	case activityEvents:
		m.selectedEvent++
		m.normalizeActivitySelection()
		m.status = m.selectedEventStatus()
	case activityAgents:
		m.selectedAgent++
		m.normalizeActivitySelection()
		m.status = m.selectedAgentStatus()
	case activityFindings:
		m.selectedFinding++
		m.normalizeActivitySelection()
		m.status = m.selectedFindingStatus()
	case activityEdits:
		m.selectedEdit++
		m.normalizeActivitySelection()
		m.status = m.selectedEditStatus()
	}
}

func (m *Model) SelectPreviousActivityItem() {
	switch m.activityView {
	case activityTools:
		m.SelectPreviousTool()
	case activityPlan:
		m.status = "Plan/Delivery view is read-only"
	case activitySkills:
		m.status = "external Skills view is read-only"
	case activityQueue:
		m.status = "operator steering view is read-only"
	case activityWork:
		m.selectedWorkItem--
		m.normalizeActivitySelection()
		m.status = m.selectedWorkItemStatus()
	case activityNotes:
		m.selectedNote--
		m.normalizeActivitySelection()
		m.status = m.selectedNoteStatus()
	case activityRounds:
		m.selectedRound--
		m.normalizeActivitySelection()
		m.status = m.selectedRoundStatus()
	case activityEvents:
		m.selectedEvent--
		m.normalizeActivitySelection()
		m.status = m.selectedEventStatus()
	case activityAgents:
		m.selectedAgent--
		m.normalizeActivitySelection()
		m.status = m.selectedAgentStatus()
	case activityFindings:
		m.selectedFinding--
		m.normalizeActivitySelection()
		m.status = m.selectedFindingStatus()
	case activityEdits:
		m.selectedEdit--
		m.normalizeActivitySelection()
		m.status = m.selectedEditStatus()
	}
}

func (m *Model) ApproveSelectedTool(ctx context.Context) error {
	run, ok := m.selectedToolRun()
	if !ok {
		m.status = "no selected tool run"
		return nil
	}
	if run.Status != toolrun.StatusProposed {
		m.status = fmt.Sprintf("%s is %s", run.ID, run.Status)
		return nil
	}
	result, err := m.approveAction(ctx, m.session, run.ID)
	if err != nil {
		return err
	}
	m.applyActionResult(result)
	return nil
}

func (m *Model) ApproveSelectedToolForSession(ctx context.Context) error {
	run, ok := m.selectedToolRun()
	if !ok {
		m.status = "no selected tool run"
		return nil
	}
	if run.Status != toolrun.StatusProposed {
		m.status = fmt.Sprintf("%s is %s", run.ID, run.Status)
		return nil
	}
	result, err := m.approveForSessionAction(ctx, m.session, run.ID)
	if err != nil {
		return err
	}
	m.applyActionResult(result)
	return nil
}

func (m *Model) DenySelectedTool(ctx context.Context, reason string) error {
	run, ok := m.selectedToolRun()
	if !ok {
		m.status = "no selected tool run"
		return nil
	}
	if run.Status != toolrun.StatusProposed {
		m.status = fmt.Sprintf("%s is %s", run.ID, run.Status)
		return nil
	}
	result, err := m.denyAction(ctx, m.session, run.ID, reason)
	if err != nil {
		return err
	}
	m.applyActionResult(result)
	return nil
}

func (m *Model) Refresh(ctx context.Context) error {
	result, err := loadActionResult(ctx, m.session, m.sessionManager, m.toolManager,
		m.workspaceStore, m.runStateStore, m.status, "")
	if err != nil {
		return err
	}
	m.applyActionResult(result)
	return nil
}

func (m *Model) refreshAction(ctx context.Context, sess session.Session, status string, selectedToolID string) (actionResult, error) {
	return loadActionResult(ctx, sess, m.sessionManager, m.toolManager,
		m.workspaceStore, m.runStateStore, status, selectedToolID)
}

func loadActionResult(ctx context.Context, sess session.Session, sessionManager *session.Manager,
	toolManager ToolManager, workspaceStore WorkspaceStore, runStateStore RunStateStore,
	status string, selectedToolID string,
) (actionResult, error) {
	for attempt := 0; attempt < maxTUISnapshotAttempts; attempt++ {
		beforeRun, beforeFound, err := currentTUIRunBoundary(ctx, runStateStore, sess.ID)
		if err != nil {
			return actionResult{}, err
		}
		beforeSequence := int64(0)
		if beforeFound {
			beforeSequence, err = runStateStore.LatestRunEventSequence(ctx, beforeRun.ID)
			if err != nil {
				return actionResult{}, err
			}
		}
		messages, err := sessionManager.History(ctx, sess.ID, false)
		if err != nil {
			return actionResult{}, err
		}
		runs, err := toolManager.List(ctx, toolrun.ListFilter{SessionID: sess.ID})
		if err != nil {
			return actionResult{}, err
		}
		runView, err := loadRunContext(ctx, runStateStore, sess)
		if err != nil {
			return actionResult{}, err
		}
		afterRun, afterFound, err := currentTUIRunBoundary(ctx, runStateStore, sess.ID)
		if err != nil {
			return actionResult{}, err
		}
		if beforeFound != afterFound {
			continue
		}
		if beforeFound {
			if beforeRun.ID != afterRun.ID || beforeRun.MissionID != afterRun.MissionID {
				continue
			}
			afterSequence, err := runStateStore.LatestRunEventSequence(ctx, afterRun.ID)
			if err != nil {
				return actionResult{}, err
			}
			if beforeSequence != afterSequence || !runView.Found ||
				runView.Run.ID != afterRun.ID || runView.Run.MissionID != afterRun.MissionID ||
				runView.EventSequence != afterSequence {
				continue
			}
		} else if runView.Found {
			continue
		}
		return actionResult{
			session: sess, messages: messages, toolRuns: runs,
			workspace: loadWorkspaceContext(ctx, workspaceStore, sess), runContext: runView,
			status: status, selectedToolID: selectedToolID,
		}, nil
	}
	return actionResult{}, errors.New("TUI Run projection changed during bounded snapshot retries")
}

const maxTUISnapshotAttempts = 8

func currentTUIRunBoundary(ctx context.Context, runStateStore RunStateStore,
	sessionID string,
) (domain.Run, bool, error) {
	if runStateStore == nil {
		return domain.Run{}, false, nil
	}
	return runStateStore.GetRunBySession(ctx, sessionID)
}

func (m *Model) approveAction(ctx context.Context, sess session.Session, id string) (actionResult, error) {
	if err := m.requireSessionToolRun(ctx, sess.ID, id); err != nil {
		return actionResult{}, err
	}
	run, err := m.toolManager.Approve(ctx, id)
	if err != nil {
		return actionResult{}, err
	}
	return m.refreshAction(ctx, sess, fmt.Sprintf("approved %s -> %s", run.ID, run.Status), run.ID)
}

func (m *Model) approveForSessionAction(ctx context.Context, sess session.Session,
	id string,
) (actionResult, error) {
	if strings.TrimSpace(id) == "" {
		return actionResult{}, errors.New("usage: /approve-session <tool-run-id>")
	}
	if m.sessionToolManager == nil {
		return actionResult{}, errors.New("session approval is unavailable")
	}
	if err := m.requireSessionToolRun(ctx, sess.ID, id); err != nil {
		return actionResult{}, err
	}
	run, grant, err := m.sessionToolManager.ApproveForSession(ctx, id, sess.ID,
		"approved for this session from TUI", "tui_operator", idgen.New("tuigrantop"))
	if err != nil {
		return actionResult{}, err
	}
	status := fmt.Sprintf("session grant %s active; approved %s -> %s", grant.ID, run.ID, run.Status)
	return m.refreshAction(ctx, sess, status, run.ID)
}

func (m *Model) denyAction(ctx context.Context, sess session.Session, id string, reason string) (actionResult, error) {
	if err := m.requireSessionToolRun(ctx, sess.ID, id); err != nil {
		return actionResult{}, err
	}
	run, err := m.toolManager.Deny(ctx, id, reason)
	if err != nil {
		return actionResult{}, err
	}
	return m.refreshAction(ctx, sess, fmt.Sprintf("denied %s -> %s", run.ID, run.Status), run.ID)
}

func (m *Model) requireSessionToolRun(ctx context.Context, sessionID string, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("tool run id is required")
	}
	run, err := m.toolManager.Get(ctx, id)
	if err != nil {
		return err
	}
	if run.SessionID != strings.TrimSpace(sessionID) {
		return errors.New("tool run does not belong to the current Session")
	}
	return nil
}

func (m *Model) submitCmd(input string) tea.Cmd {
	return m.submitCmdContext(context.Background(), input)
}

func (m *Model) submitCmdContext(ctx context.Context, input string) tea.Cmd {
	sess := m.session
	return func() tea.Msg {
		result, err := m.submitAction(ctx, sess, input)
		return actionDoneMsg{result: result, err: err}
	}
}

func (m *Model) enqueueSteeringCmd(input string) tea.Cmd {
	run := m.runContext.Run
	stateStore := m.runStateStore
	return func() tea.Msg {
		result, err := stateStore.EnqueueOperatorSteering(context.Background(),
			domain.EnqueueOperatorSteeringRequest{
				RunID: run.ID, SessionID: run.SessionID, Content: input,
				OperationKey: idgen.New("tui-steering"), RequestedBy: "tui_operator",
			})
		return steeringQueuedMsg{result: result, err: err}
	}
}

func (m *Model) refreshCmd(status string) tea.Cmd {
	sess := m.session
	return func() tea.Msg {
		result, err := m.refreshAction(context.Background(), sess, status, "")
		return actionDoneMsg{result: result, err: err}
	}
}

func (m *Model) approveToolCmd(id string) tea.Cmd {
	sess := m.session
	return func() tea.Msg {
		result, err := m.approveAction(context.Background(), sess, id)
		return actionDoneMsg{result: result, err: err}
	}
}

func (m *Model) approveToolForSessionCmd(id string) tea.Cmd {
	sess := m.session
	return func() tea.Msg {
		result, err := m.approveForSessionAction(context.Background(), sess, id)
		return actionDoneMsg{result: result, err: err}
	}
}

func (m *Model) denyToolCmd(id string, reason string) tea.Cmd {
	sess := m.session
	return func() tea.Msg {
		result, err := m.denyAction(context.Background(), sess, id, reason)
		return actionDoneMsg{result: result, err: err}
	}
}

func (m *Model) startAction(status string, cmd tea.Cmd) (tea.Model, tea.Cmd) {
	m.busy = true
	m.status = status
	return m, cmd
}

func (m *Model) selectedToolAction(verb string, makeCmd func(string) tea.Cmd) (tea.Model, tea.Cmd) {
	if m.activityView != activityTools {
		m.status = string(m.activityView) + " view is read-only"
		return m, nil
	}
	run, ok := m.selectedToolRun()
	if !ok {
		m.status = "no selected tool run"
		return m, nil
	}
	if run.Status != toolrun.StatusProposed {
		m.status = fmt.Sprintf("%s is %s", run.ID, run.Status)
		return m, nil
	}
	return m.startAction(verb+" "+run.ID+"...", makeCmd(run.ID))
}

func (m *Model) applyActionResult(result actionResult) {
	m.normalizeActivitySelection()
	oldSelection := ""
	if run, ok := m.selectedToolRun(); ok {
		oldSelection = run.ID
	}
	oldEventSequence := int64(0)
	followEventTail := len(m.runContext.Events) == 0 ||
		m.selectedEvent == len(m.runContext.Events)-1
	if len(m.runContext.Events) > 0 {
		oldEventSequence = m.runContext.Events[m.selectedEvent].Sequence
	}
	oldAgentID := ""
	if len(m.runContext.Agents) > 0 {
		oldAgentID = m.runContext.Agents[m.selectedAgent].Node.ID
	}
	oldFindingID := ""
	if len(m.runContext.FindingReports) > 0 {
		oldFindingID = m.runContext.FindingReports[m.selectedFinding].ID
	}
	oldEditID := ""
	if len(m.runContext.FileEdits) > 0 {
		oldEditID = m.runContext.FileEdits[m.selectedEdit].Preview.ID
	}
	m.session = result.session
	m.messages = result.messages
	m.toolRuns = result.toolRuns
	m.workspace = result.workspace
	m.runContext = result.runContext
	m.status = result.status
	target := result.selectedToolID
	if target == "" {
		target = oldSelection
	}
	m.selectToolByID(target)
	m.selectEvent(oldEventSequence, followEventTail)
	m.selectAgentByID(oldAgentID)
	m.selectFindingByID(oldFindingID)
	m.selectEditByID(oldEditID)
	m.normalizeActivitySelection()
}

func (m *Model) renderMessages(width int, height int) string {
	lines := []string{m.messageTitle()}
	messageLines := m.messageLines(width)
	lines = append(lines, windowFromBottom(messageLines, max(0, height-1), m.messageScroll)...)
	return strings.Join(lines, "\n")
}

func (m *Model) messageLines(width int) []string {
	var lines []string
	for _, msg := range m.messages {
		prefix := msg.Role + ": "
		lines = append(lines, wrap(prefix+singleLine(msg.Content), width)...)
	}
	if len(lines) == 0 {
		return []string{"no messages"}
	}
	return lines
}

func (m *Model) renderActivity(width int, height int) string {
	switch m.activityView {
	case activityPlan:
		return m.renderPlanDelivery(width, height)
	case activitySkills:
		return m.renderExternalSkills(width, height)
	case activityWork:
		return m.renderWorkItems(width, height)
	case activityNotes:
		return m.renderNotes(width, height)
	case activityRounds:
		return m.renderToolRounds(width, height)
	case activityEvents:
		return m.renderEvents(width, height)
	case activityAgents:
		return m.renderAgents(width, height)
	case activityFindings:
		return m.renderFindings(width, height)
	case activityEdits:
		return m.renderEdits(width, height)
	case activityQueue:
		return m.renderOperatorSteering(width, height)
	default:
		return m.renderTools(width, height)
	}
}

func (m *Model) renderOperatorSteering(width int, height int) string {
	queue := m.runContext.Steering
	lines := m.activityHeader(fmt.Sprintf("Operator Queue %d",
		queue.Pending+queue.Prepared), activityQueue, width)
	if !m.runContext.Found {
		lines = append(lines, "no Run attached")
		return strings.Join(lines, "\n")
	}
	lines = append(lines, truncate(fmt.Sprintf("queued=%d prepared=%d committed=%d cancelled=%d",
		queue.Pending, queue.Prepared, queue.Committed, queue.Cancelled), width))
	if len(queue.Messages) == 0 {
		lines = append(lines, "none")
		return strings.Join(windowTop(lines, height+1), "\n")
	}
	for _, message := range queue.Messages {
		lines = append(lines, truncate(fmt.Sprintf("#%d %s %s", message.Sequence,
			message.Status, message.ID), width))
	}
	return strings.Join(windowTop(lines, height+1), "\n")
}

func (m *Model) renderPlanDelivery(width int, height int) string {
	lines := m.activityHeader("Plan / Delivery", activityPlan, width)
	if !m.runContext.Found {
		lines = append(lines, "no Run attached")
		return strings.Join(lines, "\n")
	}
	proposal := m.runContext.PlanProposal
	if proposal == nil {
		lines = append(lines, "none")
		return strings.Join(lines, "\n")
	}
	if m.runContext.PlanSelection == nil {
		lines = append(lines, "operator choice required; no capability granted")
	} else {
		selection := m.runContext.PlanSelection
		state := fmt.Sprintf("selected=%d work-items=%d", selection.DirectionOrdinal,
			len(selection.Items))
		ready := readyDeliveryCheckpointCount(m.runContext.DeliveryCheckpoints,
			m.runContext.WorkItems, m.runContext.Mode)
		state += fmt.Sprintf("; delivery-gates=%d/%d enforced=%t", ready,
			len(selection.Items), m.runContext.DeliveryGateEnforced)
		if m.runContext.Mode.Phase == domain.ExecutionPhasePlan {
			state += "; explicit Deliver phase required"
		}
		lines = append(lines, state)
	}
	for _, direction := range proposal.Spec.Directions {
		marker := " "
		if m.runContext.PlanSelection != nil &&
			m.runContext.PlanSelection.DirectionOrdinal == direction.Ordinal {
			marker = "*"
		}
		lines = append(lines, truncate(fmt.Sprintf("%s %d. %s (%d slices)", marker,
			direction.Ordinal, direction.Title, len(direction.Modules)), width))
		lines = append(lines, truncate("  "+singleLine(direction.Summary), width))
	}
	return strings.Join(windowTop(lines, height+1), "\n")
}

func (m *Model) renderTools(width int, height int) string {
	lines := m.activityHeader("Tool Runs", activityTools, width)
	if grant, ok := m.activeGrant(string(toolrun.ShellTool)); ok {
		lines = append(lines, truncate("session grant: "+grant.ID, width))
	}
	if len(m.toolRuns) == 0 {
		lines = append(lines, "none")
		return strings.Join(windowTop(lines, height+1), "\n")
	}
	visible := max(1, height-len(lines))
	m.ensureSelectedVisible(visible)
	end := min(len(m.toolRuns), m.toolScroll+visible)
	for i := m.toolScroll; i < end; i++ {
		run := m.toolRuns[i]
		marker := " "
		if i == m.selectedTool {
			marker = ">"
		}
		lines = append(lines, truncate(fmt.Sprintf("%s %s %s", marker, run.Status, run.ID), width))
		lines = append(lines, truncate("  "+run.Command, width))
		if run.Stdout != "" && i == m.selectedTool {
			lines = append(lines, truncate("  "+run.Stdout, width))
		}
	}
	return strings.Join(windowTop(lines, height+1), "\n")
}

func (m *Model) renderWorkItems(width int, height int) string {
	lines := m.activityHeader(fmt.Sprintf("Work Board %d", len(m.runContext.WorkItems)), activityWork, width)
	if !m.runContext.Found {
		lines = append(lines, "no Run attached")
		return strings.Join(lines, "\n")
	}
	if len(m.runContext.WorkItems) == 0 {
		lines = append(lines, "none")
		return strings.Join(lines, "\n")
	}
	visible := max(1, height-2)
	m.ensureWorkItemVisible(visible)
	end := min(len(m.runContext.WorkItems), m.workItemScroll+visible)
	for index := m.workItemScroll; index < end; index++ {
		item := m.runContext.WorkItems[index]
		marker := " "
		if index == m.selectedWorkItem {
			marker = ">"
		}
		lines = append(lines, truncate(fmt.Sprintf("%s %s/%s %s", marker, item.Status,
			item.Priority, item.Title), width))
	}
	selected := m.runContext.WorkItems[m.selectedWorkItem]
	owner := selected.Owner
	if owner == "" {
		owner = selected.OwnerAgentID
	}
	detail := fmt.Sprintf("owner=%s deps=%d v=%d", defaultText(owner, "-"),
		len(selected.Dependencies), selected.Version)
	if selected.BlockedReason != "" {
		detail += " blocked=" + singleLine(selected.BlockedReason)
	}
	lines = append(lines, truncate(detail, width))
	return strings.Join(windowTop(lines, height+1), "\n")
}

func (m *Model) renderNotes(width int, height int) string {
	lines := m.activityHeader(fmt.Sprintf("Notes %d", len(m.runContext.Notes)), activityNotes, width)
	if !m.runContext.Found {
		lines = append(lines, "no Run attached")
		return strings.Join(lines, "\n")
	}
	if len(m.runContext.Notes) == 0 {
		lines = append(lines, "none")
		return strings.Join(lines, "\n")
	}
	visible := max(1, height-3)
	m.ensureNoteVisible(visible)
	end := min(len(m.runContext.Notes), m.noteScroll+visible)
	for index := m.noteScroll; index < end; index++ {
		note := m.runContext.Notes[index]
		marker := " "
		if index == m.selectedNote {
			marker = ">"
		}
		pin := ""
		if note.Pinned {
			pin = "*"
		}
		lines = append(lines, truncate(fmt.Sprintf("%s%s %s/%s %s", marker, pin, note.Category,
			note.Visibility, note.Title), width))
	}
	selected := m.runContext.Notes[m.selectedNote]
	owner := selected.Owner
	if owner == "" {
		owner = selected.OwnerAgentID
	}
	lines = append(lines, truncate("status="+string(selected.Status)+" owner="+defaultText(owner, "-")+
		" v="+fmt.Sprint(selected.Version), width))
	lines = append(lines, truncate(singleLine(selected.Content), width))
	return strings.Join(windowTop(lines, height+1), "\n")
}

func (m *Model) renderToolRounds(width int, height int) string {
	lines := m.activityHeader(fmt.Sprintf("Tool Rounds %d", len(m.runContext.ToolRounds)), activityRounds, width)
	if !m.runContext.Found {
		lines = append(lines, "no Run attached")
		return strings.Join(lines, "\n")
	}
	if len(m.runContext.ToolRounds) == 0 {
		lines = append(lines, "none")
		return strings.Join(lines, "\n")
	}
	m.normalizeActivitySelection()
	detailLines := len(m.runContext.ToolRounds[m.selectedRound].Calls)
	visible := max(1, height+1-len(lines)-detailLines)
	m.ensureRoundVisible(visible)
	end := min(len(m.runContext.ToolRounds), m.roundScroll+visible)
	for index := m.roundScroll; index < end; index++ {
		round := m.runContext.ToolRounds[index]
		marker := " "
		if index == m.selectedRound {
			marker = ">"
		}
		status := "pending"
		if round.Complete() {
			status = "complete"
		}
		lines = append(lines, truncate(fmt.Sprintf("%s turn=%d round=%d %s calls=%d", marker,
			round.Turn, round.Round, status, len(round.Calls)), width))
	}
	for _, call := range m.runContext.ToolRounds[m.selectedRound].Calls {
		lines = append(lines, truncate(fmt.Sprintf("  %d %s %s", call.Position, call.ToolName, call.Status), width))
	}
	return strings.Join(windowTop(lines, height+1), "\n")
}

func (m *Model) renderWorkspace(width int, height int) string {
	lines := []string{"Workspace"}
	if strings.TrimSpace(m.workspace.ID) == "" {
		lines = append(lines, "none attached")
		return strings.Join(windowTop(lines, height+1), "\n")
	}
	lines = append(lines, truncate("id: "+m.workspace.ID, width))
	if m.workspace.Name != "" {
		lines = append(lines, truncate("name: "+m.workspace.Name, width))
	}
	if m.workspace.RootPath != "" {
		lines = append(lines, truncate("root: "+m.workspace.RootPath, width))
	}
	if m.workspace.Error != "" {
		lines = append(lines, truncate("error: "+m.workspace.Error, width))
		return strings.Join(windowTop(lines, height+1), "\n")
	}
	for _, item := range m.workspace.Items {
		label := item.Name + ": missing"
		if item.Exists {
			label = fmt.Sprintf("%s: %d", item.Name, item.Count)
		}
		if item.Error != "" {
			label = item.Name + ": error"
		}
		lines = append(lines, truncate(label, width))
	}
	return strings.Join(windowTop(lines, height+1), "\n")
}

func Run(model *Model) error {
	_, err := tea.NewProgram(model, tea.WithAltScreen()).Run()
	return err
}

var (
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("63")).Padding(0, 1)
	panelStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	inputStyle  = lipgloss.NewStyle().Padding(0, 1)
	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Padding(0, 1)
	footerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Padding(0, 1)
)

func (m *Model) messageTitle() string {
	if m.messageScroll == 0 {
		return "Messages"
	}
	return fmt.Sprintf("Messages (scroll %d)", m.messageScroll)
}

func (m *Model) activityHeader(label string, selected activityView, width int) []string {
	if m.focus == focusTools {
		label += " (focused)"
	}
	views := activityViews()
	tabs := make([]string, 0, len(views))
	selectedIndex := 0
	for index, view := range views {
		name := activityLabel(view)
		if view == selected {
			name = "[" + name + "]"
			selectedIndex = index
		}
		tabs = append(tabs, name)
	}
	tabLine := strings.Join(tabs, " ")
	if ansi.StringWidth(tabLine) > width {
		previous := views[(selectedIndex-1+len(views))%len(views)]
		next := views[(selectedIndex+1)%len(views)]
		tabLine = fmt.Sprintf("[%s]  h:%s  l:%s", activityLabel(selected),
			activityLabel(previous), activityLabel(next))
	}
	return []string{truncate(label, width), truncate(tabLine, width)}
}

func activityLabel(view activityView) string {
	switch view {
	case activityPlan:
		return "Plan"
	case activitySkills:
		return "Skills"
	case activityWork:
		return "Work"
	case activityNotes:
		return "Notes"
	case activityRounds:
		return "Rounds"
	case activityEvents:
		return "Events"
	case activityAgents:
		return "Agents"
	case activityFindings:
		return "Findings"
	case activityEdits:
		return "Edits"
	case activityQueue:
		return "Queue"
	default:
		return "Tools"
	}
}

func activityViews() []activityView {
	return []activityView{activityTools, activityPlan, activitySkills, activityQueue, activityWork, activityNotes,
		activityRounds, activityEvents, activityAgents, activityFindings, activityEdits}
}

func (m *Model) statusLine() string {
	focus := string(m.focus)
	if m.focus == focusTools {
		focus = "activity:" + string(m.activityView)
	}
	parts := []string{m.status, "focus=" + focus}
	if m.busy {
		parts = append(parts, "busy")
	}
	if live := m.live.summary(); live != "" {
		parts = append(parts, live)
	}
	if m.runContext.Found {
		parts = append(parts, fmt.Sprintf("events=#%d", m.runContext.EventSequence))
		parts = append(parts, fmt.Sprintf("steering=%d/%d",
			m.runContext.Steering.Pending, m.runContext.Steering.Prepared))
	}
	if m.eventPollError {
		parts = append(parts, "event-stream=retrying")
	}
	return truncate(strings.Join(parts, " | "), max(20, m.width-2))
}

func footerHelp(width int) string {
	if width < 100 {
		return "Tab | h/l views | j/k | tools:a/g/d | Ctrl+X | Esc"
	}
	if width < 145 {
		return "Tab focus | h/l views | j/k select | a once | g session | d deny | Ctrl+X cancel | Ctrl+R | Esc"
	}
	return "Tab focus | h/l activity views | j/k select | tools: a approve once, g grant session, d deny | Ctrl+X cancel | Ctrl+R | Esc"
}

func (m *Model) selectedToolRun() (toolrun.ToolRun, bool) {
	if len(m.toolRuns) == 0 {
		return toolrun.ToolRun{}, false
	}
	m.normalizeSelection()
	return m.toolRuns[m.selectedTool], true
}

func loadRunContext(ctx context.Context, runStateStore RunStateStore,
	sess session.Session,
) (runContext, error) {
	if runStateStore == nil {
		return runContext{}, nil
	}
	run, found, err := runStateStore.GetRunBySession(ctx, sess.ID)
	if err != nil || !found {
		return runContext{}, err
	}
	mode, err := runStateStore.GetRunMode(ctx, run.ID)
	if err != nil {
		return runContext{}, err
	}
	if mode.RunID != run.ID || mode.MissionID != run.MissionID {
		return runContext{}, errors.New("TUI Run mode snapshot is cross-scope")
	}
	workItems, err := runStateStore.ListWorkItems(ctx, domain.WorkItemFilter{
		RunID: run.ID, Limit: maxTUIRunItems,
	})
	if err != nil {
		return runContext{}, err
	}
	notes, err := runStateStore.ListNotes(ctx, domain.NoteFilter{
		RunID: run.ID, Limit: maxTUIRunItems,
	})
	if err != nil {
		return runContext{}, err
	}
	rounds, err := runStateStore.ListRunSupervisorToolRoundsPage(ctx, run.ID, 0, maxTUIToolRounds)
	if err != nil {
		return runContext{}, err
	}
	grants, err := runStateStore.ListSessionGrants(ctx, approval.GrantListFilter{
		RunID: run.ID, SessionID: sess.ID, Status: approval.GrantActive, Limit: maxTUIRunItems,
	})
	if err != nil {
		return runContext{}, err
	}
	eventTail, err := runStateStore.LatestRunEventSequence(ctx, run.ID)
	if err != nil {
		return runContext{}, err
	}
	afterSequence := maxInt64(0, eventTail-maxTUIRunItems)
	eventList, err := runStateStore.ListRunEventsAfterSequence(ctx, run.ID,
		afterSequence, maxTUIRunItems)
	if err != nil {
		return runContext{}, err
	}
	if err := validateTUIEventBatch(run, afterSequence, eventList); err != nil {
		return runContext{}, err
	}
	eventSequence := afterSequence
	if len(eventList) > 0 {
		eventSequence = eventList[len(eventList)-1].Sequence
	}
	if eventSequence < eventTail {
		return runContext{}, errors.New("TUI event snapshot did not reach the durable Run tail")
	}
	agents, err := loadAgentContext(ctx, runStateStore, run)
	if err != nil {
		return runContext{}, err
	}
	reports, err := loadFindingContext(ctx, runStateStore, run)
	if err != nil {
		return runContext{}, err
	}
	edits, editsTruncated, err := loadFileEditContext(ctx, runStateStore, run, sess)
	if err != nil {
		return runContext{}, err
	}
	planProposal, planSelection, err := loadPlanDeliveryContext(ctx, runStateStore, run)
	if err != nil {
		return runContext{}, err
	}
	deliveryCheckpoints, err := runStateStore.ListDeliveryCheckpoints(ctx, run.ID, 500)
	if err != nil {
		return runContext{}, err
	}
	deliveryGateEnforced, err := runStateStore.DeliveryGateEnforced(ctx, run.ID)
	if err != nil {
		return runContext{}, err
	}
	steering, err := loadOperatorSteeringContext(ctx, runStateStore, run)
	if err != nil {
		return runContext{}, err
	}
	externalSkills, err := loadExternalSkillProjectionContext(ctx, runStateStore, run, mode)
	if err != nil {
		return runContext{}, err
	}
	return runContext{Found: true, Run: run, Mode: mode, WorkItems: workItems, Notes: notes,
		ToolRounds: rounds, Grants: grants, Events: eventList, EventSequence: eventSequence,
		Agents: agents, FindingReports: reports, FileEdits: edits,
		FileEditsTruncated: editsTruncated, PlanProposal: planProposal,
		PlanSelection: planSelection, DeliveryGateEnforced: deliveryGateEnforced,
		DeliveryCheckpoints: deliveryCheckpoints, Steering: steering,
		ExternalSkills: externalSkills}, nil
}

func loadOperatorSteeringContext(ctx context.Context, stateStore RunStateStore,
	run domain.Run,
) (operatorSteeringContext, error) {
	values, err := stateStore.ListOperatorSteering(ctx, run.ID, 20)
	if err != nil {
		return operatorSteeringContext{}, err
	}
	summary, err := stateStore.GetOperatorSteeringQueueSummary(ctx, run.ID)
	if err != nil {
		return operatorSteeringContext{}, err
	}
	if summary.RunID != run.ID {
		return operatorSteeringContext{}, errors.New("TUI operator steering summary is cross-Run")
	}
	result := operatorSteeringContext{
		Pending: summary.Pending, Prepared: summary.Prepared,
		Committed: summary.Committed, Cancelled: summary.Cancelled,
		Messages: make([]operatorSteeringMetadata, len(values)),
	}
	for index, value := range values {
		if err := value.Validate(); err != nil || value.RunID != run.ID ||
			value.SessionID != run.SessionID {
			return operatorSteeringContext{},
				errors.New("TUI operator steering metadata is invalid or cross-Run")
		}
		result.Messages[index] = operatorSteeringMetadata{
			ID: value.ID, Sequence: value.Sequence, Status: value.Status,
			CreatedAt: value.CreatedAt,
		}
	}
	return result, nil
}

func readyDeliveryCheckpointCount(checkpoints []domain.DeliveryCheckpoint,
	items []domain.WorkItem, mode domain.RunModeSnapshot,
) int {
	byID := make(map[string]domain.WorkItem, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	ready := make(map[string]struct{}, len(items))
	for _, checkpoint := range checkpoints {
		if item, found := byID[checkpoint.WorkItemID]; found &&
			domain.DeliveryCheckpointReady(checkpoint, item, mode) {
			ready[item.ID] = struct{}{}
		}
	}
	return len(ready)
}

func loadWorkspaceContext(ctx context.Context, workspaceStore WorkspaceStore,
	sess session.Session,
) workspaceContext {
	id := strings.TrimSpace(sess.WorkspaceID)
	if id == "" {
		return workspaceContext{}
	}
	ctxView := workspaceContext{ID: id}
	if workspaceStore == nil {
		ctxView.Error = "workspace lookup unavailable"
		return ctxView
	}
	rec, err := workspaceStore.GetWorkspaceByID(ctx, id)
	if err != nil {
		ctxView.Error = err.Error()
		return ctxView
	}
	ctxView.Name = rec.Name
	ctxView.RootPath = rec.RootPath
	for _, name := range []string{"attachments", "scripts", "outputs", "logs", "writeups"} {
		count, exists, itemErr := countDirEntries(filepath.Join(rec.RootPath, name))
		item := workspaceItem{Name: name, Count: count, Exists: exists}
		if itemErr != nil {
			item.Error = itemErr.Error()
		}
		ctxView.Items = append(ctxView.Items, item)
	}
	return ctxView
}

func (m *Model) normalizeSelection() {
	if len(m.toolRuns) == 0 {
		m.selectedTool = 0
		m.toolScroll = 0
		return
	}
	if m.selectedTool < 0 {
		m.selectedTool = 0
	}
	if m.selectedTool >= len(m.toolRuns) {
		m.selectedTool = len(m.toolRuns) - 1
	}
	if m.toolScroll < 0 {
		m.toolScroll = 0
	}
	if m.toolScroll >= len(m.toolRuns) {
		m.toolScroll = len(m.toolRuns) - 1
	}
}

func (m *Model) normalizeActivitySelection() {
	m.normalizeSelection()
	normalizeListSelection(len(m.runContext.WorkItems), &m.selectedWorkItem, &m.workItemScroll)
	normalizeListSelection(len(m.runContext.Notes), &m.selectedNote, &m.noteScroll)
	normalizeListSelection(len(m.runContext.ToolRounds), &m.selectedRound, &m.roundScroll)
	normalizeListSelection(len(m.runContext.Events), &m.selectedEvent, &m.eventScroll)
	normalizeListSelection(len(m.runContext.Agents), &m.selectedAgent, &m.agentScroll)
	normalizeListSelection(len(m.runContext.FindingReports), &m.selectedFinding, &m.findingScroll)
	normalizeListSelection(len(m.runContext.FileEdits), &m.selectedEdit, &m.editScroll)
}

func normalizeListSelection(length int, selected *int, scroll *int) {
	if length == 0 {
		*selected = 0
		*scroll = 0
		return
	}
	if *selected < 0 {
		*selected = 0
	}
	if *selected >= length {
		*selected = length - 1
	}
	if *scroll < 0 {
		*scroll = 0
	}
	if *scroll >= length {
		*scroll = length - 1
	}
}

func (m *Model) selectToolByID(id string) {
	if id == "" {
		return
	}
	for i, run := range m.toolRuns {
		if run.ID == id {
			m.selectedTool = i
			return
		}
	}
}

func (m *Model) selectEvent(sequence int64, followTail bool) {
	if len(m.runContext.Events) == 0 {
		m.selectedEvent = 0
		return
	}
	if followTail || sequence <= 0 {
		m.selectedEvent = len(m.runContext.Events) - 1
		return
	}
	for index, event := range m.runContext.Events {
		if event.Sequence == sequence {
			m.selectedEvent = index
			return
		}
	}
	if sequence < m.runContext.Events[0].Sequence {
		m.selectedEvent = 0
		return
	}
	m.selectedEvent = len(m.runContext.Events) - 1
}

func (m *Model) selectAgentByID(id string) {
	if id == "" {
		return
	}
	for index, agent := range m.runContext.Agents {
		if agent.Node.ID == id {
			m.selectedAgent = index
			return
		}
	}
}

func (m *Model) selectFindingByID(id string) {
	if id == "" {
		return
	}
	for index, finding := range m.runContext.FindingReports {
		if finding.ID == id {
			m.selectedFinding = index
			return
		}
	}
}

func (m *Model) selectEditByID(id string) {
	if id == "" {
		return
	}
	for index, edit := range m.runContext.FileEdits {
		if edit.Preview.ID == id {
			m.selectedEdit = index
			return
		}
	}
}

func (m *Model) ensureSelectedVisible(visible int) {
	m.normalizeSelection()
	if visible <= 0 || len(m.toolRuns) == 0 {
		return
	}
	if m.selectedTool < m.toolScroll {
		m.toolScroll = m.selectedTool
	}
	if m.selectedTool >= m.toolScroll+visible {
		m.toolScroll = m.selectedTool - visible + 1
	}
	m.normalizeSelection()
}

func (m *Model) ensureWorkItemVisible(visible int) {
	m.normalizeActivitySelection()
	ensureListSelectionVisible(len(m.runContext.WorkItems), m.selectedWorkItem, visible, &m.workItemScroll)
}

func (m *Model) ensureNoteVisible(visible int) {
	m.normalizeActivitySelection()
	ensureListSelectionVisible(len(m.runContext.Notes), m.selectedNote, visible, &m.noteScroll)
}

func (m *Model) ensureRoundVisible(visible int) {
	m.normalizeActivitySelection()
	ensureListSelectionVisible(len(m.runContext.ToolRounds), m.selectedRound, visible, &m.roundScroll)
}

func (m *Model) ensureEventVisible(visible int) {
	m.normalizeActivitySelection()
	ensureListSelectionVisible(len(m.runContext.Events), m.selectedEvent, visible, &m.eventScroll)
}

func (m *Model) ensureAgentVisible(visible int) {
	m.normalizeActivitySelection()
	ensureListSelectionVisible(len(m.runContext.Agents), m.selectedAgent, visible, &m.agentScroll)
}

func (m *Model) ensureFindingVisible(visible int) {
	m.normalizeActivitySelection()
	ensureListSelectionVisible(len(m.runContext.FindingReports), m.selectedFinding, visible, &m.findingScroll)
}

func (m *Model) ensureEditVisible(visible int) {
	m.normalizeActivitySelection()
	ensureListSelectionVisible(len(m.runContext.FileEdits), m.selectedEdit, visible, &m.editScroll)
}

func ensureListSelectionVisible(length int, selected int, visible int, scroll *int) {
	if length == 0 || visible <= 0 {
		return
	}
	if selected < *scroll {
		*scroll = selected
	}
	if selected >= *scroll+visible {
		*scroll = selected - visible + 1
	}
	if *scroll < 0 {
		*scroll = 0
	}
	if *scroll >= length {
		*scroll = length - 1
	}
}

func (m *Model) activeGrant(toolName string) (approval.SessionGrant, bool) {
	for _, grant := range m.runContext.Grants {
		if grant.Status == approval.GrantActive && grant.ToolName == toolName {
			return grant, true
		}
	}
	return approval.SessionGrant{}, false
}

func (m *Model) selectedWorkItemStatus() string {
	if len(m.runContext.WorkItems) == 0 {
		return "no work items"
	}
	return "selected " + m.runContext.WorkItems[m.selectedWorkItem].ID
}

func (m *Model) selectedNoteStatus() string {
	if len(m.runContext.Notes) == 0 {
		return "no notes"
	}
	return "selected " + m.runContext.Notes[m.selectedNote].ID
}

func (m *Model) selectedRoundStatus() string {
	if len(m.runContext.ToolRounds) == 0 {
		return "no tool rounds"
	}
	round := m.runContext.ToolRounds[m.selectedRound]
	return fmt.Sprintf("selected turn %d round %d", round.Turn, round.Round)
}

func (m *Model) selectedEventStatus() string {
	if len(m.runContext.Events) == 0 {
		return "no Run events"
	}
	event := m.runContext.Events[m.selectedEvent]
	return fmt.Sprintf("selected event #%d %s", event.Sequence, event.Type)
}

func (m *Model) selectedAgentStatus() string {
	if len(m.runContext.Agents) == 0 {
		return "no Agents"
	}
	return "selected " + m.runContext.Agents[m.selectedAgent].Node.ID
}

func (m *Model) selectedFindingStatus() string {
	if len(m.runContext.FindingReports) == 0 {
		return "no Findings"
	}
	return "selected " + m.runContext.FindingReports[m.selectedFinding].ID
}

func (m *Model) selectedEditStatus() string {
	if len(m.runContext.FileEdits) == 0 {
		return "no file edits"
	}
	return "selected " + m.runContext.FileEdits[m.selectedEdit].Preview.ID + "; Enter opens read-only diff"
}

func wrap(value string, width int) []string {
	value = strings.TrimSpace(value)
	if width <= 0 || ansi.StringWidth(value) <= width {
		return []string{value}
	}
	return strings.Split(ansi.Wrap(value, width, ""), "\n")
}

func windowFromBottom(lines []string, maxLines int, scroll int) []string {
	if maxLines <= 0 {
		return nil
	}
	if len(lines) <= maxLines {
		return lines
	}
	if scroll < 0 {
		scroll = 0
	}
	end := len(lines) - scroll
	if end > len(lines) {
		end = len(lines)
	}
	if end < maxLines {
		end = maxLines
	}
	start := end - maxLines
	return lines[start:end]
}

func windowTop(lines []string, maxLines int) []string {
	if maxLines <= 0 {
		return nil
	}
	if len(lines) <= maxLines {
		return lines
	}
	return lines[:maxLines]
}

func truncate(value string, width int) string {
	value = terminalSafeText(value)
	if width <= 0 || ansi.StringWidth(value) <= width {
		return value
	}
	if width <= 3 {
		return ansi.Truncate(value, width, "")
	}
	return ansi.Truncate(value, width, "...")
}

func singleLine(value string) string {
	return strings.Join(strings.Fields(terminalSafeText(value)), " ")
}

func terminalSafeText(value string) string {
	var out strings.Builder
	for _, char := range value {
		switch {
		case char == '\n' || char == '\r' || char == '\t':
			out.WriteByte(' ')
		case char < 0x20 || char == 0x7f:
			fmt.Fprintf(&out, "\\u%04X", char)
		default:
			out.WriteRune(char)
		}
	}
	return out.String()
}

func defaultText(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func countDirEntries(path string) (int, bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, true, err
	}
	return len(entries), true, nil
}

func statusForInput(input string) string {
	switch {
	case input == "/approve-session" || strings.HasPrefix(input, "/approve-session "):
		return "authorizing session..."
	case input == "/approve" || strings.HasPrefix(input, "/approve "):
		return "approving..."
	case input == "/deny" || strings.HasPrefix(input, "/deny "):
		return "denying..."
	case input == "/tools":
		return "refreshing tools..."
	case strings.HasPrefix(input, "/run "):
		return "proposing tool..."
	case strings.HasPrefix(input, "/"):
		return "working..."
	default:
		return "thinking..."
	}
}

func max(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a int, b int) int {
	if a < b {
		return a
	}
	return b
}
