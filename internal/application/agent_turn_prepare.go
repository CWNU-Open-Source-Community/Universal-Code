package application

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/skills"
)

// agentTurnPreparation groups the context, capability snapshots and request for
// one turn. Compaction and steering rebuild the request from these same inputs.
type agentTurnPreparation struct {
	context    agentTurnContext
	input      agentTurnInput
	tools      agentTurnCapabilities
	request    llm.ChatRequest
	layout     modelContextLayout
	ref        llm.ModelRef
	toolRounds []domain.SupervisorToolRound
}

type agentTurnContext struct {
	threadEndTurn              bool
	inbox                      domain.RootInboxContextBatch
	skillContext               skills.ContextAssembly
	externalSkillContext       skills.ExternalContextAssembly
	memoryBudget               int
	projectInstructionSections []contextmgr.Section
	threadPinSections          []contextmgr.Section
	history                    []session.Message
	summary                    contextmgr.Summary
	hasSummary                 bool
	workItems                  []domain.WorkItem
	notes                      []domain.Note
	longTermMemorySections     []contextmgr.Section
	continuitySections         []contextmgr.Section
	recentOperatorSections     []contextmgr.Section
	memory                     contextmgr.Selection
	contextAudit               *llm.ModelContextAudit
}

type agentTurnInput struct {
	boundaryReceipt     supervisorBoundaryReceipt
	boundaryContext     string
	visibleBaseInput    string
	modelInput          string
	pendingInstructions int
	steeringSequence    int64
	fileEffectCalls     []domain.SupervisorToolCall
	fileEffects         string
	mcpMessages         []llm.Message
	mcpEvidenceIDs      []int64
}

type agentTurnCapabilities struct {
	permission            domain.RunExecutionPermissionSnapshot
	options               supervisorToolOptions
	standardCode          *standardCodeSupervisorTurn
	guidance              string
	skillCandidateEnabled bool
}

