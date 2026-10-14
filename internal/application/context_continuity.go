package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/session"
)

type ContextContinuityStore interface {
	GetRun(context.Context, string) (domain.Run, error)
	GetMission(context.Context, string) (domain.Mission, error)
	GetRunMode(context.Context, string) (domain.RunModeSnapshot, error)
	GetSession(context.Context, string) (session.Session, error)
	GetWorkspaceInfo(context.Context, string) (session.WorkspaceInfo, error)
	LatestContextSummary(context.Context, string) (contextmgr.Summary, bool, error)
	ListRecentSessionMessages(context.Context, string, bool, int) ([]session.Message, error)
	ListContextMemories(context.Context, contextmgr.MemoryFilter, time.Time) ([]contextmgr.Memory, error)
	GetSessionContinuityNode(context.Context, string) (contextmgr.ContinuityNode, error)
	ListSessionContinuityNodes(context.Context, string, int) ([]contextmgr.ContinuityNode, error)
	ListWorkspaceContinuityNodes(context.Context, string, int) ([]contextmgr.ContinuityNode, error)
	CreateSessionContinuityNode(context.Context, contextmgr.ContinuityNode) error
	CreateMissionRunWithContinuity(context.Context, domain.Mission, domain.Run,
		domain.RunModeSnapshot, session.Session, bool, []events.Event,
		contextmgr.ContinuityNode) error
	ListNotes(context.Context, domain.NoteFilter) ([]domain.Note, error)
	ListRunArtifacts(context.Context, artifact.ListFilter) ([]artifact.Descriptor, error)
	ListDeliveryCheckpoints(context.Context, string, int) ([]domain.DeliveryCheckpoint, error)
}

type ContextContinuityService struct {
	store          ContextContinuityStore
	lifecycleHooks *hooks.Engine
}

type CreateContinuityCheckpointRequest struct {
	RunID       string
	Title       string
	Summary     string
	RequestedBy string
}

type BranchContinuityRequest struct {
	SourceNodeID string
	Kind         contextmgr.ContinuityNodeKind
	Goal         string
	RequestedBy  string
	WorkspaceID  string
	GitBranch    string
	GitHead      string
}

type ContinuityBranchResult struct {
	Mission         domain.Mission            `json:"mission"`
	Run             domain.Run                `json:"run"`
	Node            contextmgr.ContinuityNode `json:"node"`
	Inherited       []string                  `json:"inherited"`
	NotInherited    []string                  `json:"not_inherited"`
	CapabilityGrant bool                      `json:"capability_grant"`
}

func NewContextContinuityService(store ContextContinuityStore) *ContextContinuityService {
	return &ContextContinuityService{store: store}
}

func (s *ContextContinuityService) WithLifecycleHooks(
	engine *hooks.Engine,
) *ContextContinuityService {
	if s != nil {
		s.lifecycleHooks = engine
	}
	return s
}

func (s *ContextContinuityService) Checkpoint(ctx context.Context,
	request CreateContinuityCheckpointRequest,
) (contextmgr.ContinuityNode, error) {
	if s == nil || s.store == nil {
		return contextmgr.ContinuityNode{}, errors.New("context continuity store is required")
	}
	request.RunID = strings.TrimSpace(request.RunID)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	if err := contextmgr.ValidateMemoryActor(request.RequestedBy); err != nil {
		return contextmgr.ContinuityNode{}, err
	}
	run, err := s.store.GetRun(ctx, request.RunID)
	if err != nil {
		return contextmgr.ContinuityNode{}, err
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return contextmgr.ContinuityNode{}, err
	}
	if mission.WorkspaceID == "" {
		return contextmgr.ContinuityNode{}, errors.New("continuity checkpoint requires a Workspace-bound Run")
	}
	snapshot, err := s.captureSnapshot(ctx, run, mission)
	if err != nil {
		return contextmgr.ContinuityNode{}, err
	}
	values, err := s.store.ListSessionContinuityNodes(ctx, run.SessionID, 2000)
	if err != nil {
		return contextmgr.ContinuityNode{}, err
	}
	parentID := ""
	for _, value := range values {
		if value.RunID == run.ID {
			parentID = value.ID
		}
	}
	if parentID == "" {
		return contextmgr.ContinuityNode{}, errors.New("continuity root node is missing")
	}
	title := boundedContinuityText(request.Title, 1024)
	if title == "" {
		title = "Operator checkpoint"
	}
	node, err := contextmgr.NewContinuityNode(idgen.New("continuity"),
		contextmgr.ContinuityNodeCheckpoint, run.SessionID, run.ID, mission.WorkspaceID,
		parentID, "", title, boundedContinuityText(request.Summary, 4096),
		request.RequestedBy, snapshot, time.Now().UTC())
	if err != nil {
		return contextmgr.ContinuityNode{}, err
	}
	if err := executeLifecycleBoundary(ctx, s.lifecycleHooks, hooks.Checkpoint,
		run.ID, mission.WorkspaceID, map[string]any{
			"session_id": run.SessionID, "checkpoint_id": node.ID,
			"kind": node.Kind, "source": "context_continuity",
		}); err != nil {
		return contextmgr.ContinuityNode{}, err
	}
	if err := s.store.CreateSessionContinuityNode(ctx, node); err != nil {
		return contextmgr.ContinuityNode{}, err
	}
	return node, nil
}

func (s *ContextContinuityService) Branch(ctx context.Context,
	request BranchContinuityRequest,
) (ContinuityBranchResult, error) {
	prepared, node, result, err := s.prepareBranch(ctx, request)
	if err != nil {
		return ContinuityBranchResult{}, err
	}
	if prepared.CreateSession {
		if err := executeLifecycleBoundary(ctx, s.lifecycleHooks, hooks.SessionOpened,
			prepared.Run.ID, prepared.Mission.WorkspaceID, map[string]any{
				"session_id": prepared.Session.ID, "source": "context_branch",
			}); err != nil {
			return ContinuityBranchResult{}, err
		}
	}
	if err := s.store.CreateMissionRunWithContinuity(ctx, prepared.Mission, prepared.Run,
		prepared.Mode, prepared.Session, prepared.CreateSession, prepared.InitialEvents, node); err != nil {
		return ContinuityBranchResult{}, err
	}
	return result, nil
}