// prepareAgentTurn preserves the preparation order: inbox and skills, durable
// context and pinned instructions, current capabilities, then the model request.
func (s *AgentRunner) prepareAgentTurn(ctx context.Context, turn *domain.SupervisorTurn,
	result LifecycleResult, input string,
) (*agentTurnPreparation, LifecycleResult, error) {
	threadEndTurn, err := supervisorThreadEndTurn(ctx, s.store, *turn)
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	historyRecallAvailable, err := s.historyRecallForTurn(ctx, *turn)
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	inbox, err := s.store.PrepareRootInboxContext(ctx, turn.Checkpoint)
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	result.InboxMessages = len(inbox.Messages)
	result.InboxRecovered = inbox.Recovered
	skillContext, skillPreparation, err := s.prepareRootSkillContext(ctx, *turn)
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	result.SkillItems = skillContext.ItemCount
	result.SkillTokens = skillContext.TokenUpperBound
	result.SkillBudget = skillContext.TokenBudget
	result.SkillRedactions = skillContext.RedactionCount
	result.SkillRecovered = skillPreparation.Recovered
	externalSkillContext, externalSkillPreparation, err :=
		s.prepareRootExternalSkillContext(ctx, *turn)
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	result.ExternalSkillItems = externalSkillContext.ItemCount
	result.ExternalSkillTokens = externalSkillContext.TokenUpperBound
	result.ExternalSkillBudget = externalSkillContext.TokenBudget
	result.ExternalSkillRedactions = externalSkillContext.RedactionCount
	result.ExternalSkillRecovered = externalSkillPreparation.Recovered
	ref, err := supervisorModelRef(s.router, turn.Run.Config.ModelRoute)
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	toolRounds, err := s.store.ListSupervisorToolRounds(ctx, turn.Checkpoint)
	if err != nil {
		return nil, result, s.recordFailure(ctx, &result, err, 0)
	}
	// A resumed exhausted recovery must fail before even an auxiliary summary
	// can be generated from late history. The atomic model-start gate remains.
	if reader, ok := s.store.(supervisorContextRecoveryLimitStore); ok {
		repairPhase := 0
		if turn.Checkpoint.RepairPhase != "" {
			repairPhase = 1
		}
		if _, _, err := reader.SupervisorContextRecoveryInputLimit(ctx, turn.Checkpoint, repairPhase, len(toolRounds)); err != nil {
			return nil, result, s.recordFailure(ctx, &result, err, 0)
		}
	}
	memoryBudget := supervisorMemoryBudget(s.router.ContextWindow(ref))
	projectInstructionSections, err := projectInstructionContextSections(turn.Run.Config)
	if err != nil {
		return nil, result, s.recordFailure(ctx, &result, err, 0)
	}
	threadPinSections, err := LoadThreadContextPinSections(ctx, s.store, turn.Run.ID)
	if err != nil {
		return nil, result, s.recordFailure(ctx, &result, err, 0)
	}
	// Refuse an impossible mandatory set before optional generated history
	// compaction can spend another model call.
	requiredSections := append(slices.Clone(projectInstructionSections), threadPinSections...)
	if _, err := contextmgr.SelectSections(requiredSections, memoryBudget); err != nil {
		if errors.Is(err, contextmgr.ErrRequiredContextBudget) {
			err = requiredThreadContextDeliveryFailure(requiredSections, memoryBudget)
		}
		return nil, result, s.recordFailure(ctx, &result, err, 0)
	}
	history, summary, hasSummary, didCompact, err := s.supervisorConversationContext(ctx, turn)
	result.Checkpoint = turn.Checkpoint
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	result.ContextCompacted, result.ContextSummaryID = didCompact, summary.ID
	workItems, err := s.store.ListWorkItems(ctx, domain.WorkItemFilter{
		RunID: turn.Run.ID,
		Statuses: []domain.WorkItemStatus{
			domain.WorkItemInProgress, domain.WorkItemBlocked, domain.WorkItemPending,
		},
		Limit: maxSupervisorWorkItems,
	})
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	notes, err := s.store.ListNotes(ctx, domain.NoteFilter{
		RunID: turn.Run.ID, Statuses: []domain.NoteStatus{domain.NoteActive},
		Viewer: "root", ViewerAgentID: turn.Agent.ID, Limit: maxSupervisorNotes,
	})
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	longTermMemories, err := loadSupervisorLongTermMemories(ctx, s.store,
		turn.Mission.WorkspaceID)
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	longTermMemorySections, err := longTermMemoryContextSections(longTermMemories)
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	result.LongTermMemoryItems = len(longTermMemories)
	continuitySections, err := continuityContextSections(turn.Run.Config)
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	recentOperatorSections, err := loadRecentOperatorContext(ctx, s.store, turn.Run.ID, memoryBudget)
	if err != nil {
		return nil, result, s.recordFailure(ctx, &result, err, 0)
	}
	memory, err := supervisorMemoryContextWithinBudget(memoryBudget, threadEndTurn, summary, hasSummary, workItems, notes, inbox.Messages,
		projectInstructionSections, threadPinSections, longTermMemorySections, continuitySections, supervisorGoalContext(*turn), recentOperatorSections)
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	if err := requireSupervisorContinuityContext(memory, summary, hasSummary, turn.Mission.ID, turn.Run.Config.ContinuityContextFingerprint); err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	if err := requirePinnedProjectInstructionSelection(turn.Run.Config, memory); err != nil {
		return nil, result, s.recordFailure(ctx, &result, err, 0)
	}
	contextAudit := supervisorModelContextAudit(memory)
	executionPermission, err := s.store.GetRunExecutionPermission(ctx, turn.Run.ID)
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	commandRuntime, err := s.supervisorCommandRuntimeTools(ctx, turn.Run.ID,
		executionPermission.Mode)
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	agentCodeCapabilities, agentCodeAuthority, err :=
		s.supervisorAgentCodeCapabilities(ctx, *turn, executionPermission)
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	ownedFileWorkspace, err := runHasOwnedFileWorkspace(ctx, s.store, turn.Run.ID)
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	codeIntelCapabilities, err := s.supervisorCodeIntelCapabilities(ctx, *turn)
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	mcpCapabilities, err := s.supervisorMCPCapabilities(ctx, *turn, executionPermission)
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	mcpMessages, mcpEvidenceIDs, err := s.materializeSupervisorMCPReferences(ctx, *turn, s.router.DescribeVision(ref).State == llm.VisionSupported)
	if err != nil {
		return nil, result, s.recordFailure(ctx, &result, err, 0)
	}
	history = withoutCurrentMCPReferenceEvidence(history, mcpEvidenceIDs)
	webEvidenceCapabilities, webEvidenceAuthority, err :=
		s.supervisorWebEvidenceCapabilities(ctx, *turn, executionPermission)
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	browserActionCapabilities, browserActionAuthority, err :=
		s.supervisorBrowserActionCapabilities(ctx, *turn, executionPermission)
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	standardCode, err := s.prepareStandardCodeSupervisor(ctx, *turn, executionPermission,
		agentCodeCapabilities.Generation, agentCodeAuthority)
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	standardCodeGuidance := ""
	if standardCode != nil {
		standardCodeGuidance = standardCode.Guidance()
	}
	boundaryReceipt, err := s.boundaryReceiptPlan(ctx, turn.Checkpoint, turn.Run.SessionID)
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	visibleBaseInput := supervisorBoundaryInputDelivery(input, boundaryReceipt.Returned)
	modelInput, pendingInstructions, steeringSequence, err := supervisorInputWithPendingInstructions(ctx, s.store, *turn, visibleBaseInput)
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return nil, result, failure
	}
	messages, contextLayout := supervisorMessagesWithLayout(history, modelInput, memory,
		skillContext, externalSkillContext, turn.Mode, threadEndTurn, standardCodeGuidance)
	messages, err = s.supervisorMessagesWithImages(ctx, *turn, history, messages, contextLayout)
	if err != nil {
		return nil, result, s.recordFailure(ctx, &result, err, 0)
	}
	messages, err = s.supervisorMessagesWithOriginalFiles(ctx, *turn, messages, commandRuntime)
	if err != nil {
		return nil, result, s.recordFailure(ctx, &result, err, 0)
	}
	messages, contextLayout, err = s.supervisorMessagesWithPrivateHistory(ctx, *turn, history, messages, contextLayout)
	if err != nil {
		return nil, result, s.recordFailure(ctx, &result, err, 0)
	}
	messages = append(messages, mcpMessages...)
	boundaryContext := boundaryReceipt.Content
	if boundaryContext != "" {
		messages = append(messages, toolBoundaryEvidenceMessage(turn.Run.SessionID, turn.Checkpoint.AttemptID, boundaryContext))
	}
	fileEffectCalls, err := s.fileEffectCalls(ctx, turn.Checkpoint)
	if err != nil {
		return nil, result, s.recordFailure(ctx, &result, err, 0)
	}
	fileEffects := ""
	if len(fileEffectCalls) > 0 {
		fileEffects, err = boundedSupervisorFileEffectContext(turn.Checkpoint, fileEffectCalls, boundaryContext)
		if err != nil {
			return nil, result, s.recordFailure(ctx, &result, err, 0)
		}
	}
	if fileEffects != "" {
		messages = append(messages, toolBoundaryEvidenceMessage(turn.Run.SessionID, turn.Checkpoint.AttemptID, fileEffects))
	}
	builtinSkills, skillCatalogDiagnostic, err := s.builtinSkillCatalog(ctx, *turn)
	if err != nil {
		return nil, result, s.recordFailure(ctx, &result, err, 0)
	}
	if skillCatalogDiagnostic != "" {
		messages = append(messages, llm.Message{Role: "system", Content: skillCatalogDiagnostic})
	}
	skillCandidateEnabled := slices.ContainsFunc(skillContext.Items,
		func(item skills.ContextItem) bool { return item.Name == runSkillGeneratorName })
	toolOptions := supervisorToolOptions{HistoryRecall: historyRecallAvailable, BuiltinSkills: builtinSkills, CommandRuntime: commandRuntime, OwnedFileWorkspace: ownedFileWorkspace,
		AgentCode: supervisorAgentCodeTools{Capabilities: agentCodeCapabilities,
			Authority: agentCodeAuthority},
		CodeIntel: supervisorCodeIntelTools{Capabilities: codeIntelCapabilities,
			Authority: agentCodeAuthority}, MCP: mcpCapabilities,
		WebEvidence: supervisorWebEvidenceTools{Capabilities: webEvidenceCapabilities,
			Authority: webEvidenceAuthority},
		BrowserActions: supervisorBrowserActionTools{
			Capabilities: browserActionCapabilities,
			Authority:    browserActionAuthority}}
	request := llm.ChatRequest{
		Messages: messages,
		Tools: supervisorStructuredToolSpecs(turn.Mode.Surface, turn.Mode.Phase,
			executionPermission.Mode, skillCandidateEnabled, s.debugTerminalEnabled,
			toolOptions),
		JSONMode: true,
		Metadata: map[string]string{
			"run_id": turn.Run.ID, "mission_id": turn.Mission.ID, "session_id": turn.Run.SessionID,
			"agent_id": turn.Agent.ID,
			"turn":     fmt.Sprint(turn.Checkpoint.NextTurn), "attempt_id": turn.Checkpoint.AttemptID,
			"response_schema":           domain.RootLifecycleVersion,
			"active_work_items":         fmt.Sprint(len(workItems)),
			"available_notes":           fmt.Sprint(len(notes)),
			"pending_user_instructions": fmt.Sprint(pendingInstructions),
			"selected_notes":            fmt.Sprint(countContextSources(memory.IncludedSources, "note")),
			"inbox_messages":            fmt.Sprint(len(inbox.Messages)),
			"inbox_recovered":           fmt.Sprint(inbox.Recovered),
			"memory_sections":           fmt.Sprint(len(memory.Sections)),
			"memory_omitted":            fmt.Sprint(len(memory.OmittedSources)),
			"memory_tokens":             fmt.Sprint(memory.EstimatedTokens),
			"memory_budget":             fmt.Sprint(memory.TokenBudget),
			"project_instruction_items": fmt.Sprint(countContextSources(memory.IncludedSources,
				"project_instruction")),
			"project_instruction_fingerprint": turn.Run.Config.ProjectInstructionsFingerprint,
			"long_term_memory_items":          fmt.Sprint(len(longTermMemories)),
			"long_term_memory_selected": fmt.Sprint(countContextSources(memory.IncludedSources,
				"long_term_memory")),
			"continuity_context_items": fmt.Sprint(countContextSources(memory.IncludedSources,
				"continuity_context")),
			"continuity_context_fingerprint": turn.Run.Config.ContinuityContextFingerprint,
			"skill_items":                    fmt.Sprint(skillContext.ItemCount),
			"skill_tokens":                   fmt.Sprint(skillContext.TokenUpperBound),
			"skill_budget":                   fmt.Sprint(skillContext.TokenBudget),
			"skill_redactions":               fmt.Sprint(skillContext.RedactionCount),
			"skill_recovered":                fmt.Sprint(skillPreparation.Recovered),
			"skill_protocol":                 skillContext.ProtocolVersion,
			"external_skill_items":           fmt.Sprint(externalSkillContext.ItemCount),
			"external_skill_tokens":          fmt.Sprint(externalSkillContext.TokenUpperBound),
			"external_skill_budget":          fmt.Sprint(externalSkillContext.TokenBudget),
			"external_skill_redactions":      fmt.Sprint(externalSkillContext.RedactionCount),
			"external_skill_recovered":       fmt.Sprint(externalSkillPreparation.Recovered),
			"external_skill_protocol":        externalSkillContext.ProtocolVersion,
			"mode_protocol":                  turn.Mode.ProtocolVersion,
			"mode_policy":                    turn.Mode.PolicyVersion,
			"mode_surface":                   string(turn.Mode.Surface),
			"mode_phase":                     string(turn.Mode.Phase),
			"mode_revision":                  fmt.Sprint(turn.Mode.Revision),
			"mode_network":                   turn.Mode.Scope.NetworkMode,
			"mode_target_count":              fmt.Sprint(len(turn.Mode.Scope.AllowedTargets)),
			"execution_permission_mode":      string(executionPermission.Mode),
			"agent_code_tools_protocol":      agentCodeCapabilities.ProtocolVersion,
			"agent_code_tools_generation":    agentCodeCapabilities.Generation,
		},
	}
	if boundaryContext != "" {
		request.Metadata["context_boundary_receipts"] = fmt.Sprint(len(boundaryReceipt.Calls))
	}
	if boundaryReceipt.Returned > 0 {
		request.Metadata["input_delivery"] = "tool_boundary_continuation"
	}
	supervisorSummaryMetadata(&request, summary, hasSummary)
	refreshStandardCodeSupervisorRequest(&request, standardCode)
	return &agentTurnPreparation{
		context: agentTurnContext{
			threadEndTurn:              threadEndTurn,
			inbox:                      inbox,
			skillContext:               skillContext,
			externalSkillContext:       externalSkillContext,
			memoryBudget:               memoryBudget,
			projectInstructionSections: projectInstructionSections,
			threadPinSections:          threadPinSections,
			history:                    history,
			summary:                    summary,
			hasSummary:                 hasSummary,
			workItems:                  workItems,
			notes:                      notes,
			longTermMemorySections:     longTermMemorySections,
			continuitySections:         continuitySections,
			recentOperatorSections:     recentOperatorSections,
			memory:                     memory,
			contextAudit:               contextAudit,
		},
		input: agentTurnInput{
			boundaryReceipt:     boundaryReceipt,
			boundaryContext:     boundaryContext,
			visibleBaseInput:    visibleBaseInput,
			modelInput:          modelInput,
			pendingInstructions: pendingInstructions,
			steeringSequence:    steeringSequence,
			fileEffectCalls:     fileEffectCalls,
			fileEffects:         fileEffects,
			mcpMessages:         mcpMessages,
			mcpEvidenceIDs:      mcpEvidenceIDs,
		},
		tools: agentTurnCapabilities{permission: executionPermission, options: toolOptions,
			standardCode: standardCode, guidance: standardCodeGuidance,
			skillCandidateEnabled: skillCandidateEnabled},
		request: request, layout: contextLayout, ref: ref, toolRounds: toolRounds,
	}, result, nil
}