func (s *ContextContinuityService) prepareBranch(ctx context.Context,
	request BranchContinuityRequest,
) (preparedRun, contextmgr.ContinuityNode, ContinuityBranchResult, error) {
	if s == nil || s.store == nil {
		return preparedRun{}, contextmgr.ContinuityNode{}, ContinuityBranchResult{},
			errors.New("context continuity store is required")
	}
	request.SourceNodeID = strings.TrimSpace(request.SourceNodeID)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	request.GitBranch = strings.TrimSpace(request.GitBranch)
	request.GitHead = strings.TrimSpace(request.GitHead)
	if err := contextmgr.ValidateMemoryActor(request.RequestedBy); err != nil {
		return preparedRun{}, contextmgr.ContinuityNode{}, ContinuityBranchResult{}, err
	}
	if request.Kind != contextmgr.ContinuityNodeFork &&
		request.Kind != contextmgr.ContinuityNodeResume {
		return preparedRun{}, contextmgr.ContinuityNode{}, ContinuityBranchResult{},
			errors.New("continuity branch kind must be fork or resume")
	}
	source, err := s.store.GetSessionContinuityNode(ctx, request.SourceNodeID)
	if err != nil {
		return preparedRun{}, contextmgr.ContinuityNode{}, ContinuityBranchResult{}, err
	}
	sourceRun, err := s.store.GetRun(ctx, source.RunID)
	if err != nil {
		return preparedRun{}, contextmgr.ContinuityNode{}, ContinuityBranchResult{}, err
	}
	sourceMission, err := s.store.GetMission(ctx, sourceRun.MissionID)
	if err != nil {
		return preparedRun{}, contextmgr.ContinuityNode{}, ContinuityBranchResult{}, err
	}
	mode, err := s.store.GetRunMode(ctx, sourceRun.ID)
	if err != nil {
		return preparedRun{}, contextmgr.ContinuityNode{}, ContinuityBranchResult{}, err
	}
	goal := boundedContinuityText(request.Goal, 16*1024)
	if goal == "" {
		goal = sourceMission.Goal
	}
	var project *projectconfig.Effective
	if len(sourceRun.Config.ProjectConfig) > 0 {
		var value projectconfig.Effective
		if err := json.Unmarshal(sourceRun.Config.ProjectConfig, &value); err != nil {
			return preparedRun{}, contextmgr.ContinuityNode{}, ContinuityBranchResult{},
				fmt.Errorf("decode pinned project config: %w", err)
		}
		if value.Fingerprint() != sourceRun.Config.ProjectConfigFingerprint {
			return preparedRun{}, contextmgr.ContinuityNode{}, ContinuityBranchResult{},
				errors.New("pinned project config fingerprint mismatch")
		}
		project = &value
	}
	var instructions *projectconfig.InstructionSnapshot
	if len(sourceRun.Config.ProjectInstructions) > 0 {
		var value projectconfig.InstructionSnapshot
		if err := json.Unmarshal(sourceRun.Config.ProjectInstructions, &value); err != nil {
			return preparedRun{}, contextmgr.ContinuityNode{}, ContinuityBranchResult{},
				fmt.Errorf("decode pinned project instructions: %w", err)
		}
		if err := value.Validate(); err != nil {
			return preparedRun{}, contextmgr.ContinuityNode{}, ContinuityBranchResult{},
				fmt.Errorf("pinned project instructions: %w", err)
		}
		instructions = &value
	}
	continuity := source.Snapshot
	workspaceID := sourceMission.WorkspaceID
	if request.WorkspaceID != "" {
		workspaceID = request.WorkspaceID
		continuity.WorkspaceID = workspaceID
		continuity.GitBranch, continuity.GitHead = request.GitBranch, request.GitHead
		inherited := make([]string, 0, len(continuity.InheritedContext)+1)
		for _, value := range continuity.InheritedContext {
			if !strings.HasPrefix(value, "git:") {
				inherited = append(inherited, value)
			}
		}
		if continuity.GitHead != "" {
			inherited = append(inherited, "git:"+continuity.GitBranch+":"+continuity.GitHead)
		}
		continuity.InheritedContext = inherited
		continuity.Fingerprint = ""
		continuity, err = contextmgr.SealContinuitySnapshot(continuity)
		if err != nil {
			return preparedRun{}, contextmgr.ContinuityNode{}, ContinuityBranchResult{}, err
		}
	}
	prepared, err := prepareRun(ctx, CreateRunRequest{
		Goal: goal, Profile: string(sourceMission.Profile), Surface: string(mode.Surface),
		Phase: string(mode.Phase), WorkspaceID: workspaceID,
		ModelRoute: sourceRun.Config.ModelRoute, Interactive: sourceRun.Config.Interactive,
		Budget: sourceRun.Budget, RequestedBudget: sourceRun.Config.RequestedBudget, RequestedBy: request.RequestedBy,
		ProjectConfig: project, ProjectInstructions: instructions,
		ContinuityContext: &continuity,
		WebAccessMode:     string(mode.Scope.WebAccessMode), WebAllowedTargets: append([]string(nil), mode.Scope.WebAllowedTargets...),
	}, s.store.GetSession)
	if err != nil {
		return preparedRun{}, contextmgr.ContinuityNode{}, ContinuityBranchResult{}, err
	}
	title := "Fork from " + source.ID
	if request.Kind == contextmgr.ContinuityNodeResume {
		title = "Resume from " + source.ID
	}
	node, err := contextmgr.NewContinuityNode(idgen.New("continuity"), request.Kind,
		prepared.Run.SessionID, prepared.Run.ID, prepared.Mission.WorkspaceID, "",
		source.ID, title, "Explicit context snapshot inherited; all authority reset",
		request.RequestedBy, continuity, time.Now().UTC())
	if err != nil {
		return preparedRun{}, contextmgr.ContinuityNode{}, ContinuityBranchResult{}, err
	}
	result := ContinuityBranchResult{Mission: prepared.Mission, Run: prepared.Run, Node: node,
		Inherited: append([]string{}, source.Snapshot.InheritedContext...),
		NotInherited: []string{"approvals", "capability grants", "credentials", "debug sessions",
			"execution leases", "network authorization", "processes", "terminal leases",
			"execution profiles"},
		CapabilityGrant: false}
	if request.WorkspaceID != "" {
		result.Inherited = append([]string{}, continuity.InheritedContext...)
	}
	return prepared, node, result, nil
}

func (s *ContextContinuityService) Tree(ctx context.Context,
	sessionID string,
) (contextmgr.SessionTree, error) {
	if s == nil || s.store == nil {
		return contextmgr.SessionTree{}, errors.New("context continuity store is required")
	}
	sess, err := s.store.GetSession(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return contextmgr.SessionTree{}, err
	}
	if sess.WorkspaceID == "" {
		return contextmgr.SessionTree{}, errors.New("session tree requires a Workspace-bound Session")
	}
	all, err := s.store.ListWorkspaceContinuityNodes(ctx, sess.WorkspaceID, 2000)
	if err != nil {
		return contextmgr.SessionTree{}, err
	}
	selected := continuityComponent(all, sess.ID)
	memories, err := s.currentMemoryMap(ctx, sess.WorkspaceID)
	if err != nil {
		return contextmgr.SessionTree{}, err
	}
	workspace, err := s.store.GetWorkspaceInfo(ctx, sess.WorkspaceID)
	if err != nil {
		return contextmgr.SessionTree{}, err
	}
	repo, repoErr := repository.Inspect(ctx, workspace.RootPath, workspace.ID)
	if repoErr != nil {
		repo = repository.State{}
	}
	nodes := make([]contextmgr.SessionTreeNode, 0, len(selected)+64)
	latestByRun := make(map[string]string)
	latestBySession := make(map[string]string)
	runBySession := make(map[string]string)
	sessionByRun := make(map[string]string)
	runs := make(map[string]struct{})
	sessions := make(map[string]struct{})
	for _, node := range selected {
		status, warnings := continuityNodeStatus(node, memories, repo, repoErr)
		nodes = append(nodes, contextmgr.SessionTreeNode{ID: node.ID, ParentID: node.ParentID,
			SourceNodeID: node.SourceNodeID, Kind: string(node.Kind), RunID: node.RunID,
			SessionID: node.SessionID, Title: node.Title, Summary: node.Summary,
			Fingerprint:                    node.ContextSHA256,
			ProjectConfigFingerprint:       node.ProjectConfigFingerprint,
			ProjectInstructionsFingerprint: node.ProjectInstructionsFingerprint,
			GitBranch:                      node.GitBranch, GitHead: node.GitHead,
			Status: status, Warnings: warnings,
			Derived: false, CreatedAt: node.CreatedAt})
		latestByRun[node.RunID] = node.ID
		latestBySession[node.SessionID] = node.ID
		runBySession[node.SessionID] = node.RunID
		sessionByRun[node.RunID] = node.SessionID
		runs[node.RunID] = struct{}{}
		sessions[node.SessionID] = struct{}{}
	}
	for value := range sessions {
		summary, found, summaryErr := s.store.LatestContextSummary(ctx, value)
		if summaryErr != nil || !found {
			continue
		}
		nodes = append(nodes, contextmgr.SessionTreeNode{
			ID: fmt.Sprintf("summary:%s:%d", value, summary.ID), Kind: "compaction",
			ParentID: latestBySession[value], RunID: runBySession[value], SessionID: value,
			Title: "Compaction summary", Summary: boundedContinuityText(summary.Content, 4096),
			Fingerprint: summary.ContentSHA256, Status: "historical", Warnings: []string{},
			Derived: true, CreatedAt: summary.CreatedAt,
		})
	}
	for runID := range runs {
		parent := latestByRun[runID]
		decisions, listErr := s.store.ListNotes(ctx, domain.NoteFilter{RunID: runID,
			Statuses:   []domain.NoteStatus{domain.NoteActive},
			Categories: []domain.NoteCategory{domain.NoteDecision}, Limit: 100})
		if listErr == nil {
			for _, note := range decisions {
				nodes = append(nodes, contextmgr.SessionTreeNode{ID: "decision:" + note.ID,
					ParentID: parent, Kind: "decision", RunID: runID,
					SessionID: sessionByRun[runID], Title: note.Title,
					Summary: boundedContinuityText(note.Content, 4096), Status: string(note.Status),
					Warnings: []string{}, Derived: true, CreatedAt: note.CreatedAt})
			}
		}
		artifacts, listErr := s.store.ListRunArtifacts(ctx, artifact.ListFilter{RunID: runID, Limit: 100})
		if listErr == nil {
			for _, value := range artifacts {
				nodes = append(nodes, contextmgr.SessionTreeNode{ID: "artifact:" + value.ID,
					ParentID: parent, Kind: "artifact", RunID: runID, SessionID: value.SessionID,
					Title:       value.ToolName + " " + string(value.Stream),
					Summary:     fmt.Sprintf("%s · %d bytes", value.MIME, value.SizeBytes),
					Fingerprint: value.SHA256, Status: "available", Warnings: []string{},
					Derived: true, CreatedAt: value.CreatedAt})
			}
		}
		checkpoints, listErr := s.store.ListDeliveryCheckpoints(ctx, runID, 100)
		if listErr == nil {
			for _, value := range checkpoints {
				nodes = append(nodes, contextmgr.SessionTreeNode{ID: "delivery:" + value.ID,
					ParentID: parent, Kind: "delivery_checkpoint", RunID: runID,
					SessionID:   sessionByRun[runID],
					Title:       fmt.Sprintf("Delivery checkpoint %d/%d", value.ModuleOrdinal, value.ModuleCount),
					Summary:     boundedContinuityText(value.FocusedVerification, 4096),
					Fingerprint: value.SourceFingerprint, Status: "historical", Warnings: []string{},
					Derived: true, CreatedAt: value.CreatedAt})
			}
		}
	}
	sort.Slice(nodes, func(left, right int) bool {
		if nodes[left].CreatedAt.Equal(nodes[right].CreatedAt) {
			return nodes[left].ID < nodes[right].ID
		}
		return nodes[left].CreatedAt.Before(nodes[right].CreatedAt)
	})
	return contextmgr.SessionTree{ProtocolVersion: contextmgr.SessionTreeProtocolVersion,
		SessionID: sess.ID, WorkspaceID: sess.WorkspaceID, Nodes: nodes,
		CapabilityGrant: false, GeneratedAt: time.Now().UTC()}, nil
}

func (s *ContextContinuityService) captureSnapshot(ctx context.Context, run domain.Run,
	mission domain.Mission,
) (contextmgr.ContinuitySnapshot, error) {
	workspace, err := s.store.GetWorkspaceInfo(ctx, mission.WorkspaceID)
	if err != nil {
		return contextmgr.ContinuitySnapshot{}, err
	}
	snapshot := contextmgr.ContinuitySnapshot{SourceRunID: run.ID,
		SourceSessionID: run.SessionID, WorkspaceID: mission.WorkspaceID,
		RecentMessages:                 []contextmgr.ContinuityMessage{},
		Memories:                       []contextmgr.ContinuityMemoryReference{},
		ProjectConfigFingerprint:       run.Config.ProjectConfigFingerprint,
		ProjectInstructionsFingerprint: run.Config.ProjectInstructionsFingerprint,
		InheritedContext:               []string{}, CreatedAt: time.Now().UTC()}
	inherited := make(map[string]struct{})
	if summary, found, summaryErr := s.store.LatestContextSummary(ctx, run.SessionID); summaryErr != nil {
		return contextmgr.ContinuitySnapshot{}, summaryErr
	} else if found {
		snapshot.SummaryID = summary.ID
		snapshot.SummaryContent = boundedContinuityText(summary.Content, contextmgr.MaxContinuitySummaryBytes)
		snapshot.SummaryContentSHA256 = session.ContentSHA256(snapshot.SummaryContent)
		inherited[fmt.Sprintf("compaction:%d:%s", summary.ID, snapshot.SummaryContentSHA256)] = struct{}{}
	}
	messages, err := s.store.ListRecentSessionMessages(ctx, run.SessionID, true,
		contextmgr.MaxContinuityRecentMessages)
	if err != nil {
		return contextmgr.ContinuitySnapshot{}, err
	}
	for _, message := range messages {
		content := boundedContinuityText(message.Content, contextmgr.MaxContinuityMessageBytes)
		item := contextmgr.ContinuityMessage{ID: message.ID, Role: message.Role,
			SourceKind:    boundedContinuityText(message.Provenance.SourceKind, contextmgr.MaxMemoryReferenceBytes),
			SourceRef:     boundedContinuityText(message.Provenance.SourceRef, contextmgr.MaxMemoryReferenceBytes),
			ContentSHA256: session.ContentSHA256(content), Content: content,
			InstructionAuthorized: message.Role == "user" &&
				message.Provenance.SourceKind == session.SourceOperatorMessage &&
				message.Provenance.InstructionAuthorized}
		snapshot.RecentMessages = append(snapshot.RecentMessages, item)
		if message.ID > snapshot.ThroughMessageID {
			snapshot.ThroughMessageID = message.ID
		}
		inherited[fmt.Sprintf("message:%d:%s:%s:instruction_authorized=%t", item.ID,
			item.ContentSHA256, item.SourceKind, item.InstructionAuthorized)] = struct{}{}
	}
	for _, filter := range []contextmgr.MemoryFilter{
		{Scope: contextmgr.MemoryScopeUser, ScopeID: contextmgr.LocalUserMemoryScope,
			Limit: contextmgr.MaxMemoryListItems},
		{Scope: contextmgr.MemoryScopeProject, ScopeID: mission.WorkspaceID,
			Limit: contextmgr.MaxMemoryListItems},
	} {
		values, listErr := s.store.ListContextMemories(ctx, filter, time.Now().UTC())
		if listErr != nil {
			return contextmgr.ContinuitySnapshot{}, listErr
		}
		for _, memory := range values {
			if len(snapshot.Memories) >= contextmgr.MaxContinuityMemoryReferences {
				break
			}
			ref := contextmgr.ContinuityMemoryReferenceOf(memory)
			snapshot.Memories = append(snapshot.Memories, ref)
			inherited[fmt.Sprintf("memory:%s:%s:v%d:%s", ref.Scope, ref.ID,
				ref.Version, ref.ContentSHA256)] = struct{}{}
		}
	}
	contextmgr.SortContinuityMemoryReferences(snapshot.Memories)
	state, err := repository.Inspect(ctx, workspace.RootPath, workspace.ID)
	if err != nil {
		return contextmgr.ContinuitySnapshot{}, err
	}
	if state.Available {
		snapshot.GitBranch, snapshot.GitHead = state.Branch, state.FullHead
		inherited["git:"+state.Branch+":"+state.FullHead] = struct{}{}
	}
	if snapshot.ProjectConfigFingerprint != "" {
		inherited["project_config:"+snapshot.ProjectConfigFingerprint] = struct{}{}
	}
	if snapshot.ProjectInstructionsFingerprint != "" {
		inherited["project_instructions:"+snapshot.ProjectInstructionsFingerprint] = struct{}{}
	}
	for value := range inherited {
		snapshot.InheritedContext = append(snapshot.InheritedContext, value)
	}
	return contextmgr.SealContinuitySnapshot(snapshot)
}

func (s *ContextContinuityService) currentMemoryMap(ctx context.Context,
	workspaceID string,
) (map[string]contextmgr.Memory, error) {
	result := make(map[string]contextmgr.Memory)
	for _, filter := range []contextmgr.MemoryFilter{
		{Scope: contextmgr.MemoryScopeUser, ScopeID: contextmgr.LocalUserMemoryScope,
			IncludeDisabled: true, IncludeExpired: true, Limit: contextmgr.MaxMemoryListItems},
		{Scope: contextmgr.MemoryScopeProject, ScopeID: workspaceID,
			IncludeDisabled: true, IncludeExpired: true, Limit: contextmgr.MaxMemoryListItems},
	} {
		values, err := s.store.ListContextMemories(ctx, filter, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			result[value.ID] = value
		}
	}
	return result, nil
}

func continuityComponent(all []contextmgr.ContinuityNode,
	sessionID string,
) []contextmgr.ContinuityNode {
	included := make(map[string]bool)
	for _, node := range all {
		if node.SessionID == sessionID {
			included[node.ID] = true
		}
	}
	changed := true
	for changed {
		changed = false
		for _, node := range all {
			linked := included[node.ParentID] || included[node.SourceNodeID]
			if !linked {
				for _, candidate := range all {
					if included[candidate.ID] &&
						(candidate.ParentID == node.ID || candidate.SourceNodeID == node.ID) {
						linked = true
						break
					}
				}
			}
			if linked && !included[node.ID] {
				included[node.ID], changed = true, true
			}
		}
	}
	result := make([]contextmgr.ContinuityNode, 0, len(included))
	for _, node := range all {
		if included[node.ID] {
			result = append(result, node)
		}
	}
	return result
}

func continuityNodeStatus(node contextmgr.ContinuityNode,
	memories map[string]contextmgr.Memory, repo repository.State, repoErr error,
) (string, []string) {
	warnings := make([]string, 0)
	status := "available"
	now := time.Now().UTC()
	for _, ref := range node.Snapshot.Memories {
		memory, ok := memories[ref.ID]
		switch {
		case !ok:
			status = "memory_deleted"
			warnings = append(warnings, "memory "+ref.ID+" was deleted")
		case memory.Status == contextmgr.MemoryStatusDisabled || memory.Expired(now):
			if status != "memory_deleted" {
				status = "memory_expired"
			}
			warnings = append(warnings, "memory "+ref.ID+" is disabled or expired")
		case memory.Version != ref.Version || memory.ContentSHA256 != ref.ContentSHA256:
			if status == "available" {
				status = "memory_changed"
			}
			warnings = append(warnings, "memory "+ref.ID+" changed after this checkpoint")
		}
	}
	if repoErr != nil {
		warnings = append(warnings, "current repository state could not be inspected")
	} else if node.GitHead != "" && (node.GitHead != repo.FullHead || node.GitBranch != repo.Branch) {
		if status == "available" {
			status = "git_drift"
		}
		warnings = append(warnings, "Git branch or HEAD differs from this checkpoint")
	}
	sort.Strings(warnings)
	return status, warnings
}

func boundedContinuityText(value string, maxBytes int) string {
	value = redact.String(strings.TrimSpace(value))
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
